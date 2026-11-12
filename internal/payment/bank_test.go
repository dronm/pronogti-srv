package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func bankRequestFixture() BankRequest {
	return BankRequest{
		AttemptID: "dcaa5980-c7c1-4d09-a535-55643eeffedb", AmountKopecks: 1234567890123,
		Purpose: "Оплата заказа", TTLMinutes: 15,
		StartedAt: time.Date(2026, 11, 3, 14, 0, 0, 0, time.FixedZone("UTC+05", 5*3600)),
		Token:     "phony.jwt.token",
		Profile:   Profile{Environment: "sandbox", SBPMerchantID: "MF0000000001", AccountID: "40817810802000000008/044525104", HTTPTimeoutSeconds: 20},
	}
}

func bankResponse(payload string) string {
	data, _ := json.Marshal(map[string]any{"Data": map[string]any{
		"qrcId": "AS000000000000000000000000000001", "payload": payload,
		"image": map[string]any{"mediaType": "image/svg+xml", "width": 512, "height": 512, "content": "<svg onload=alert(1)></svg>"},
	}})
	return string(data)
}

func requireBankError(t *testing.T, err error, code string, unknown bool) {
	t.Helper()
	var bankErr *BankError
	if !errors.As(err, &bankErr) || bankErr.Code != code || bankErr.Unknown != unknown {
		t.Fatalf("error=%v, want code=%s unknown=%t", err, code, unknown)
	}
}

func TestSDKBankUsesExactDynamicQRContractAndRendersPayloadPNG(t *testing.T) {
	request := bankRequestFixture()
	request.Profile.AccountID = "40817810802000000008"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/sbp/v1.0/qr-code/merchant/MF0000000001/40817810802000000008/044525104" {
			t.Fatalf("method/path=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer phony.jwt.token" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Fatal("missing bank authorization/format headers")
		}
		body, _ := io.ReadAll(r.Body)
		var actual struct {
			Data struct {
				Amount                                        json.Number `json:"amount"`
				Currency, PaymentPurpose, QRCType, SourceName string
				TTL                                           int `json:"ttl"`
				ImageParams                                   struct {
					Width, Height int
					MediaType     string
				}
			}
		}
		if err := json.Unmarshal(body, &actual); err != nil {
			t.Fatal(err)
		}
		if actual.Data.Amount.String() != "1234567890123" || actual.Data.Currency != "RUB" || actual.Data.PaymentPurpose != request.Purpose || actual.Data.QRCType != "02" || actual.Data.TTL != 15 || actual.Data.ImageParams.MediaType != "image/png" {
			t.Fatalf("wrong bank request: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, bankResponse("https://qr.nspk.ru/AS000000000000000000000000000001?sum=1234567890123&cur=RUB"))
	}))
	defer server.Close()
	request.Profile.APIBaseURL = server.URL
	result, err := (SDKBank{}).Create(context.Background(), request)
	if err != nil || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
	if result.ImageMediaType != "image/png" || !bytes.HasPrefix(result.Image, []byte("\x89PNG\r\n\x1a\n")) || bytes.Contains(result.Image, []byte("<svg")) {
		t.Fatal("bank markup was not replaced by a PNG")
	}
	image, err := png.DecodeConfig(bytes.NewReader(result.Image))
	if err != nil || image.Width != 512 || image.Height != 512 {
		t.Fatalf("invalid QR PNG: %+v %v", image, err)
	}
	if result.QRCID == "" || !result.ExpiresAt.Equal(request.StartedAt.Add(15*time.Minute)) || result.ExpiresAt.Location() != time.UTC {
		t.Fatalf("wrong result/UTC expiry: %+v", result)
	}
}

func TestTochkaAccountIDPreservesExplicitBIC(t *testing.T) {
	const explicit = "40817810802000000008/049999999"
	if actual := TochkaAccountID(explicit); actual != explicit {
		t.Fatalf("TochkaAccountID(%q) = %q", explicit, actual)
	}
}

