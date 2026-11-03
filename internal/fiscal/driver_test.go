package fiscal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const completedDriverResponse = `{"deviceId":"cashdesk-1","cmd":"createReceipt","requestId":"receipt-1","status":"completed","data":{"shiftOpenedByRequest":false,"receipt":{"receiptNumber":1,"documentNumber":10,"fiscalSign":123456,"dateTime":"2026-11-03T10:00","shiftNumber":2}}}`

func testDriverConfig(baseURL string) DriverConfig {
	return DriverConfig{BaseURL: baseURL, DeviceID: "cashdesk-1", Token: "test-token", Timeout: time.Second}
}

func TestHTTPDriverCreatePreservesIdentityAndExactMoney(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/terminal/api/v1/commands" || r.URL.RawQuery != "" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("driver authentication or media type missing")
		}
		var request struct {
			DeviceID  string          `json:"deviceId"`
			Command   string          `json:"cmd"`
			RequestID string          `json:"requestId"`
			Params    json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.DeviceID != "cashdesk-1" || request.Command != "createReceipt" || request.RequestID != "receipt-1" ||
			!strings.Contains(string(request.Params), `"totalKopecks":1099511627775`) || !strings.Contains(string(request.Params), `"quantity":"0.125"`) {
			t.Errorf("request identity or exact numeric fields changed: %+v", request)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprint(w, completedDriverResponse)
	}))
	defer server.Close()
	result, err := (HTTPDriver{}).Create(context.Background(), testDriverConfig(server.URL+"/terminal/"), "receipt-1",
		json.RawMessage(`{"items":[{"quantity":"0.125"}],"totalKopecks":1099511627775}`))
	if err != nil || result.State != "completed" || string(result.Response) != completedDriverResponse || calls.Load() != 1 {
		t.Fatalf("result=%+v, err=%v, calls=%d", result, err, calls.Load())
	}
}

func TestHTTPDriverDoesNotGuessSuccessOrFailure(t *testing.T) {
	failed := `{"deviceId":"cashdesk-1","cmd":"createReceipt","requestId":"receipt-1","status":"failed","error":{"code":"device_error","deviceCode":40}}`
	cases := []struct {
		name        string
		status      int
		body        string
		contentType string
		wantState   string
		wantError   bool
	}{
		{"definite device rejection", 502, failed, "application/json", "failed", false},
		{"explicit unknown despite cleanup", 409, `{"deviceId":"cashdesk-1","cmd":"createReceipt","requestId":"receipt-1","status":"unknown","data":{"cleanup":{"succeeded":true}},"error":{"code":"outcome_unknown"}}`, "application/json", "unknown", false},
		{"wrong device", 200, strings.Replace(completedDriverResponse, "cashdesk-1", "cashdesk-2", 1), "application/json", "unknown", true},
		{"wrong request", 200, strings.Replace(completedDriverResponse, "receipt-1", "receipt-2", 1), "application/json", "unknown", true},
		{"wrong command", 200, strings.Replace(completedDriverResponse, "createReceipt", "getSerialNumber", 1), "application/json", "unknown", true},
		{"missing fiscal identifiers", 200, `{"deviceId":"cashdesk-1","cmd":"createReceipt","requestId":"receipt-1","status":"completed","data":{}}`, "application/json", "unknown", true},
		{"invalid fiscal date", 200, strings.Replace(completedDriverResponse, "2026-11-03T10:00", "2026-02-31T10:00", 1), "application/json", "unknown", true},
		{"success status on server error", 500, completedDriverResponse, "application/json", "unknown", true},
		{"failed status on server error", 500, failed, "application/json", "unknown", true},
		{"journal identity conflict", 409, strings.Replace(failed, "device_error", "idempotency_conflict", 1), "application/json", "unknown", true},
		{"unknown future status", 200, strings.Replace(completedDriverResponse, "completed", "accepted", 1), "application/json", "unknown", true},
		{"duplicate outcome", 200, strings.Replace(completedDriverResponse, `"status":"completed"`, `"status":"unknown","status":"completed"`, 1), "application/json", "unknown", true},
		{"duplicate fiscal identifier", 200, strings.Replace(completedDriverResponse, `"documentNumber":10`, `"documentNumber":11,"documentNumber":10`, 1), "application/json", "unknown", true},
		{"truncated JSON", 200, completedDriverResponse[:50], "application/json", "unknown", true},
		{"HTML response", 200, "<html>gateway error</html>", "text/html", "unknown", true},
		{"wrong media type", 200, completedDriverResponse, "text/plain", "unknown", true},
		{"oversized response", 200, strings.Repeat(" ", maxDriverResponseBytes+1), "application/json", "unknown", true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", item.contentType)
				w.WriteHeader(item.status)
				fmt.Fprint(w, item.body)
			}))
			defer server.Close()
			result, err := (HTTPDriver{}).Create(context.Background(), testDriverConfig(server.URL), "receipt-1", json.RawMessage(`{}`))
			if result.State != item.wantState || (err != nil) != item.wantError || calls.Load() != 1 {
				t.Fatalf("result=%+v, err=%v, calls=%d", result, err, calls.Load())
			}
			if len(result.Response) > 0 && !json.Valid(result.Response) {
				t.Fatal("invalid JSON must not be persisted as a JSON response")
			}
		})
	}
}

