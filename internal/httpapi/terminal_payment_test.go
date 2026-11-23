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
	resolutionPending    bool
}

func (f *terminalPaymentFixture) Availability(ctx context.Context, owner checkout.Owner, id string) (terminalbridge.Availability, error) {
	result := terminalbridge.Availability{TerminalCard: terminalbridge.TerminalAvailability{Configured: true, Available: true, Status: "ready", Environment: "mock"}}
	if id != "" {
		if _, err := f.payments.orders.Get(ctx, id); err != nil {
			return result, err
		}
		if f.id != "" {
			if _, err := f.ExistingAttempt(ctx, owner, id, "test"); err != nil {
				return result, err
			}
			result.ActivePayment = &terminalbridge.ActivePayment{PaymentID: f.id, CheckoutID: id, Method: "terminal_card"}
		}
	}
	return result, nil
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
	return terminalbridge.TerminalDetails{CommandID: "16d7bdbd-03cd-451b-b001-e3713253824c", TerminalID: "terminal-1", State: f.commandState, ResolutionPending: f.resolutionPending}, nil
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

func TestTerminalHTTPAvailabilityRequiresKioskCustomerAndSupportsRecovery(t *testing.T) {
	runtime, fixture, current, order := terminalPaymentRuntime(t)
	path := "/kiosk/payment-methods?checkout_id=" + order.ID
	for _, test := range []struct {
		path, nonce string
		status      int
	}{
		{path, "", 401},
		{path, current.Customer.SessionID, 200},
		{"/kiosk/me/payment-methods?checkout_id=" + order.ID, current.Customer.SessionID, 200},
		{"/kiosk/payment-methods?checkout_id=one&checkout_id=two", current.Customer.SessionID, 400},
		{"/kiosk/payment-methods?unexpected=1", current.Customer.SessionID, 400},
		{"/customer/payment-methods", current.Customer.SessionID, 404},
	} {
		response := runtime.checkoutRequest(http.MethodGet, test.path, "", test.nonce, "")
		if response.Code != test.status {
			t.Fatalf("availability=%d %s expected=%d", response.Code, response.Body, test.status)
		}
	}
	response := runtime.checkoutRequest(http.MethodPost, "/kiosk/init-terminal-pay", `{"checkout_id":"`+order.ID+`"}`, current.Customer.SessionID, "terminal:"+order.ID)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	response = runtime.checkoutRequest(http.MethodGet, path, "", current.Customer.SessionID, "")
	var result terminalbridge.Availability
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.ActivePayment == nil || result.ActivePayment.PaymentID != fixture.id || result.ActivePayment.Method != "terminal_card" {
		t.Fatal("owned recovery unavailable", response.Code, response.Body)
	}
	next := runtime.start(t, `{"guest":true}`)
	response = runtime.checkoutRequest(http.MethodGet, path, "", next.Customer.SessionID, "")
	if response.Code != 404 {
		t.Fatal("next customer recovered old payment", response.Code, response.Body)
	}
}

func TestTerminalHTTPExposesOnlyResolutionPendingFlag(t *testing.T) {
	runtime, fixture, current, order := terminalPaymentRuntime(t)
	response := runtime.checkoutRequest(http.MethodPost, "/kiosk/init-terminal-pay", `{"checkout_id":"`+order.ID+`"}`, current.Customer.SessionID, "terminal:"+order.ID)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	fixture.resolutionPending = true
	response = runtime.checkoutRequest(http.MethodGet, "/kiosk/payments/"+fixture.id, "", current.Customer.SessionID, "")
	var view payment.View
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &view) != nil || !view.TerminalResolutionPending {
		t.Fatal("resolution acknowledgement state missing", response.Code, response.Body)
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document["error_message"]; ok {
		t.Fatal("raw terminal errors exposed")
	}
}

func terminalHTTPDependencies(t *testing.T, runtime *testRuntime) *CheckoutDependencies {
	t.Helper()
	definition, err := webapp.LookupService("Kiosk")
	if err != nil {
		t.Fatal(err)
	}
	return definition.Factory(webapp.ServiceContext{Session: runtime.sess}).(*service).checkout
}

func TestTerminalHTTPDisabledFeatureStillRecoversPendingAndPaidAttempts(t *testing.T) {
	runtime, fixture, current, order := terminalPaymentRuntime(t)
	body := `{"checkout_id":"` + order.ID + `"}`
	response := runtime.checkoutRequest(http.MethodPost, "/kiosk/init-terminal-pay", body, current.Customer.SessionID, "terminal:"+order.ID)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	terminalHTTPDependencies(t, runtime).TerminalEnabled = false
	fixture.failPreflight = true
	for _, state := range []string{"pending", "paid"} {
		attempt := fixture.payments.attempts[fixture.id]
		attempt.State = state
		fixture.payments.attempts[fixture.id] = attempt
		if state == "paid" {
			order.State = "paid"
			order.ExpiresAt = time.Now().Add(-time.Hour)
			fixture.payments.orders.orders[order.ID] = order
		}
		response = runtime.checkoutRequest(http.MethodPost, "/kiosk/init-terminal-pay", body, current.Customer.SessionID, "recover-after-disable")
		var view payment.View
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &view) != nil || view.State != state || view.PaymentID != fixture.id || fixture.initiated != 1 || fixture.preflight != 1 {
			t.Fatal("disabled gate lost existing payment", state, response.Code, response.Body)
		}
		response = runtime.checkoutRequest(http.MethodGet, "/kiosk/payment-methods?checkout_id="+order.ID, "", current.Customer.SessionID, "")
		var available terminalbridge.Availability
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &available) != nil || available.ActivePayment == nil || available.ActivePayment.PaymentID != fixture.id {
			t.Fatal("disabled gate lost recovery pointer", response.Code, response.Body)
		}
	}
	terminalHTTPDependencies(t, runtime).TerminalBridge = nil
	response = runtime.checkoutRequest(http.MethodPost, "/kiosk/init-terminal-pay", body, current.Customer.SessionID, "recover-after-store-missing")
	if response.Code != 500 {
		t.Fatal("unavailable store must not claim no operation started", response.Code, response.Body)
	}
}

func TestTerminalHTTPDisabledFeatureAuthenticatesBeforeRejectingNewPayment(t *testing.T) {
	runtime, fixture, current, order := terminalPaymentRuntime(t)
	terminalHTTPDependencies(t, runtime).TerminalEnabled = false
	body := `{"checkout_id":"` + order.ID + `"}`
	response := runtime.checkoutRequest(http.MethodPost, "/kiosk/init-terminal-pay", body, "", "new-payment")
	if response.Code != 401 {
		t.Fatal("disabled gate bypassed authentication", response.Code, response.Body)
	}
	response = runtime.checkoutRequest(http.MethodPost, "/kiosk/init-terminal-pay", body, current.Customer.SessionID, "new-payment")
	var failure map[string]any
	if response.Code != 409 || json.Unmarshal(response.Body.Bytes(), &failure) != nil || fixture.initiated != 0 || fixture.preflight != 0 {
		t.Fatal("new disabled payment was not safely rejected", response.Code, response.Body)
	}
}
