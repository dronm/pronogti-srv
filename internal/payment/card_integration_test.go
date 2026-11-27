package payment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/fiscal"
	"github.com/dronm/pronogti/internal/settlement"
	"github.com/jackc/pgx/v5"
)

const cardIntegrationMerchant = "200000000001056"
const cardIntegrationCustomer = "300000092"

type cardIntegrationTokens struct{}

func (cardIntegrationTokens) Token(context.Context, Profile) (string, error) {
	return "phony.jwt.token", nil
}

func cardIntegrationProfile(t *testing.T, conn *pgx.Conn, endpoint string) int64 {
	t.Helper()
	id := completionProfile(t, conn, 781898, "mock")
	_, err := conn.Exec(context.Background(), `UPDATE public.payment_profiles SET
		api_base_url=$2,customer_code=$3,cards_enabled=true,acquiring_merchant_id=$4,
		card_ttl_minutes=15,card_redirect_url='https://frontend.invalid/pay-bank-card',
		card_fail_redirect_url='https://frontend.invalid/pay-bank-card' WHERE id=$1`, id, endpoint, cardIntegrationCustomer, cardIntegrationMerchant)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func cardIntegrationActor() checkout.Actor {
	return checkout.Actor{CompanyID: 781898, Mode: "public", ClientID: 42, ScopeID: "card-scope", CustomerSessionID: "card-customer"}
}

func cardIntegrationCounts(t *testing.T, conn *pgx.Conn, orderID string) (string, int, int) {
	t.Helper()
	var state string
	var receipts, crm int
	err := conn.QueryRow(context.Background(), `SELECT state,
		(SELECT count(*) FROM public.fiscal_receipts WHERE order_id=$1),
		(SELECT count(*) FROM public.yclients_jobs WHERE order_id=$1)
		FROM public.payment_orders WHERE id=$1`, orderID).Scan(&state, &receipts, &crm)
	if err != nil {
		t.Fatal(err)
	}
	return state, receipts, crm
}

func cardIntegrationDue(t *testing.T, conn *pgx.Conn, id string) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), `UPDATE public.payments SET next_check_at=NULL,confirmation_claimed_at=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}

func TestCardIntegrationKioskAvailabilityUsesEffectiveProfile(t *testing.T) {
	provider, conn := completionTestDatabase(t)
	ctx := context.Background()
	const companyID = 781898
	kioskProfileID := completionProfile(t, conn, companyID, "mock")
	cardIntegrationProfile(t, conn, "http://127.0.0.1:8099")

	var kioskID int64
	if err := conn.QueryRow(ctx, `INSERT INTO public.kiosks (name,company_id,token_hash)
		VALUES ('Test kiosk',$1,repeat('a',64)) RETURNING id`, companyID).Scan(&kioskID); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(provider)
	check := func(step string, want bool) {
		t.Helper()
		enabled, err := repository.OnlineCardEnabled(ctx, companyID, &kioskID)
		if err != nil || enabled != want {
			t.Fatalf("%s: online card enabled = %v, want %v (err=%v)", step, enabled, want, err)
		}
	}
	check("salon default allows online cards", true)
	if _, err := conn.Exec(ctx, `UPDATE public.kiosks SET payment_profile_id=$1 WHERE id=$2`, kioskProfileID, kioskID); err != nil {
		t.Fatal(err)
	}
	check("kiosk profile overrides enabled salon default", false)
	if _, err := conn.Exec(ctx, `UPDATE public.payment_profiles SET cards_enabled=true, acquiring_merchant_id=$2 WHERE id=$1`, kioskProfileID, cardIntegrationMerchant); err != nil {
		t.Fatal(err)
	}
	check("online card enabled on kiosk profile", true)
	if _, err := conn.Exec(ctx, `UPDATE public.payment_profiles SET enabled=false WHERE id=$1`, kioskProfileID); err != nil {
		t.Fatal(err)
	}
	check("disabled kiosk profile does not fall back to salon", false)
}

func TestCardIntegrationIndependentDemoCompletion(t *testing.T) {
	provider, conn := completionTestDatabase(t)
	cardIntegrationProfile(t, conn, "http://127.0.0.1:8099")
	ctx := context.Background()
	actor := cardIntegrationActor()
	order := completionOrder(t, provider, actor, "card-demo-checkout")
	repository := NewPostgresRepository(provider)
	service := NewService(Dependencies{Repository: repository, CardBank: SDKCardBank{}}, Options{})
	view, err := service.InitCard(ctx, actor, "card:"+order.ID, order.ID)
	if err != nil || view.Method != "card" || view.State != "pending" || view.CheckoutURL == "" || view.QRImageURL != "" {
		t.Fatalf("card initialization: %+v, %v", view, err)
	}
	state, receipts, crm := cardIntegrationCounts(t, conn, order.ID)
	if state != "payment_pending" || receipts != 0 || crm != 0 {
		t.Fatalf("creating a link completed money: %s receipts=%d CRM=%d", state, receipts, crm)
	}
	// This fresh worker has no customer session or browser state.
	worker := NewCardConfirmationWorker(NewPostgresRepository(provider), SDKCardChecker{}, nil, ConfirmationOptions{MockAutoPayAfter: time.Nanosecond})
	if worked, err := worker.ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("independent card worker: %v, %v", worked, err)
	}
	if worked, err := worker.ProcessOne(ctx); err != nil || worked {
		t.Fatalf("paid card was claimed again: %v, %v", worked, err)
	}
	state, receipts, crm = cardIntegrationCounts(t, conn, order.ID)
	if state != "paid" || receipts != 1 || crm != 1 {
		t.Fatalf("completion jobs: %s receipts=%d CRM=%d", state, receipts, crm)
	}
	if worked, err := settlement.NewWorker(settlement.NewPostgresRepository(provider), completionNoSettlement{t}, settlement.Options{}).ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("card CRM demo: %v, %v", worked, err)
	}
	if worked, err := fiscal.NewWorker(fiscal.NewPostgresRepository(provider), completionNoReceipt{t}, fiscal.Options{}).ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("card receipt demo: %v, %v", worked, err)
	}
	view, err = service.Get(ctx, actor, view.PaymentID)
	if err != nil || view.State != "paid" || view.OrderState != "paid" || view.YClientsSyncStatus != "demo" || view.FiscalStatus != "demo" || view.PaidAt == nil || view.CheckoutURL != "" {
		t.Fatalf("paid card view: %+v, %v", view, err)
	}
	other := actor
	other.CustomerSessionID = "another-card-customer"
	if _, err = service.Get(ctx, other, view.PaymentID); err == nil {
		t.Fatal("another customer recovered a paid card attempt")
	}
}

type cardIntegrationBank struct {
	mu                         sync.Mutex
	status, merchant, customer string
	amount                     json.Number
	attemptID, purpose         string
	posts, gets                int
	loseCreate                 bool
}

func (b *cardIntegrationBank) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/acquiring/v1.0/payments") {
		b.posts++
		var request struct {
			Data struct {
				Amount           json.Number `json:"amount"`
				Purpose          string      `json:"purpose"`
				PaymentLinkID    string      `json:"paymentLinkId"`
				PaymentMode      []string    `json:"paymentMode"`
				PreAuthorization bool        `json:"preAuthorization"`
			} `json:"Data"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Data.PaymentMode) != 1 || request.Data.PaymentMode[0] != "card" || request.Data.PreAuthorization || request.Data.Amount.String() != "123.45" {
			http.Error(w, "invalid card request", http.StatusBadRequest)
			return
		}
		b.attemptID, b.purpose = request.Data.PaymentLinkID, request.Data.Purpose
		if b.loseCreate {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":"503","message":"simulated lost creation response"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Data": b.payment("CREATED")})
		return
	}
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/acquiring/v1.0/payments/card-integration-operation") {
		b.gets++
		_ = json.NewEncoder(w).Encode(map[string]any{"Data": map[string]any{"Operation": []any{b.payment(b.status)}}})
		return
	}
	http.NotFound(w, r)
}

