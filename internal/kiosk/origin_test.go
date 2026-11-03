package kiosk

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCrossOriginMutationRejectedBeforeHandler(t *testing.T) {
	for _, tt := range []struct {
		method, origin string
		status         int
	}{
		{http.MethodPost, "https://shop.example", 204},
		{http.MethodPost, "https://attacker.example", 403},
		{http.MethodDelete, "null", 403},
		{http.MethodPost, "", 204},
		{http.MethodGet, "https://attacker.example", 204},
	} {
		r := httptest.NewRequest(tt.method, "/kiosk/customer-session", nil)
		r.Header.Set("Origin", tt.origin)
		w := httptest.NewRecorder()
		OriginMiddleware([]string{"https://shop.example"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
		if w.Code != tt.status {
			t.Errorf("%s %q: status=%d", tt.method, tt.origin, w.Code)
		}
	}
}
