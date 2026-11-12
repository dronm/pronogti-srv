package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/ds/v4/pgds"
	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/fiscal"
	"github.com/dronm/pronogti/internal/settlement"
	"github.com/dronm/webapp"
	"github.com/jackc/pgx/v5"
)

// These tests create and remove a dedicated database, never the configured
// application's schema. They require an explicit loopback-only test connection
// with permission to CREATE DATABASE; ordinary go test remains network-free.
func completionTestDatabase(t *testing.T) (ds.Provider, *pgx.Conn) {
	t.Helper()
	value := os.Getenv("PRONOGTI_TEST_DATABASE_URL")
	if value == "" {
		t.Skip("set PRONOGTI_TEST_DATABASE_URL to an isolated local PostgreSQL server")
	}
	config, err := pgx.ParseConfig(value)
	if err != nil {
		t.Fatal("invalid PRONOGTI_TEST_DATABASE_URL")
	}
	if config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1" {
		t.Fatal("integration tests require a loopback PostgreSQL server")
	}
	ctx := context.Background()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	name := fmt.Sprintf("pronogti_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP DATABASE "+quoted+" WITH (FORCE)") })
	config.Database = name
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil || len(files) < 9 {
		t.Fatalf("migration inventory: %d, %v", len(files), err)
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Exec(ctx, string(body)); err != nil {
			t.Fatalf("migration %s: %v", filepath.Base(file), err)
		}
	}
	// pgx.ConnConfig.ConnString retains the original string even after changing
	// Database; build the pool URL explicitly to keep it in our fresh database.
	poolURL := url.URL{Scheme: "postgres", User: url.UserPassword(config.User, config.Password), Host: net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port))), Path: "/" + name, RawQuery: "sslmode=disable"}
	provider, err := pgds.New(&pgds.Config{PrimaryConnStr: poolURL.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.(*pgds.Provider).Close() })
	return provider, conn
}

