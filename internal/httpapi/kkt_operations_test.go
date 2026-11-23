package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/pronogti/internal/fiscal"
	websession "github.com/dronm/webapp/session"
)

// Exercise real route authorization, profile lookup, JSONB snapshots and replay,
// using a loopback driver that deliberately loses its first registration reply.
func TestAdminKKTRegistrationRecovery(t *testing.T) {
	provider, conn := adminIntegrationDatabase(t, nil)
	var calls int
	var firstID, firstParams string
	driver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Device  string          `json:"deviceId"`
			Command string          `json:"cmd"`
			ID      string          `json:"requestId"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Command != "registerKkt" || req.Device != "test-kkt" {
			t.Errorf("unexpected request: %+v", req)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			firstID, firstParams = req.ID, string(req.Params)
			fmt.Fprint(w, "{")
			return
		}
		if req.ID != firstID || string(req.Params) != firstParams {
			t.Error("retry changed request")
		}
		fmt.Fprintf(w, `{"cmd":"registerKkt","deviceId":"test-kkt","requestId":%q,"status":"completed","data":{"documentNumber":42,"fiscalSign":0}}`, req.ID)
	}))
	defer driver.Close()
	var id int64
	ctx := context.Background()
	if err := conn.QueryRow(ctx, `INSERT INTO public.fiscal_profiles(company_id,name,mode,driver_base_url,device_id)
		VALUES (781898,'Test KKT','live',$1,'test-kkt') RETURNING id`, driver.URL).Scan(&id); err != nil {
		t.Fatal(err)
	}
	auth := &adminTestService{}
	sessions := NewAdminSessions(&adminTestManager{}, websession.Config{CookieName: "admin_test", HTTPOnly: true, SameSite: http.SameSiteLaxMode, MaxLifeTime: 3600, MaxIdleTime: 600}, "/", auth.Invalidate)
	handler := NewAdminHandler(auth, sessions, AdminHTTPOptions{AllowedOrigins: []string{"http://localhost:5174"}, KKT: fiscal.NewKKTService(fiscal.NewPostgresRepository(provider), true)})
	base := fmt.Sprintf("/admin/fiscal-profiles/%d/kkt/", id)
	if resp := adminRequest(handler, "GET", base+"fn-format", "", nil, ""); resp.Code != 401 {
		t.Fatalf("anonymous read: %d", resp.Code)
	}
	login := adminRequest(handler, "POST", "/admin/login", `{"name":"admin","pwd":"password"}`, nil, "")
	cookie := login.Result().Cookies()[0]
	send := func(action, body, nonce, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", base+action, strings.NewReader(body))
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
	nonce := adminTestResponse().Auth.Token
	const key = "one-registration"
	for _, tc := range []struct {
		action, body, token, key string
		status                   int
	}{
		{"close-fn", "{}", "", key, 403}, {"open-shift", "{}", nonce, "", 400}, {"close-shift", `{"deviceId":"other"}`, nonce, key, 400}, {"register-fn", `{"cmd":"other"}`, nonce, key, 400}, {"register-fn", `{"reportType":1,"taxationSystems":1}`, nonce, key, 400},
	} {
		if resp := send(tc.action, tc.body, tc.token, tc.key); resp.Code != tc.status {
			t.Fatalf("%s: %d", tc.action, resp.Code)
		}
	}
	if calls != 0 {
		t.Fatal("unauthorized or invalid request reached the driver")
	}
	const body = `{"reportType":1,"reregistrationReason":2147483648,"taxationSystems":1,"userInn":"7707083893","registrationNumber":"0000000001046446","userName":"Test","representativeName":"Director","settlementAddress":"Office","settlementPlace":"Office","operatingMode":2,"applicationFlags":0}`
	if resp := send("register-fn", body, nonce, key); resp.Code != 200 || !strings.Contains(resp.Body.String(), `"state":"unknown"`) {
		t.Fatal(resp.Code, resp.Body)
	}
	if _, err := conn.Exec(ctx, `UPDATE public.fiscal_profiles SET device_id='edited-device' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if resp := send("register-fn", strings.Replace(body, "2147483648", "1", 1), nonce, key); resp.Code != 409 || calls != 1 {
		t.Fatal("key reused with an edited payload", resp.Code, calls)
	}
	if resp := send("retry", "{}", nonce, key); resp.Code != 200 || !strings.Contains(resp.Body.String(), `"documentNumber":42`) || calls != 2 {
		t.Fatal(resp.Code, resp.Body, calls)
	}
}
