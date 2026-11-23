package terminalbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTerminalReadinessMatchesConnectorAndEnvironment(t *testing.T) {
	now := time.Now()
	options := Options{Enabled: true, AllowLive: true, AllowSimulated: true, HeartbeatMaxAge: time.Minute}
	heartbeat := Heartbeat{Terminals: []DeviceStatus{{TerminalID: "device", ConnectorReachable: true, Simulated: true}}}
	heartbeat.Terminals[0].Capabilities.Sale = true
	for _, test := range []struct {
		name, environment, status, code string
		change                          func(*Options, *Heartbeat, **time.Time)
	}{
		{"connector without connected", "mock", "ready", "", nil},
		{"disabled", "mock", "disabled", "terminal_payments_disabled", func(o *Options, _ *Heartbeat, _ **time.Time) { o.Enabled = false }},
		{"no heartbeat", "mock", "offline", "terminal_agent_offline", func(_ *Options, _ *Heartbeat, s **time.Time) { *s = nil }},
		{"old heartbeat", "mock", "offline", "terminal_agent_offline", func(_ *Options, _ *Heartbeat, s **time.Time) { past := now.Add(-2 * time.Minute); *s = &past }},
		{"no sale capability", "mock", "offline", "terminal_disconnected", func(_ *Options, h *Heartbeat, _ **time.Time) { h.Terminals[0].Capabilities.Sale = false }},
		{"disconnected", "mock", "offline", "terminal_disconnected", func(_ *Options, h *Heartbeat, _ **time.Time) { h.Terminals[0].ConnectorReachable = false }},
		{"mock requires simulator", "mock", "unavailable", "terminal_simulation_required", func(_ *Options, h *Heartbeat, _ **time.Time) { h.Terminals[0].Simulated = false }},
		{"production rejects simulator", "production", "unavailable", "terminal_simulation_forbidden", nil},
		{"production gate", "production", "disabled", "terminal_live_disabled", func(o *Options, _ *Heartbeat, _ **time.Time) { o.AllowLive = false }},
		{"mock gate", "mock", "disabled", "terminal_simulation_disabled", func(o *Options, _ *Heartbeat, _ **time.Time) { o.AllowSimulated = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			localOptions, localHeartbeat, lastSeen := options, heartbeat, &now
			localHeartbeat.Terminals = append([]DeviceStatus(nil), heartbeat.Terminals...)
			if test.change != nil {
				test.change(&localOptions, &localHeartbeat, &lastSeen)
			}
			encoded, err := json.Marshal(localHeartbeat)
			if err != nil {
				t.Fatal(err)
			}
			status, code, _ := terminalReadiness(localOptions, test.environment, lastSeen, encoded, "device")
			if status != test.status || code != test.code {
				t.Fatalf("status=%s code=%s", status, code)
			}
		})
	}
}

func TestTerminalIntegrationAvailabilityIsReadOnlyAndOwnershipScoped(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	ctx := context.Background()
	order := bridgeIntegrationOrder(t, f.provider, f.owner, "availability")
	check := func(status, code string, available bool) Availability {
		t.Helper()
		result, err := f.repository.Availability(ctx, f.owner, order.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !result.TerminalCard.Configured || result.TerminalCard.Status != status || result.TerminalCard.ReasonCode != code || result.TerminalCard.Available != available {
			t.Fatalf("availability=%+v", result)
		}
		data, _ := json.Marshal(result)
		for _, secret := range []string{f.token, f.agent.ID, f.terminal.TerminalID, f.terminal.BankTerminalID, f.terminal.PhysicalID, "payment_profile_id", "token_hash"} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("public status exposed internal binding: %s", secret)
			}
		}
		return result
	}
	if result := check("ready", "", true); result.ActivePayment != nil {
		t.Fatal("status created payment")
	}
	for i := 0; i < 2; i++ {
		check("ready", "", true)
	}
	var commands int
	if err := f.conn.QueryRow(ctx, `SELECT count(*) FROM terminal_commands`).Scan(&commands); err != nil || commands != 0 {
		t.Fatal("status dispatched payment", commands, err)
	}
	if _, err := f.conn.Exec(ctx, `UPDATE terminal_agents SET last_seen_at=now()-interval '2 minutes' WHERE id=$1`, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	check("offline", "terminal_agent_offline", false)
	heartbeat := Heartbeat{Terminals: []DeviceStatus{{TerminalID: f.terminal.TerminalID, ConnectorReachable: true, Simulated: true}}}
	heartbeat.Terminals[0].Capabilities.Sale = true
	if err := f.repository.Heartbeat(ctx, f.agent, heartbeat); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.SetTerminalEnabled(ctx, f.terminal.ID, false); err != nil {
		t.Fatal(err)
	}
	check("disabled", "terminal_unavailable", false)
	if err := f.repository.SetTerminalEnabled(ctx, f.terminal.ID, true); err != nil {
		t.Fatal(err)
	}
	var fiscalProfileID int64
	if err := f.conn.QueryRow(ctx, `INSERT INTO fiscal_profiles(company_id,name,internet_payment,settlement_place) VALUES($1,'Online fiscal profile',true,'https://example.test') RETURNING id`, f.owner.CompanyID).Scan(&fiscalProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(ctx, `UPDATE kiosks SET fiscal_profile_id=$2 WHERE id=$1`, *f.owner.KioskID, fiscalProfileID); err != nil {
		t.Fatal(err)
	}
	onlineOrder := bridgeIntegrationOrder(t, f.provider, f.owner, "online-fiscal")
	if result, err := f.repository.Availability(ctx, f.owner, onlineOrder.ID); err != nil || result.TerminalCard.Available || result.TerminalCard.ReasonCode != "terminal_fiscal_profile_invalid" {
		t.Fatal("online-only fiscal profile allowed terminal", result, err)
	}
	if _, err := f.conn.Exec(ctx, `UPDATE kiosks SET fiscal_profile_id=NULL WHERE id=$1`, *f.owner.KioskID); err != nil {
		t.Fatal(err)
	}
	paymentID, err := f.repository.Initiate(ctx, f.owner, order.ID, "terminal:"+order.ID)
	if err != nil {
		t.Fatal(err)
	}
	result := check("busy", "terminal_busy", false)
	if result.ActivePayment == nil || result.ActivePayment.PaymentID != paymentID || result.ActivePayment.CheckoutID != order.ID || result.ActivePayment.Method != Method {
		t.Fatal("owned active attempt not recoverable", result.ActivePayment)
	}
	other := f.owner
	other.CustomerSessionHash = strings.Repeat("d", 64)
	if result, err := f.repository.Availability(ctx, other, order.ID); err == nil || result.ActivePayment != nil {
		t.Fatal("another customer recovered payment", result, err)
	}
	f.repository.options.Enabled = false
	if result := check("disabled", "terminal_payments_disabled", false); result.ActivePayment == nil {
		t.Fatal("disabled gate hid owned recovery")
	}
}