func TestSDKBankMockIsSelfContainedAndNonpayable(t *testing.T) {
	request := bankRequestFixture()
	request.Profile.Environment, request.Profile.APIBaseURL, request.Token = "mock", "https://enter.tochka.com/uapi", ""
	result, err := (SDKBank{}).Create(context.Background(), request)
	if err != nil || result.Payload != "https://example.invalid/pronogti/mock/"+request.AttemptID || result.QRCID != "mock-"+request.AttemptID || len(result.Image) == 0 {
		t.Fatalf("unexpected mock result: %+v %v", result, err)
	}
}

func TestSDKBankExternalMockRequiresLoopbackAndUsesSDK(t *testing.T) {
	request := bankRequestFixture()
	request.Profile.Environment = "mock"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, bankResponse("https://example.invalid/external-test"))
	}))
	defer server.Close()
	request.Profile.APIBaseURL = server.URL
	result, err := (SDKBank{MockExternal: true}).Create(context.Background(), request)
	if err != nil || calls != 1 || result.Payload != "https://example.invalid/external-test" {
		t.Fatalf("calls=%d result=%+v error=%v", calls, result, err)
	}
	request.Profile.APIBaseURL = "https://enter.tochka.com/uapi"
	_, err = (SDKBank{MockExternal: true}).Create(context.Background(), request)
	requireBankError(t, err, "bank_invalid_configuration", false)
}

func TestSDKBankUncertainWritesAreNeverRetriedOrLeaked(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		body, code string
		unknown    bool
	}{
		{"server failure", 503, `{"message":"secret phony.jwt.token"}`, "bank_outcome_unknown", true},
		{"upstream dependency", 424, `{"message":"secret phony.jwt.token"}`, "bank_outcome_unknown", true},
		{"bad request", 400, `{"message":"secret phony.jwt.token"}`, "bank_rejected", false},
		{"malformed success", 200, `{"Data":`, "bank_outcome_unknown", true},
		{"missing QR", 200, `{"Data":{"payload":"https://qr.nspk.ru/test"}}`, "bank_outcome_unknown", true},
		{"unsafe payload", 200, bankResponse("javascript:alert(1)"), "bank_invalid_response", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			request := bankRequestFixture()
			request.Profile.APIBaseURL = server.URL
			_, err := (SDKBank{}).Create(context.Background(), request)
			requireBankError(t, err, test.code, test.unknown)
			if calls != 1 || strings.Contains(err.Error(), "phony.jwt.token") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("calls=%d unsafe error=%v", calls, err)
			}
		})
	}
}

func TestSDKBankRefusesUnsafeEnvironmentConfiguration(t *testing.T) {
	for _, test := range []struct {
		env, url string
		allowed  bool
	}{
		{"sandbox", "https://enter.tochka.com/sandbox/v2", true},
		{"sandbox", "https://enter.tochka.com/sandbox/v2/", true},
		{"sandbox", "https://enter.tochka.com/uapi", false},
		{"sandbox", "https://attacker.invalid/sandbox/v2", false},
		{"sandbox", "http://localhost:8099", true},
		{"mock", "http://127.0.0.1:8099", true},
		{"mock", "http://192.0.2.1:8099", false},
		{"mock", "https://enter.tochka.com/uapi", false},
		{"production", "http://localhost:8099", false},
		{"production", "https://enter.tochka.com/uapi", true},
		{"production", "https://user:secret@enter.tochka.com/uapi", false},
		{"production", "https://enter.tochka.com/uapi?token=secret", false},
	} {
		if actual := allowedBankEndpoint(test.env, test.url); actual != test.allowed {
			t.Fatalf("%s %s: got %t", test.env, test.url, actual)
		}
	}
}

func TestSDKBankValidatesBeforeDispatchAndHonorsCancelledContext(t *testing.T) {
	request := bankRequestFixture()
	request.Profile.APIBaseURL = "http://127.0.0.1:1"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (SDKBank{}).Create(ctx, request)
	requireBankError(t, err, "bank_request_cancelled", false)
	request.TTLMinutes = 0
	_, err = (SDKBank{}).Create(context.Background(), request)
	requireBankError(t, err, "bank_invalid_request", false)
}
