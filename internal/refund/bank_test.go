package refund

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dronm/paytochkabank"
	"github.com/dronm/pronogti/internal/payment"
)

type refundTestToken struct{}

func (refundTestToken) Token(context.Context, payment.Profile) (string, error) {
	return "test-token", nil
}

func TestSDKBankSBPRefundWireAndConfirmation(t *testing.T) {
	t.Run("default BIC", func(t *testing.T) {
		testSDKBankSBPRefundWireAndConfirmation(t, "40817810802000000008", "044525104")
	})
	t.Run("legacy explicit BIC", func(t *testing.T) {
		testSDKBankSBPRefundWireAndConfirmation(t, "40817810802000000008/049999999", "049999999")
	})
}

func testSDKBankSBPRefundWireAndConfirmation(t *testing.T, accountID, bic string) {
	t.Helper()
	var posts atomic.Int32
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing bank authorization")
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/uapi/sbp/v1.0/refund":
			posts.Add(1)
			var body struct {
				Data map[string]string
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode bank request: %v", err)
			}
			for key, want := range map[string]string{
				"amount": "12.34", "currency": "RUB", "bankCode": bic,
				"accountCode": "40817810802000000008", "qrcId": "one-use-qr", "trxId": strings.Repeat("A", 32),
			} {
				if body.Data[key] != want {
					t.Errorf("%s = %q, want %q", key, body.Data[key], want)
				}
			}
			fmt.Fprint(w, `{"Data":{"requestId":"request-1","status":"Confirmed"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/uapi/sbp/v1.0/refund/request-1":
			gets.Add(1)
			fmt.Fprint(w, `{"Data":{"requestId":"request-1","status":"Accepted"}}`)
		default:
			t.Errorf("unexpected bank request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	bank := SDKBank{Tokens: refundTestToken{}}
	sale := BankPayment{
		PaymentID: "payment", Method: payment.MethodSBP, AmountKopecks: 4000,
		QRCID: "one-use-qr", TransactionID: strings.Repeat("A", 32),
		Profile: payment.Profile{ID: 1, CompanyID: 1, Environment: "sandbox", APIBaseURL: server.URL + "/uapi",
			AccountID: accountID, HTTPTimeoutSeconds: 2},
	}
	ctx := context.Background()
	baseline, err := bank.Prepare(ctx, sale, 1234)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := bank.Submit(ctx, sale, 1234, baseline)
	if err != nil || pending.State != "pending" || pending.ExternalID != "request-1" {
		t.Fatalf("unexpected submission result: %#v, %v", pending, err)
	}
	confirmed, err := bank.Check(ctx, sale, 1234, baseline, pending.ExternalID)
	if err != nil || confirmed.State != "confirmed" || confirmed.ExternalID != pending.ExternalID {
		t.Fatalf("unexpected status result: %#v, %v", confirmed, err)
	}
	for _, transactionID := range []string{"", "48232c9a-ce82-1593-3cb6-5c85a1ffef8f"} {
		sale.TransactionID = transactionID
		sale.RefTransactionID = "bank-reference"
		result, err := bank.Submit(ctx, sale, 1234, baseline)
		if err != nil || result.State != "rejected" || result.Code != "bank_refund_request_invalid" {
			t.Fatalf("unexpected result for unsupported rail %q: %#v, %v", transactionID, result, err)
		}
	}
	if posts.Load() != 1 || gets.Load() != 1 {
		t.Fatalf("unexpected number of bank calls: %d POST, %d GET", posts.Load(), gets.Load())
	}
}

func TestSDKBankCardRefundReconcilesNewOrderAfterAcknowledgment(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/uapi/acquiring/v1.0/payments/operation-1":
			status, newOrder := "APPROVED", ""
			if posts.Load() != 0 {
				status = "REFUNDED_PARTIALLY"
				newOrder = `,{"orderId":"new-refund","type":"refund","amount":12.34}`
			}
			fmt.Fprintf(w, `{"Data":{"Operation":[{"operationId":"operation-1","customerCode":"customer1","merchantId":"merchant1","paymentType":"card","amount":50,"status":%q,"Order":[{"orderId":"old-refund","type":"refund","amount":5}%s]}]}}`, status, newOrder)
		case r.Method == http.MethodPost && r.URL.Path == "/uapi/acquiring/v1.0/payments/operation-1/refund":
			posts.Add(1)
			var body struct {
				Data struct {
					Amount json.RawMessage
				}
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || string(body.Data.Amount) != "12.34" {
				t.Errorf("invalid exact card refund amount %s: %v", body.Data.Amount, err)
			}
			fmt.Fprint(w, `{"Data":{"isRefund":true,"amount":12.34,"operationId":"operation-1","status":"ON-REFUND"}}`)
		default:
			t.Errorf("unexpected bank request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	bank := SDKBank{Tokens: refundTestToken{}}
	sale := BankPayment{
		PaymentID: "payment", Method: payment.MethodCard, AmountKopecks: 5000, OperationID: "operation-1",
		Profile: payment.Profile{ID: 1, CompanyID: 1, Environment: "sandbox", APIBaseURL: server.URL + "/uapi",
			CustomerCode: "customer1", AcquiringMerchantID: "merchant1", HTTPTimeoutSeconds: 2},
	}
	ctx := context.Background()
	baseline, err := bank.Prepare(ctx, sale, 1234)
	if err != nil || len(baseline) != 1 || baseline[0] != "old-refund" {
		t.Fatalf("unexpected card baseline: %#v, %v", baseline, err)
	}
	result, err := bank.Submit(ctx, sale, 1234, baseline)
	if err != nil || result.State != "pending" || result.ExternalID != "" {
		t.Fatalf("acknowledged write was not retained as pending: %#v, %v", result, err)
	}
	confirmed, err := bank.Check(ctx, sale, 1234, baseline, "")
	if err != nil || confirmed.State != "confirmed" || confirmed.ExternalID != "new-refund" {
		t.Fatalf("unexpected card reconciliation: %#v, %v", confirmed, err)
	}
	mismatch, err := bank.Check(ctx, sale, 1235, baseline, "")
	if err != nil || mismatch.State != "unknown" {
		t.Fatalf("unexpected mismatched amount result: %#v, %v", mismatch, err)
	}
	if posts.Load() != 1 {
		t.Fatalf("bank write was repeated: %d POSTs", posts.Load())
	}
}

func TestSDKBankDoesNotTreatHTTPStatusAsCardRefundAcknowledgment(t *testing.T) {
	for name, test := range map[string]struct {
		status int
		body   string
	}{
		"ambiguous HTTP error":   {http.StatusFailedDependency, `{"message":"private provider payload"}`},
		"missing acknowledgment": {http.StatusOK, `{"Data":{"amount":12.34,"operationId":"operation-1","status":"ON-REFUND"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			t.Cleanup(server.Close)
			bank := SDKBank{Tokens: refundTestToken{}}
			sale := BankPayment{
				PaymentID: "payment", Method: payment.MethodCard, AmountKopecks: 5000, OperationID: "operation-1",
				Profile: payment.Profile{ID: 1, CompanyID: 1, Environment: "sandbox", APIBaseURL: server.URL,
					CustomerCode: "customer1", AcquiringMerchantID: "merchant1", HTTPTimeoutSeconds: 2},
			}
			result, err := bank.Submit(context.Background(), sale, 1234, []string{})
			if err != nil || result.State != "unknown" || result.Code != "bank_refund_outcome_unknown" {
				t.Fatalf("ambiguous write was not retained: %#v, %v", result, err)
			}
		})
	}
}

