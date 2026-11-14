package terminalbridge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dronm/ds/v4"
)

const commandColumns = `c.id::text,c.payment_id::text,c.terminal_id,c.amount_kopecks,c.currency,c.expires_at,
	c.order_id::text,c.company_id,c.kiosk_id,c.payment_profile_id,c.agent_id,c.device_id,c.bank_terminal_id,c.physical_id,c.environment,c.state,
	c.lease_until,c.first_claimed_at,c.agent_outcome,COALESCE(c.transaction_id,''),COALESCE(c.rrn,''),COALESCE(c.stan,''),COALESCE(c.authorization_code,''),COALESCE(c.response_code,''),COALESCE(c.error_code,''),COALESCE(c.error_message,''),c.created_at,c.updated_at,
	EXISTS(SELECT 1 FROM public.terminal_resolutions pending_resolution WHERE pending_resolution.command_id=c.id AND pending_resolution.status='pending')`

func scanCommand(row ds.Row) (CommandStatus, error) {
	var c CommandStatus
	err := row.Scan(&c.CommandID, &c.PaymentID, &c.TerminalID, &c.AmountKopecks, &c.Currency, &c.ExpiresAt, &c.OrderID, &c.CompanyID, &c.KioskID, &c.PaymentProfileID, &c.AgentID, &c.DeviceID, &c.BankTerminalID, &c.PhysicalID, &c.Environment, &c.State, &c.LeaseUntil, &c.FirstClaimedAt, &c.Outcome, &c.TransactionID, &c.RRN, &c.STAN, &c.AuthorizationCode, &c.ResponseCode, &c.ErrorCode, &c.ErrorMessage, &c.CreatedAt, &c.UpdatedAt, &c.ResolutionPending)
	return c, err
}

func (r *Repository) GetAgent(ctx context.Context, id string) (Agent, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Agent{}, err
	}
	defer release()
	agent, err := scanAgent(conn.QueryRow(ctx, `SELECT `+agentColumns+` FROM public.terminal_agents WHERE id=$1`, id))
	if errors.Is(err, ds.ErrNoRows) {
		return Agent{}, notFound()
	}
	return agent, err
}
func (r *Repository) GetTerminal(ctx context.Context, id int64) (Terminal, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Terminal{}, err
	}
	defer release()
	terminal, err := scanTerminal(conn.QueryRow(ctx, `SELECT `+terminalColumns+` FROM public.payment_terminals WHERE id=$1`, id))
	if errors.Is(err, ds.ErrNoRows) {
		return Terminal{}, notFound()
	}
	return terminal, err
}

func (r *Repository) GetCommand(ctx context.Context, id string) (CommandStatus, error) {
	if !uuidPattern.MatchString(id) {
		return CommandStatus{}, invalid("valid command ID is required")
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return CommandStatus{}, err
	}
	defer release()
	command, err := scanCommand(conn.QueryRow(ctx, `SELECT `+commandColumns+` FROM public.terminal_commands c WHERE c.id=$1`, id))
	if errors.Is(err, ds.ErrNoRows) {
		return CommandStatus{}, notFound()
	}
	return command, err
}

