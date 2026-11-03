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

func (r *paymentHTTPRepository) ReserveCard(ctx context.Context, owner checkout.Owner, id, key, hash string, now time.Time) (payment.Reservation, error) {
	for _, attempt := range r.attempts {
		if attempt.OrderID == id && attempt.Method != "card" {
			return payment.Reservation{}, webapp.Conflict("another payment method is active", nil)
		}
	}
	result, err := r.Reserve(ctx, owner, id, key, hash, now)
	if err != nil {
		return result, err
	}
	result.Attempt.Method = "card"
	r.attempts[result.Attempt.ID] = result.Attempt
	result.Profile.CardsEnabled = true
	result.Profile.AcquiringMerchantID = "200000000001056"
	result.Profile.CardTTLMinutes = 5
	return result, nil
}

func (r *paymentHTTPRepository) CompleteCard(_ context.Context, id string, value payment.CardResult) error {
	item := r.attempts[id]
	item.State = "pending"
	item.OperationID, item.Payload, item.ExpiresAt = value.OperationID, value.CheckoutURL, &value.ExpiresAt
	r.attempts[id] = item
	return nil
}

type cardHTTPBank struct {
	calls       int
	afterCreate func()
}

func (b *cardHTTPBank) CreateCard(ctx context.Context, request payment.BankRequest) (payment.CardResult, error) {
	b.calls++
	if b.afterCreate != nil {
		b.afterCreate()
	}
	return (payment.SDKCardBank{}).CreateCard(ctx, request)
}

func cardRuntime(t *testing.T) (*testRuntime, *cardHTTPBank, *paymentHTTPRepository, models.SessionResponse, checkout.Order) {
	t.Helper()
	deps, _, orders := checkoutDependencies()
	repo := &paymentHTTPRepository{orders: orders, attempts: map[string]payment.Attempt{}}
	bank := &cardHTTPBank{}
	deps.Payment = &payment.Dependencies{Repository: repo, CardBank: bank, Tokens: paymentHTTPToken{}, CreatingTimeout: time.Minute}
	r := newRuntime(&fakeBackend{clientResult: goyclients.Client{ID: 45}}, deps)
	if res := r.request(http.MethodPost, "/salons/salon-one/session", `{}`, ""); res.Code != http.StatusOK {
		t.Fatalf("select salon: %d %s", res.Code, res.Body)
	}
	response := r.request(http.MethodPost, "/customer/session", `{"phone":"79991234567"}`, "")
	var current models.SessionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &current); err != nil || current.Customer == nil {
		t.Fatalf("customer: %d %s", response.Code, response.Body)
	}
	order := decodeCheckout(t, r.checkoutRequest(http.MethodPost, "/customer/checkouts", checkoutCatalogBody, current.Customer.SessionID, "card-order"))
	return r, bank, repo, current, order
}

func TestCardHTTPAuthoritativeIdempotentAndCustomerScoped(t *testing.T) {
	r, bank, _, current, order := cardRuntime(t)
	body := `{"checkout_id":"` + order.ID + `"}`
	first := r.checkoutRequest(http.MethodPost, "/customer/init-card-pay", body, current.Customer.SessionID, "card:"+order.ID)
	var view payment.View
	if err := json.Unmarshal(first.Body.Bytes(), &view); err != nil || first.Code != http.StatusOK {
		t.Fatalf("create card: %d %s", first.Code, first.Body)
	}
	if view.Method != "card" || view.State != "pending" || view.AmountKopecks != order.PayableKopecks || view.CheckoutURL == "" || view.QRImageURL != "" || view.Payload != "" {
		t.Fatalf("wrong card view: %+v", view)
	}
	for _, secret := range []string{"demo-customer", "200000000001056", "demo.jwt"} {
		if strings.Contains(first.Body.String(), secret) {
			t.Fatal("bank configuration leaked")
		}
	}
	retry := r.checkoutRequest(http.MethodPost, "/customer/init-card-pay", body, current.Customer.SessionID, "new-browser-key")
	if retry.Code != http.StatusOK || bank.calls != 1 {
		t.Fatalf("retry: %d calls=%d", retry.Code, bank.calls)
	}
	read := r.request(http.MethodGet, "/customer/payments/"+view.PaymentID+"?success=true&status=APPROVED", "", current.Customer.SessionID)
	var persisted payment.View
	_ = json.Unmarshal(read.Body.Bytes(), &persisted)
	if read.Code != http.StatusOK || persisted.State != "pending" || bank.calls != 1 {
		t.Fatalf("browser return affected confirmation: %d %s", read.Code, read.Body)
	}
	if res := r.request(http.MethodGet, "/kiosk/payments/"+view.PaymentID, "", current.Customer.SessionID); res.Code != http.StatusUnauthorized {
		t.Fatalf("public customer used kiosk alias: %d", res.Code)
	}
	next := r.request(http.MethodPost, "/customer/session", `{"guest":true}`, "")
	var replaced models.SessionResponse
	_ = json.Unmarshal(next.Body.Bytes(), &replaced)
	if res := r.request(http.MethodGet, "/customer/payments/"+view.PaymentID, "", replaced.Customer.SessionID); res.Code != http.StatusNotFound {
		t.Fatalf("next customer recovered card checkout: %d %s", res.Code, res.Body)
	}
}

func TestCardHTTPRejectsOverridesAndChangedSession(t *testing.T) {
	r, bank, repo, current, order := cardRuntime(t)
	for _, body := range []string{
		`{"checkout_id":"` + order.ID + `","amount":1}`,
		`{"checkout_id":"` + order.ID + `","redirect_url":"https://attacker.invalid"}`,
		`{"checkout_id":"` + order.ID + `","company_id":99}`,
	} {
		if res := r.checkoutRequest(http.MethodPost, "/customer/init-card-pay", body, current.Customer.SessionID, "card-key"); res.Code != http.StatusBadRequest {
			t.Fatalf("accepted unsafe input: %d %s", res.Code, res.Body)
		}
	}
	body := `{"checkout_id":"` + order.ID + `"}`
	if res := r.checkoutRequest(http.MethodPost, "/customer/init-card-pay", body, current.Customer.SessionID, ""); res.Code != http.StatusBadRequest || bank.calls != 0 {
		t.Fatalf("missing key reached bank: %d calls=%d", res.Code, bank.calls)
	}
	bank.afterCreate = func() {
		if res := r.request(http.MethodPost, "/customer/session", `{"guest":true}`, ""); res.Code != http.StatusOK {
			t.Fatal(res.Body)
		}
	}
	res := r.checkoutRequest(http.MethodPost, "/customer/init-card-pay", body, current.Customer.SessionID, "card-key")
	if res.Code != http.StatusUnauthorized || strings.Contains(res.Body.String(), "checkout_url") || bank.calls != 1 {
		t.Fatalf("late card result leaked: %d %s calls=%d", res.Code, res.Body, bank.calls)
	}
	for _, attempt := range repo.attempts {
		if attempt.State != "pending" || attempt.OperationID == "" {
			t.Fatal("authorized operation was lost when customer changed")
		}
	}
}
