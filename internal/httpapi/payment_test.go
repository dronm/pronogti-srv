package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dronm/goyclients"
	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/pronogti/internal/payment"
	"github.com/dronm/webapp"
)

type paymentHTTPRepository struct {
	orders   *checkoutTestRepository
	attempts map[string]payment.Attempt
	owner    checkout.Owner
}

func (r *paymentHTTPRepository) GetOrder(ctx context.Context, id string) (checkout.Order, error) {
	return r.orders.Get(ctx, id)
}
func (r *paymentHTTPRepository) Reserve(ctx context.Context, owner checkout.Owner, id, key, hash string, now time.Time) (payment.Reservation, error) {
	for _, a := range r.attempts {
		if a.OrderID == id {
			return payment.Reservation{Attempt: a}, nil
		}
	}
	o, err := r.orders.Get(ctx, id)
	if err != nil {
		return payment.Reservation{}, err
	}
	r.owner = owner
	expires := now.Add(5 * time.Minute)
	if o.ExpiresAt.Before(expires) {
		expires = o.ExpiresAt
	}
	a := payment.Attempt{ID: "3d8bc324-1ca2-4e65-87f2-29a8c5d07ba3", OrderID: id, CompanyID: owner.CompanyID, PaymentProfileID: 1, Environment: "mock", State: "creating", AmountKopecks: o.PayableKopecks, Currency: o.Currency, IdempotencyKey: key, RequestHash: hash, ExpiresAt: &expires, CreatedAt: now, UpdatedAt: now}
	r.attempts[a.ID] = a
	profile := payment.Profile{ID: 1, CompanyID: owner.CompanyID, Enabled: true, Environment: "mock", APIBaseURL: "http://127.0.0.1:8099", CustomerCode: "demo-customer", SBPMerchantID: "demo-merchant", AccountID: "40702810900000000001/044525104", QRTTLMinutes: 5, HTTPTimeoutSeconds: 20}
	return payment.Reservation{Attempt: a, Profile: profile, Created: true}, nil
}
func (r *paymentHTTPRepository) Get(_ context.Context, owner checkout.Owner, id string) (payment.Attempt, error) {
	a, ok := r.attempts[id]
	if !ok || owner.CompanyID != r.owner.CompanyID || owner.CustomerSessionHash != r.owner.CustomerSessionHash || owner.ScopeHash != r.owner.ScopeHash {
		return payment.Attempt{}, webapp.NotFound("payment not found", nil)
	}
	return a, nil
}
func (r *paymentHTTPRepository) Complete(_ context.Context, id string, q payment.QRResult) error {
	a := r.attempts[id]
	a.State = "pending"
	a.ExpiresAt = &q.ExpiresAt
	a.QRCID = q.QRCID
	a.Payload = q.Payload
	a.Image = q.Image
	a.ImageMediaType = q.ImageMediaType
	r.attempts[id] = a
	return nil
}
func (r *paymentHTTPRepository) Fail(_ context.Context, id string, unknown bool, code string) error {
	a := r.attempts[id]
	a.State = "failed"
	if unknown {
		a.State = "unknown"
	}
	a.ErrorCode = code
	r.attempts[id] = a
	return nil
}
func (r *paymentHTTPRepository) UpdateExpired(_ context.Context, id string, _ time.Time) (payment.Attempt, error) {
	return r.attempts[id], nil
}
func (r *paymentHTTPRepository) AbandonStale(_ context.Context, id string, _ time.Time) (payment.Attempt, error) {
	return r.attempts[id], nil
}

type paymentHTTPBank struct {
	calls    int
	request  payment.BankRequest
	onCreate func()
}

func (b *paymentHTTPBank) Create(ctx context.Context, req payment.BankRequest) (payment.QRResult, error) {
	b.calls++
	b.request = req
	if b.onCreate != nil {
		b.onCreate()
	}
	return payment.SDKBank{}.Create(ctx, req)
}

type paymentHTTPToken struct{}

func (paymentHTTPToken) Token(context.Context, payment.Profile) (string, error) {
	return "demo.jwt", nil
}

func paymentRuntime(t *testing.T) (*testRuntime, *paymentHTTPBank, *paymentHTTPRepository, models.SessionResponse, checkout.Order) {
	t.Helper()
	dependencies, _, orders := checkoutDependencies()
	repository := &paymentHTTPRepository{orders: orders, attempts: map[string]payment.Attempt{}}
	bank := &paymentHTTPBank{}
	dependencies.Payment = &payment.Dependencies{Repository: repository, Bank: bank, Tokens: paymentHTTPToken{}, CreatingTimeout: time.Minute}
	r := newRuntime(&fakeBackend{clientResult: goyclients.Client{ID: 45}}, dependencies)
	if res := r.request(http.MethodPost, "/salons/salon-one/session", `{}`, ""); res.Code != 200 {
		t.Fatalf("salon: %d %s", res.Code, res.Body)
	}
	res := r.request(http.MethodPost, "/customer/session", `{"phone":"79991234567"}`, "")
	var current models.SessionResponse
	if err := json.Unmarshal(res.Body.Bytes(), &current); err != nil || current.Customer == nil {
		t.Fatalf("customer: %d %s", res.Code, res.Body)
	}
	order := decodeCheckout(t, r.checkoutRequest(http.MethodPost, "/customer/checkouts", checkoutCatalogBody, current.Customer.SessionID, "payment-order"))
	return r, bank, repository, current, order
}

