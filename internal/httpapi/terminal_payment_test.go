package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/pronogti/internal/payment"
	"github.com/dronm/pronogti/internal/terminalbridge"
	"github.com/dronm/webapp"
)

type terminalPaymentFixture struct {
	payments             *paymentHTTPRepository
	initiated, preflight int
	failPreflight        bool
	afterPreflight       func()
	id                   string
	commandState         string
}

func (f *terminalPaymentFixture) ExistingAttempt(ctx context.Context, owner checkout.Owner, id, key string) (string, error) {
	if f.id == "" {
		return "", nil
	}
	if owner.CompanyID != f.payments.owner.CompanyID || owner.CustomerSessionHash != f.payments.owner.CustomerSessionHash || owner.ScopeHash != f.payments.owner.ScopeHash {
		return "", webapp.NotFound("payment not found", nil)
	}
	if f.payments.attempts[f.id].OrderID != id {
		return "", webapp.NotFound("payment not found", nil)
	}
	return f.id, nil
}
func (f *terminalPaymentFixture) Initiate(ctx context.Context, owner checkout.Owner, id, key string) (string, error) {
	order, err := f.payments.orders.Get(ctx, id)
	if err != nil {
		return "", err
	}
	f.initiated++
	f.id = "8257d169-828c-4d1b-97ec-30d1c4329dd6"
	f.commandState = "queued"
	f.payments.owner = owner
	f.payments.attempts[f.id] = payment.Attempt{ID: f.id, OrderID: id, CompanyID: owner.CompanyID, Method: terminalbridge.Method, State: "pending", AmountKopecks: order.PayableKopecks, Currency: order.Currency, Environment: "mock"}
	return f.id, nil
}
func (f *terminalPaymentFixture) PaymentDetails(context.Context, string) (terminalbridge.TerminalDetails, error) {
	return terminalbridge.TerminalDetails{CommandID: "16d7bdbd-03cd-451b-b001-e3713253824c", TerminalID: "terminal-1", State: f.commandState}, nil
}
func (f *terminalPaymentFixture) ExpirePayment(context.Context, string) (bool, error) {
	return false, nil
}

func terminalPaymentRuntime(t *testing.T) (*testRuntime, *terminalPaymentFixture, models.SessionResponse, checkout.Order) {
	t.Helper()
	deps, _, orders := checkoutDependencies()
	fixture := &terminalPaymentFixture{payments: &paymentHTTPRepository{orders: orders, attempts: make(map[string]payment.Attempt)}}
	deps.Payment = &payment.Dependencies{Repository: fixture.payments, ValidatePayable: func(ctx context.Context, order checkout.Order) error {
		fixture.preflight++
		if fixture.afterPreflight != nil {
			fixture.afterPreflight()
		}
		if fixture.failPreflight {
			return webapp.Conflict("visit has changed", nil)
		}
		return nil
	}}
	deps.TerminalBridge = fixture
	deps.TerminalEnabled = true
	runtime := newRuntime(&fakeBackend{}, deps)
	runtime.bind(t)
	current := runtime.start(t, `{"guest":true}`)
	order := decodeCheckout(t, runtime.checkoutRequest(http.MethodPost, "/kiosk/checkouts", checkoutCatalogBody, current.Customer.SessionID, "terminal-checkout"))
	return runtime, fixture, current, order
}

func TestTerminalHTTPUsesAuthoritativeCheckoutAndRecoversPaidExpiredRetry(t *testing.T) {
	runtime, fixture, current, order := terminalPaymentRuntime(t)
	body := `{"checkout_id":"` + order.ID + `"}`
	response := runtime.checkoutRequest(http.MethodPost, "/kiosk/init-terminal-pay", body, current.Customer.SessionID, "terminal-payment")
	var view payment.View
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &view) != nil {
		t.Fatalf("init=%d %s", response.Code, response.Body)
	}
	if view.Method != "terminal_card" || view.AmountKopecks != order.PayableKopecks || view.TerminalID != "terminal-1" || view.CommandID == "" || view.CheckoutURL != "" || view.Payload != "" || view.QRImageURL != "" {
		t.Fatalf("invalid terminal view: %+v", view)
	}
	if fixture.initiated != 1 || fixture.preflight != 1 {
		t.Fatal("payment was not validated and reserved once")
	}
	item := fixture.payments.attempts[fixture.id]
	item.State = "paid"
	fixture.payments.attempts[fixture.id] = item
	order.State = "paid"
	order.ExpiresAt = time.Now().Add(-time.Hour)
	fixture.payments.orders.orders[order.ID] = order
	fixture.commandState = "approved"
	fixture.failPreflight = true
	response = runtime.checkoutRequest(http.MethodPost, "/kiosk/me/init-terminal-pay", body, current.Customer.SessionID, "different-browser-key")
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &view) != nil || view.State != "paid" || fixture.initiated != 1 || fixture.preflight != 1 {
		t.Fatalf("paid retry was repriced or redispatched: %d %s", response.Code, response.Body)
	}
	if response := runtime.checkoutRequest(http.MethodPost, "/customer/init-terminal-pay", body, current.Customer.SessionID, "key"); response.Code != 404 {
		t.Fatalf("public terminal route exposed: %d", response.Code)
	}
}

func TestTerminalHTTPRejectsOverridesAndChangedCustomerBeforeReservation(t *testing.T) {
	runtime, fixture, current, order := terminalPaymentRuntime(t)
	for _, extra := range []string{`,"amount_kopecks":1`, `,"terminal_id":"other-device"`, `,"company_id":99`, `,"currency":"USD"`} {
		response := runtime.checkoutRequest(http.MethodPost, "/kiosk/init-terminal-pay", `{"checkout_id":"`+order.ID+`"`+extra+`}`, current.Customer.SessionID, "key")
		if response.Code != 400 {
			t.Fatalf("override accepted: %d %s", response.Code, response.Body)
		}
	}
	fixture.afterPreflight = func() { runtime.start(t, `{"guest":true}`) }
	response := runtime.checkoutRequest(http.MethodPost, "/kiosk/init-terminal-pay", `{"checkout_id":"`+order.ID+`"}`, current.Customer.SessionID, "key")
	if response.Code != 401 || fixture.initiated != 0 {
		t.Fatalf("changed customer initiated terminal: %d %s", response.Code, response.Body)
	}
}
