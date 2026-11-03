package kiosk

import (
	"net/http"
	"strings"

	"github.com/dronm/webapp"
)

// OriginMiddleware rejects cross-origin mutations outside configured frontend
// origins. Header-less CLI requests remain supported; the JSON/nonce binders
// enforce the request contract independently of browser CORS enforcement.
func OriginMiddleware(origins []string) webapp.Middleware {
	allowed := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		allowed[strings.TrimRight(origin, "/")] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				origin := r.Header.Get("Origin")
				if origin != "" {
					if _, ok := allowed[origin]; !ok {
						webapp.WriteServiceError(w, webapp.Forbidden("request origin is not allowed", nil))
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