func (b *cardIntegrationBank) payment(status string) map[string]any {
	return map[string]any{"operationId": "card-integration-operation", "paymentLink": "https://bank.invalid/pay/card-integration-operation", "status": status,
		"amount": b.amount, "merchantId": b.merchant, "customerCode": b.customer, "paymentLinkId": b.attemptID, "purpose": b.purpose,
		"paymentMode": []string{"card"}, "paymentType": "card", "paymentId": "card-integration-payment", "transactionId": "card-integration-transaction"}
}

func (b *cardIntegrationBank) set(status, merchant, customer, amount string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status, b.merchant, b.customer, b.amount = status, merchant, customer, json.Number(amount)
}

func cardIntegrationExternal(t *testing.T, loseCreate bool) (ds.Provider, *pgx.Conn, *PostgresRepository, *Service, *cardIntegrationBank, checkout.Actor, checkout.Order, int64) {
	t.Helper()
	provider, conn := completionTestDatabase(t)
	bank := &cardIntegrationBank{loseCreate: loseCreate}
	bank.set("CREATED", cardIntegrationMerchant, cardIntegrationCustomer, "123.45")
	server := httptest.NewServer(bank)
	t.Cleanup(server.Close)
	profileID := cardIntegrationProfile(t, conn, server.URL)
	repository := NewPostgresRepository(provider)
	service := NewService(Dependencies{Repository: repository, CardBank: SDKCardBank{MockExternal: true}, Tokens: cardIntegrationTokens{}, MockExternal: true}, Options{})
	actor := cardIntegrationActor()
	order := completionOrder(t, provider, actor, "card-external-checkout")
	return provider, conn, repository, service, bank, actor, order, profileID
}

