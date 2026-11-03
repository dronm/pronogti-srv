package httpapi

import (
	"net/http"
	"testing"

	"github.com/dronm/webapp"
)

func TestProductionRoutesRegisterWithoutAmbiguity(t *testing.T) {
	// Exercise every production pattern together. The previous API had crossing
	// wildcard patterns that caused ServeMux to panic during startup.
	_ = webapp.MustBuildRouter(&webapp.AppContext{}, BuildRoutes())
	seen := map[string]bool{}
	for _, route := range BuildRoutes() {
		key := route.Method + " " + route.Pattern
		if seen[key] {
			t.Errorf("duplicate %s", key)
		}
		seen[key] = true
		if route.Method == http.MethodGet && route.Pattern == "/companies" {
			t.Fatal("unscoped companies endpoint remains")
		}
	}
	for _, required := range []string{
		"GET /session", "GET /salons", "POST /salons/{slug}/session",
		"POST /customer/session", "DELETE /customer/session", "GET /customer/me/pay-draft",
		"POST /kiosk/session", "GET /kiosk/session",
	} {
		if !seen[required] {
			t.Errorf("missing entry/session route %s", required)
		}
	}
}
