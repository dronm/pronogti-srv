package payment

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func cardBankRequest() BankRequest {
	r := bankRequestFixture()
	r.OrderID = "11111111-1111-4111-8111-111111111111"
	r.Profile.CardsEnabled = true
	r.Profile.CustomerCode = "300000092"
	r.Profile.AcquiringMerchantID = "200000000001056"
	r.Profile.CardRedirectURL = "https://salon.example/pay-bank-card?screen=card"
	r.Profile.CardFailRedirectURL = "https://salon.example/pay-bank-card?result=failed"
	return r
}

func cardResponse(r BankRequest) map[string]any {
	return map[string]any{"Data": map[string]any{"operationId": "48232c9a-ce82-1593-3cb6-5c85a1ffef8f", "paymentLink": "https://merch.tochka.com/order/example", "amount": "12345678901.23", "purpose": r.Purpose, "merchantId": r.Profile.AcquiringMerchantID, "paymentLinkId": r.AttemptID, "status": "CREATED", "paymentMode": []string{"card"}}}
}

func TestCardBankCreatesExactCardOnlyCheckoutWithScopedReturns(t *testing.T) {
	r := cardBankRequest()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.Method != "POST" || req.URL.Path != "/acquiring/v1.0/payments" || req.Header.Get("Authorization") != "Bearer phony.jwt.token" {
			t.Errorf("unexpected bank request %s %s", req.Method, req.URL.Path)
		}
		var payload struct{ Data map[string]json.RawMessage }
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if string(payload.Data["amount"]) != "12345678901.23" || string(payload.Data["paymentMode"]) != `["card"]` || string(payload.Data["paymentLinkId"]) != `"`+r.AttemptID+`"` || string(payload.Data["ttl"]) != "15" {
			t.Errorf("unexpected acquiring payload: %v", payload.Data)
		}
		for _, forbidden := range []string{"preAuthorization", "saveCard", "Client", "receipt"} {
			if _, present := payload.Data[forbidden]; present {
				t.Errorf("unexpected acquiring feature %s", forbidden)
			}
		}
		for _, field := range []string{"redirectUrl", "failRedirectUrl"} {
			var destination string
			_ = json.Unmarshal(payload.Data[field], &destination)
			u, err := url.Parse(destination)
			if err != nil || u.Host != "salon.example" || u.Path != "/pay-bank-card" || u.Query().Get("checkout_id") != r.OrderID || u.Query().Get("payment_id") != r.AttemptID {
				t.Errorf("invalid return URL: %s", destination)
			}
		}
		_ = json.NewEncoder(w).Encode(cardResponse(r))
	}))
	defer server.Close()
	r.Profile.APIBaseURL = server.URL
	result, err := (SDKCardBank{}).CreateCard(context.Background(), r)
	if err != nil || calls != 1 || result.OperationID == "" || result.Status != "CREATED" || !result.ExpiresAt.Equal(r.StartedAt.Add(15*time.Minute)) {
		t.Fatalf("result=%+v calls=%d error=%v", result, calls, err)
	}
}

func TestCardBankRejectsMismatchedOrUnsafeSuccessfulResponsesAsUnknown(t *testing.T) {
	for _, field := range []string{"amount", "purpose", "merchantId", "paymentLinkId", "paymentLink", "operationId", "paymentMode"} {
		t.Run(field, func(t *testing.T) {
			r := cardBankRequest()
			response := cardResponse(r)
			data := response["Data"].(map[string]any)
			data[field] = "wrong"
			if field == "amount" {
				data[field] = "0.01"
			}
			if field == "paymentMode" {
				data[field] = []string{"sbp"}
			}
			if field == "operationId" {
				data[field] = "../another-operation"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { _ = json.NewEncoder(w).Encode(response) }))
			defer server.Close()
			r.Profile.APIBaseURL = server.URL
			_, err := (SDKCardBank{}).CreateCard(context.Background(), r)
			requireBankError(t, err, "bank_invalid_response", true)
		})
	}
}

func TestCardBankDoesNotFollowRedirectOrRetryUnknownWrite(t *testing.T) {
	targetCalls, calls := 0, 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = io.WriteString(w, "secret response body")
	}))
	defer server.Close()
	r := cardBankRequest()
	r.Profile.APIBaseURL = server.URL
	_, err := (SDKCardBank{}).CreateCard(context.Background(), r)
	requireBankError(t, err, "bank_outcome_unknown", true)
	if calls != 1 || targetCalls != 0 || strings.Contains(err.Error(), "secret") {
		t.Fatalf("requests=%d redirect requests=%d error=%v", calls, targetCalls, err)
	}
}

func TestCardBankBuiltinDemoAndConfigurationFailBeforeDispatch(t *testing.T) {
	r := cardBankRequest()
	r.Profile.Environment, r.Profile.APIBaseURL, r.Token = "mock", "https://enter.tochka.com/uapi", ""
	result, err := (SDKCardBank{}).CreateCard(context.Background(), r)
	if err != nil || !strings.HasPrefix(result.CheckoutURL, "https://example.invalid/") || result.OperationID != "mock-card-"+r.AttemptID {
		t.Fatalf("mock=%+v err=%v", result, err)
	}
	r.Profile.CardsEnabled = false
	_, err = (SDKCardBank{}).CreateCard(context.Background(), r)
	requireBankError(t, err, "card_payment_unavailable", false)
	r = cardBankRequest()
	r.Profile.APIBaseURL = "http://127.0.0.1:1"
	r.Profile.CardRedirectURL = "https://user:password@salon.example/pay-bank-card"
	_, err = (SDKCardBank{}).CreateCard(context.Background(), r)
	requireBankError(t, err, "bank_invalid_configuration", false)
	r.Profile.CardRedirectURL = "http://salon.example/pay-bank-card"
	_, err = (SDKCardBank{}).CreateCard(context.Background(), r)
	requireBankError(t, err, "bank_invalid_configuration", false)
}
