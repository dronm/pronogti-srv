package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dronm/goyclients"
	"github.com/dronm/pronogti/internal/models"
)

func TestPublicDiscoveryDoesNotRequireKiosk(t *testing.T) {
	r := newRuntime(&fakeBackend{})
	res := r.request(http.MethodGet, "/session", "", "")
	var current models.SessionResponse
	if err := json.Unmarshal(res.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if res.Code != 200 || current.Kiosk != nil || current.Salon != nil || current.Customer != nil || current.Mode != "" {
		t.Fatalf("anonymous session: %d %s", res.Code, res.Body)
	}
	res = r.request(http.MethodGet, "/salons", "", "")
	var salons []models.SalonIdentity
	if err := json.Unmarshal(res.Body.Bytes(), &salons); err != nil {
		t.Fatal(err)
	}
	if res.Code != 200 || len(salons) != 2 || salons[0].Slug != "salon-one" || salons[1].Slug != "salon-two" || r.backend.call != "" {
		t.Fatalf("salons: %d %s backend=%+v", res.Code, res.Body, r.backend)
	}
	if res := r.request(http.MethodGet, "/kiosk/session", "", ""); res.Code != 401 {
		t.Fatalf("anonymous device access: %d %s", res.Code, res.Body)
	}
	if res := r.request(http.MethodPost, "/customer/session", `{"phone":"79991234567"}`, ""); res.Code != 401 {
		t.Fatalf("customer start without salon: %d %s", res.Code, res.Body)
	}
}

func TestPublicSalonSelectionAndCustomerScope(t *testing.T) {
	r := newRuntime(&fakeBackend{clientResult: goyclients.Client{ID: 45, Name: "Anna"}})
	for _, path := range []string{"/salons/missing/session", "/salons/disabled-salon/session"} {
		if res := r.request(http.MethodPost, path, `{}`, ""); res.Code != 404 {
			t.Fatalf("unavailable salon %s: %d %s", path, res.Code, res.Body)
		}
	}
	res := r.request(http.MethodPost, "/salons/salon-one/session?company_id=99", `{"company_id":99}`, "")
	var selected models.SessionResponse
	if err := json.Unmarshal(res.Body.Bytes(), &selected); err != nil {
		t.Fatal(err)
	}
	if res.Code != 200 || selected.Mode != "public" || selected.Kiosk != nil || selected.Salon == nil || selected.Salon.CompanyID != 12 || selected.Customer != nil {
		t.Fatalf("selected salon: %d %s", res.Code, res.Body)
	}
	res = r.request(http.MethodPost, "/customer/session", `{"phone":"79991234567","company_id":99,"client_id":999}`, "")
	var started models.SessionResponse
	if err := json.Unmarshal(res.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if res.Code != 200 || started.Customer == nil || started.Customer.ClientID != 45 || r.backend.companyID != 12 {
		t.Fatalf("public start: %d %s backend=%+v", res.Code, res.Body, r.backend)
	}
	nonce := started.Customer.SessionID
	res = r.request(http.MethodGet, "/customer/me/pay-draft?company_id=99&client_id=999", "", nonce)
	if res.Code != 200 || r.backend.companyID != 12 || r.backend.clientID != 45 {
		t.Fatalf("public draft: %d %s backend=%+v", res.Code, res.Body, r.backend)
	}
	if res := r.request(http.MethodGet, "/kiosk/me/pay-draft", "", nonce); res.Code != 401 {
		t.Fatalf("public session accepted as kiosk: %d %s", res.Code, res.Body)
	}
	if res := r.request(http.MethodPost, "/customer/session/touch", `{}`, nonce); res.Code != 200 {
		t.Fatalf("public touch: %d %s", res.Code, res.Body)
	}
	if res := r.request(http.MethodDelete, "/customer/session", "", nonce); res.Code != 200 {
		t.Fatalf("public logout: %d %s", res.Code, res.Body)
	}
	res = r.request(http.MethodGet, "/session", "", "")
	if err := json.Unmarshal(res.Body.Bytes(), &selected); err != nil {
		t.Fatal(err)
	}
	if res.Code != 200 || selected.Customer != nil || selected.Mode != "public" || selected.Salon == nil || selected.Salon.CompanyID != 12 {
		t.Fatalf("public postlogout: %d %s", res.Code, res.Body)
	}
}

func TestSalonSwitchInvalidatesPreviousCustomerAndLegacyKioskScope(t *testing.T) {
	r := newRuntime(&fakeBackend{clientResult: goyclients.Client{ID: 45}})
	r.bind(t)
	first := r.start(t, `{"phone":"79991234567"}`)
	res := r.request(http.MethodPost, "/salons/salon-two/session", `{}`, "")
	if res.Code != 200 {
		t.Fatalf("public selection on kiosk browser: %d %s", res.Code, res.Body)
	}
	if res := r.request(http.MethodGet, "/customer/me/pay-draft", "", first.Customer.SessionID); res.Code != 401 {
		t.Fatalf("previous company interaction accepted: %d %s", res.Code, res.Body)
	}
	for _, path := range []string{"/kiosk/session", "/kiosk/services", "/kiosk/me/pay-draft"} {
		if res := r.request(http.MethodGet, path, "", first.Customer.SessionID); res.Code != 401 {
			t.Fatalf("public mode used legacy kiosk route %s: %d %s", path, res.Code, res.Body)
		}
	}
	res = r.request(http.MethodPost, "/customer/session", `{"phone":"79991234567"}`, "")
	var current models.SessionResponse
	if err := json.Unmarshal(res.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if res.Code != 200 || current.Customer == nil || current.Customer.SessionID == first.Customer.SessionID || current.Salon == nil || current.Salon.CompanyID != 99 || r.backend.companyID != 99 {
		t.Fatalf("new company interaction: %d %s backend=%+v", res.Code, res.Body, r.backend)
	}
	r.salons.entries[1].IsActive = false
	res = r.request(http.MethodGet, "/customer/me/pay-draft", "", current.Customer.SessionID)
	if res.Code != 401 {
		t.Fatalf("disabled salon retained customer access: %d %s", res.Code, res.Body)
	}
}

func TestCustomerRequestRequiresItsRenderedSalonScope(t *testing.T) {
	r := newRuntime(&fakeBackend{clientResult: goyclients.Client{ID: 45}})
	if res := r.request(http.MethodPost, "/salons/salon-one/session", `{}`, ""); res.Code != 200 {
		t.Fatalf("select first salon: %d %s", res.Code, res.Body)
	}
	firstScope := r.scopeID
	if res := r.request(http.MethodPost, "/salons/salon-two/session", `{}`, ""); res.Code != 200 {
		t.Fatalf("select second salon: %d %s", res.Code, res.Body)
	}
	secondScope := r.scopeID
	for _, scopeID := range []string{"", firstScope} {
		r.backend.call = ""
		res := r.requestInScope(http.MethodPost, "/customer/session", `{"phone":"79991234567"}`, "", scopeID)
		if res.Code != 401 || r.backend.call != "" {
			t.Fatalf("missing/stale salon scope accepted: %d %s backend=%+v", res.Code, res.Body, r.backend)
		}
	}
	// A slow earlier selection could commit after the user's newer selection.
	// Its response is discarded in the UI; the browser still sends secondScope.
	if res := r.request(http.MethodPost, "/salons/salon-one/session", `{}`, ""); res.Code != 200 {
		t.Fatalf("late selection: %d %s", res.Code, res.Body)
	}
	r.backend.call = ""
	res := r.requestInScope(http.MethodPost, "/customer/session", `{"phone":"79991234567"}`, "", secondScope)
	if res.Code != 401 || r.backend.call != "" {
		t.Fatalf("late selection changed target company: %d %s backend=%+v", res.Code, res.Body, r.backend)
	}
}
