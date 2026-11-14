package terminalbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/dronm/ds/v4"
)

func finalState(state string) bool {
	return state == "approved" || state == "declined" || state == "cancelled" || state == "unknown" || state == "expired" || state == "rejected"
}
func progressState(state string) bool {
	return state == "queued" || state == "dispatching" || state == "waiting"
}

func validateEvent(agent Agent, event Event) error {
	if !identityPattern.MatchString(event.EventID) || event.AgentID != agent.ID || !uuidPattern.MatchString(event.CommandID) || !uuidPattern.MatchString(event.PaymentID) || !identityPattern.MatchString(event.TerminalID) || event.CreatedAt.IsZero() || !progressState(event.State) && !finalState(event.State) {
		return invalid("invalid terminal event identity or state")
	}
	if event.ResolutionID != "" && !uuidPattern.MatchString(event.ResolutionID) {
		return invalid("invalid resolution ID")
	}
	if progressState(event.State) {
		if event.Outcome != nil || event.ResolutionID != "" {
			return invalid("progress events must not contain a financial outcome or resolution")
		}
		return nil
	}
	o := event.Outcome
	if o == nil || o.State != event.State || o.CommandID != event.CommandID || o.PaymentID != event.PaymentID || o.TerminalID != event.TerminalID || o.AmountKopecks < 1 || o.AmountKopecks > 9007199254740991 || o.Currency != "RUB" || o.FinishedAt.IsZero() {
		return invalid("final event requires an exact matching financial outcome")
	}
	if agent.Environment == "production" && o.Simulated {
		return conflict("terminal_simulation_forbidden", "simulated outcomes cannot confirm production payments")
	}
	if agent.Environment == "mock" && !o.Simulated {
		return conflict("terminal_simulation_required", "mock agents must report explicitly simulated outcomes")
	}
	for _, value := range []string{o.TransactionID, o.RRN, o.STAN, o.AuthorizationCode, o.ResponseCode, o.ErrorCode} {
		if len(value) > 200 || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") {
			return invalid("invalid terminal outcome reference")
		}
	}
	if len(o.ErrorMessage) > 2000 || !utf8.ValidString(o.ErrorMessage) || strings.ContainsRune(o.ErrorMessage, 0) {
		return invalid("terminal outcome message exceeds limits")
	}
	return nil
}

func (r *Repository) Events(ctx context.Context, agent Agent, events []Event) ([]string, error) {
	if len(events) < 1 || len(events) > 100 {
		return nil, invalid("events must contain between 1 and 100 entries")
	}
	ack := make([]string, 0, len(events))
	seen := make(map[string]bool)
	for _, event := range events {
		if seen[event.EventID] {
			return nil, invalid("duplicate event ID within one batch")
		}
		seen[event.EventID] = true
		if err := validateEvent(agent, event); err != nil {
			return nil, err
		}
		if err := r.receiveEvent(ctx, agent, event); err != nil {
			return nil, err
		}
		ack = append(ack, event.EventID)
	}
	return ack, nil
}