func TestCardIntegrationBankApprovalAndIdentityIsolation(t *testing.T) {
	_, conn, repository, service, bank, actor, order, _ := cardIntegrationExternal(t, false)
	ctx := context.Background()
	view, err := service.InitCard(ctx, actor, "card:"+order.ID, order.ID)
	if err != nil || view.State != "pending" {
		t.Fatalf("external card initialization: %+v, %v", view, err)
	}
	worker := NewCardConfirmationWorker(repository, SDKCardChecker{MockExternal: true}, cardIntegrationTokens{}, ConfirmationOptions{MockExternal: true})
	for _, test := range []struct{ name, status, merchant, customer, amount string }{
		{"authorized only", "AUTHORIZED", cardIntegrationMerchant, cardIntegrationCustomer, "123.45"},
		{"partial payment", "WAIT_FULL_PAYMENT", cardIntegrationMerchant, cardIntegrationCustomer, "123.45"},
		{"wrong merchant", "APPROVED", "200000000009999", cardIntegrationCustomer, "123.45"},
		{"wrong customer", "APPROVED", cardIntegrationMerchant, "300000099", "123.45"},
		{"wrong amount", "APPROVED", cardIntegrationMerchant, cardIntegrationCustomer, "123.46"},
	} {
		t.Run(test.name, func(t *testing.T) {
			bank.set(test.status, test.merchant, test.customer, test.amount)
			cardIntegrationDue(t, conn, view.PaymentID)
			if worked, err := worker.ProcessOne(ctx); err != nil || !worked {
				t.Fatalf("bank read: %v, %v", worked, err)
			}
			state, receipts, crm := cardIntegrationCounts(t, conn, order.ID)
			if state != "payment_pending" || receipts != 0 || crm != 0 {
				t.Fatalf("unverified card credited: %s receipts=%d CRM=%d", state, receipts, crm)
			}
		})
	}
	bank.set("APPROVED", cardIntegrationMerchant, cardIntegrationCustomer, "123.45")
	cardIntegrationDue(t, conn, view.PaymentID)
	if worked, err := worker.ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("approved bank read: %v, %v", worked, err)
	}
	state, receipts, crm := cardIntegrationCounts(t, conn, order.ID)
	if state != "paid" || receipts != 1 || crm != 1 {
		t.Fatalf("approved completion: %s receipts=%d CRM=%d", state, receipts, crm)
	}
	bank.mu.Lock()
	posts, gets := bank.posts, bank.gets
	bank.mu.Unlock()
	if posts != 1 || gets != 6 {
		t.Fatalf("unexpected bank calls: POST=%d GET=%d", posts, gets)
	}
}

