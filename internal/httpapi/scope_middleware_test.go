package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dronm/pronogti/internal/kiosk"
)

func TestSalonScopeMiddlewareOnlyCorrelatesCustomerRequests(t *testing.T) {
	for _, path := range []string{"/customer/session", "/customer/me/pay-draft", "/session", "/salons", "/salons/salon-one/session", "/kiosk/session"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set(kiosk.ScopeHeader, "browser-salon-scope")
		var received string
		handler := SalonScopeMiddleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			received = kiosk.ScopeFromContext(r.Context())
		}))
		handler.ServeHTTP(httptest.NewRecorder(), request)
		want := ""
		if path == "/customer/session" || path == "/customer/me/pay-draft" {
			want = "browser-salon-scope"
		}
		if received != want {
			t.Errorf("%s scope=%q want=%q", path, received, want)
		}
	}
}
