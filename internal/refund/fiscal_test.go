package refund

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/fiscal"
)

func returnReceiptFixture(t *testing.T, profile fiscal.Profile) (json.RawMessage, []Line) {
	t.Helper()
	order := checkout.Order{
		Owner: checkout.Owner{CompanyID: profile.CompanyID}, Currency: "RUB", SubtotalKopecks: 350, PayableKopecks: 350,
		Items: []checkout.Item{
			{Selection: checkout.Selection{Kind: checkout.KindService, Quantity: "1.5"}, Title: "Service", UnitPriceKopecks: 100, AmountKopecks: 150},
			{Selection: checkout.Selection{Kind: checkout.KindGood, Quantity: "2"}, Title: "Good", UnitPriceKopecks: 100, AmountKopecks: 200},
		},
	}
	payload, err := fiscal.BuildPayload(order, profile)
	if err != nil {
		t.Fatal(err)
	}
	return payload, []Line{
		{LineIndex: 0, Kind: checkout.KindService, Title: "Service", Quantity: "0.5", QuantityMilli: 500, UnitPriceKopecks: 100, AmountKopecks: 50},
		{LineIndex: 1, Kind: checkout.KindGood, Title: "Good", Quantity: "1", QuantityMilli: 1000, UnitPriceKopecks: 100, AmountKopecks: 100},
	}
}

func TestPrepareReturnReceiptPreservesOriginalFiscalFields(t *testing.T) {
	profile := fiscal.DefaultProfile(1)
	profile.ServiceVATRate, profile.GoodVATRate, profile.TaxSystem = 5, 6, 4
	salePayload, lines := returnReceiptFixture(t, profile)
	returned, err := PrepareReturnReceipt(profile, "mock", "demo", salePayload, lines, 150, 350, false)
	if err != nil {
		t.Fatal(err)
	}
	var sale, receipt map[string]any
	if err := json.Unmarshal(salePayload, &sale); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(returned, &receipt); err != nil {
		t.Fatal(err)
	}
	sale["calculationSign"] = float64(2)
	sale["totalKopecks"] = float64(150)
	sale["payment"].(map[string]any)["cashlessKopecks"] = float64(150)
	for i, line := range lines {
		sale["items"].([]any)[i].(map[string]any)["quantity"] = line.Quantity
	}
	if !reflect.DeepEqual(sale, receipt) {
		t.Fatalf("returned receipt changed immutable fiscal details: %s", returned)
	}
	if _, err := PrepareReturnReceipt(profile, "production", "demo", salePayload, lines, 150, 350, true); err == nil {
		t.Fatal("allowed real bank refund without live sale receipt")
	}
	if _, err := PrepareReturnReceipt(profile, "mock", "demo", salePayload, lines, 150, 349, false); err == nil {
		t.Fatal("accepted a sale receipt with a different original payment total")
	}
	lines[0].UnitPriceKopecks++
	if _, err := PrepareReturnReceipt(profile, "mock", "demo", salePayload, lines, 150, 350, false); err == nil {
		t.Fatal("allowed return at a different unit price")
	}
}

func TestProcessReturnReceiptRecoversThroughOriginalTerminalFARequest(t *testing.T) {
	const requestID = "refund-fiscal-test"
	var posts, lookups atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			posts.Add(1)
			var request struct {
				DeviceID  string             `json:"deviceId"`
				Command   string             `json:"cmd"`
				RequestID string             `json:"requestId"`
				Params    saleReceiptAmounts `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || r.URL.Path != "/api/v1/commands" ||
				request.RequestID != requestID || request.DeviceID != "device" || request.Command != "createReceipt" ||
				request.Params.CalculationSign != 2 || request.Params.TotalKopecks != 150 || request.Params.Payment.CashlessKopecks != 150 {
				t.Errorf("unexpected TerminalFA request: %+v, %v", request, err)
			}
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, `{}`)
		case http.MethodGet:
			lookups.Add(1)
			if r.URL.Path != "/api/v1/requests" || r.URL.Query().Get("requestId") != requestID || r.URL.Query().Get("deviceId") != "device" {
				t.Errorf("unexpected TerminalFA lookup: %s", r.URL)
			}
			fmt.Fprint(w, `{"deviceId":"device","requestId":"refund-fiscal-test","cmd":"createReceipt","status":"completed","data":{"shiftOpenedByRequest":false,"receipt":{"receiptNumber":1,"documentNumber":2,"fiscalSign":3,"dateTime":"2026-11-05T12:00","shiftNumber":4}}}`)
		default:
			t.Errorf("unexpected TerminalFA method %s", r.Method)
		}
	}))
	defer server.Close()
	profile := fiscal.DefaultProfile(1)
	profile.Mode, profile.DriverBaseURL, profile.DeviceID = "live", server.URL, "device"
	salePayload, lines := returnReceiptFixture(t, profile)
	payload, err := PrepareReturnReceipt(profile, "production", "issued", salePayload, lines, 150, 350, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	driver := fiscal.HTTPDriver{}
	if result := ProcessReturnReceipt(ctx, driver, profile, "sandbox", requestID, payload, true, false); result.State != "demo" {
		t.Fatalf("sandbox reached live fiscal path: %+v", result)
	}
	if result := ProcessReturnReceipt(ctx, driver, profile, "production", requestID, payload, false, true); result.State != "unknown" {
		t.Fatalf("live disabled did not preserve uncertainty: %+v", result)
	}
	if result := ProcessReturnReceipt(ctx, driver, profile, "production", requestID, payload, false, false); result.State != "queued" {
		t.Fatalf("disabled live fiscalization lost an undispatched receipt: %+v", result)
	}
	missingTokenProfile := profile
	missingTokenProfile.BearerTokenEnv = "PRONOGTI_REFUND_TEST_TOKEN"
	t.Setenv(missingTokenProfile.BearerTokenEnv, "")
	if result := ProcessReturnReceipt(ctx, driver, missingTokenProfile, "production", requestID, payload, true, false); result.State != "queued" {
		t.Fatalf("missing token did not queue an undispatched receipt: %+v", result)
	}
	if result := ProcessReturnReceipt(ctx, driver, profile, "production", requestID, payload, true, false); result.State != "unknown" {
		t.Fatalf("ambiguous response became definitive: %+v", result)
	}
	if result := ProcessReturnReceipt(ctx, driver, profile, "production", requestID, payload, true, true); result.State != "issued" {
		t.Fatalf("lookup failed to reconcile receipt: %+v", result)
	}
	if posts.Load() != 1 || lookups.Load() != 1 {
		t.Fatalf("TerminalFA got %d submits and %d lookups, expected one of each", posts.Load(), lookups.Load())
	}
}
