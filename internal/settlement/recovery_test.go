package settlement

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dronm/goyclients"
)

func recoveryFixture() (Job, Operation) {
	job := jobFixture()
	job.Order.State = "paid"
	request, _ := json.Marshal(operationRequest{DocumentID: 99, AccountID: 11, AmountKopecks: 250001, BaselineTransactionIDs: []int64{1}})
	return job, Operation{ID: "visit-payment", Kind: KindVisitPayment, State: "unknown", Request: request}
}

func recoveryRows() []goyclients.SalePaymentTransaction {
	return []goyclients.SalePaymentTransaction{
		{ID: 1, DocumentID: 99, AccountID: 11, Amount: "50.00"},
		{ID: 77, DocumentID: 99, AccountID: 11, Amount: "1000.00"},
		{ID: 78, DocumentID: 99, AccountID: 11, Amount: "1500.01"},
	}
}

func TestOperatorEvidenceConfirmsOnlyExplicitTransactionsAndPreservesAudit(t *testing.T) {
	job, operation := recoveryFixture()
	reads := 0
	provider := NewYClientsProvider(goyclients.NewYClients("partner", providerHTTP(func(request *http.Request) (*http.Response, error) {
		reads++
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/company/781898/sale/99" {
			t.Fatal("operator evidence verification mutated or read a different sale")
		}
		return response(goyclients.Sale{State: goyclients.SaleState{PaymentTransactions: recoveryRows()}}), nil
	})))
	before := time.Now().UTC()
	outcome, err := provider.VerifyPaymentEvidence(context.Background(), job, operation, []int64{77, 78}, "  Reviewed bank reference and YCLIENTS transactions  ")
	if err != nil || outcome.State != "succeeded" || reads != 1 {
		t.Fatalf("outcome=%+v reads=%d err=%v", outcome, reads, err)
	}
	var result struct {
		DocumentID       int64   `json:"document_id"`
		TransactionIDs   []int64 `json:"transaction_ids"`
		OperatorEvidence struct {
			Reason     string    `json:"reason"`
			VerifiedAt time.Time `json:"verified_at"`
		} `json:"operator_evidence"`
	}
	if json.Unmarshal(outcome.Result, &result) != nil || result.DocumentID != 99 || len(result.TransactionIDs) != 2 || result.TransactionIDs[0] != 77 || result.TransactionIDs[1] != 78 || result.OperatorEvidence.Reason != "Reviewed bank reference and YCLIENTS transactions" || result.OperatorEvidence.VerifiedAt.Before(before) {
		t.Fatalf("operator evidence audit missing: %s", outcome.Result)
	}
}

func TestOperatorEvidenceRejectsInvalidIntentBeforeContactingYClients(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		ids    []int64
		reason string
		mutate func(*Job, *Operation)
	}{
		{name: "missing IDs", reason: "reviewed"},
		{name: "duplicate IDs", ids: []int64{77, 77}, reason: "reviewed"},
		{name: "baseline ID", ids: []int64{1}, reason: "reviewed"},
		{name: "nonpositive ID", ids: []int64{0}, reason: "reviewed"},
		{name: "missing reason", ids: []int64{77, 78}, reason: "  "},
		{name: "unpaid bank", ids: []int64{77, 78}, reason: "reviewed", mutate: func(job *Job, _ *Operation) { job.Order.State = "awaiting_payment" }},
		{name: "sandbox bank", ids: []int64{77, 78}, reason: "reviewed", mutate: func(job *Job, _ *Operation) { job.Environment = "sandbox" }},
		{name: "not dispatched", ids: []int64{77, 78}, reason: "reviewed", mutate: func(_ *Job, operation *Operation) { operation.State = "pending" }},
		{name: "not account payment", ids: []int64{77, 78}, reason: "reviewed", mutate: func(_ *Job, operation *Operation) { operation.Kind = KindServiceIncome }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			job, operation := recoveryFixture()
			if testCase.mutate != nil {
				testCase.mutate(&job, &operation)
			}
			provider := NewYClientsProvider(goyclients.NewYClients("partner", providerHTTP(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid operator evidence contacted YCLIENTS")
				return nil, nil
			})))
			if _, err := provider.VerifyPaymentEvidence(context.Background(), job, operation, testCase.ids, testCase.reason); err == nil {
				t.Fatal("invalid operator evidence accepted")
			}
		})
	}
}

func TestOperatorEvidenceRejectsWrongAmountAccountDocumentAndAmbiguity(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func([]goyclients.SalePaymentTransaction) []goyclients.SalePaymentTransaction
	}{
		{"wrong amount", func(rows []goyclients.SalePaymentTransaction) []goyclients.SalePaymentTransaction {
			rows[2].Amount = "1500.00"
			return rows
		}},
		{"wrong account", func(rows []goyclients.SalePaymentTransaction) []goyclients.SalePaymentTransaction {
			rows[2].AccountID = 12
			return rows
		}},
		{"wrong document", func(rows []goyclients.SalePaymentTransaction) []goyclients.SalePaymentTransaction {
			rows[2].DocumentID = 100
			return rows
		}},
		{"missing transaction", func(rows []goyclients.SalePaymentTransaction) []goyclients.SalePaymentTransaction { return rows[:2] }},
		{"duplicate response ID", func(rows []goyclients.SalePaymentTransaction) []goyclients.SalePaymentTransaction {
			return append(rows, rows[2])
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			job, operation := recoveryFixture()
			provider := NewYClientsProvider(goyclients.NewYClients("partner", providerHTTP(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodGet {
					t.Fatal("operator evidence verification sent a write")
				}
				return response(goyclients.Sale{State: goyclients.SaleState{PaymentTransactions: testCase.mutate(recoveryRows())}}), nil
			})))
			if _, err := provider.VerifyPaymentEvidence(context.Background(), job, operation, []int64{77, 78}, "reviewed"); err == nil {
				t.Fatal("invalid financial evidence accepted")
			}
		})
	}
}

func TestOperatorEvidenceUsesOriginalGoodsDocumentDependency(t *testing.T) {
	job, operation := recoveryFixture()
	request, _ := json.Marshal(operationRequest{DependsOn: "goods-create", AccountID: 11, AmountKopecks: 250001})
	operation.Kind, operation.Request = KindGoodsPayment, request
	job.Operations = []Operation{{ID: "goods-create", State: "succeeded", Result: json.RawMessage(`{"document_id":99}`)}}
	provider := NewYClientsProvider(goyclients.NewYClients("partner", providerHTTP(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/company/781898/sale/99" {
			t.Fatal("goods resolution did not use its original created document")
		}
		return response(goyclients.Sale{State: goyclients.SaleState{PaymentTransactions: recoveryRows()}}), nil
	})))
	if outcome, err := provider.VerifyPaymentEvidence(context.Background(), job, operation, []int64{77, 78}, "reviewed goods payment"); err != nil || outcome.State != "succeeded" {
		t.Fatalf("goods evidence outcome=%+v err=%v", outcome, err)
	}
}