func (r *Repository) ListCommands(ctx context.Context) ([]CommandStatus, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := conn.Query(ctx, `SELECT `+commandColumns+` FROM public.terminal_commands c WHERE c.state IN ('queued','leased','dispatching','waiting','unknown') OR c.id IN (SELECT id FROM public.terminal_commands ORDER BY created_at DESC LIMIT 200) ORDER BY c.created_at DESC,c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]CommandStatus, 0)
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// PaymentDetails excludes processor authorization codes, physical routes and any
// credentials. The HTTP caller must first authorize the parent payment owner.
func (r *Repository) PaymentDetails(ctx context.Context, paymentID string) (TerminalDetails, error) {
	if !uuidPattern.MatchString(paymentID) {
		return TerminalDetails{}, invalid("valid payment ID is required")
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return TerminalDetails{}, err
	}
	defer release()
	var result TerminalDetails
	err = conn.QueryRow(ctx, `SELECT c.id::text,c.terminal_id,c.agent_id,c.state,COALESCE(c.rrn,''),COALESCE(c.transaction_id,''),COALESCE(c.error_code,''),COALESCE(c.error_message,''),EXISTS(SELECT 1 FROM public.terminal_resolutions r WHERE r.command_id=c.id AND r.status='pending') FROM public.terminal_commands c WHERE c.payment_id=$1`, paymentID).Scan(&result.CommandID, &result.TerminalID, &result.AgentID, &result.State, &result.RRN, &result.TransactionID, &result.ErrorCode, &result.ErrorMessage, &result.ResolutionPending)
	if errors.Is(err, ds.ErrNoRows) {
		return result, notFound()
	}
	return result, err
}

const resolutionColumns = `r.id::text,r.command_id::text,r.payment_id::text,r.terminal_id,r.decision,r.amount_kopecks,r.currency,r.reason,r.reference,r.resolved_at`

func scanResolution(row ds.Row) (Resolution, error) {
	var result Resolution
	err := row.Scan(&result.ResolutionID, &result.CommandID, &result.PaymentID, &result.TerminalID, &result.Decision, &result.AmountKopecks, &result.Currency, &result.Reason, &result.Reference, &result.ResolvedAt)
	return result, err
}

func (r *Repository) Claim(ctx context.Context, agent Agent, input ClaimRequest) (*Delivery, error) {
	if len(input.TerminalIDs) < 1 || len(input.TerminalIDs) > 100 || input.WaitSeconds < 0 {
		return nil, invalid("claim requires 1 to 100 terminal IDs and nonnegative wait_seconds")
	}
	seen := make(map[string]bool)
	for _, id := range input.TerminalIDs {
		if !identityPattern.MatchString(id) || seen[id] {
			return nil, invalid("claim terminal IDs must be valid and unique")
		}
		seen[id] = true
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return nil, err
	}
	var registered int
	err = conn.QueryRow(ctx, `SELECT count(*) FROM public.payment_terminals WHERE agent_id=$1 AND terminal_id=ANY($2::text[])`, agent.ID, input.TerminalIDs).Scan(&registered)
	release()
	if err != nil {
		return nil, err
	}
	if registered != len(input.TerminalIDs) {
		return nil, conflict("terminal_agent_scope_mismatch", "claim includes a terminal outside this agent")
	}
	wait := time.Duration(input.WaitSeconds) * time.Second
	if wait > r.options.MaxPoll {
		wait = r.options.MaxPoll
	}
	deadline := time.Now().Add(wait)
	for {
		delivery, err := r.claimOnce(ctx, agent, input.TerminalIDs)
		if err != nil || delivery != nil {
			return delivery, err
		}
		if !time.Now().Before(deadline) {
			return nil, nil
		}
		remaining := time.Until(deadline)
		if remaining > 200*time.Millisecond {
			remaining = 200 * time.Millisecond
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (r *Repository) claimOnce(ctx context.Context, agent Agent, terminalIDs []string) (*Delivery, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	current, err := scanAgent(conn.QueryRow(ctx, `SELECT `+agentColumns+` FROM public.terminal_agents WHERE id=$1 AND enabled AND token_hash=$2`, agent.ID, agent.credentialHash))
	if errors.Is(err, ds.ErrNoRows) {
		return nil, unauthorized()
	}
	if err != nil {
		return nil, err
	}
	if current.CompanyID != agent.CompanyID || current.KioskID != agent.KioskID || current.Environment != agent.Environment {
		return nil, unauthorized()
	}
	var hb Heartbeat
	if err := json.Unmarshal(current.Heartbeat, &hb); err != nil {
		return nil, err
	}
	if hb.SupportsResolutions {
		resolution, err := scanResolution(conn.QueryRow(ctx, `SELECT `+resolutionColumns+` FROM public.terminal_resolutions r WHERE r.agent_id=$1 AND r.terminal_id=ANY($2::text[]) AND r.status='pending' ORDER BY r.created_at,r.id LIMIT 1`, agent.ID, terminalIDs))
		if err == nil {
			return &Delivery{Resolution: &resolution}, nil
		}
		if !errors.Is(err, ds.ErrNoRows) {
			return nil, err
		}
	}
	var id, orderID string
	err = conn.QueryRow(ctx, `SELECT id::text,order_id::text FROM public.terminal_commands WHERE agent_id=$1 AND terminal_id=ANY($2::text[]) AND state IN ('queued','leased','dispatching','waiting') AND (lease_until IS NULL OR lease_until<=now()) AND (first_claimed_at IS NOT NULL OR $3 OR expires_at<=now()) ORDER BY created_at,id LIMIT 1`, agent.ID, terminalIDs, r.options.Enabled && (agent.Environment != "production" || r.options.AllowLive) && (agent.Environment != "mock" || r.options.AllowSimulated)).Scan(&id, &orderID)
	if errors.Is(err, ds.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result *Delivery
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM public.payment_orders WHERE id=$1 FOR UPDATE`, orderID).Scan(&locked); err != nil {
			return err
		}
		command, err := scanCommand(tx.QueryRow(ctx, `SELECT `+commandColumns+` FROM public.terminal_commands c WHERE c.id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if command.AgentID != agent.ID || command.State != "queued" && command.State != "leased" && command.State != "dispatching" && command.State != "waiting" || command.LeaseUntil != nil && command.LeaseUntil.After(time.Now()) {
			return nil
		}
		if command.FirstClaimedAt == nil && !command.ExpiresAt.After(time.Now()) {
			if _, err := tx.Exec(ctx, `UPDATE public.terminal_commands SET state='expired',error_code='command_expired_before_delivery',lease_until=NULL WHERE id=$1`, id); err != nil {
				return err
			}
			return failPayment(ctx, tx, command, "command_expired_before_delivery", "expired")
		}
		if command.FirstClaimedAt == nil {
			ready, err := r.firstDeliveryReady(ctx, tx, command, agent.credentialHash)
			if err != nil || !ready {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE public.terminal_commands SET state=CASE WHEN state='queued' THEN 'leased' ELSE state END,lease_until=now()+$2::bigint*interval '1 millisecond',first_claimed_at=COALESCE(first_claimed_at,now()) WHERE id=$1`, id, r.options.Lease.Milliseconds()); err != nil {
			return err
		}
		wire := command.Command
		result = &Delivery{Command: &wire}
		return nil
	})
	return result, err
}

