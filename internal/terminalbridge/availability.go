package terminalbridge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/pronogti/internal/checkout"
)

// Availability contains only kiosk-safe capabilities. It is advisory: Initiate
// revalidates every mutable binding while reserving the actual payment.
type Availability struct {
	TerminalCard  TerminalAvailability `json:"terminal_card"`
	ActivePayment *ActivePayment       `json:"active_payment,omitempty"`
}

type TerminalAvailability struct {
	Configured  bool   `json:"configured"`
	Available   bool   `json:"available"`
	Status      string `json:"status"`
	ReasonCode  string `json:"reason_code,omitempty"`
	Environment string `json:"environment,omitempty"`
}

type ActivePayment struct {
	PaymentID  string `json:"payment_id"`
	CheckoutID string `json:"checkout_id"`
	Method     string `json:"method"`
}

// terminalReadiness is shared by advisory status and authoritative initiation.
// DC2 can prove connector reachability without proving physical connectivity.
func terminalReadiness(options Options, environment string, lastSeen *time.Time, heartbeat json.RawMessage, terminalID string) (status, code, message string) {
	if !options.Enabled {
		return "disabled", "terminal_payments_disabled", "terminal payments are disabled"
	}
	if environment == "production" && !options.AllowLive {
		return "disabled", "terminal_live_disabled", "live terminal payments are disabled"
	}
	if environment == "mock" && !options.AllowSimulated {
		return "disabled", "terminal_simulation_disabled", "simulated terminal payments are disabled"
	}
	if lastSeen == nil || time.Since(*lastSeen) > options.HeartbeatMaxAge {
		return "offline", "terminal_agent_offline", "terminal agent has no recent authenticated heartbeat"
	}
	var heartbeatStatus Heartbeat
	if json.Unmarshal(heartbeat, &heartbeatStatus) != nil {
		return "unavailable", "terminal_disconnected", "terminal connector status is unavailable"
	}
	for _, device := range heartbeatStatus.Terminals {
		if device.TerminalID != terminalID {
			continue
		}
		if environment == "production" && device.Simulated {
			return "unavailable", "terminal_simulation_forbidden", "production terminal cannot use a simulator"
		}
		if environment == "mock" && !device.Simulated {
			return "unavailable", "terminal_simulation_required", "mock payments require an explicitly simulated terminal"
		}
		if (device.Connected || device.ConnectorReachable) && device.Capabilities.Sale {
			return "ready", "", ""
		}
	}
	return "offline", "terminal_disconnected", "terminal connector is not reachable"
}