func TestSDKBankDoesNotConfirmAmbiguousCardRefundHistory(t *testing.T) {
	original := &paytochkabank.AcquiringPayment{
		Checkout: paytochkabank.Checkout{Status: paytochkabank.AcquiringRefundedPartially, Amount: 5000},
		Orders: []paytochkabank.AcquiringOrder{
			{OrderID: "old", Type: "refund", Amount: 1234},
			{OrderID: "new1", Type: "refund", Amount: 1234},
			{OrderID: "new2", Type: "refund", Amount: 1234},
		},
	}
	if result := cardRefundObservation(original, 1234, []string{"old"}, ""); result.State != "unknown" {
		t.Fatalf("multiple exact refund candidates were accepted: %#v", result)
	}
	if result := cardRefundObservation(original, 1234, []string{"old"}, "new2"); result.State != "confirmed" || result.ExternalID != "new2" {
		t.Fatalf("the acknowledged refund ID did not identify the exact bank record: %#v", result)
	}
}

func TestSDKBankRejectsLocalValidationFailureBeforeDispatch(t *testing.T) {
	result, err := (SDKBank{}).Submit(context.Background(), BankPayment{}, 100, nil)
	if err != nil || result.State != "rejected" || result.Code != "bank_refund_request_invalid" {
		t.Fatalf("local validation did not release the refund reservation: %#v, %v", result, err)
	}
}
