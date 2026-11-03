package httpapi

import (
	"net/http"
	"strings"

	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/webapp"
)

// SalonScopeMiddleware correlates a customer request with the exact salon
// interaction shown by that browser. Bootstrap and salon selection remain public.
func SalonScopeMiddleware() webapp.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/customer/") {
				r = r.WithContext(kiosk.ContextWithScope(r.Context(), r.Header.Get(kiosk.ScopeHeader)))
			}
			next.ServeHTTP(w, r)
		})
	}
}
