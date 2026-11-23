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
)

func TestCorrectionDriverUsesDedicatedCommandAndLookup(t *testing.T) {
	var creates, lookups atomic.Int32
	response := strings.ReplaceAll(completedDriverResponse, "createReceipt", "createCorrectionReceipt")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			creates.Add(1)
			var input struct {
				Command   string          `json:"cmd"`
				RequestID string          `json:"requestId"`
				Params    json.RawMessage `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.Command != "createCorrectionReceipt" || input.RequestID != "receipt-1" || !strings.Contains(string(input.Params), `"correctedCalculationDate":1794873600`) {
				t.Errorf("incorrect correction dispatch: %+v", input)
			}
		} else {
			lookups.Add(1)
			if r.URL.Path != "/api/v1/requests" || r.URL.Query().Get("requestId") != "receipt-1" {
				t.Errorf("incorrect journal lookup: %s", r.URL)
			}
		}
		fmt.Fprint(w, response)
	}))
	defer server.Close()
	driver, cfg := HTTPDriver{}, testDriverConfig(server.URL)
	created, err := driver.CreateCorrection(context.Background(), cfg, "receipt-1", json.RawMessage(`{"additionalData":{"correctedCalculationDate":1794873600}}`))
	if err != nil || created.State != "completed" {
		t.Fatalf("create: %+v %v", created, err)
	}
	found, err := driver.LookupCorrection(context.Background(), cfg, "receipt-1")
	if err != nil || found.State != "completed" || creates.Load() != 1 || lookups.Load() != 1 {
		t.Fatalf("lookup: %+v %v creates=%d lookups=%d", found, err, creates.Load(), lookups.Load())
	}
	// A sale request must never accept a correction outcome for the same ID.
	result, err := driver.Lookup(context.Background(), cfg, "receipt-1")
	if err == nil || result.State != "unknown" {
		t.Fatalf("accepted the wrong command's outcome: %+v %v", result, err)
	}
}

func TestCorrectionDriverPreservesUnknownDespiteCleanup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"deviceId":"cashdesk-1","cmd":"createCorrectionReceipt","requestId":"receipt-1","status":"unknown","data":{"cleanup":{"attempted":true,"succeeded":true}},"error":{"code":"outcome_unknown"}}`)
	}))
	defer server.Close()
	result, err := (HTTPDriver{}).CreateCorrection(context.Background(), testDriverConfig(server.URL), "receipt-1", json.RawMessage(`{}`))
	if err != nil || result.State != "unknown" || result.ErrorCode != "outcome_unknown" {
		t.Fatalf("lost correction result treated as definite: %+v %v", result, err)
	}
}
