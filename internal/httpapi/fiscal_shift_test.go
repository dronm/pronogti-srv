package httpapi

import (
	"context"
	"github.com/dronm/pronogti/internal/kiosk"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/pronogti/internal/fiscal"
	websession "github.com/dronm/webapp/session"
)

type shiftHTTPFixture struct {
	calls     int
	profileID int64
	key       string
}

func (f *shiftHTTPFixture) CloseProfile(_ context.Context, id int64, key string) (fiscal.ShiftResult, error) {
	f.calls++
	f.profileID = id
	f.key = key
	return fiscal.ShiftResult{State: "closed", Message: "Смена закрыта."}, nil
}
func (f *shiftHTTPFixture) CloseKiosk(context.Context, int64, int64, string) (fiscal.ShiftResult, error) {
	f.calls++
	return fiscal.ShiftResult{}, nil
}

func TestAdminCloseShiftRequiresAdminNonceKeyAndStrictBody(t *testing.T) {
	const path = "/admin/fiscal-profiles/5/close-shift"
	auth := &adminTestService{}
	sessions := NewAdminSessions(&adminTestManager{}, websession.Config{CookieName: "admin_test", HTTPOnly: true, SameSite: http.SameSiteLaxMode, MaxLifeTime: 3600, MaxIdleTime: 600}, "/", auth.Invalidate)
	fixture := &shiftHTTPFixture{}
	handler := NewAdminHandler(auth, sessions, AdminHTTPOptions{AllowedOrigins: []string{"http://localhost:5174"}, Shifts: fixture})
	login := adminRequest(handler, http.MethodPost, "/admin/login", `{"name":"admin","pwd":"password"}`, nil, "")
	cookie := login.Result().Cookies()[0]
	send := func(nonce, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost:5174")
		r.Header.Set(adminauth.SessionHeader, nonce)
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if got := adminRequest(handler, http.MethodPost, path, `{}`, nil, ""); got.Code != 401 {
		t.Fatalf("anonymous %d", got.Code)
	}
	if got := send("", "attempt-1", `{}`); got.Code != 403 {
		t.Fatalf("missing nonce %d %s", got.Code, got.Body)
	}
	if got := send(adminTestResponse().Auth.Token, "", `{}`); got.Code != 400 {
		t.Fatalf("missing key %d", got.Code)
	}
	if got := send(adminTestResponse().Auth.Token, "attempt-1", `{"device_id":"other"}`); got.Code != 400 {
		t.Fatalf("unknown field %d", got.Code)
	}
	if fixture.calls != 0 {
		t.Fatal("invalid request reached hardware service")
	}
	if got := send(adminTestResponse().Auth.Token, "attempt-1", `{}`); got.Code != 200 || fixture.calls != 1 || fixture.profileID != 5 || fixture.key != "attempt-1" {
		t.Fatalf("close %d %s %+v", got.Code, got.Body, fixture)
	}
}

func TestKioskShiftBinderRejectsDeviceSelectors(t *testing.T) {
	for _, body := range []string{`{"operator_token":"secret","device_id":"other"}`, `{"operator_token":"secret","profile_id":2}`} {
		r := httptest.NewRequest(http.MethodPost, "/kiosk/fiscal/close-shift", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "attempt-1")
		if _, err := closeKioskShiftBinder(r); err == nil {
			t.Fatal("accepted user-selected hardware")
		}
	}
	fixture := &shiftHTTPFixture{}
	s := &service{kioskOnly: false, checkout: &CheckoutDependencies{Shifts: &ShiftDependencies{Service: fixture, OperatorToken: "secret"}}}
	if _, err := s.CloseKioskShift(context.Background(), closeShiftInput{OperatorToken: "secret", IdempotencyKey: "attempt-1"}); err == nil || fixture.calls != 0 {
		t.Fatal("customer channel closed shift")
	}
}

func TestKioskShiftRequiresBindingAndOperatorWithoutCustomer(t *testing.T) {
	fixture := &shiftHTTPFixture{}
	runtime := newRuntime(&fakeBackend{}, CheckoutDependencies{Shifts: &ShiftDependencies{Service: fixture, OperatorToken: "operator-secret-123"}})
	send := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/kiosk/fiscal/close-shift", strings.NewReader(`{"operator_token":"`+token+`"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "attempt-1")
		if runtime.scopeID != "" {
			r.Header.Set(kiosk.ScopeHeader, runtime.scopeID)
		}
		w := httptest.NewRecorder()
		runtime.handler.ServeHTTP(w, r)
		return w
	}
	if got := send("operator-secret-123"); got.Code != 401 {
		t.Fatalf("unbound kiosk: %d %s", got.Code, got.Body)
	}
	runtime.bind(t)
	if got := send("wrong-secret"); got.Code != 403 {
		t.Fatalf("wrong operator: %d %s", got.Code, got.Body)
	}
	if fixture.calls != 0 {
		t.Fatal("unauthorized kiosk request reached service")
	}
	if got := send("operator-secret-123"); got.Code != 200 || fixture.calls != 1 {
		t.Fatalf("bound kiosk without customer: %d %s", got.Code, got.Body)
	}
}