func (r *Repository) receiveEvent(ctx context.Context, agent Agent, event Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	return ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		// Serialize the immutable per-agent inbox before taking order/payment locks.
		var currentID string
		if err := tx.QueryRow(ctx, `SELECT id FROM public.terminal_agents WHERE id=$1 AND enabled AND company_id=$2 AND kiosk_id=$3 AND environment=$4 AND token_hash=$5 FOR NO KEY UPDATE`, agent.ID, agent.CompanyID, agent.KioskID, agent.Environment, agent.credentialHash).Scan(&currentID); errors.Is(err, ds.ErrNoRows) {
			return unauthorized()
		} else if err != nil {
			return err
		}
		var previous string
		err := tx.QueryRow(ctx, `SELECT content_hash FROM public.terminal_agent_events WHERE agent_id=$1 AND event_id=$2`, agent.ID, event.EventID).Scan(&previous)
		if err == nil {
			if previous != hash {
				return conflict("terminal_event_conflict", "event ID already contains different immutable contents")
			}
			return nil
		}
		if !errors.Is(err, ds.ErrNoRows) {
			return err
		}
		var orderID string
		if err := tx.QueryRow(ctx, `SELECT order_id::text FROM public.terminal_commands WHERE id=$1 AND agent_id=$2`, event.CommandID, agent.ID).Scan(&orderID); errors.Is(err, ds.ErrNoRows) {
			return notFound()
		} else if err != nil {
			return err
		}
		var locked, paymentState string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM public.payment_orders WHERE id=$1 FOR UPDATE`, orderID).Scan(&locked); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT state FROM public.payments WHERE id=$1 AND order_id=$2 FOR UPDATE`, event.PaymentID, orderID).Scan(&paymentState); errors.Is(err, ds.ErrNoRows) {
			return conflict("terminal_event_scope_mismatch", "event payment does not belong to the command")
		} else if err != nil {
			return err
		}
		command, err := scanCommand(tx.QueryRow(ctx, `SELECT `+commandColumns+` FROM public.terminal_commands c WHERE c.id=$1 FOR UPDATE`, event.CommandID))
		if err != nil {
			return err
		}
		if command.AgentID != agent.ID || command.CompanyID != agent.CompanyID || command.KioskID != agent.KioskID || command.Environment != agent.Environment || command.PaymentID != event.PaymentID || command.TerminalID != event.TerminalID {
			return conflict("terminal_event_scope_mismatch", "event does not match its frozen command scope")
		}
		if command.FirstClaimedAt == nil {
			return conflict("terminal_command_not_delivered", "command has not been delivered to this agent")
		}
		if event.Outcome != nil && (event.Outcome.AmountKopecks != command.AmountKopecks || event.Outcome.Currency != command.Currency) {
			return conflict("terminal_event_amount_mismatch", "terminal outcome amount does not match authoritative checkout")
		}
		processing, code := "processed", ""
		if event.ResolutionID != "" {
			if err := r.applyResolutionAck(ctx, tx, command, event); err != nil {
				return err
			}
		} else {
			var pendingPaid bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.terminal_resolutions WHERE command_id=$1 AND status='pending' AND decision='paid')`, command.CommandID).Scan(&pendingPaid); err != nil {
				return err
			}
			if pendingPaid && finalState(event.State) && event.State != "approved" && event.State != "unknown" {
				processing, code = "conflict", "terminal_resolution_contradiction"
				_, err = tx.Exec(ctx, `UPDATE public.terminal_commands SET error_code=$2,error_message='Agent nonpayment result contradicts pending verified paid evidence; the payment remains held.' WHERE id=$1`, command.CommandID, code)
			} else {
				processing, code, err = r.applyEvent(ctx, tx, command, paymentState, event)
			}
			if err != nil {
				return err
			}
			if finalState(event.State) && event.State != "unknown" && processing != "conflict" {
				if _, err := tx.Exec(ctx, `UPDATE public.terminal_resolutions SET status='superseded',acknowledged_at=now() WHERE command_id=$1 AND status='pending'`, command.CommandID); err != nil {
					return err
				}
			}
		}
		var resolution any
		if event.ResolutionID != "" {
			resolution = event.ResolutionID
		}
		_, err = tx.Exec(ctx, `INSERT INTO public.terminal_agent_events(agent_id,event_id,command_id,payment_id,terminal_id,state,payload,content_hash,created_at,processed_at,processing_state,error_code,resolution_id) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,now(),$10,NULLIF($11,''),$12)`, agent.ID, event.EventID, event.CommandID, event.PaymentID, event.TerminalID, event.State, string(payload), hash, event.CreatedAt, processing, code, resolution)
		return err
	})
}

func (r *Repository) applyEvent(ctx context.Context, tx ds.Tx, command CommandStatus, paymentState string, event Event) (string, string, error) {
	if command.State == "approved" || paymentState == "paid" {
		return "ignored", "", nil
	}
	if progressState(event.State) {
		if finalState(command.State) {
			return "ignored", "", nil
		}
		rank := map[string]int{"queued": 0, "leased": 1, "dispatching": 2, "waiting": 3}
		if rank[event.State] <= rank[command.State] {
			return "ignored", "", nil
		}
		_, err := tx.Exec(ctx, `UPDATE public.terminal_commands SET state=$2 WHERE id=$1`, command.CommandID, event.State)
		return "processed", "", err
	}
	if event.State != "approved" && command.State != "unknown" && finalState(command.State) {
		return "ignored", "", nil
	}
	if event.State == "approved" {
		return r.approve(ctx, tx, command, *event.Outcome)
	}
	encoded, err := json.Marshal(event.Outcome)
	if err != nil {
		return "", "", err
	}
	o := event.Outcome
	_, err = tx.Exec(ctx, `UPDATE public.terminal_commands SET state=$2,lease_until=NULL,agent_outcome=$3::jsonb,transaction_id=NULLIF($4,''),rrn=NULLIF($5,''),stan=NULLIF($6,''),authorization_code=NULLIF($7,''),response_code=NULLIF($8,''),error_code=NULLIF($9,''),error_message=NULLIF($10,'') WHERE id=$1`, command.CommandID, event.State, string(encoded), o.TransactionID, o.RRN, o.STAN, o.AuthorizationCode, o.ResponseCode, o.ErrorCode, o.ErrorMessage)
	if err != nil {
		return "", "", err
	}
	if event.State == "unknown" {
		_, err = tx.Exec(ctx, `UPDATE public.payments SET state='unknown',bank_status='unknown',error_details=jsonb_build_object('code',$2::text),next_check_at=NULL WHERE id=$1 AND state<>'paid'`, command.PaymentID, first(o.ErrorCode, "terminal_outcome_unknown"))
	} else {
		err = failPayment(ctx, tx, command, first(o.ErrorCode, "terminal_"+event.State), event.State)
	}
	return "processed", "", err
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (r *Repository) approve(ctx context.Context, tx ds.Tx, command CommandStatus, outcome Outcome) (string, string, error) {
	encoded, err := json.Marshal(outcome)
	if err != nil {
		return "", "", err
	}
	var other string
	err = tx.QueryRow(ctx, `SELECT id::text FROM public.payments WHERE order_id=$1 AND state='paid' AND id<>$2 LIMIT 1`, command.OrderID, command.PaymentID).Scan(&other)
	if err != nil && !errors.Is(err, ds.ErrNoRows) {
		return "", "", err
	}
	duplicate := err == nil
	code := ""
	if duplicate {
		code = "duplicate_payment_approval"
	}
	if _, err := tx.Exec(ctx, `UPDATE public.terminal_commands SET state='approved',lease_until=NULL,agent_outcome=$2::jsonb,transaction_id=NULLIF($3,''),rrn=NULLIF($4,''),stan=NULLIF($5,''),authorization_code=NULLIF($6,''),response_code=NULLIF($7,''),error_code=NULLIF($8,''),error_message=CASE WHEN $8='' THEN NULL ELSE 'Another payment already completed this checkout; investigate both approvals.' END WHERE id=$1`, command.CommandID, string(encoded), outcome.TransactionID, outcome.RRN, outcome.STAN, outcome.AuthorizationCode, outcome.ResponseCode, code); err != nil {
		return "", "", err
	}
	if duplicate {
		_, err := tx.Exec(ctx, `UPDATE public.payments SET state='unknown',bank_status='duplicate_approval',error_details=jsonb_build_object('code','duplicate_payment_approval'),next_check_at=NULL WHERE id=$1 AND state<>'paid'`, command.PaymentID)
		return "conflict", code, err
	}
	if _, err := tx.Exec(ctx, `UPDATE public.payment_orders SET state='paid',yclients_sync_status=CASE WHEN yclients_sync_status='not_started' THEN 'pending' ELSE yclients_sync_status END WHERE id=$1 AND company_id=$2`, command.OrderID, command.CompanyID); err != nil {
		return "", "", err
	}
	// Native terminal counters remain device-scoped in terminal_commands. The
	// global payments reference is our immutable command identity, never a guess.
	_, err = tx.Exec(ctx, `UPDATE public.payments SET state='paid',bank_status='APPROVED',transaction_id=$2,ref_transaction_id=$2,paid_at=COALESCE(paid_at,$3),error_details=NULL,next_check_at=NULL,confirmation_claimed_at=NULL WHERE id=$1 AND state<>'paid'`, command.PaymentID, command.CommandID, outcome.FinishedAt)
	return "processed", "", err
}
