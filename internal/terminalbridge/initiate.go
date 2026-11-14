package terminalbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/pronogti/internal/checkout"
)

type orderRecord struct {
	Owner               checkout.Owner
	ID, State, Currency string
	Amount              int64
	Expires             time.Time
	Fiscal              json.RawMessage
}

func validKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func lockOrder(ctx context.Context, tx ds.Tx, owner checkout.Owner, id string) (orderRecord, error) {
	var order orderRecord
	var kioskID int64
	err := tx.QueryRow(ctx, `SELECT id::text,company_id,COALESCE(kiosk_id,0),entry_mode,COALESCE(client_id,0),scope_hash,customer_session_hash,state,payable_kopecks,currency,expires_at,fiscal_snapshot FROM public.payment_orders WHERE id=$1 FOR UPDATE`, id).Scan(&order.ID, &order.Owner.CompanyID, &kioskID, &order.Owner.Mode, &order.Owner.ClientID, &order.Owner.ScopeHash, &order.Owner.CustomerSessionHash, &order.State, &order.Amount, &order.Currency, &order.Expires, &order.Fiscal)
	if errors.Is(err, ds.ErrNoRows) {
		return order, notFound()
	}
	if err != nil {
		return order, err
	}
	if kioskID > 0 {
		order.Owner.KioskID = &kioskID
	}
	if owner.Mode != "kiosk" || owner.KioskID == nil || owner.CompanyID != order.Owner.CompanyID || order.Owner.Mode != "kiosk" || order.Owner.KioskID == nil || *owner.KioskID != kioskID || owner.ClientID != order.Owner.ClientID || owner.ScopeHash != order.Owner.ScopeHash || owner.CustomerSessionHash != order.Owner.CustomerSessionHash {
		return order, notFound()
	}
	return order, nil
}

func requestHash(orderID string) string {
	sum := sha256.Sum256([]byte("terminal-card:" + orderID))
	return hex.EncodeToString(sum[:])
}

func existingAttempt(ctx context.Context, tx ds.Tx, orderID, key string) (string, error) {
	var id, method, hash string
	err := tx.QueryRow(ctx, `SELECT id::text,method,request_hash FROM public.payments WHERE order_id=$1 AND idempotency_key=$2`, orderID, key).Scan(&id, &method, &hash)
	if err == nil {
		if method != Method || hash != requestHash(orderID) {
			return "", conflict("payment_idempotency_conflict", "payment key is already used for another operation")
		}
		return id, nil
	}
	if !errors.Is(err, ds.ErrNoRows) {
		return "", err
	}
	err = tx.QueryRow(ctx, `SELECT id::text,method FROM public.payments WHERE order_id=$1 AND state IN ('creating','pending','unknown','paid') ORDER BY CASE WHEN state='paid' THEN 0 ELSE 1 END,created_at DESC LIMIT 1`, orderID).Scan(&id, &method)
	if err == nil {
		if method != Method {
			return "", conflict("payment_method_conflict", "checkout has an unresolved payment using another method")
		}
		return id, nil
	}
	if !errors.Is(err, ds.ErrNoRows) {
		return "", err
	}
	return "", nil
}

func (r *Repository) ExistingAttempt(ctx context.Context, owner checkout.Owner, orderID, key string) (string, error) {
	if !uuidPattern.MatchString(orderID) || !validKey(key) {
		return "", invalid("valid checkout ID and Idempotency-Key are required")
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	var result string
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		if _, err := lockOrder(ctx, tx, owner, orderID); err != nil {
			return err
		}
		var err error
		result, err = existingAttempt(ctx, tx, orderID, key)
		return err
	})
	return result, err
}