func TestHTTPDriverLookupJournalStates(t *testing.T) {
	cases := []struct {
		name, body, state string
		status            int
	}{
		{"completed original", completedDriverResponse, "completed", 200},
		{"absent", `{"deviceId":"cashdesk-1","requestId":"receipt-1","status":"failed","error":{"code":"not_found"}}`, "not_found", 404},
		{"in progress", `{"deviceId":"cashdesk-1","requestId":"receipt-1","status":"in_progress","error":{"code":"request_in_progress"}}`, "in_progress", 409},
		{"unknown", `{"deviceId":"cashdesk-1","requestId":"receipt-1","status":"unknown","error":{"code":"outcome_unknown"}}`, "unknown", 409},
		{"saved definite failure", `{"deviceId":"cashdesk-1","cmd":"createReceipt","requestId":"receipt-1","status":"failed","error":{"code":"device_error"}}`, "failed", 502},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/requests" || r.URL.Query().Get("deviceId") != "cashdesk-1" || r.URL.Query().Get("requestId") != "receipt-1" {
					t.Errorf("incorrect lookup request: %s %s", r.Method, r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(item.status)
				fmt.Fprint(w, item.body)
			}))
			defer server.Close()
			result, err := (HTTPDriver{}).Lookup(context.Background(), testDriverConfig(server.URL), "receipt-1")
			if err != nil || result.State != item.state {
				t.Fatalf("result=%+v, err=%v", result, err)
			}
		})
	}
}

func TestHTTPDriverDoesNotFollowRedirectsOrRepeatLostRequests(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	for _, redirect := range []bool{true, false} {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if redirect {
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				return
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			conn.Close()
		}))
		result, err := (HTTPDriver{}).Create(context.Background(), testDriverConfig(server.URL), "receipt-1", json.RawMessage(`{}`))
		server.Close()
		if err == nil || result.State != "unknown" || calls.Load() != 1 || targetCalls.Load() != 0 {
			t.Fatalf("redirect=%v, result=%+v, err=%v, calls=%d, targetCalls=%d", redirect, result, err, calls.Load(), targetCalls.Load())
		}
	}
}

func TestHTTPDriverRejectsInvalidConfigBeforeSending(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	cases := []struct {
		name string
		edit func(*DriverConfig)
	}{
		{"userinfo", func(cfg *DriverConfig) { cfg.BaseURL = "http://secret@example.test" }},
		{"query", func(cfg *DriverConfig) { cfg.BaseURL += "?token=secret" }},
		{"fragment", func(cfg *DriverConfig) { cfg.BaseURL += "#secret" }},
		{"scheme", func(cfg *DriverConfig) { cfg.BaseURL = "file:///dev/terminal" }},
		{"device", func(cfg *DriverConfig) { cfg.DeviceID = "../other" }},
		{"header injection", func(cfg *DriverConfig) { cfg.Token = "secret\r\nX-Test: injected" }},
		{"timeout", func(cfg *DriverConfig) { cfg.Timeout = 0 }},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			cfg := testDriverConfig(server.URL)
			item.edit(&cfg)
			result, err := (HTTPDriver{}).Create(context.Background(), cfg, "receipt-1", json.RawMessage(`{}`))
			if err == nil || result.State != "failed" || calls.Load() != 0 || strings.Contains(err.Error(), "secret") {
				t.Fatalf("result=%+v, err=%v, calls=%d", result, err, calls.Load())
			}
		})
	}
	result, err := (HTTPDriver{}).Create(context.Background(), testDriverConfig(server.URL), "receipt-1", json.RawMessage(`null`))
	if err == nil || result.State != "failed" || calls.Load() != 0 {
		t.Fatalf("invalid params result=%+v, err=%v, calls=%d", result, err, calls.Load())
	}
}
