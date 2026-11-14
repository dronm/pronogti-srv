package terminalbridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/payment"
	"github.com/jackc/pgx/v5"
)

type bridgeIntegrationFixture struct {
	provider   ds.Provider
	conn       *pgx.Conn
	repository *Repository
	agent      Agent
	token      string
	terminal   Terminal
	owner      checkout.Owner
	profileID  int64
	simulated  bool
}

func bridgeIntegrationSetup(t *testing.T, production bool) *bridgeIntegrationFixture {
	t.Helper()
	provider, conn := bridgeIntegrationDatabase(t)
	ctx := context.Background()
	environment := "mock"
	if production {
		environment = "production"
	}
	var profileID, kioskID int64
	if err := conn.QueryRow(ctx, `INSERT INTO public.payment_profiles
		(company_id,name,environment,api_base_url,terminals_enabled,enabled)
		VALUES(781898,'Terminal integration profile',$1,'',true,true) RETURNING id`, environment).Scan(&profileID); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `INSERT INTO public.kiosks(name,company_id,token_hash,is_active,payment_profile_id)
		VALUES('Terminal integration kiosk',781898,$1,true,$2) RETURNING id`, strings.Repeat("a", 64), profileID).Scan(&kioskID); err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(provider, Options{Enabled: true, AllowLive: production, AllowSimulated: !production, CommandTTL: time.Minute, MaxPoll: time.Second, Lease: 10 * time.Millisecond, HeartbeatMaxAge: time.Minute})
	credentials, err := repository.RegisterAgent(ctx, AgentInput{ID: "integration-agent", CompanyID: 781898, KioskID: kioskID, Environment: environment, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := repository.RegisterTerminal(ctx, TerminalInput{AgentID: credentials.Agent.ID, CompanyID: 781898, KioskID: kioskID, PaymentProfileID: profileID, TerminalID: "integration-terminal", BankTerminalID: "00199991", PhysicalID: "windows:COM3", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repository.Authenticate(ctx, credentials.Agent.ID, credentials.Token)
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := Heartbeat{Version: "0.2.0", Simulated: !production, SupportsResolutions: true, At: time.Now().UTC(), Terminals: []DeviceStatus{{TerminalID: "integration-terminal", Connected: false, ConnectorReachable: true, Simulated: !production, Message: "Connector reachable; physical device unverified"}}}
	heartbeat.Terminals[0].Capabilities.Sale = true
	if err := repository.Heartbeat(ctx, agent, heartbeat); err != nil {
		t.Fatal(err)
	}
	agent, err = repository.Authenticate(ctx, credentials.Agent.ID, credentials.Token)
	if err != nil {
		t.Fatal(err)
	}
	return &bridgeIntegrationFixture{provider: provider, conn: conn, repository: repository, agent: agent, token: credentials.Token, terminal: terminal,
		owner: checkout.Owner{CompanyID: 781898, KioskID: &kioskID, Mode: "kiosk", ClientID: 42, ScopeHash: strings.Repeat("b", 64), CustomerSessionHash: strings.Repeat("c", 64)}, profileID: profileID, simulated: !production}
}

func (f *bridgeIntegrationFixture) initiate(t *testing.T, key string) (checkout.Order, string, Command) {
	t.Helper()
	ctx := context.Background()
	order := bridgeIntegrationOrder(t, f.provider, f.owner, key)
	paymentID, err := f.repository.Initiate(ctx, f.owner, order.ID, "pay:"+key)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := f.repository.Claim(ctx, f.agent, ClaimRequest{TerminalIDs: []string{"integration-terminal"}, WaitSeconds: 1})
	if err != nil || delivery == nil || delivery.Command == nil {
		t.Fatalf("claim=%+v error=%v", delivery, err)
	}
	return order, paymentID, *delivery.Command
}

func (f *bridgeIntegrationFixture) event(command Command, state string) Event {
	event := Event{EventID: "event-" + command.CommandID + "-" + state, AgentID: f.agent.ID, CommandID: command.CommandID, PaymentID: command.PaymentID, TerminalID: command.TerminalID, State: state, CreatedAt: time.Now().UTC()}
	if state != "queued" && state != "dispatching" && state != "waiting" {
		event.Outcome = &Outcome{State: state, CommandID: command.CommandID, PaymentID: command.PaymentID, TerminalID: command.TerminalID,
			AmountKopecks: command.AmountKopecks, Currency: command.Currency, TransactionID: "native-transaction-1", RRN: "123456789012", AuthorizationCode: "AUTH01", ResponseCode: "00", FinishedAt: time.Now().UTC(), Simulated: f.simulated}
	}
	return event
}

func TestTerminalIntegrationSaleCommitsJobsOnceAndPreservesPaidState(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	ctx := context.Background()
	order, paymentID, command := f.initiate(t, "sale")
	if command.PaymentID != paymentID || command.AmountKopecks != order.PayableKopecks || command.Currency != "RUB" {
		t.Fatalf("command changed checkout: %+v", command)
	}
	state, receipts, settlements := bridgeIntegrationCounts(t, f.conn, order.ID)
	if state != "payment_pending" || receipts != 0 || settlements != 0 {
		t.Fatalf("initialization completed payment: %s %d %d", state, receipts, settlements)
	}
	approved := f.event(command, "approved")
	for i := 0; i < 2; i++ {
		ack, err := f.repository.Events(ctx, f.agent, []Event{approved})
		if err != nil || len(ack) != 1 || ack[0] != approved.EventID {
			t.Fatalf("ack=%v err=%v", ack, err)
		}
	}
	if _, err := f.repository.Events(ctx, f.agent, []Event{f.event(command, "dispatching")}); err != nil {
		t.Fatal(err)
	}
	state, receipts, settlements = bridgeIntegrationCounts(t, f.conn, order.ID)
	if state != "paid" || receipts != 1 || settlements != 1 {
		t.Fatalf("duplicate or reordered event broke completion: %s %d %d", state, receipts, settlements)
	}
	var method string
	if err := f.conn.QueryRow(ctx, `SELECT method FROM public.payments WHERE id=$1`, paymentID).Scan(&method); err != nil || method != "terminal_card" {
		t.Fatal(method, err)
	}
	var count int
	if err := f.conn.QueryRow(ctx, `SELECT count(*) FROM public.terminal_agent_events WHERE agent_id=$1 AND event_id=$2`, f.agent.ID, approved.EventID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestTerminalIntegrationRejectsMismatchedEvidenceAndConflictingEventReuse(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	ctx := context.Background()
	order, _, command := f.initiate(t, "evidence")
	for _, mutate := range []func(*Event){
		func(event *Event) { event.AgentID = "another-agent" },
		func(event *Event) { event.PaymentID = bridgeIntegrationUUID(t) },
		func(event *Event) { event.TerminalID = "another-terminal" },
		func(event *Event) { event.Outcome.AmountKopecks++ },
		func(event *Event) { event.Outcome.Currency = "USD" },
		func(event *Event) { event.Outcome.CommandID = bridgeIntegrationUUID(t) },
	} {
		event := f.event(command, "approved")
		mutate(&event)
		if ack, err := f.repository.Events(ctx, f.agent, []Event{event}); err == nil || len(ack) != 0 {
			t.Fatalf("mismatched event accepted: %+v ack=%v", event, ack)
		}
	}
	state, receipts, settlements := bridgeIntegrationCounts(t, f.conn, order.ID)
	if state != "payment_pending" || receipts != 0 || settlements != 0 {
		t.Fatal("invalid evidence changed payment")
	}
	approved := f.event(command, "approved")
	if _, err := f.repository.Events(ctx, f.agent, []Event{approved}); err != nil {
		t.Fatal(err)
	}
	approved.Outcome.ResponseCode = "changed"
	if _, err := f.repository.Events(ctx, f.agent, []Event{approved}); err == nil {
		t.Fatal("conflicting event reuse accepted")
	}
}

func TestTerminalIntegrationProductionRejectsSimulatedApproval(t *testing.T) {
	f := bridgeIntegrationSetup(t, true)
	order, _, command := f.initiate(t, "live-evidence")
	event := f.event(command, "approved")
	event.Outcome.Simulated = true
	if _, err := f.repository.Events(context.Background(), f.agent, []Event{event}); err == nil {
		t.Fatal("simulated result completed a production payment")
	}
	state, receipts, settlements := bridgeIntegrationCounts(t, f.conn, order.ID)
	if state != "payment_pending" || receipts != 0 || settlements != 0 {
		t.Fatal("simulator reached production completion jobs")
	}
}

func TestTerminalIntegrationInitializationReusesAttemptAndSerializesTerminal(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	ctx := context.Background()
	order := bridgeIntegrationOrder(t, f.provider, f.owner, "concurrent")
	var group sync.WaitGroup
	ids := make(chan string, 8)
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			id, err := f.repository.Initiate(ctx, f.owner, order.ID, "same-key")
			ids <- id
			errors <- err
		}()
	}
	group.Wait()
	close(ids)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("concurrent duplicate created multiple attempts")
		}
	}
	other, err := f.repository.Initiate(ctx, f.owner, order.ID, "different-key")
	if err != nil || other != first {
		t.Fatalf("new key bypassed checkout lock: %s %s %v", first, other, err)
	}
	second := bridgeIntegrationOrder(t, f.provider, f.owner, "other-order")
	if _, err := f.repository.Initiate(ctx, f.owner, second.ID, "second-payment"); err == nil {
		t.Fatal("same terminal accepted overlapping orders")
	}
	var count int
	if err := f.conn.QueryRow(ctx, `SELECT count(*) FROM public.terminal_commands WHERE order_id=$1`, order.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestTerminalIntegrationLostClaimRedeliversImmutableCommand(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	_, _, command := f.initiate(t, "lost-claim")
	// A response may be lost after the claim committed. Lease expiry permits
	// only the identical command, even after its dispatch deadline has passed.
	if _, err := f.conn.Exec(context.Background(), `UPDATE public.terminal_commands SET lease_until=now()-interval '1 second' WHERE id=$1`, command.CommandID); err != nil {
		t.Fatal(err)
	}
	delivery, err := f.repository.Claim(context.Background(), f.agent, ClaimRequest{TerminalIDs: []string{command.TerminalID}, WaitSeconds: 1})
	if err != nil || delivery == nil || delivery.Command == nil {
		t.Fatalf("redelivery unavailable: %+v %v", delivery, err)
	}
	before, _ := json.Marshal(command)
	after, _ := json.Marshal(delivery.Command)
	if string(before) != string(after) {
		t.Fatalf("redelivery changed command: %s %s", before, after)
	}
}

func TestTerminalIntegrationUnknownRetainsHoldAndLateApprovalCompletes(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	ctx := context.Background()
	order, _, command := f.initiate(t, "unknown")
	if _, err := f.repository.Events(ctx, f.agent, []Event{f.event(command, "unknown")}); err != nil {
		t.Fatal(err)
	}
	other := bridgeIntegrationOrder(t, f.provider, f.owner, "unknown-second")
	if _, err := f.repository.Initiate(ctx, f.owner, other.ID, "new-payment"); err == nil {
		t.Fatal("unknown terminal outcome released device hold")
	}
	if _, err := f.repository.Events(ctx, f.agent, []Event{f.event(command, "approved")}); err != nil {
		t.Fatal("late approval was rejected", err)
	}
	state, receipts, settlements := bridgeIntegrationCounts(t, f.conn, order.ID)
	if state != "paid" || receipts != 1 || settlements != 1 {
		t.Fatalf("late approval lost: %s %d %d", state, receipts, settlements)
	}
}

func TestTerminalIntegrationApprovalRollsBackIfCompletionCannotBeQueued(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	ctx := context.Background()
	order, paymentID, command := f.initiate(t, "atomic-completion")
	if _, err := f.conn.Exec(ctx, `CREATE FUNCTION public.terminal_test_fail_receipt() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'injected receipt queue failure'; END; $$ LANGUAGE plpgsql;
		CREATE TRIGGER terminal_test_fail_receipt BEFORE INSERT ON public.fiscal_receipts
		FOR EACH ROW EXECUTE FUNCTION public.terminal_test_fail_receipt()`); err != nil {
		t.Fatal(err)
	}
	event := f.event(command, "approved")
	if ack, err := f.repository.Events(ctx, f.agent, []Event{event}); err == nil || len(ack) != 0 {
		t.Fatalf("approval acknowledged before downstream queue commit: %v %v", ack, err)
	}
	state, receipts, settlements := bridgeIntegrationCounts(t, f.conn, order.ID)
	if state != "payment_pending" || receipts != 0 || settlements != 0 {
		t.Fatalf("failed completion left partial financial state: %s %d %d", state, receipts, settlements)
	}
	var paymentState string
	var eventCount int
	if err := f.conn.QueryRow(ctx, `SELECT p.state,(SELECT count(*) FROM public.terminal_agent_events WHERE agent_id=$2 AND event_id=$3)
		FROM public.payments p WHERE p.id=$1`, paymentID, f.agent.ID, event.EventID).Scan(&paymentState, &eventCount); err != nil {
		t.Fatal(err)
	}
	if paymentState == "paid" || eventCount != 0 {
		t.Fatal("failed payment transaction committed an acknowledgement inbox record")
	}
	if _, err := f.conn.Exec(ctx, `DROP TRIGGER terminal_test_fail_receipt ON public.fiscal_receipts; DROP FUNCTION public.terminal_test_fail_receipt()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.Events(ctx, f.agent, []Event{event}); err != nil {
		t.Fatal("durable retry could not complete after storage recovered", err)
	}
	state, receipts, settlements = bridgeIntegrationCounts(t, f.conn, order.ID)
	if state != "paid" || receipts != 1 || settlements != 1 {
		t.Fatal("retry did not create exactly one completion job of each kind")
	}
}

func TestTerminalIntegrationAuthenticationIsScopedAndRevocable(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	ctx := context.Background()
	if _, err := f.repository.Authenticate(ctx, f.agent.ID, "incorrect-token"); err == nil {
		t.Fatal("incorrect agent token accepted")
	}
	if _, err := f.repository.Authenticate(ctx, "different-agent", f.token); err == nil {
		t.Fatal("token accepted under a different agent identity")
	}
	var stored string
	if err := f.conn.QueryRow(ctx, `SELECT token_hash FROM public.terminal_agents WHERE id=$1`, f.agent.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == f.token || len(stored) != 64 {
		t.Fatal("backend did not store a token hash")
	}
	if _, err := f.conn.Exec(ctx, `UPDATE public.terminal_agents SET enabled=false WHERE id=$1`, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.Authenticate(ctx, f.agent.ID, f.token); err == nil {
		t.Fatal("disabled agent still authenticated")
	}
}

func TestTerminalIntegrationExpiredUndeliveredCommandDoesNotCharge(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	ctx := context.Background()
	// Change the repository's TTL rather than rewriting an immutable command.
	repository := NewRepository(f.provider, Options{Enabled: true, AllowSimulated: true, CommandTTL: time.Millisecond, MaxPoll: time.Second, Lease: 10 * time.Millisecond, HeartbeatMaxAge: time.Minute})
	order := bridgeIntegrationOrder(t, f.provider, f.owner, "expire-before-delivery")
	paymentID, err := repository.Initiate(ctx, f.owner, order.ID, "expire-payment")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	delivery, err := repository.Claim(ctx, f.agent, ClaimRequest{TerminalIDs: []string{"integration-terminal"}, WaitSeconds: 1})
	if err != nil || (delivery != nil && delivery.Command != nil) {
		t.Fatalf("expired never-delivered command was dispatched: %+v %v", delivery, err)
	}
	var paymentState string
	if err := f.conn.QueryRow(ctx, `SELECT state FROM public.payments WHERE id=$1`, paymentID).Scan(&paymentState); err != nil {
		t.Fatal(err)
	}
	if paymentState != "expired" && paymentState != "failed" {
		t.Fatalf("undelivered expiry retained unexpected payment state %q", paymentState)
	}
	_, receipts, settlements := bridgeIntegrationCounts(t, f.conn, order.ID)
	if receipts != 0 || settlements != 0 {
		t.Fatal("expiry queued financial completion")
	}
}

func TestTerminalIntegrationNotPaidResolutionRetainsHoldUntilBridgeAcknowledges(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	ctx := context.Background()
	order, paymentID, command := f.initiate(t, "operator-resolution")
	if _, err := f.repository.Events(ctx, f.agent, []Event{f.event(command, "unknown")}); err != nil {
		t.Fatal(err)
	}
	var operatorID int64
	if err := f.conn.QueryRow(ctx, `INSERT INTO public.users(name,role_id) VALUES('terminal-operator','admin') RETURNING id`).Scan(&operatorID); err != nil {
		t.Fatal(err)
	}
	resolution, err := f.repository.CreateResolution(ctx, command.CommandID, ResolutionInput{Decision: "not_paid", Reason: "Bank confirmed the original transaction was not charged", Reference: "bank-investigation-17", Evidence: "Verified terminal journal and bank response for the original command", OperatorID: operatorID})
	if err != nil {
		t.Fatal(err)
	}
	var state string
	if err := f.conn.QueryRow(ctx, `SELECT state FROM public.payments WHERE id=$1`, paymentID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "unknown" {
		t.Fatalf("operator decision released payment before local journal acknowledgement: %s", state)
	}
	other := bridgeIntegrationOrder(t, f.provider, f.owner, "operator-second-order")
	if _, err := f.repository.Initiate(ctx, f.owner, other.ID, "operator-retry"); err == nil {
		t.Fatal("pending local resolution released terminal")
	}
	delivery, err := f.repository.Claim(ctx, f.agent, ClaimRequest{TerminalIDs: []string{command.TerminalID}, WaitSeconds: 1})
	if err != nil || delivery == nil || delivery.Resolution == nil || delivery.Resolution.ResolutionID != resolution.ResolutionID {
		t.Fatalf("resolution delivery=%+v err=%v", delivery, err)
	}
	event := f.event(command, "declined")
	event.ResolutionID = resolution.ResolutionID
	event.Outcome.TransactionID = ""
	event.Outcome.RRN = ""
	for i := 0; i < 2; i++ {
		if _, err := f.repository.Events(ctx, f.agent, []Event{event}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.conn.QueryRow(ctx, `SELECT state FROM public.payments WHERE id=$1`, paymentID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("verified nonpayment was not finalized after bridge acknowledgement: %s", state)
	}
	if _, err := f.repository.Initiate(ctx, f.owner, other.ID, "operator-retry"); err != nil {
		t.Fatal("terminal remained locked after durable nonpayment acknowledgement", err)
	}
	_, receipts, settlements := bridgeIntegrationCounts(t, f.conn, order.ID)
	if receipts != 0 || settlements != 0 {
		t.Fatal("verified nonpayment created paid completion jobs")
	}
	var status string
	if err := f.conn.QueryRow(ctx, `SELECT status FROM public.terminal_resolutions WHERE id=$1`, resolution.ResolutionID).Scan(&status); err != nil || status != "acknowledged" {
		t.Fatal(status, err)
	}
}

func TestTerminalIntegrationSaleCapabilityAndSimulationMustMatchProfile(t *testing.T) {
	for _, test := range []struct {
		name            string
		sale, simulated bool
	}{
		{"sale capability absent", false, true},
		{"physical device advertised to mock environment", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := bridgeIntegrationSetup(t, false)
			heartbeat := Heartbeat{Version: "0.2.0", Simulated: test.simulated, SupportsResolutions: true, At: time.Now().UTC(), Terminals: []DeviceStatus{{TerminalID: "integration-terminal", ConnectorReachable: true, Simulated: test.simulated}}}
			heartbeat.Terminals[0].Capabilities.Sale = test.sale
			err := f.repository.Heartbeat(context.Background(), f.agent, heartbeat)
			if err != nil {
				return
			}
			order := bridgeIntegrationOrder(t, f.provider, f.owner, "unsupported-terminal")
			if _, err := f.repository.Initiate(context.Background(), f.owner, order.ID, "unsupported-payment"); err == nil {
				t.Fatal("unsupported terminal configuration was dispatched")
			}
		})
	}
}

func TestTerminalIntegrationCrossMethodHoldsWorkInBothDirections(t *testing.T) {
	for _, terminalFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "terminal blocks QR", false: "QR blocks terminal"}[terminalFirst], func(t *testing.T) {
			f := bridgeIntegrationSetup(t, false)
			ctx := context.Background()
			if _, err := f.conn.Exec(ctx, `UPDATE public.payment_profiles SET api_base_url='https://bank.invalid',jwt_env='TERMINAL_TEST_JWT',customer_code='test-customer',sbp_merchant_id='test-merchant',account_id='40702810900000000001/044525104' WHERE id=$1`, f.profileID); err != nil {
				t.Fatal(err)
			}
			order := bridgeIntegrationOrder(t, f.provider, f.owner, "cross-method")
			qr := payment.NewPostgresRepository(f.provider)
			if terminalFirst {
				if _, err := f.repository.Initiate(ctx, f.owner, order.ID, "terminal-first"); err != nil {
					t.Fatal(err)
				}
				if _, err := qr.Reserve(ctx, f.owner, order.ID, "qr-second", strings.Repeat("d", 64), time.Now().UTC()); err == nil {
					t.Fatal("QR bypassed a terminal payment hold")
				}
			} else {
				if _, err := qr.Reserve(ctx, f.owner, order.ID, "qr-first", strings.Repeat("d", 64), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if _, err := f.repository.Initiate(ctx, f.owner, order.ID, "terminal-second"); err == nil {
					t.Fatal("terminal bypassed an online payment hold")
				}
			}
			var count int
			if err := f.conn.QueryRow(ctx, `SELECT count(*) FROM public.payments WHERE order_id=$1`, order.ID).Scan(&count); err != nil || count != 1 {
				t.Fatal(count, err)
			}
		})
	}
}

func TestTerminalIntegrationOwnershipAndStaleHeartbeatCannotInitiate(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	ctx := context.Background()
	order := bridgeIntegrationOrder(t, f.provider, f.owner, "ownership")
	for _, owner := range []checkout.Owner{
		{CompanyID: f.owner.CompanyID, KioskID: f.owner.KioskID, Mode: "public", ClientID: f.owner.ClientID, ScopeHash: f.owner.ScopeHash, CustomerSessionHash: f.owner.CustomerSessionHash},
		{CompanyID: f.owner.CompanyID, KioskID: f.owner.KioskID, Mode: "kiosk", ClientID: f.owner.ClientID + 1, ScopeHash: f.owner.ScopeHash, CustomerSessionHash: f.owner.CustomerSessionHash},
		{CompanyID: f.owner.CompanyID, KioskID: f.owner.KioskID, Mode: "kiosk", ClientID: f.owner.ClientID, ScopeHash: strings.Repeat("d", 64), CustomerSessionHash: f.owner.CustomerSessionHash},
	} {
		if _, err := f.repository.Initiate(ctx, owner, order.ID, "wrong-owner"); err == nil {
			t.Fatal("another owner initiated terminal payment")
		}
	}
	if _, err := f.conn.Exec(ctx, `UPDATE public.terminal_agents SET last_seen_at=now()-interval '5 minutes' WHERE id=$1`, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.Initiate(ctx, f.owner, order.ID, "stale-agent"); err == nil {
		t.Fatal("stale offline agent accepted a new payment")
	}
	var count int
	if err := f.conn.QueryRow(ctx, `SELECT count(*) FROM public.payments WHERE order_id=$1`, order.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestTerminalIntegrationCommandIdentityAndSnapshotsAreImmutable(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	ctx := context.Background()
	order, _, command := f.initiate(t, "immutable")
	var items, fiscal, yclients []byte
	if err := f.conn.QueryRow(ctx, `SELECT items_snapshot,fiscal_snapshot,yclients_snapshot FROM public.payment_orders WHERE id=$1`, order.ID).Scan(&items, &fiscal, &yclients); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE public.terminal_commands SET amount_kopecks=amount_kopecks+1 WHERE id=$1`,
		`UPDATE public.terminal_commands SET terminal_id='replacement-device' WHERE id=$1`,
		`UPDATE public.terminal_commands SET expires_at=expires_at+interval '1 minute' WHERE id=$1`,
		`UPDATE public.terminal_commands SET physical_id='windows:COM99' WHERE id=$1`,
	} {
		if _, err := f.conn.Exec(ctx, query, command.CommandID); err == nil {
			t.Fatal("database allowed mutation of financial command", query)
		}
	}
	if _, err := f.conn.Exec(ctx, `UPDATE public.payment_terminals SET bank_terminal_id='replacement-bank-device' WHERE id=$1`, f.terminal.ID); err == nil {
		t.Fatal("configured terminal bank identity was rewritten")
	}
	if _, err := f.repository.Events(ctx, f.agent, []Event{f.event(command, "approved")}); err != nil {
		t.Fatal(err)
	}
	var afterItems, afterFiscal, afterYclients []byte
	if err := f.conn.QueryRow(ctx, `SELECT items_snapshot,fiscal_snapshot,yclients_snapshot FROM public.payment_orders WHERE id=$1`, order.ID).Scan(&afterItems, &afterFiscal, &afterYclients); err != nil {
		t.Fatal(err)
	}
	if string(items) != string(afterItems) || string(fiscal) != string(afterFiscal) || string(yclients) != string(afterYclients) {
		t.Fatal("terminal completion rewrote checkout snapshots")
	}
}

func TestTerminalIntegrationRollbackRefusesToErasePaymentHistory(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	_, paymentID, _ := f.initiate(t, "rollback-history")
	data, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000024_terminal_payments.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(context.Background(), string(data)); err == nil {
		t.Fatal("migration rollback erased terminal payment history")
	}
	_, _ = f.conn.Exec(context.Background(), "ROLLBACK")
	var method string
	if err := f.conn.QueryRow(context.Background(), `SELECT method FROM public.payments WHERE id=$1`, paymentID).Scan(&method); err != nil || method != "terminal_card" {
		t.Fatal("financial history lost", method, err)
	}
}

func TestTerminalIntegrationPaidResolutionCompletesOnceAndWaitsForLocalUnlock(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	ctx := context.Background()
	order, _, command := f.initiate(t, "operator-paid")
	if _, err := f.repository.Events(ctx, f.agent, []Event{f.event(command, "unknown")}); err != nil {
		t.Fatal(err)
	}
	var operatorID int64
	if err := f.conn.QueryRow(ctx, `INSERT INTO public.users(name,role_id) VALUES('paid-resolution-operator','admin') RETURNING id`).Scan(&operatorID); err != nil {
		t.Fatal(err)
	}
	resolution, err := f.repository.CreateResolution(ctx, command.CommandID, ResolutionInput{Decision: "paid", Reason: "Original bank transaction verified", Reference: "verified-bank-payment-42", Evidence: "Bank statement and original terminal journal identify exact amount and original checkout", OperatorID: operatorID})
	if err != nil {
		t.Fatal(err)
	}
	state, receipts, settlements := bridgeIntegrationCounts(t, f.conn, order.ID)
	if state != "payment_pending" || receipts != 0 || settlements != 0 {
		t.Fatalf("operator request completed payment before Bridge acknowledged durable application: %s %d %d", state, receipts, settlements)
	}
	other := bridgeIntegrationOrder(t, f.provider, f.owner, "operator-paid-next")
	if _, err := f.repository.Initiate(ctx, f.owner, other.ID, "operator-paid-retry"); err == nil {
		t.Fatal("paid resolution released physical terminal before Bridge acknowledged local unlock")
	}
	event := f.event(command, "approved")
	event.ResolutionID = resolution.ResolutionID
	event.Outcome.TransactionID = resolution.Reference
	for i := 0; i < 2; i++ {
		if _, err := f.repository.Events(ctx, f.agent, []Event{event}); err != nil {
			t.Fatal(err)
		}
	}
	state, receipts, settlements = bridgeIntegrationCounts(t, f.conn, order.ID)
	if state != "paid" || receipts != 1 || settlements != 1 {
		t.Fatalf("resolution acknowledgement duplicated completion: %s %d %d", state, receipts, settlements)
	}
	if _, err := f.repository.Initiate(ctx, f.owner, other.ID, "operator-paid-retry"); err != nil {
		t.Fatal("verified paid resolution never released physical terminal", err)
	}
}

func TestTerminalIntegrationFirstDeliveryRechecksEnabledRoute(t *testing.T) {
	for _, test := range []struct{ name, disable, enable string }{
		{"terminal disabled", `UPDATE public.payment_terminals SET enabled=false WHERE id=$1`, `UPDATE public.payment_terminals SET enabled=true WHERE id=$1`},
		{"profile disabled", `UPDATE public.payment_profiles SET enabled=false WHERE id=$1`, `UPDATE public.payment_profiles SET enabled=true WHERE id=$1`},
		{"kiosk disabled", `UPDATE public.kiosks SET is_active=false WHERE id=$1`, `UPDATE public.kiosks SET is_active=true WHERE id=$1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := bridgeIntegrationSetup(t, false)
			ctx := context.Background()
			order := bridgeIntegrationOrder(t, f.provider, f.owner, "route-changed")
			if _, err := f.repository.Initiate(ctx, f.owner, order.ID, "route-payment"); err != nil {
				t.Fatal(err)
			}
			id := f.terminal.ID
			if test.name == "profile disabled" {
				id = f.profileID
			} else if test.name == "kiosk disabled" {
				id = f.agent.KioskID
			}
			if _, err := f.conn.Exec(ctx, test.disable, id); err != nil {
				t.Fatal(err)
			}
			delivery, err := f.repository.Claim(ctx, f.agent, ClaimRequest{TerminalIDs: []string{"integration-terminal"}, WaitSeconds: 0})
			if err != nil || (delivery != nil && delivery.Command != nil) {
				t.Fatalf("new financial command delivered after route was disabled: %+v %v", delivery, err)
			}
			if _, err := f.conn.Exec(ctx, test.enable, id); err != nil {
				t.Fatal(err)
			}
			delivery, err = f.repository.Claim(ctx, f.agent, ClaimRequest{TerminalIDs: []string{"integration-terminal"}, WaitSeconds: 0})
			if err != nil || delivery == nil || delivery.Command == nil {
				t.Fatalf("original route not restored: %+v %v", delivery, err)
			}
		})
	}
}

func TestTerminalIntegrationTokenRotationFencesAuthenticatedRequests(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	ctx := context.Background()
	oldAgent := f.agent
	type claimResult struct {
		delivery *Delivery
		err      error
	}
	claimFinished := make(chan claimResult, 1)
	go func() {
		delivery, err := f.repository.Claim(ctx, oldAgent, ClaimRequest{TerminalIDs: []string{"integration-terminal"}, WaitSeconds: 1})
		claimFinished <- claimResult{delivery: delivery, err: err}
	}()
	select {
	case result := <-claimFinished:
		t.Fatalf("empty long poll returned before token rotation: %+v %v", result.delivery, result.err)
	case <-time.After(50 * time.Millisecond):
	}
	credentials, err := f.repository.RotateAgentToken(ctx, oldAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-claimFinished:
		if result.err == nil || result.delivery != nil {
			t.Fatalf("in-flight long poll survived credential rotation: %+v %v", result.delivery, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rotated long-poll credential was not fenced")
	}
	if _, err := f.repository.Authenticate(ctx, oldAgent.ID, f.token); err == nil {
		t.Fatal("old token still authenticated after rotation")
	}
	newAgent, err := f.repository.Authenticate(ctx, oldAgent.ID, credentials.Token)
	if err != nil {
		t.Fatal("replacement token did not authenticate", err)
	}
	var heartbeat Heartbeat
	if err := json.Unmarshal(oldAgent.Heartbeat, &heartbeat); err != nil {
		t.Fatal(err)
	}
	heartbeat.At = time.Now().UTC()
	if err := f.repository.Heartbeat(ctx, oldAgent, heartbeat); err == nil {
		t.Fatal("already authenticated old credential wrote a heartbeat after rotation")
	}
	if _, err := f.repository.Claim(ctx, oldAgent, ClaimRequest{TerminalIDs: []string{"integration-terminal"}, WaitSeconds: 0}); err == nil {
		t.Fatal("already authenticated old credential claimed after rotation")
	}
	f.agent = newAgent
	order, _, command := f.initiate(t, "rotated-agent-payment")
	event := f.event(command, "approved")
	if ack, err := f.repository.Events(ctx, oldAgent, []Event{event}); err == nil || len(ack) != 0 {
		t.Fatalf("already authenticated old credential committed financial evidence: %v %v", ack, err)
	}
	state, receipts, settlements := bridgeIntegrationCounts(t, f.conn, order.ID)
	if state != "payment_pending" || receipts != 0 || settlements != 0 {
		t.Fatal("rejected old credential changed payment state")
	}
	if _, err := f.repository.Events(ctx, newAgent, []Event{event}); err != nil {
		t.Fatal("replacement credential could not deliver the original result", err)
	}
	state, receipts, settlements = bridgeIntegrationCounts(t, f.conn, order.ID)
	if state != "paid" || receipts != 1 || settlements != 1 {
		t.Fatal("replacement credential lost exactly-once completion")
	}
}

func TestTerminalIntegrationPendingPaidResolutionRetainsContradictoryAgentResults(t *testing.T) {
	for _, negativeState := range []string{"declined", "expired"} {
		t.Run(negativeState, func(t *testing.T) {
			f := bridgeIntegrationSetup(t, false)
			ctx := context.Background()
			order, paymentID, command := f.initiate(t, "contradictory-resolution")
			if _, err := f.repository.Events(ctx, f.agent, []Event{f.event(command, "unknown")}); err != nil {
				t.Fatal(err)
			}
			var operatorID int64
			if err := f.conn.QueryRow(ctx, `INSERT INTO public.users(name,role_id) VALUES('contradiction-operator','admin') RETURNING id`).Scan(&operatorID); err != nil {
				t.Fatal(err)
			}
			resolution, err := f.repository.CreateResolution(ctx, command.CommandID, ResolutionInput{Decision: "paid", Reason: "Original payment verified in bank records", Reference: "verified-original-bank-payment", Evidence: "Exact original payment identity and amount verified by operator", OperatorID: operatorID})
			if err != nil {
				t.Fatal(err)
			}
			negative := f.event(command, negativeState)
			ack, err := f.repository.Events(ctx, f.agent, []Event{negative})
			if err != nil || len(ack) != 1 || ack[0] != negative.EventID {
				t.Fatalf("contradictory evidence was not durably retained: %v %v", ack, err)
			}
			var commandState, paymentState, resolutionState, eventState string
			if err := f.conn.QueryRow(ctx, `SELECT c.state,p.state,r.status,e.processing_state
				FROM public.terminal_commands c JOIN public.payments p ON p.id=c.payment_id
				JOIN public.terminal_resolutions r ON r.command_id=c.id
				JOIN public.terminal_agent_events e ON e.command_id=c.id AND e.event_id=$2
				WHERE c.id=$1 AND p.id=$3`, command.CommandID, negative.EventID, paymentID).Scan(&commandState, &paymentState, &resolutionState, &eventState); err != nil {
				t.Fatal(err)
			}
			if commandState != "unknown" || paymentState != "unknown" || resolutionState != "pending" || eventState != "conflict" {
				t.Fatalf("late negative result superseded verified paid decision: %s %s %s %s", commandState, paymentState, resolutionState, eventState)
			}
			_, receipts, settlements := bridgeIntegrationCounts(t, f.conn, order.ID)
			if receipts != 0 || settlements != 0 {
				t.Fatal("unacknowledged operator decision queued completion jobs")
			}
			other := bridgeIntegrationOrder(t, f.provider, f.owner, "contradiction-next-order")
			if _, err := f.repository.Initiate(ctx, f.owner, other.ID, "contradiction-retry"); err == nil {
				t.Fatal("contradictory agent evidence released device hold")
			}
			approved := f.event(command, "approved")
			approved.ResolutionID = resolution.ResolutionID
			approved.Outcome.TransactionID = resolution.Reference
			if _, err := f.repository.Events(ctx, f.agent, []Event{approved}); err != nil {
				t.Fatal("correct resolution acknowledgement could not complete", err)
			}
			state, receipts, settlements := bridgeIntegrationCounts(t, f.conn, order.ID)
			if state != "paid" || receipts != 1 || settlements != 1 {
				t.Fatal("verified resolution did not complete once after acknowledgement")
			}
		})
	}
}