func (r *Repository) Initiate(ctx context.Context, owner checkout.Owner, orderID, key string) (string, error) {
	if !uuidPattern.MatchString(orderID) || !validKey(key) {
		return "", invalid("valid checkout ID and Idempotency-Key are required")
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	var result string
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		order, err := lockOrder(ctx, tx, owner, orderID)
		if err != nil {
			return err
		}
		result, err = existingAttempt(ctx, tx, orderID, key)
		if err != nil || result != "" {
			return err
		}
		if !r.options.Enabled {
			return conflict("terminal_bridge_disabled", "terminal payments are disabled")
		}
		if order.State != "prepared" || !order.Expires.After(time.Now()) {
			return conflict("checkout_not_prepared", "checkout is expired or already requires payment reconciliation")
		}
		var fiscal struct {
			InternetPayment bool `json:"internet_payment"`
		}
		if err := json.Unmarshal(order.Fiscal, &fiscal); err != nil {
			return err
		}
		if fiscal.InternetPayment {
			return conflict("terminal_fiscal_profile_invalid", "terminal checkout requires an in-person fiscal profile")
		}
		var terminal Terminal
		var environment string
		var heartbeat json.RawMessage
		var lastSeen *time.Time
		var snapshot json.RawMessage
		err = tx.QueryRow(ctx, `SELECT d.id,d.agent_id,d.company_id,d.kiosk_id,d.payment_profile_id,d.terminal_id,d.bank_terminal_id,d.physical_id,d.enabled,a.environment,a.last_seen_at,a.heartbeat,to_jsonb(p)-'jwt_ciphertext'-'jwt_env' FROM public.payment_terminals d JOIN public.terminal_agents a ON a.id=d.agent_id JOIN public.payment_profiles p ON p.id=d.payment_profile_id AND p.company_id=d.company_id AND p.environment=a.environment JOIN public.kiosks k ON k.id=d.kiosk_id AND k.company_id=d.company_id WHERE d.kiosk_id=$1 AND d.company_id=$2 AND d.enabled AND a.enabled AND k.is_active AND p.enabled AND p.terminals_enabled FOR UPDATE OF d`, *owner.KioskID, owner.CompanyID).Scan(&terminal.ID, &terminal.AgentID, &terminal.CompanyID, &terminal.KioskID, &terminal.PaymentProfileID, &terminal.TerminalID, &terminal.BankTerminalID, &terminal.PhysicalID, &terminal.Enabled, &environment, &lastSeen, &heartbeat, &snapshot)
		if errors.Is(err, ds.ErrNoRows) {
			return conflict("terminal_unavailable", "no enabled terminal is assigned to this kiosk")
		}
		if err != nil {
			return err
		}
		if environment == "production" && !r.options.AllowLive {
			return conflict("terminal_live_disabled", "live terminal payments are disabled")
		}
		if environment == "mock" && !r.options.AllowSimulated {
			return conflict("terminal_simulation_disabled", "simulated terminal payments are disabled")
		}
		if lastSeen == nil || time.Since(*lastSeen) > r.options.HeartbeatMaxAge {
			return conflict("terminal_agent_offline", "terminal agent has no recent authenticated heartbeat")
		}
		var status Heartbeat
		if err := json.Unmarshal(heartbeat, &status); err != nil {
			return err
		}
		reachable := false
		for _, device := range status.Terminals {
			if device.TerminalID == terminal.TerminalID {
				reachable = (device.Connected || device.ConnectorReachable) && device.Capabilities.Sale
				if environment == "production" && device.Simulated {
					return conflict("terminal_simulation_forbidden", "production terminal cannot use a simulator")
				}
				if environment == "mock" && !device.Simulated {
					return conflict("terminal_simulation_required", "mock payments require an explicitly simulated terminal")
				}
			}
		}
		if !reachable {
			return conflict("terminal_disconnected", "terminal connector is not reachable")
		}
		var active string
		err = tx.QueryRow(ctx, `SELECT id::text FROM public.terminal_commands WHERE (device_id=$1 OR (company_id=$2 AND physical_id=$3) OR (company_id=$2 AND bank_terminal_id=$4)) AND state IN ('queued','leased','dispatching','waiting','unknown') LIMIT 1`, terminal.ID, owner.CompanyID, terminal.PhysicalID, terminal.BankTerminalID).Scan(&active)
		if err == nil {
			return conflict("terminal_busy", "terminal has an unresolved payment")
		}
		if !errors.Is(err, ds.ErrNoRows) {
			return err
		}
		paymentID, err := newID()
		if err != nil {
			return err
		}
		commandID, err := newID()
		if err != nil {
			return err
		}
		deadline := time.Now().UTC().Add(r.options.CommandTTL)
		if order.Expires.Before(deadline) {
			deadline = order.Expires
		}
		if _, err := tx.Exec(ctx, `UPDATE public.payment_orders SET state='payment_pending' WHERE id=$1`, orderID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO public.payments(id,order_id,company_id,payment_profile_id,environment,amount_kopecks,currency,state,idempotency_key,request_hash,expires_at,profile_snapshot,method) VALUES($1,$2,$3,$4,$5,$6,$7,'pending',$8,$9,$10,$11::jsonb,'terminal_card')`, paymentID, orderID, owner.CompanyID, terminal.PaymentProfileID, environment, order.Amount, order.Currency, key, requestHash(orderID), deadline, string(snapshot)); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO public.terminal_commands(id,payment_id,order_id,company_id,kiosk_id,payment_profile_id,agent_id,device_id,terminal_id,bank_terminal_id,physical_id,environment,amount_kopecks,currency,expires_at,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,'queued')`, commandID, paymentID, orderID, owner.CompanyID, *owner.KioskID, terminal.PaymentProfileID, terminal.AgentID, terminal.ID, terminal.TerminalID, terminal.BankTerminalID, terminal.PhysicalID, environment, order.Amount, order.Currency, deadline)
		if err == nil {
			result = paymentID
		}
		return err
	})
	return result, err
}
