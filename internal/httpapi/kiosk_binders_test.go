package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSalonSelectionRequiresCanonicalSlugAndJSON(t *testing.T) {
	for _, slug := range []string{"", "Salon", "salon--one", "-salon", "salon-", "salon_name", strings.Repeat("a", 101)} {
		r := httptest.NewRequest(http.MethodPost, "/salons/placeholder/session", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.SetPathValue("slug", slug)
		if _, err := selectSalonBinder(r); err == nil {
			t.Errorf("accepted invalid slug %q", slug)
		}
	}
	for _, tt := range []struct{ contentType, body string }{
		{"application/json", ""},
		{"application/json", "null"},
		{"application/json", "[]"},
		{"application/json", strings.Repeat(" ", (1<<20)+1)},
		{"application/x-www-form-urlencoded", "company_id=99"},
		{"text/plain", `{}`},
	} {
		r := httptest.NewRequest(http.MethodPost, "/salons/salon-one/session", strings.NewReader(tt.body))
		r.Header.Set("Content-Type", tt.contentType)
		r.SetPathValue("slug", "salon-one")
		if _, err := selectSalonBinder(r); err == nil {
			t.Errorf("accepted invalid selection document %s %q", tt.contentType, tt.body[:min(len(tt.body), 30)])
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/salons/salon-one/session?company_id=99", strings.NewReader(`{"company_id":99}`))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	r.SetPathValue("slug", "salon-one")
	value, err := selectSalonBinder(r)
	if err != nil || value.(selectSalonRequest).Slug != "salon-one" {
		t.Fatalf("selection=%+v error=%v", value, err)
	}
}
