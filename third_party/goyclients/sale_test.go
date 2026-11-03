package goyclients

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const saleTestResponse = `{"success":true,"data":{"state":{"items":[{"id":7,"type":"service","document_id":22,"title":"Service","amount":"1.125","default_cost_per_unit":1.01,"default_cost_total":1.14,"client_discount_percent":0,"cost_to_pay_total":"0.01"}],"payment_transactions":[{"id":33,"document_id":22,"sale_item_id":7,"sale_item_type":"service","account_id":44,"amount":"0.01","account":{"id":44,"title":"Cashless","is_cash":false}}],"loyalty_transactions":[]},"kkm_state":{"last_operation_type":1},"payment_methods":[{"slug":"account","is_applicable":true,"applicable_amount":0.01,"account_id":44,"account":{"id":44,"is_cash":false}}]},"meta":[]}`

func TestSalePayExactAccountWireAndSingleRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kopecks int64
		rubles  string
	}{
		{"one kopeck", 1, "0.01"},
		{"partial rubles", 12345, "123.45"},
		{"whole rubles", 12300, "123.00"},
		{"maximum", MaxPaymentKopecks, "90071992547409.91"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := NewYClients("partner-example", &mockHTTPClient{doFunc: func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodPost || req.URL.Path != "/api/v1/company/781898/sale/22/payment" || req.URL.RawQuery != "" {
					t.Fatalf("unexpected request %s %s", req.Method, req.URL)
				}
				if req.Header.Get("Accept") != apiVersion || req.Header.Get("Authorization") != "Bearer partner-example, User user-example" || req.Header.Get("Content-Type") != "application/json" {
					t.Fatal("required YCLIENTS authentication/content headers missing")
				}
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				want := `{"payment":{"method":{"slug":"account","account_id":44},"amount":` + tc.rubles + `}}`
				if string(body) != want {
					t.Fatalf("amount was not encoded exactly: %s, want %s", body, want)
				}
				return newMockResponse(http.StatusOK, saleTestResponse), nil
			}})
			client.User.UserToken = "user-example"
			result, err := client.NewSaleAPI(781898).Pay(22, AccountPayment{AccountID: 44, AmountKopecks: tc.kopecks})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("payment made %d requests", calls)
			}
			if len(result.State.PaymentTransactions) != 1 || result.State.PaymentTransactions[0].ID != 33 || result.State.PaymentTransactions[0].Amount.String() != "0.01" || result.State.Items[0].Amount.String() != "1.125" {
				t.Fatal("sale response lost exact reconciliation data")
			}
		})
	}
}

func TestSaleGetPreservesDecimalValues(t *testing.T) {
	client := NewYClients("partner", &mockHTTPClient{doFunc: func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/api/v1/company/781898/sale/22" || req.Body != nil {
			t.Fatal("sale lookup must use GET without a request body")
		}
		return newMockResponse(http.StatusOK, saleTestResponse), nil
	}})
	sale, err := client.NewSaleAPI(781898).GetContext(context.Background(), 22)
	if err != nil {
		t.Fatal(err)
	}
	if sale.State.Items[0].DefaultCostPerUnit.String() != "1.01" || sale.PaymentMethods[0].ApplicableAmount.String() != "0.01" || string(sale.KKMState) != `{"last_operation_type":1}` {
		t.Fatalf("incorrect exact sale response: %+v", sale)
	}
	encoded, err := json.Marshal(sale)
	if err != nil || !strings.Contains(string(encoded), `"cost_to_pay_total":0.01`) {
		t.Fatalf("decimal values did not survive JSON round trip: %s %v", encoded, err)
	}
}

func TestSalePaymentValidationDoesNotSendRequests(t *testing.T) {
	calls := 0
	client := NewYClients("partner", &mockHTTPClient{doFunc: func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not send") }})
	for _, tc := range []struct{ company, document, account, amount int64 }{
		{0, 22, 44, 100}, {-1, 22, 44, 100}, {781898, 0, 44, 100}, {781898, -1, 44, 100},
		{781898, 22, 0, 100}, {781898, 22, -1, 100}, {781898, 22, 44, 0}, {781898, 22, 44, -1}, {781898, 22, 44, MaxPaymentKopecks + 1},
	} {
		if _, err := client.NewSaleAPI(tc.company).PayContext(context.Background(), tc.document, AccountPayment{AccountID: tc.account, AmountKopecks: tc.amount}); err == nil {
			t.Fatalf("invalid payment accepted: %+v", tc)
		}
	}
	if _, err := client.NewSaleAPI(0).Get(22); err == nil {
		t.Fatal("invalid company lookup accepted")
	}
	if _, err := client.NewSaleAPI(781898).Get(0); err == nil {
		t.Fatal("invalid document lookup accepted")
	}
	var uninitialized *SaleAPI
	if _, err := uninitialized.Get(22); err == nil {
		t.Fatal("nil sale API accepted")
	}
	if _, err := (*YClients)(nil).NewSaleAPI(781898).Pay(22, AccountPayment{44, 100}); err == nil {
		t.Fatal("nil client accepted")
	}
	if calls != 0 {
		t.Fatal("validation sent an HTTP request")
	}
}

func TestSalePaymentFailureNeverRetriesAndPreservesUncertainty(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		transport error
		unknown   bool
		sentinel  error
	}{
		{"forbidden", 403, `{"success":false,"meta":{"message":"denied"}}`, nil, false, ErrForbidden},
		{"unauthorized", 401, `{"success":false,"meta":{"message":"denied"}}`, nil, false, ErrUnauthorized},
		{"server failure", 500, `{"success":false,"meta":{"message":"failed"}}`, nil, true, nil},
		{"transport loss", 0, "", errors.New("connection lost"), true, nil},
		{"malformed response", 200, `{"success":true,"data":`, nil, true, nil},
		{"missing sale state", 200, `{"success":true,"data":{}}`, nil, true, nil},
		{"null sale state", 200, `{"success":true,"data":{"state":null}}`, nil, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := NewYClients("partner", &mockHTTPClient{doFunc: func(*http.Request) (*http.Response, error) {
				calls++
				if tc.transport != nil {
					return nil, tc.transport
				}
				return newMockResponse(tc.status, tc.body), nil
			}})
			_, err := client.NewSaleAPI(781898).Pay(22, AccountPayment{44, 100})
			if err == nil || calls != 1 {
				t.Fatalf("failed payment retried or accepted: calls=%d err=%v", calls, err)
			}
			if IsOutcomeUnknown(err) != tc.unknown {
				t.Fatalf("uncertainty=%v want%v err=%v", IsOutcomeUnknown(err), tc.unknown, err)
			}
			if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Fatalf("missing compatible auth sentinel: %v", err)
			}
		})
	}
}