func completionProfile(t *testing.T, conn *pgx.Conn, companyID int64, environment string) int64 {
	t.Helper()
	var id int64
	err := conn.QueryRow(context.Background(), `INSERT INTO public.payment_profiles
		(company_id,name,environment,api_base_url,jwt_env,customer_code,sbp_merchant_id,account_id,enabled)
		VALUES($1,'Integration profile',$2,'https://bank.invalid','PRONOGTI_TEST_JWT',$3,$4,'40702810900000000001/044525104',true) RETURNING id`, companyID, environment, fmt.Sprintf("customer-%d", companyID), fmt.Sprintf("merchant-%d", companyID)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(context.Background(), `UPDATE public.salons SET default_payment_profile_id=$2 WHERE company_id=$1`, companyID, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func completionOrder(t *testing.T, provider ds.Provider, actor checkout.Actor, key string) checkout.Order {
	t.Helper()
	id, err := newAttemptID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	order := checkout.Order{ID: id, Owner: ownerFor(actor), Items: []checkout.Item{{Selection: checkout.Selection{Source: checkout.SourceCatalog, Kind: checkout.KindGood, ID: 7, StaffID: 9, Quantity: "1"}, Title: "Integration good", UnitPriceKopecks: 12345, AmountKopecks: 12345}},
		SubtotalKopecks: 12345, PayableKopecks: 12345, Currency: "RUB", State: "prepared", IdempotencyKey: key, RequestHash: strings.Repeat("b", 64),
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(15 * time.Minute), YClientsSyncStatus: "not_started", CustomerContact: "79990000000"}
	stored, err := checkout.NewPostgresRepository(provider).Create(context.Background(), order)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

type completionNoSettlement struct{ t *testing.T }

func (p completionNoSettlement) Plan(context.Context, settlement.Job) ([]settlement.Operation, error) {
	p.t.Fatal("demo contacted YCLIENTS plan")
	return nil, nil
}
func (p completionNoSettlement) Execute(context.Context, settlement.Job, settlement.Operation) (settlement.Outcome, error) {
	p.t.Fatal("demo contacted YCLIENTS execute")
	return settlement.Outcome{}, nil
}
func (p completionNoSettlement) Reconcile(context.Context, settlement.Job, settlement.Operation) (settlement.Outcome, error) {
	p.t.Fatal("demo contacted YCLIENTS reconcile")
	return settlement.Outcome{}, nil
}

type completionNoReceipt struct{ t *testing.T }

func (p completionNoReceipt) Create(context.Context, fiscal.DriverConfig, string, json.RawMessage) (fiscal.DriverResult, error) {
	p.t.Fatal("demo contacted receipt device")
	return fiscal.DriverResult{}, nil
}
func (p completionNoReceipt) Lookup(context.Context, fiscal.DriverConfig, string) (fiscal.DriverResult, error) {
	p.t.Fatal("demo contacted receipt journal")
	return fiscal.DriverResult{}, nil
}

func TestCompletionIntegrationDemoFlow(t *testing.T) {
	provider, conn := completionTestDatabase(t)
	profileID := completionProfile(t, conn, 781898, "mock")
	ctx := context.Background()
	actor := checkout.Actor{CompanyID: 781898, Mode: "public", ClientID: 42, ScopeID: "scope-native", CustomerSessionID: "customer-native"}
	order := completionOrder(t, provider, actor, "native-demo")
	repository := NewPostgresRepository(provider)
	service := NewService(Dependencies{Repository: repository, Bank: SDKBank{}}, Options{})
	view, err := service.Init(ctx, actor, "qr:"+order.ID, order.ID)
	if err != nil || view.State != "pending" {
		t.Fatalf("QR initialization: %+v, %v", view, err)
	}
	// A newly constructed worker has no browser/session reference and recovers
	// the durable registration solely from PostgreSQL.
	worker := NewConfirmationWorker(NewPostgresRepository(provider), SDKChecker{}, nil, ConfirmationOptions{MockAutoPayAfter: time.Nanosecond})
	if worked, err := worker.ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("independent confirmation: %v, %v", worked, err)
	}
	message := VerifiedSBPNotification{EventKey: "duplicate-demo", CustomerCode: "customer-781898", MerchantID: "merchant-781898", QRCID: "mock-" + view.PaymentID,
		TransactionID: "mock-" + view.PaymentID, RefTransactionID: "mock-" + view.PaymentID, PaymentType: "sbpPayment", AmountKopecks: 12345, Payload: json.RawMessage(`{"source":"verified_test"}`)}
	for i := 0; i < 2; i++ {
		if err := repository.Receive(ctx, profileID, message); err != nil {
			t.Fatal(err)
		}
	}
	var group sync.WaitGroup
	for i := 0; i < 6; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := NewPostgresRepository(provider).ConfirmDemo(ctx, view.PaymentID); err != nil {
				t.Errorf("duplicate confirmation: %v", err)
			}
		}()
	}
	group.Wait()
	var fiscalCount, crmCount int
	if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM public.fiscal_receipts WHERE order_id=$1),(SELECT count(*) FROM public.yclients_jobs WHERE order_id=$1)`, order.ID).Scan(&fiscalCount, &crmCount); err != nil {
		t.Fatal(err)
	}
	if fiscalCount != 1 || crmCount != 1 {
		t.Fatalf("duplicate completion jobs: fiscal=%d CRM=%d", fiscalCount, crmCount)
	}
	crm := settlement.NewWorker(settlement.NewPostgresRepository(provider), completionNoSettlement{t}, settlement.Options{})
	if worked, err := crm.ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("CRM demo: %v, %v", worked, err)
	}
	receipts := fiscal.NewWorker(fiscal.NewPostgresRepository(provider), completionNoReceipt{t}, fiscal.Options{})
	if worked, err := receipts.ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("receipt demo: %v, %v", worked, err)
	}
	view, err = NewService(Dependencies{Repository: NewPostgresRepository(provider), Bank: SDKBank{}}, Options{}).Get(ctx, actor, view.PaymentID)
	if err != nil || view.State != "paid" || view.OrderState != "paid" || view.YClientsSyncStatus != "demo" || view.FiscalStatus != "demo" || view.PaidAt == nil {
		t.Fatalf("completed view: %+v, %v", view, err)
	}
	if view.Receipt == nil || view.Receipt.Kind != "demo" || view.Receipt.Currency != "RUB" || view.Receipt.TotalKopecks != 12345 ||
		len(view.Receipt.Items) != 1 || view.Receipt.Items[0].Name != "Integration good" || view.Receipt.Items[0].AmountKopecks != 12345 ||
		view.Receipt.DateTime != "" || view.Receipt.FiscalSign != nil || view.Receipt.DocumentNumber != nil {
		t.Fatalf("demo receipt: %+v", view.Receipt)
	}
	other := actor
	other.CustomerSessionID = "next-customer"
	_, err = service.Get(ctx, other, view.PaymentID)
	recorder := httptest.NewRecorder()
	webapp.WriteServiceError(recorder, err)
	if recorder.Code != 404 {
		t.Fatalf("another customer read payment: status=%d error=%v", recorder.Code, err)
	}
	// Paid is monotonic even after a stale negative bank observation.
	if _, err = conn.Exec(ctx, `UPDATE public.payments SET confirmation_claimed_at=now() WHERE id=$1`, view.PaymentID); err != nil {
		t.Fatal(err)
	}
	attempt, err := repository.Get(ctx, ownerFor(actor), view.PaymentID)
	if err != nil {
		t.Fatal(err)
	}
	var claim time.Time
	if err = conn.QueryRow(ctx, `SELECT confirmation_claimed_at FROM public.payments WHERE id=$1`, view.PaymentID).Scan(&claim); err != nil {
		t.Fatal(err)
	}
	profile, err := frozenConfirmationProfile(attempt)
	if err != nil {
		t.Fatal(err)
	}
	if err = repository.Observe(ctx, ConfirmationCandidate{Attempt: attempt, Profile: profile, ClaimedAt: claim}, BankObservation{QRCID: attempt.QRCID, Status: "Rejected"}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = conn.QueryRow(ctx, `SELECT state FROM public.payments WHERE id=$1`, view.PaymentID).Scan(&state); err != nil || state != "paid" {
		t.Fatalf("paid regressed: %s, %v", state, err)
	}
}

func TestCompletionIntegrationWebhookIsolationAndClaimRecovery(t *testing.T) {
	provider, conn := completionTestDatabase(t)
	ctx := context.Background()
	profileID := completionProfile(t, conn, 781898, "mock")
	actor := checkout.Actor{CompanyID: 781898, Mode: "public", ClientID: 42, ScopeID: "scope-native", CustomerSessionID: "customer-native"}
	order := completionOrder(t, provider, actor, "native-isolation")
	repository := NewPostgresRepository(provider)
	view, err := NewService(Dependencies{Repository: repository, Bank: SDKBank{}}, Options{}).Init(ctx, actor, "qr:"+order.ID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	message := VerifiedSBPNotification{EventKey: "wrong-amount", CustomerCode: "customer-781898", MerchantID: "merchant-781898", QRCID: "mock-" + view.PaymentID,
		TransactionID: "provider-id", RefTransactionID: "provider-id", PaymentType: "sbpPayment", AmountKopecks: 12346, Payload: json.RawMessage(`{"source":"verified_wrong_amount"}`)}
	if err = repository.Receive(ctx, profileID, message); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = conn.QueryRow(ctx, `SELECT state FROM public.payments WHERE id=$1`, view.PaymentID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("wrong amount credited: %s %v", state, err)
	}
	message.EventKey = "wrong-merchant"
	message.AmountKopecks = 12345
	message.MerchantID = "other-merchant"
	message.Payload = json.RawMessage(`{"source":"verified_wrong_merchant"}`)
	if err = repository.Receive(ctx, profileID, message); err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRow(ctx, `SELECT state FROM public.payments WHERE id=$1`, view.PaymentID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("wrong merchant credited: %s %v", state, err)
	}
	if _, err = repository.UpdateExpired(ctx, view.PaymentID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = repository.ConfirmDemo(ctx, view.PaymentID); err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRow(ctx, `SELECT state FROM public.payments WHERE id=$1`, view.PaymentID).Scan(&state); err != nil || state != "paid" {
		t.Fatalf("late payment not accepted: %s %v", state, err)
	}
	crmRepo := settlement.NewPostgresRepository(provider)
	jobs := make(chan settlement.Job, 6)
	var group sync.WaitGroup
	for i := 0; i < 6; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			job, err := crmRepo.Claim(ctx)
			if err == nil {
				jobs <- job
			} else if !errors.Is(err, settlement.ErrNoJob) {
				t.Errorf("claim: %v", err)
			}
		}()
	}
	group.Wait()
	close(jobs)
	var original settlement.Job
	count := 0
	for job := range jobs {
		count++
		original = job
	}
	if count != 1 {
		t.Fatalf("multiple workers claimed one job: %d", count)
	}
	if _, err = conn.Exec(ctx, `UPDATE public.yclients_jobs SET claimed_at=now()-interval '11 minutes' WHERE order_id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	recovered, err := crmRepo.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = crmRepo.Finish(ctx, original, "demo", ""); err == nil {
		t.Fatal("stale worker overwrote recovered claim")
	}
	if err = crmRepo.Finish(ctx, recovered, "demo", ""); err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRow(ctx, `SELECT yclients_sync_status FROM public.payment_orders WHERE id=$1`, order.ID).Scan(&state); err != nil || state != "demo" {
		t.Fatalf("recovery outcome: %s %v", state, err)
	}
}