// Revalidate the mutable configuration at the first irreversible handoff. A
// previously leased command remains bound to its original journal and may be
// redelivered after configuration changes to recover that original outcome.
func (r *Repository) firstDeliveryReady(ctx context.Context, tx ds.Tx, command CommandStatus, credentialHash string) (bool, error) {
	if !r.options.Enabled || command.Environment == "production" && !r.options.AllowLive || command.Environment == "mock" && !r.options.AllowSimulated {
		return false, nil
	}
	var heartbeat json.RawMessage
	var lastSeen *time.Time
	var valid bool
	err := tx.QueryRow(ctx, `SELECT d.enabled AND a.enabled AND a.token_hash=$2 AND k.is_active AND k.company_id=c.company_id AND p.enabled AND p.terminals_enabled AND p.environment=c.environment AND a.environment=c.environment,a.last_seen_at,a.heartbeat FROM public.terminal_commands c JOIN public.payment_terminals d ON d.id=c.device_id JOIN public.terminal_agents a ON a.id=c.agent_id JOIN public.kiosks k ON k.id=c.kiosk_id JOIN public.payment_profiles p ON p.id=c.payment_profile_id AND p.company_id=c.company_id WHERE c.id=$1 FOR UPDATE OF d FOR KEY SHARE OF a`, command.CommandID, credentialHash).Scan(&valid, &lastSeen, &heartbeat)
	if err != nil || !valid || lastSeen == nil || time.Since(*lastSeen) > r.options.HeartbeatMaxAge {
		return false, err
	}
	var hb Heartbeat
	if err := json.Unmarshal(heartbeat, &hb); err != nil {
		return false, err
	}
	for _, device := range hb.Terminals {
		if device.TerminalID == command.TerminalID {
			expectedSimulation := command.Environment == "mock"
			return device.Capabilities.Sale && (device.Connected || device.ConnectorReachable) && device.Simulated == expectedSimulation, nil
		}
	}
	return false, nil
}

func failPayment(ctx context.Context, tx ds.Tx, command CommandStatus, code, status string) error {
	if _, err := tx.Exec(ctx, `UPDATE public.payments SET state='failed',bank_status=$2,error_details=jsonb_build_object('code',$3::text),next_check_at=NULL WHERE id=$1 AND state<>'paid'`, command.PaymentID, status, code); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE public.payment_orders o SET state=CASE WHEN o.expires_at>now() THEN 'prepared' ELSE 'expired' END WHERE o.id=$1 AND o.state='payment_pending' AND NOT EXISTS(SELECT 1 FROM public.payments p WHERE p.order_id=o.id AND p.state IN ('creating','pending','unknown','paid'))`, command.OrderID)
	return err
}