func (r *Repository) Availability(ctx context.Context, owner checkout.Owner, checkoutID string) (Availability, error) {
	result := Availability{TerminalCard: TerminalAvailability{Status: "unavailable", ReasonCode: "terminal_unavailable"}}
	if owner.Mode != "kiosk" || owner.KioskID == nil || owner.CompanyID <= 0 || owner.CustomerSessionHash == "" || owner.ScopeHash == "" {
		return result, notFound()
	}
	if checkoutID != "" && !uuidPattern.MatchString(checkoutID) {
		return result, invalid("valid checkout ID is required")
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	var fiscal json.RawMessage
	methodConflict := false
	if checkoutID != "" {
		err := conn.QueryRow(ctx, `SELECT fiscal_snapshot FROM public.payment_orders WHERE id=$1 AND company_id=$2 AND kiosk_id=$3 AND entry_mode='kiosk' AND COALESCE(client_id,0)=$4 AND scope_hash=$5 AND customer_session_hash=$6`, checkoutID, owner.CompanyID, *owner.KioskID, owner.ClientID, owner.ScopeHash, owner.CustomerSessionHash).Scan(&fiscal)
		if errors.Is(err, ds.ErrNoRows) {
			return result, notFound()
		}
		if err != nil {
			return result, err
		}
		var active ActivePayment
		err = conn.QueryRow(ctx, `SELECT id::text,order_id::text,method FROM public.payments WHERE order_id=$1 AND state IN ('creating','pending','unknown','paid') ORDER BY CASE WHEN state='paid' THEN 0 ELSE 1 END,created_at DESC LIMIT 1`, checkoutID).Scan(&active.PaymentID, &active.CheckoutID, &active.Method)
		if err == nil {
			if active.Method == Method {
				result.ActivePayment = &active
			} else {
				methodConflict = true
			}
		} else if !errors.Is(err, ds.ErrNoRows) {
			return result, err
		}
	}
	var terminal Terminal
	var environment string
	var heartbeat json.RawMessage
	var lastSeen *time.Time
	var agentEnabled, kioskEnabled, profileEnabled, profileTerminals bool
	err = conn.QueryRow(ctx, `SELECT d.id,d.company_id,d.terminal_id,d.bank_terminal_id,d.physical_id,d.enabled,a.environment,a.last_seen_at,a.heartbeat,a.enabled,k.is_active,p.enabled,p.terminals_enabled FROM public.payment_terminals d JOIN public.terminal_agents a ON a.id=d.agent_id JOIN public.payment_profiles p ON p.id=d.payment_profile_id AND p.company_id=d.company_id AND p.environment=a.environment JOIN public.kiosks k ON k.id=d.kiosk_id AND k.company_id=d.company_id WHERE d.kiosk_id=$1 AND d.company_id=$2 ORDER BY d.enabled DESC,d.id DESC LIMIT 1`, *owner.KioskID, owner.CompanyID).Scan(&terminal.ID, &terminal.CompanyID, &terminal.TerminalID, &terminal.BankTerminalID, &terminal.PhysicalID, &terminal.Enabled, &environment, &lastSeen, &heartbeat, &agentEnabled, &kioskEnabled, &profileEnabled, &profileTerminals)
	if errors.Is(err, ds.ErrNoRows) {
		if !r.options.Enabled {
			result.TerminalCard.Status, result.TerminalCard.ReasonCode = "disabled", "terminal_payments_disabled"
		}
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.TerminalCard.Configured = true
	result.TerminalCard.Environment = environment
	if methodConflict {
		result.TerminalCard.Status, result.TerminalCard.ReasonCode = "unavailable", "payment_method_conflict"
		return result, nil
	}
	if !terminal.Enabled || !agentEnabled || !kioskEnabled || !profileEnabled || !profileTerminals {
		result.TerminalCard.Status, result.TerminalCard.ReasonCode = "disabled", "terminal_unavailable"
		return result, nil
	}
	status, code, _ := terminalReadiness(r.options, environment, lastSeen, heartbeat, terminal.TerminalID)
	result.TerminalCard.Status, result.TerminalCard.ReasonCode = status, code
	if code != "" {
		return result, nil
	}
	var busy bool
	err = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.terminal_commands WHERE (device_id=$1 OR (company_id=$2 AND physical_id=$3) OR (company_id=$2 AND bank_terminal_id=$4)) AND state IN ('queued','leased','dispatching','waiting','unknown'))`, terminal.ID, owner.CompanyID, terminal.PhysicalID, terminal.BankTerminalID).Scan(&busy)
	if err != nil {
		return result, err
	}
	if busy {
		result.TerminalCard.Status, result.TerminalCard.ReasonCode = "busy", "terminal_busy"
		return result, nil
	}
	if len(fiscal) > 0 {
		var settings struct {
			InternetPayment bool `json:"internet_payment"`
		}
		if err := json.Unmarshal(fiscal, &settings); err != nil {
			return result, err
		}
		if settings.InternetPayment {
			result.TerminalCard.Status, result.TerminalCard.ReasonCode = "unavailable", "terminal_fiscal_profile_invalid"
			return result, nil
		}
	}
	result.TerminalCard.Available = true
	return result, nil
}