func TestCompletionIntegrationEarlyWebhookAndProfileIsolation(t *testing.T) {
	provider, conn := completionTestDatabase(t)
	ctx := context.Background()
	profileA := completionProfile(t, conn, 781898, "production")
	actor := checkout.Actor{CompanyID: 781898, Mode: "public", ClientID: 42, ScopeID: "scope-native", CustomerSessionID: "customer-native"}
	orderA := completionOrder(t, provider, actor, "early-webhook")
	repository := NewPostgresRepository(provider)
	reservationA, err := repository.Reserve(ctx, ownerFor(actor), orderA.ID, "qr:"+orderA.ID, strings.Repeat("a", 64), time.Now().UTC())
	if err != nil || !reservationA.Created {
		t.Fatalf("first registration: %+v %v", reservationA, err)
	}
	// The same merchant can have several independently bound profiles. A signed
	// callback delivered to the other profile must not cross that boundary.
	profileB := completionProfile(t, conn, 781898, "production")
	orderB := completionOrder(t, provider, actor, "other-profile")
	reservationB, err := repository.Reserve(ctx, ownerFor(actor), orderB.ID, "qr:"+orderB.ID, strings.Repeat("c", 64), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	message := VerifiedSBPNotification{EventKey: "before-qr-result", CustomerCode: "customer-781898", MerchantID: "merchant-781898", QRCID: "native-production-qr-A",
		TransactionID: "native-bank-operation-A", RefTransactionID: "native-bank-reference-A", PaymentType: "sbpPayment", AmountKopecks: 12345,
		Purpose: "Checkout " + orderA.ID, Payload: json.RawMessage(`{"source":"early_verified_callback"}`)}
	if err := repository.Receive(ctx, profileB, message); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := conn.QueryRow(ctx, `SELECT state FROM public.payments WHERE id=$1`, reservationA.Attempt.ID).Scan(&state); err != nil || state != "creating" {
		t.Fatalf("other profile changed registration: %s %v", state, err)
	}
	// Generic webhook matching correlates the signed purpose with one durable
	// registration while qrc_id is still absent, then survives process restart.
	if err := NewPostgresRepository(provider).ReceiveAny(ctx, message); err != nil {
		t.Fatal(err)
	}
	if err := NewPostgresRepository(provider).ReceiveAny(ctx, message); err != nil {
		t.Fatal(err)
	}
	result, err := renderQR(message.QRCID, "https://example.invalid/native-payment", BankRequest{StartedAt: time.Now().UTC(), TTLMinutes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Complete(ctx, reservationA.Attempt.ID, result); err != nil {
		t.Fatal(err)
	}
	if err := repository.Fail(ctx, reservationA.Attempt.ID, true, "late_transport_timeout"); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT state FROM public.payments WHERE id=$1`, reservationB.Attempt.ID).Scan(&state); err != nil || state != "creating" {
		t.Fatalf("other profile was credited: %s %v", state, err)
	}
	var orderState, paymentState, inboxState string
	var fiscalCount, crmCount, profileEvents int
	if err := conn.QueryRow(ctx, `SELECT o.state,p.state,(SELECT state FROM public.bank_webhook_inbox WHERE event_key=$2),
		(SELECT count(*) FROM public.fiscal_receipts WHERE order_id=o.id),(SELECT count(*) FROM public.yclients_jobs WHERE order_id=o.id),
		(SELECT count(*) FROM public.payment_events WHERE payment_profile_id=$3 AND processing_state='processed')
		FROM public.payment_orders o JOIN public.payments p ON p.order_id=o.id WHERE p.id=$1`, reservationA.Attempt.ID, message.EventKey, profileA).Scan(&orderState, &paymentState, &inboxState, &fiscalCount, &crmCount, &profileEvents); err != nil {
		t.Fatal(err)
	}
	if orderState != "paid" || paymentState != "paid" || inboxState != "processed" || fiscalCount != 1 || crmCount != 1 || profileEvents != 1 {
		t.Fatalf("early completion: order=%s payment=%s inbox=%s fiscal=%d CRM=%d events=%d", orderState, paymentState, inboxState, fiscalCount, crmCount, profileEvents)
	}
}

func TestCompletionIntegrationFiscalClaimRecovery(t *testing.T) {
	provider, conn := completionTestDatabase(t)
	ctx := context.Background()
	completionProfile(t, conn, 781898, "mock")
	actor := checkout.Actor{CompanyID: 781898, Mode: "public", ClientID: 42, ScopeID: "scope-native", CustomerSessionID: "customer-native"}
	order := completionOrder(t, provider, actor, "fiscal-recovery")
	repository := NewPostgresRepository(provider)
	view, err := NewService(Dependencies{Repository: repository, Bank: SDKBank{}}, Options{}).Init(ctx, actor, "qr:"+order.ID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmDemo(ctx, view.PaymentID); err != nil {
		t.Fatal(err)
	}
	fiscalRepo := fiscal.NewPostgresRepository(provider)
	jobs := make(chan fiscal.Job, 6)
	var group sync.WaitGroup
	for i := 0; i < 6; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			job, err := fiscalRepo.Claim(ctx)
			if err == nil {
				jobs <- job
			} else if !errors.Is(err, fiscal.ErrNoJob) {
				t.Errorf("receipt claim: %v", err)
			}
		}()
	}
	group.Wait()
	close(jobs)
	var original fiscal.Job
	count := 0
	for job := range jobs {
		count++
		original = job
	}
	if count != 1 {
		t.Fatalf("multiple receipt claims: %d", count)
	}
	// Simulate a crash before dispatch: the immutable payload and request ID
	// remain attached to the job while a replacement worker takes the lease.
	payload, err := fiscal.BuildPayloadWithContact(original.Order, original.Profile, original.CustomerContact)
	if err != nil {
		t.Fatal(err)
	}
	if err := fiscalRepo.SavePayload(ctx, original, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE public.fiscal_receipts SET claimed_at=now()-interval '31 seconds' WHERE order_id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	recovered, err := fiscalRepo.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.RequestID != original.RequestID || len(recovered.Payload) == 0 || recovered.DispatchStartedAt != nil {
		t.Fatal("receipt recovery lost its frozen request")
	}
	if err := fiscalRepo.BeginDispatch(ctx, original); err == nil {
		t.Fatal("stale receipt worker dispatched after replacement")
	}
	if err := fiscalRepo.Finish(ctx, original, "demo", nil, ""); err == nil {
		t.Fatal("stale receipt worker overwrote replacement")
	}
	if err := fiscalRepo.Finish(ctx, recovered, "demo", json.RawMessage(`{"demo":true}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := fiscalRepo.Claim(ctx); !errors.Is(err, fiscal.ErrNoJob) {
		t.Fatalf("completed receipt was reclaimed: %v", err)
	}
}

func TestCompletionIntegrationAtomicEnqueueAndMigrationRoundTrip(t *testing.T) {
	provider, conn := completionTestDatabase(t)
	ctx := context.Background()
	completionProfile(t, conn, 781898, "mock")
	actor := checkout.Actor{CompanyID: 781898, Mode: "public", ClientID: 42, ScopeID: "scope-native", CustomerSessionID: "customer-native"}
	order := completionOrder(t, provider, actor, "atomic-enqueue")
	repository := NewPostgresRepository(provider)
	view, err := NewService(Dependencies{Repository: repository, Bank: SDKBank{}}, Options{}).Init(ctx, actor, "qr:"+order.ID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	// An enqueue failure must roll back the order, payment, audit and other
	// follow-up job together; a later independent retry can settle the transfer.
	if _, err := conn.Exec(ctx, `CREATE FUNCTION public.integration_reject_job() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'test enqueue interruption'; END; $$ LANGUAGE plpgsql;
		CREATE TRIGGER integration_reject_job BEFORE INSERT ON public.yclients_jobs FOR EACH ROW EXECUTE FUNCTION public.integration_reject_job()`); err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmDemo(ctx, view.PaymentID); err == nil {
		t.Fatal("enqueue failure committed payment")
	}
	var orderState, paymentState string
	var receiptCount, crmCount int
	if err := conn.QueryRow(ctx, `SELECT o.state,p.state,(SELECT count(*) FROM public.fiscal_receipts WHERE order_id=o.id),(SELECT count(*) FROM public.yclients_jobs WHERE order_id=o.id)
		FROM public.payment_orders o JOIN public.payments p ON p.order_id=o.id WHERE p.id=$1`, view.PaymentID).Scan(&orderState, &paymentState, &receiptCount, &crmCount); err != nil {
		t.Fatal(err)
	}
	if orderState != "payment_pending" || paymentState != "pending" || receiptCount != 0 || crmCount != 0 {
		t.Fatalf("partial payment commit: order=%s payment=%s receipts=%d CRM=%d", orderState, paymentState, receiptCount, crmCount)
	}
	if _, err := conn.Exec(ctx, `DROP TRIGGER integration_reject_job ON public.yclients_jobs; DROP FUNCTION public.integration_reject_job()`); err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmDemo(ctx, view.PaymentID); err != nil {
		t.Fatal(err)
	}
	// Verify that the reversible migration is executable with real paid rows,
	// and its forward backfill recreates the durable completion queue.
	for _, migration := range []string{"000009_card_checkout.down.sql", "000008_payment_completion.down.sql", "000008_payment_completion.up.sql", "000009_card_checkout.up.sql"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "migrations", migration))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(body)); err != nil {
			t.Fatalf("migration roundtrip %s: %v", migration, err)
		}
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM public.yclients_jobs WHERE order_id=$1`, order.ID).Scan(&crmCount); err != nil || crmCount != 1 {
		t.Fatalf("paid migration backfill: count=%d error=%v", crmCount, err)
	}
}

func TestCompletionIntegrationOperatorSettlementRecovery(t *testing.T) {
	provider, conn := completionTestDatabase(t)
	ctx := context.Background()
	profileID := completionProfile(t, conn, 781898, "production")
	var crmProfileID int64
	if err := conn.QueryRow(ctx, `INSERT INTO public.yclients_profiles(company_id,name,mode,cashless_account_id,storage_id) VALUES(781898,'Integration live settlement','live',2,3) RETURNING id`).Scan(&crmProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE public.salons SET default_yclients_profile_id=$1 WHERE company_id=781898`, crmProfileID); err != nil {
		t.Fatal(err)
	}
	actor := checkout.Actor{CompanyID: 781898, Mode: "public", ClientID: 42, ScopeID: "scope-native", CustomerSessionID: "customer-native"}
	order := completionOrder(t, provider, actor, "operator-recovery")
	repository := NewPostgresRepository(provider)
	registration, err := repository.Reserve(ctx, ownerFor(actor), order.ID, "qr:"+order.ID, strings.Repeat("a", 64), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	message := VerifiedSBPNotification{EventKey: "operator-recovery", CustomerCode: "customer-781898", MerchantID: "merchant-781898", QRCID: "native-operator-recovery",
		TransactionID: "native-recovery-operation", RefTransactionID: "native-recovery-reference", PaymentType: "sbpPayment", AmountKopecks: 12345, Purpose: "Checkout " + order.ID, Payload: json.RawMessage(`{"source":"operator_recovery_fixture"}`)}
	if err := repository.Receive(ctx, profileID, message); err != nil {
		t.Fatal(err)
	}
	crmRepo := settlement.NewPostgresRepository(provider)
	job, err := crmRepo.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	op := settlement.Operation{ID: "account-payment", Kind: "goods_payment", State: "pending", Request: json.RawMessage(`{"account_id":2,"amount":123.45}`)}
	if err := crmRepo.SavePlan(ctx, job, []settlement.Operation{op}); err != nil {
		t.Fatal(err)
	}
	proof := settlement.Outcome{State: "succeeded", Result: json.RawMessage(`{"document_id":987,"transaction_ids":[654],"operator_evidence":{"reason":"Confirmed matching cashless transaction in the YCLIENTS journal","verified_at":"2026-11-03T12:00:00Z"}}`)}
	if err := crmRepo.ResolveOperation(ctx, order.ID, op.ID, proof); err == nil {
		t.Fatal("operator replaced an active processing lease")
	}
	if err := crmRepo.BeginDispatch(ctx, job, op.ID); err != nil {
		t.Fatal(err)
	}
	if err := crmRepo.SaveOutcome(ctx, job, op, settlement.Outcome{State: "unknown", ErrorCode: "simulated_lost_response"}); err != nil {
		t.Fatal(err)
	}
	if err := crmRepo.Finish(ctx, job, "unknown", "simulated_lost_response"); err != nil {
		t.Fatal(err)
	}
	if err := crmRepo.ResolveOperation(ctx, order.ID, op.ID, settlement.Outcome{State: "succeeded", Result: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("operator resolution accepted missing evidence")
	}
	if err := crmRepo.ResolveOperation(ctx, order.ID, op.ID, settlement.Outcome{State: "succeeded", Result: json.RawMessage(`{"document_id":987,"transaction_ids":[654],"operator_evidence":{}}`)}); err == nil {
		t.Fatal("operator resolution accepted empty evidence")
	}
	loaded, err := crmRepo.LoadJob(ctx, order.ID)
	if err != nil || len(loaded.Operations) != 1 || loaded.Operations[0].State != "unknown" {
		t.Fatalf("operator journal: %+v %v", loaded, err)
	}
	if err := crmRepo.ResolveOperation(ctx, order.ID, op.ID, proof); err != nil {
		t.Fatal(err)
	}
	var orderState, jobState, paymentState string
	if err := conn.QueryRow(ctx, `SELECT o.state,j.state,p.state FROM public.payment_orders o JOIN public.yclients_jobs j ON j.order_id=o.id JOIN public.payments p ON p.id=j.payment_id WHERE o.id=$1`, order.ID).Scan(&orderState, &jobState, &paymentState); err != nil {
		t.Fatal(err)
	}
	if orderState != "paid" || paymentState != "paid" || jobState != "pending" {
		t.Fatalf("operator recovery changed payment: order=%s job=%s payment=%s", orderState, jobState, paymentState)
	}
	// A live worker resumes its frozen successful operation without Plan,
	// Execute or Reconcile. In particular it cannot POST a second payment.
	worker := settlement.NewWorker(crmRepo, completionNoSettlement{t}, settlement.Options{AllowLive: true})
	if worked, err := worker.ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("resume recovered settlement: %v %v", worked, err)
	}
	if err := conn.QueryRow(ctx, `SELECT yclients_sync_status FROM public.payment_orders WHERE id=$1`, order.ID).Scan(&jobState); err != nil || jobState != "succeeded" {
		t.Fatalf("operator settlement outcome: %s %v", jobState, err)
	}
	if registration.Attempt.ID == "" {
		t.Fatal("registration fixture was empty")
	}
}