func TestQRHTTPCreatesOnceAndRecoversOwnedAttempt(t *testing.T) {
	r, bank, _, current, order := paymentRuntime(t)
	body := `{"checkout_id":"` + order.ID + `"}`
	res := r.checkoutRequest(http.MethodPost, "/customer/init-qr-pay", body, current.Customer.SessionID, "qr-key")
	if res.Code != 200 {
		t.Fatalf("QR: %d %s", res.Code, res.Body)
	}
	var first payment.View
	if err := json.Unmarshal(res.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.State != "pending" || first.AmountKopecks != 25100 || bank.request.AmountKopecks != 25100 || first.CheckoutID != order.ID || bank.calls != 1 {
		t.Fatalf("QR=%+v bank=%+v", first, bank.request)
	}
	for _, secret := range []string{"demo-customer", "demo-merchant", "demo.jwt", "407028109"} {
		if strings.Contains(res.Body.String(), secret) {
			t.Fatalf("credential or merchant identifier leaked: %s", secret)
		}
	}
	res = r.checkoutRequest(http.MethodPost, "/customer/init-qr-pay", body, current.Customer.SessionID, "different-key")
	if res.Code != 200 || bank.calls != 1 {
		t.Fatalf("retry duplicated bank creation: %d %s calls=%d", res.Code, res.Body, bank.calls)
	}
	res = r.request(http.MethodGet, "/customer/payments/"+first.PaymentID, "", current.Customer.SessionID)
	if res.Code != 200 || bank.calls != 1 {
		t.Fatalf("read queried bank: %d %s", res.Code, res.Body)
	}
	for _, path := range []string{"/kiosk/payments/" + first.PaymentID, "/kiosk/me/payments/" + first.PaymentID} {
		if alias := r.request(http.MethodGet, path, "", current.Customer.SessionID); alias.Code != 401 {
			t.Fatalf("public session used kiosk payment alias: %d %s", alias.Code, alias.Body)
		}
	}
	res = r.request(http.MethodPost, "/customer/session", `{"guest":true}`, "")
	var next models.SessionResponse
	_ = json.Unmarshal(res.Body.Bytes(), &next)
	res = r.request(http.MethodGet, "/customer/payments/"+first.PaymentID, "", next.Customer.SessionID)
	if res.Code != 404 || bank.calls != 1 {
		t.Fatalf("new customer could recover previous QR: %d %s", res.Code, res.Body)
	}
}

func TestQRHTTPRequiresKeyAndRejectsAmountOverrides(t *testing.T) {
	r, bank, _, current, order := paymentRuntime(t)
	for _, body := range []string{`{"checkout_id":"` + order.ID + `","amount":1}`, `{"checkout_id":"` + order.ID + `","company_id":99}`, `{"checkout_id":"` + order.ID + `"}`} {
		res := r.checkoutRequest(http.MethodPost, "/customer/init-qr-pay", body, current.Customer.SessionID, "")
		if res.Code != 400 {
			t.Fatalf("unsafe QR request accepted: %d %s", res.Code, res.Body)
		}
	}
	if bank.calls != 0 {
		t.Fatal("invalid requests reached bank")
	}
}

func TestQRHTTPDoesNotReturnPaymentToReplacedCustomer(t *testing.T) {
	r, bank, repository, current, order := paymentRuntime(t)
	bank.onCreate = func() {
		res := r.request(http.MethodPost, "/customer/session", `{"guest":true}`, "")
		if res.Code != 200 {
			t.Fatalf("replace customer: %d %s", res.Code, res.Body)
		}
	}
	res := r.checkoutRequest(http.MethodPost, "/customer/init-qr-pay", `{"checkout_id":"`+order.ID+`"}`, current.Customer.SessionID, "bank-completes-for-previous-customer")
	if res.Code != 401 || strings.Contains(res.Body.String(), "qr_image_url") || strings.Contains(res.Body.String(), "example.invalid") {
		t.Fatalf("delayed result leaked to another customer: %d %s", res.Code, res.Body)
	}
	if bank.calls != 1 || len(repository.attempts) != 1 {
		t.Fatal("authorized bank operation was duplicated or discarded")
	}
	for _, attempt := range repository.attempts {
		if attempt.State != "pending" {
			t.Fatalf("confirmed QR result was not persisted: %+v", attempt)
		}
	}
}
