package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckoutBinderRejectsFinancialAndIdentityOverrides(t *testing.T) {
	for _, body := range []string{
		`{"amount":1,"items":[]}`,
		`{"company_id":99,"items":[]}`,
		`{"client_id":99,"items":[]}`,
		`{"items":[{"source":"catalog","kind":"good","id":1,"staff_id":5,"quantity":"1","price":1}]}`,
		`{"items":[{"source":"catalog","kind":"good","id":1,"staff_id":5,"quantity":1}]}`,
		`{} {}`,
		`null`,
		`[]`,
	} {
		r := httptest.NewRequest(http.MethodPost, "/customer/checkouts", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if _, err := checkoutPrepareBinder(r); err == nil {
			t.Errorf("accepted unsafe checkout body: %s", body)
		}
	}
}

func TestCheckoutBinderRetainsOnlySelectionAndCorrelation(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/customer/checkouts", strings.NewReader(`{"visit_id":12,"record_id":34,"items":[{"source":"visit","kind":"good","id":2,"line_id":3,"staff_id":5,"quantity":"1.5"}],"bonus_to_apply_kopecks":0}`))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	r.Header.Set("X-Customer-Session", "customer-nonce")
	r.Header.Set("Idempotency-Key", "checkout-key")
	value, err := checkoutPrepareBinder(r)
	if err != nil {
		t.Fatal(err)
	}
	input := value.(checkoutPrepareInput)
	if input.Nonce != "customer-nonce" || input.IdempotencyKey != "checkout-key" || input.Request.VisitID != 12 || input.Request.RecordID != 34 || len(input.Request.Items) != 1 || input.Request.Items[0].Quantity != "1.5" {
		t.Fatalf("checkout selection changed: %+v", input)
	}
}

func TestCheckoutAndQRRequireBoundedJSONObject(t *testing.T) {
	for _, bind := range []func(*http.Request) (any, error){checkoutPrepareBinder, kioskQRBinder} {
		for _, body := range []string{`{}`, strings.Repeat(" ", (1<<20)+1)} {
			r := httptest.NewRequest(http.MethodPost, "/customer/checkouts", strings.NewReader(body))
			r.Header.Set("Content-Type", "text/plain")
			if _, err := bind(r); err == nil {
				t.Fatal("accepted non-JSON checkout request")
			}
			r = httptest.NewRequest(http.MethodPost, "/customer/checkouts", strings.NewReader(strings.Repeat(" ", (1<<20)+1)))
			r.Header.Set("Content-Type", "application/json")
			if _, err := bind(r); err == nil {
				t.Fatal("accepted oversized checkout request")
			}
		}
	}
}

func TestQRBinderRequiresCheckoutInsteadOfClientAmount(t *testing.T) {
	for _, body := range []string{`{"amount":125.50}`, `{"checkout_id":"a","client_id":3}`, `{"checkout_id":"a","amount":1}`} {
		r := httptest.NewRequest(http.MethodPost, "/customer/init-qr-pay", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if _, err := kioskQRBinder(r); err == nil {
			t.Errorf("accepted an untrusted QR amount or client: %s", body)
		}
	}
}
