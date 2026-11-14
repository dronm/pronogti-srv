package terminalbridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dronm/ds/v4"
)

func validateResolution(input ResolutionInput) error {
	if input.Decision != "paid" && input.Decision != "not_paid" || input.OperatorID <= 0 {
		return invalid("operator identity and paid/not_paid decision are required")
	}
	if strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 2000 || !utf8.ValidString(input.Reason) || strings.ContainsRune(input.Reason, 0) {
		return invalid("resolution reason must contain 1 to 2000 UTF-8 bytes")
	}
	if strings.TrimSpace(input.Reference) == "" || len(input.Reference) > 500 || !utf8.ValidString(input.Reference) || strings.ContainsAny(input.Reference, "\r\n\x00") {
		return invalid("resolution reference must contain 1 to 500 UTF-8 bytes on one line")
	}
	if strings.TrimSpace(input.Evidence) == "" || len(input.Evidence) > 8000 || !utf8.ValidString(input.Evidence) || strings.ContainsRune(input.Evidence, 0) {
		return invalid("documented bank evidence is required, up to 8000 UTF-8 bytes")
	}
	return nil
}

// CreateResolution records an operator decision, but does not release any
// financial hold. Only the original Bridge's durable acknowledgement can do so.
func (r *Repository) CreateResolution(ctx context.Context, commandID string, input ResolutionInput) (Resolution, error) {
	if !uuidPattern.MatchString(commandID) {
		return Resolution{}, invalid("valid command ID is required")
	}
	if err := validateResolution(input); err != nil {
		return Resolution{}, err
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Resolution{}, err
	}
	defer release()
	var result Resolution
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		var orderID string
		if err := tx.QueryRow(ctx, `SELECT order_id::text FROM public.terminal_commands WHERE id=$1`, commandID).Scan(&orderID); errors.Is(err, ds.ErrNoRows) {
			return notFound()
		} else if err != nil {
			return err
		}
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM public.payment_orders WHERE id=$1 FOR UPDATE`, orderID).Scan(&locked); err != nil {
			return err
		}
		command, err := scanCommand(tx.QueryRow(ctx, `SELECT `+commandColumns+` FROM public.terminal_commands c WHERE c.id=$1 FOR UPDATE`, commandID))
		if err != nil {
			return err
		}
		var evidence, status string
		var operatorID int64
		previous, err := scanResolutionWithAudit(tx.QueryRow(ctx, `SELECT `+resolutionColumns+`,r.evidence,r.operator_id,r.status FROM public.terminal_resolutions r WHERE r.command_id=$1 ORDER BY r.created_at DESC LIMIT 1`, commandID), &evidence, &operatorID, &status)
		if err == nil {
			if previous.Decision == input.Decision && previous.Reason == input.Reason && previous.Reference == input.Reference && evidence == input.Evidence && operatorID == input.OperatorID {
				result = previous
				return nil
			}
			if status == "pending" {
				return conflict("terminal_resolution_pending", "an immutable decision is already awaiting Bridge acknowledgement")
			}
		} else if !errors.Is(err, ds.ErrNoRows) {
			return err
		}
		if command.State != "unknown" {
			return conflict("terminal_resolution_not_unknown", "only an unresolved terminal payment can be resolved")
		}
		var heartbeat json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT heartbeat FROM public.terminal_agents WHERE id=$1 AND enabled`, command.AgentID).Scan(&heartbeat); errors.Is(err, ds.ErrNoRows) {
			return conflict("terminal_agent_unavailable", "original terminal agent is disabled or unavailable")
		} else if err != nil {
			return err
		}
		var hb Heartbeat
		if err := json.Unmarshal(heartbeat, &hb); err != nil {
			return err
		}
		if !hb.SupportsResolutions {
			return conflict("terminal_resolution_unsupported", "original Bridge must advertise resolution support before a decision can be delivered")
		}
		id, err := newID()
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		result = Resolution{ResolutionID: id, CommandID: command.CommandID, PaymentID: command.PaymentID, TerminalID: command.TerminalID, Decision: input.Decision, AmountKopecks: command.AmountKopecks, Currency: command.Currency, Reason: input.Reason, Reference: input.Reference, ResolvedAt: now}
		_, err = tx.Exec(ctx, `INSERT INTO public.terminal_resolutions(id,command_id,payment_id,agent_id,terminal_id,amount_kopecks,currency,decision,reason,reference,evidence,operator_id,resolved_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, id, command.CommandID, command.PaymentID, command.AgentID, command.TerminalID, command.AmountKopecks, command.Currency, input.Decision, input.Reason, input.Reference, input.Evidence, input.OperatorID, now)
		return err
	})
	return result, err
}

func scanResolutionWithAudit(row ds.Row, evidence *string, operatorID *int64, status *string) (Resolution, error) {
	var r Resolution
	err := row.Scan(&r.ResolutionID, &r.CommandID, &r.PaymentID, &r.TerminalID, &r.Decision, &r.AmountKopecks, &r.Currency, &r.Reason, &r.Reference, &r.ResolvedAt, evidence, operatorID, status)
	return r, err
}

func (r *Repository) applyResolutionAck(ctx context.Context, tx ds.Tx, command CommandStatus, event Event) error {
	var resolution Resolution
	var status string
	err := tx.QueryRow(ctx, `SELECT `+resolutionColumns+`,r.status FROM public.terminal_resolutions r WHERE r.id=$1 AND r.command_id=$2 AND r.payment_id=$3 AND r.agent_id=$4 FOR UPDATE`, event.ResolutionID, command.CommandID, command.PaymentID, command.AgentID).Scan(&resolution.ResolutionID, &resolution.CommandID, &resolution.PaymentID, &resolution.TerminalID, &resolution.Decision, &resolution.AmountKopecks, &resolution.Currency, &resolution.Reason, &resolution.Reference, &resolution.ResolvedAt, &status)
	if errors.Is(err, ds.ErrNoRows) {
		return conflict("terminal_resolution_mismatch", "resolution does not belong to this immutable command")
	}
	if err != nil {
		return err
	}
	if event.Outcome == nil || resolution.TerminalID != event.TerminalID || resolution.AmountKopecks != event.Outcome.AmountKopecks || resolution.Currency != event.Outcome.Currency || resolution.Decision == "paid" && event.State != "approved" || resolution.Decision == "not_paid" && event.State != "declined" && event.State != "cancelled" {
		return conflict("terminal_resolution_mismatch", "Bridge acknowledgement does not match the operator decision")
	}
	if status == "superseded" && resolution.Decision == "paid" {
		var paid bool
		if err := tx.QueryRow(ctx, `SELECT state='paid' FROM public.payments WHERE id=$1`, command.PaymentID).Scan(&paid); err != nil {
			return err
		}
		if !paid {
			return conflict("terminal_resolution_contradiction", "verified paid decision cannot be discarded while its payment remains unpaid")
		}
	}
	if status == "acknowledged" || status == "superseded" {
		return nil
	}
	if command.State != "unknown" {
		return conflict("terminal_resolution_not_unknown", "original command is no longer awaiting resolution")
	}
	if resolution.Decision == "paid" {
		processing, _, err := r.approve(ctx, tx, command, *event.Outcome)
		if err != nil {
			return err
		}
		if processing == "conflict" {
			return conflict("terminal_duplicate_approval", "another payment already completed this checkout; investigate the recorded decision")
		}
	} else {
		encoded, err := json.Marshal(event.Outcome)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE public.terminal_commands SET state=$2,agent_outcome=$3::jsonb,lease_until=NULL,error_code='operator_confirmed_not_paid',error_message=$4 WHERE id=$1`, command.CommandID, event.State, string(encoded), resolution.Reason); err != nil {
			return err
		}
		if err := failPayment(ctx, tx, command, "operator_confirmed_not_paid", event.State); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE public.terminal_resolutions SET status='acknowledged',acknowledged_at=now() WHERE id=$1 AND status='pending'`, resolution.ResolutionID)
	return err
}
