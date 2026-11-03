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

func paymentVisitResponse(records string) string {
	return `{"success":true,"data":{"records":` + records + `},"meta":[]}`
}

func TestVisitPaymentUsesLinkedDocumentAndExplicitCashdesk(t *testing.T) {
	calls := 0
	yc := NewYClients("partner", &mockHTTPClient{doFunc: func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if req.Method != "GET" || req.URL.Path != "/api/v1/visits/88" {
				t.Fatalf("visit lookup: %s %s", req.Method, req.URL.Path)
			}
			return newMockResponse(200, paymentVisitResponse(`[{"id":99,"company_id":781898,"documents":[{"id":777,"type_id":7,"company_id":781898,"visit_id":88,"record_id":99}]}]`)), nil
		}
		if calls != 2 || req.Method != "POST" || req.URL.Path != "/api/v1/company/781898/sale/777/payment" {
			t.Fatalf("payment: %s %s", req.Method, req.URL.Path)
		}
		body, _ := io.ReadAll(req.Body)
		if string(body) != `{"payment":{"method":{"slug":"account","account_id":123},"amount":2500.01}}` {
			t.Fatalf("payment body: %s", body)
		}
		return newMockResponse(200, `{"success":true,"data":{"state":{"items":[],"payment_transactions":[{"id":501,"document_id":777,"account_id":123,"amount":2500.01}]}},"meta":[]}`), nil
	}})
	_, err := yc.NewVisitAPI(781898).Pay(88, 99, AccountPayment{AccountID: 123, AmountKopecks: 250001})
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestVisitPaymentRejectsMissingAmbiguousOrForeignDocumentBeforeWrite(t *testing.T) {
	cases := map[string]string{
		"missing record":      `[]`,
		"foreign record":      `[{"id":99,"company_id":8,"documents":[]}]`,
		"missing document":    `[{"id":99,"company_id":781898,"documents":[]}]`,
		"goods document only": `[{"id":99,"company_id":781898,"documents":[{"id":1,"type_id":1,"company_id":781898}]}]`,
		"foreign document":    `[{"id":99,"company_id":781898,"documents":[{"id":1,"type_id":7,"company_id":8}]}]`,
		"wrong visit":         `[{"id":99,"company_id":781898,"documents":[{"id":1,"type_id":7,"company_id":781898,"visit_id":89}]}]`,
		"wrong record":        `[{"id":99,"company_id":781898,"documents":[{"id":1,"type_id":7,"company_id":781898,"record_id":98}]}]`,
		"multiple documents":  `[{"id":99,"company_id":781898,"documents":[{"id":1,"type_id":7,"company_id":781898},{"id":2,"type_id":7,"company_id":781898}]}]`,
		"duplicate record":    `[{"id":99,"company_id":781898,"documents":[{"id":1,"type_id":7,"company_id":781898}]},{"id":99,"company_id":781898}]`,
	}
	for name, records := range cases {
		t.Run(name, func(t *testing.T) {
			calls := 0
			yc := NewYClients("partner", &mockHTTPClient{doFunc: func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" {
					t.Fatal("invalid visit caused a payment write")
				}
				return newMockResponse(200, paymentVisitResponse(records)), nil
			}})
			_, err := yc.NewVisitAPI(781898).Pay(88, 99, AccountPayment{AccountID: 1, AmountKopecks: 100})
			if err == nil || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestVisitPaymentValidationAndCancellationDoNotWrite(t *testing.T) {
	yc := NewYClients("partner", &mockHTTPClient{doFunc: func(*http.Request) (*http.Response, error) { t.Fatal("unexpected network call"); return nil, nil }})
	for _, values := range [][3]int64{{0, 88, 99}, {781898, 0, 99}, {781898, 88, -1}} {
		_, err := yc.NewVisitAPI(values[0]).PaymentDocumentID(values[1], values[2])
		if err == nil {
			t.Fatal("invalid identity accepted")
		}
	}
	_, err := yc.NewVisitAPI(781898).Pay(88, 99, AccountPayment{})
	if err == nil {
		t.Fatal("invalid payment accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = yc.NewVisitAPI(781898).PaymentDocumentIDContext(ctx, 88, 99)
	if !errors.Is(err, context.Canceled) || IsOutcomeUnknown(err) {
		t.Fatalf("pre-read cancellation: %v", err)
	}
}

func TestVisitLookupFailureIsNotAnUncertainPayment(t *testing.T) {
	yc := NewYClients("partner", &mockHTTPClient{doFunc: func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" {
			t.Fatal("lookup failure caused payment")
		}
		return nil, errors.New("offline")
	}})
	_, err := yc.NewVisitAPI(781898).Pay(88, 99, AccountPayment{AccountID: 1, AmountKopecks: 100})
	if err == nil || IsOutcomeUnknown(err) {
		t.Fatalf("lookup error: %v", err)
	}
}

func TestVisitDocumentResolutionIgnoresUnrelatedLegacyFields(t *testing.T) {
	yc := NewYClients("partner", &mockHTTPClient{doFunc: func(req *http.Request) (*http.Response, error) {
		body := paymentVisitResponse(`[{"id":99,"company_id":781898,"client":{"phone":79990000000},"staff":[],"documents":[{"id":777,"type_id":7,"company_id":781898}]}]`)
		if !json.Valid([]byte(body)) || strings.Contains(req.URL.Path, "sale") {
			t.Fatal("bad test lookup")
		}
		return newMockResponse(200, body), nil
	}})
	id, err := yc.NewVisitAPI(781898).PaymentDocumentID(88, 99)
	if err != nil || id != 777 {
		t.Fatalf("id=%d error=%v", id, err)
	}
}