func TestCardIntegrationConcurrentQRAndCardReservation(t *testing.T) {
	provider, conn := completionTestDatabase(t)
	cardIntegrationProfile(t, conn, "http://127.0.0.1:8099")
	actor := cardIntegrationActor()
	order := completionOrder(t, provider, actor, "card-qr-race-checkout")
	repository := NewPostgresRepository(provider)
	ctx := context.Background()
	var group sync.WaitGroup
	var mu sync.Mutex
	created, conflicts := 0, 0
	for index := 0; index < 12; index++ {
		group.Add(1)
		go func(card bool) {
			defer group.Done()
			var reservation Reservation
			var err error
			if card {
				reservation, err = repository.ReserveCard(ctx, ownerFor(actor), order.ID, "card:"+order.ID, strings.Repeat("c", 64), time.Now().UTC())
			} else {
				reservation, err = repository.Reserve(ctx, ownerFor(actor), order.ID, "qr:"+order.ID, strings.Repeat("d", 64), time.Now().UTC())
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				conflicts++
			} else if reservation.Created {
				created++
			}
		}(index%2 == 0)
	}
	group.Wait()
	var count int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM public.payments WHERE order_id=$1`, order.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if created != 1 || count != 1 || conflicts != 6 {
		t.Fatalf("cross-method reservation: created=%d rows=%d conflicts=%d", created, count, conflicts)
	}
}

func TestCardIntegrationLostCreationAndVerifiedCallbackRecovery(t *testing.T) {
	_, conn, repository, service, bank, actor, order, _ := cardIntegrationExternal(t, true)
	ctx := context.Background()
	view, err := service.InitCard(ctx, actor, "card:"+order.ID, order.ID)
	if err != nil || view.State != "unknown" {
		t.Fatalf("lost creation: %+v, %v", view, err)
	}
	reused, err := service.InitCard(ctx, actor, "another-key", order.ID)
	if err != nil || reused.PaymentID != view.PaymentID || reused.State != "unknown" {
		t.Fatalf("lost-response retry changed attempt: %+v, %v", reused, err)
	}
	message := VerifiedCardNotification{EventKey: "card-early-approved", CustomerCode: cardIntegrationCustomer, MerchantID: cardIntegrationMerchant,
		OperationID: "card-integration-operation", PaymentLinkID: view.PaymentID, PaymentType: "card", Status: "APPROVED", AmountKopecks: 12345,
		Payload: json.RawMessage(`{"webhookType":"acquiringInternetPayment","source":"verified_test"}`)}
	// Wrong profile paths cannot attach an operation to a different merchant.
	otherProfileID := completionProfile(t, conn, 781898, "mock")
	if err := repository.ReceiveCard(ctx, otherProfileID, message); err != nil {
		t.Fatal(err)
	}
	var operationID string
	if err := conn.QueryRow(ctx, `SELECT COALESCE(acquiring_operation_id,'') FROM public.payments WHERE id=$1`, view.PaymentID).Scan(&operationID); err != nil {
		t.Fatal(err)
	}
	if operationID != "" {
		t.Fatal("wrong profile recovered the operation")
	}
	for index := 0; index < 2; index++ {
		if err := repository.ReceiveCard(ctx, 0, message); err != nil {
			t.Fatal(err)
		}
	}
	state, receipts, crm := cardIntegrationCounts(t, conn, order.ID)
	if state != "payment_pending" || receipts != 0 || crm != 0 {
		t.Fatalf("callback itself credited payment: %s receipts=%d CRM=%d", state, receipts, crm)
	}
	bank.set("APPROVED", cardIntegrationMerchant, cardIntegrationCustomer, "123.45")
	worker := NewCardConfirmationWorker(repository, SDKCardChecker{MockExternal: true}, cardIntegrationTokens{}, ConfirmationOptions{MockExternal: true})
	if worked, err := worker.ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("recovered card bank read: %v, %v", worked, err)
	}
	state, receipts, crm = cardIntegrationCounts(t, conn, order.ID)
	if state != "paid" || receipts != 1 || crm != 1 {
		t.Fatalf("recovered completion: %s receipts=%d CRM=%d", state, receipts, crm)
	}
	bank.mu.Lock()
	posts := bank.posts
	bank.mu.Unlock()
	if posts != 1 {
		t.Fatalf("lost bank request was repeated %d times", posts)
	}
}

type cardIntegrationCreateApproved struct{}

func (cardIntegrationCreateApproved) CreateCard(_ context.Context, request BankRequest) (CardResult, error) {
	// Even an unexpected APPROVED in the creation response is not sufficient
	// evidence: independent detail confirmation must happen first.
	return CardResult{OperationID: "card-production-operation", CheckoutURL: "https://bank.invalid/checkout", Status: "APPROVED", ExpiresAt: request.StartedAt.Add(time.Duration(request.TTLMinutes) * time.Minute)}, nil
}

type cardIntegrationApprovedChecker struct{}

func (cardIntegrationApprovedChecker) Check(_ context.Context, candidate ConfirmationCandidate, _ string) (CardObservation, error) {
	return CardObservation{OperationID: candidate.Attempt.OperationID, Status: "APPROVED", CustomerCode: candidate.Profile.CustomerCode,
		MerchantID: candidate.Profile.AcquiringMerchantID, AmountKopecks: candidate.Attempt.AmountKopecks, Paid: true, PaymentType: "card", PaymentID: "production-payment", TransactionID: "production-transaction"}, nil
}

type cardIntegrationLiveCRM struct {
	t             *testing.T
	plans, writes int
}

func (p *cardIntegrationLiveCRM) Plan(_ context.Context, job settlement.Job) ([]settlement.Operation, error) {
	p.plans++
	if job.Order.PayableKopecks != 12345 || job.Config.CompanyID != 781898 || job.Config.CashlessAccountID != 77 || job.Config.StorageID != 88 || job.Environment != "production" || job.PaidAt.IsZero() {
		p.t.Fatalf("wrong frozen CRM job: %+v", job)
	}
	return []settlement.Operation{{ID: "goods-create", Kind: "goods_create", State: "pending", Request: json.RawMessage(`{"amount_kopecks":12345}`)}, {ID: "goods-payment", Kind: "goods_payment", State: "pending", Request: json.RawMessage(`{"amount_kopecks":12345}`)}}, nil
}
func (p *cardIntegrationLiveCRM) Execute(_ context.Context, _ settlement.Job, _ settlement.Operation) (settlement.Outcome, error) {
	p.writes++
	return settlement.Outcome{State: "succeeded", Result: json.RawMessage(`{"document_id":100,"transaction_ids":[200]}`)}, nil
}
func (p *cardIntegrationLiveCRM) Reconcile(context.Context, settlement.Job, settlement.Operation) (settlement.Outcome, error) {
	p.t.Fatal("a completed card CRM operation was replayed")
	return settlement.Outcome{}, nil
}

func TestCardIntegrationProductionQueuesLiveDownstreamAdapters(t *testing.T) {
	provider, conn := completionTestDatabase(t)
	profileID := cardIntegrationProfile(t, conn, "https://bank.invalid")
	ctx := context.Background()
	if _, err := conn.Exec(ctx, `UPDATE public.payment_profiles SET environment='production' WHERE id=$1`, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE public.yclients_profiles SET mode='live',cashless_account_id=77,storage_id=88 WHERE company_id=781898`); err != nil {
		t.Fatal(err)
	}
	receiptCalls := 0
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receiptCalls++
		var request struct {
			DeviceID  string          `json:"deviceId"`
			Command   string          `json:"cmd"`
			RequestID string          `json:"requestId"`
			Params    json.RawMessage `json:"params"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/commands" || json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid receipt command")
			http.Error(w, "invalid command", http.StatusBadRequest)
			return
		}
		if request.DeviceID != "card-kkt" || request.Command != "createReceipt" || !strings.HasPrefix(request.RequestID, "pronogti-sale-") || !strings.Contains(string(request.Params), `"cashlessKopecks":12345`) || !strings.Contains(string(request.Params), `"customerContact":"79990000000"`) {
			t.Errorf("wrong card receipt payload: %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"deviceId": request.DeviceID, "cmd": request.Command, "requestId": request.RequestID, "status": "completed",
			"data": map[string]any{"shiftOpenedByRequest": true, "receipt": map[string]any{"receiptNumber": 1, "documentNumber": 10, "fiscalSign": 123456, "dateTime": "2026-11-03T10:00", "shiftNumber": 2}}})
	}))
	t.Cleanup(device.Close)
	if _, err := conn.Exec(ctx, `UPDATE public.fiscal_profiles SET mode='live',driver_base_url=$1,device_id='card-kkt' WHERE company_id=781898`, device.URL); err != nil {
		t.Fatal(err)
	}
	actor := cardIntegrationActor()
	order := completionOrder(t, provider, actor, "card-live-checkout")
	repository := NewPostgresRepository(provider)
	service := NewService(Dependencies{Repository: repository, CardBank: cardIntegrationCreateApproved{}, Tokens: cardIntegrationTokens{}}, Options{})
	var emails atomic.Int32
	service.dependencies.ReceiptEmail = &ReceiptEmailDependencies{
		RegistrationNumber: func(baseURL, deviceID string) string {
			if baseURL == device.URL && deviceID == "card-kkt" {
				return "0000000000000123"
			}
			return ""
		},
		Send: func(_ context.Context, email, link, dateTime string) error {
			if email != "customer@example.test" || link != "https://saby.ru/ofd/check/rec/0000000000000123/031126/123456" ||
				dateTime != "2026-11-03T10:00" {
				t.Errorf("wrong receipt email: %q %q %q", email, link, dateTime)
			}
			emails.Add(1)
			return nil
		},
	}
	view, err := service.InitCard(ctx, actor, "card:"+order.ID, order.ID)
	if err != nil || view.State != "pending" {
		t.Fatalf("card creation response credited payment: %+v, %v", view, err)
	}
	qrRequireStatus(t, service.SendReceiptEmail(ctx, actor, view.PaymentID, "customer@example.test"), 409)
	crm := &cardIntegrationLiveCRM{t: t}
	crmWorker := settlement.NewWorker(settlement.NewPostgresRepository(provider), crm, settlement.Options{AllowLive: true})
	receiptWorker := fiscal.NewWorker(fiscal.NewPostgresRepository(provider), fiscal.HTTPDriver{}, fiscal.Options{AllowLive: true})
	if worked, err := crmWorker.ProcessOne(ctx); err != nil || worked {
		t.Fatalf("CRM ran before bank credit: %v %v", worked, err)
	}
	if worked, err := receiptWorker.ProcessOne(ctx); err != nil || worked {
		t.Fatalf("receipt ran before bank credit: %v %v", worked, err)
	}
	worker := NewCardConfirmationWorker(repository, cardIntegrationApprovedChecker{}, cardIntegrationTokens{}, ConfirmationOptions{})
	if worked, err := worker.ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("production card confirmation: %v %v", worked, err)
	}
	for _, run := range []func(context.Context) (bool, error){crmWorker.ProcessOne, receiptWorker.ProcessOne} {
		if worked, err := run(ctx); err != nil || !worked {
			t.Fatalf("downstream completion: %v %v", worked, err)
		}
		if worked, err := run(ctx); err != nil || worked {
			t.Fatalf("downstream work was repeated: %v %v", worked, err)
		}
	}
	view, err = service.Get(ctx, actor, view.PaymentID)
	if err != nil || view.State != "paid" || view.YClientsSyncStatus != "succeeded" || view.FiscalStatus != "issued" || crm.plans != 1 || crm.writes != 2 || receiptCalls != 1 {
		t.Fatalf("card live completion: %+v err=%v CRM plans=%d writes=%d receipts=%d", view, err, crm.plans, crm.writes, receiptCalls)
	}
	if view.Receipt == nil || view.Receipt.Kind != "fiscal" || !view.Receipt.EmailAvailable || view.Receipt.DateTime != "2026-11-03T10:00" ||
		view.Receipt.FiscalSign == nil || *view.Receipt.FiscalSign != 123456 || view.Receipt.DocumentNumber == nil || *view.Receipt.DocumentNumber != 10 ||
		len(view.Receipt.Items) != 1 || view.Receipt.Items[0].Quantity != "1" || view.Receipt.Items[0].UnitPriceKopecks != 12345 {
		t.Fatalf("live receipt: %+v", view.Receipt)
	}
	other := actor
	other.CustomerSessionID = "another-customer-session"
	qrRequireStatus(t, service.SendReceiptEmail(ctx, other, view.PaymentID, "customer@example.test"), 404)
	qrRequireStatus(t, service.SendReceiptEmail(ctx, actor, view.PaymentID, "bad@example.test\r\nBcc: x@example.test"), 400)
	var sends sync.WaitGroup
	outcomes := make(chan error, 5)
	for range 5 {
		sends.Go(func() {
			outcomes <- service.SendReceiptEmail(ctx, actor, view.PaymentID, "customer@example.test")
		})
	}
	sends.Wait()
	close(outcomes)
	rejected := 0
	for err := range outcomes {
		if err != nil {
			qrRequireStatus(t, err, 429)
			rejected++
		}
	}
	if emails.Load() != 1 || rejected != 4 {
		t.Fatalf("concurrent sends=%d rejected=%d", emails.Load(), rejected)
	}
	for range 2 {
		if _, err := conn.Exec(ctx, `UPDATE public.fiscal_receipts SET email_attempted_at=now()-interval '31 seconds' WHERE payment_id=$1`, view.PaymentID); err != nil {
			t.Fatal(err)
		}
		if err := service.SendReceiptEmail(ctx, actor, view.PaymentID, "customer@example.test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(ctx, `UPDATE public.fiscal_receipts SET email_attempted_at=now()-interval '31 seconds' WHERE payment_id=$1`, view.PaymentID); err != nil {
		t.Fatal(err)
	}
	qrRequireStatus(t, service.SendReceiptEmail(ctx, actor, view.PaymentID, "customer@example.test"), 429)
	if emails.Load() != 3 {
		t.Fatalf("receipt cap failed: sent=%d", emails.Load())
	}
}

func TestCardIntegrationClaimFencingAndDuplicateConfirmation(t *testing.T) {
	provider, conn := completionTestDatabase(t)
	cardIntegrationProfile(t, conn, "http://127.0.0.1:8099")
	ctx := context.Background()
	actor := cardIntegrationActor()
	order := completionOrder(t, provider, actor, "card-fencing-checkout")
	repository := NewPostgresRepository(provider)
	view, err := NewService(Dependencies{Repository: repository, CardBank: SDKCardBank{}}, Options{}).InitCard(ctx, actor, "card:"+order.ID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	var mu sync.Mutex
	var first ConfirmationCandidate
	claims := 0
	for index := 0; index < 8; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			candidate, err := NewPostgresRepository(provider).ClaimCardConfirmation(ctx)
			if err == nil {
				mu.Lock()
				claims++
				first = candidate
				mu.Unlock()
			} else if err != ErrNoConfirmation {
				t.Errorf("concurrent card claim: %v", err)
			}
		}()
	}
	group.Wait()
	if claims != 1 {
		t.Fatalf("concurrent workers claimed one card %d times", claims)
	}
	if _, err := conn.Exec(ctx, `UPDATE public.payments SET confirmation_claimed_at=now()-interval '11 minutes',next_check_at=NULL WHERE id=$1`, view.PaymentID); err != nil {
		t.Fatal(err)
	}
	second, err := NewPostgresRepository(provider).ClaimCardConfirmation(ctx)
	if err != nil || !second.ClaimedAt.After(first.ClaimedAt) {
		t.Fatalf("restarted card claim: %+v %v", second, err)
	}
	observation := CardObservation{OperationID: first.Attempt.OperationID, Status: "APPROVED", CustomerCode: first.Profile.CustomerCode,
		MerchantID: first.Profile.AcquiringMerchantID, AmountKopecks: first.Attempt.AmountKopecks, Paid: true, PaymentType: "card", PaymentID: "fencing-payment", TransactionID: "fencing-transaction"}
	if err := repository.ObserveCard(ctx, first, observation); err == nil {
		t.Fatal("stale card worker completed a newer claim")
	}
	for index := 0; index < 6; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := NewPostgresRepository(provider).ObserveCard(ctx, second, observation); err != nil {
				t.Errorf("duplicate card confirmation: %v", err)
			}
		}()
	}
	group.Wait()
	state, receipts, crm := cardIntegrationCounts(t, conn, order.ID)
	if state != "paid" || receipts != 1 || crm != 1 {
		t.Fatalf("duplicate card jobs: %s receipts=%d CRM=%d", state, receipts, crm)
	}
	observation.Paid, observation.Status = false, "EXPIRED"
	if err := repository.ObserveCard(ctx, first, observation); err != nil {
		t.Fatal(err)
	}
	state, _, _ = cardIntegrationCounts(t, conn, order.ID)
	if state != "paid" {
		t.Fatal("a late negative card observation reversed bank credit")
	}
}

func TestCardIntegrationMigrationRoundtripAndPaidIdentityProtection(t *testing.T) {
	// Exercise migration 9 without later administrative views that legitimately
	// reference its columns; production rollback must retain those dependencies.
	provider, conn := completionTestDatabaseThrough(t, 9)
	ctx := context.Background()
	for _, direction := range []string{"down", "up"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000009_card_checkout."+direction+".sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(body)); err != nil {
			t.Fatalf("migration9 %s: %v", direction, err)
		}
	}
	cardIntegrationProfile(t, conn, "http://127.0.0.1:8099")
	actor := cardIntegrationActor()
	order := completionOrder(t, provider, actor, "card-migration-checkout")
	view, err := NewService(Dependencies{Repository: NewPostgresRepository(provider), CardBank: SDKCardBank{}}, Options{}).InitCard(ctx, actor, "card:"+order.ID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000009_card_checkout.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(body)); err == nil {
		t.Fatal("rollback discarded an existing card operation identity")
	}
	if _, err := conn.Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	var method, operationID string
	if err := conn.QueryRow(ctx, `SELECT method,acquiring_operation_id FROM public.payments WHERE id=$1`, view.PaymentID).Scan(&method, &operationID); err != nil || method != "card" || operationID == "" {
		t.Fatalf("protected card identity: %q %q %v", method, operationID, err)
	}
}
