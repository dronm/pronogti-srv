package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dronm/goyclients"
	"github.com/dronm/pronogti/internal/checkout"
)

type providerHTTP func(*http.Request) (*http.Response, error)

func (f providerHTTP) Do(request *http.Request) (*http.Response, error) { return f(request) }
func response(data any) *http.Response {
	body, _ := json.Marshal(map[string]any{"success": true, "data": data, "meta": []any{}})
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}
}

func jobFixture() Job {
	return Job{
		Order: checkout.Order{ID: "4e6f624c-2607-4116-8f97-496708663c2d", Owner: checkout.Owner{CompanyID: 781898, ClientID: 42}, Currency: "RUB", PayableKopecks: 250001, SubtotalKopecks: 250001,
			Items: []checkout.Item{{Selection: checkout.Selection{Source: checkout.SourceCatalog, Kind: checkout.KindService, ID: 7, StaffID: 9, Quantity: "1"}, Title: "Маникюр", AmountKopecks: 250001, UnitPriceKopecks: 250001}}},
		PaymentID: "8f043761-b187-4f38-8987-f4dfc7463c0c", Environment: "production", PaidAt: time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC),
		Config: Profile{ID: 1, CompanyID: 781898, Enabled: true, Mode: "live", CashlessAccountID: 11, StorageID: 22, ServiceIncomeExpenseID: 5, ServicePolicy: "financial_income"},
	}
}

func TestCatalogServicePlanNeverCreatesAppointmentAndEncodesMoneyExactly(t *testing.T) {
	job := jobFixture()
	calls := 0
	client := goyclients.NewYClients("partner", providerHTTP(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method == "GET" && req.URL.Path == "/api/v1/accounts/781898" {
			return response([]any{map[string]any{"id": 11, "type": 1}}), nil
		}
		if req.Method != "POST" || req.URL.Path != "/api/v1/finance_transactions/781898" {
			t.Fatalf("unexpected mutation %s %s", req.Method, req.URL.Path)
		}
		var body financialWrite
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Amount.String() != "2500.01" || body.AccountID != 11 || body.ExpenseID != 5 || body.ClientID != 42 || body.MasterID != 9 || !strings.HasPrefix(body.Comment, "pronogti:"+job.Order.ID+":service-1") {
			t.Fatalf("incorrect service accounting: %+v", body)
		}
		return response(map[string]any{"id": 77, "document_id": 0, "account_id": 11, "client_id": 42, "expense_id": 5, "master_id": 9, "amount": json.Number("2500.01"), "comment": body.Comment}), nil
	}))
	provider := NewYClientsProvider(client)
	plan, err := provider.Plan(context.Background(), job)
	if err != nil || len(plan) != 1 || plan[0].Kind != KindServiceIncome {
		t.Fatalf("plan=%+v error=%v", plan, err)
	}
	outcome, err := provider.Execute(context.Background(), job, plan[0])
	if err != nil || outcome.State != "succeeded" || calls != 2 {
		t.Fatalf("outcome=%+v calls=%d error=%v", outcome, calls, err)
	}
}

func TestServiceIncomeLostResponseRecoversOnlyExactReferenceWithoutRepeatingWrite(t *testing.T) {
	job := jobFixture()
	writes := 0
	var stored financialWrite
	client := goyclients.NewYClients("partner", providerHTTP(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/v1/accounts/781898":
			return response([]any{map[string]any{"id": 11, "type": 1}}), nil
		case "/api/v1/finance_transactions/781898":
			writes++
			json.NewDecoder(req.Body).Decode(&stored)
			return nil, errors.New("response lost after upstream commit")
		case "/api/v1/transactions/781898":
			if req.Method != "GET" || req.URL.Query().Get("account_id") != "11" || req.URL.Query().Get("client_id") != "42" || req.Body != nil {
				t.Fatal("reconciliation did not use documented query parameters")
			}
			return response([]any{map[string]any{"id": "77", "document_id": 0, "account": map[string]any{"id": "11"}, "client": map[string]any{"id": "42"}, "amount": "2500.01", "comment": stored.Comment}}), nil
		default:
			t.Fatalf("unexpected request %s", req.URL.Path)
			return nil, nil
		}
	}))
	provider := NewYClientsProvider(client)
	plan, err := provider.Plan(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	outcome, _ := provider.Execute(context.Background(), job, plan[0])
	if outcome.State != "unknown" {
		t.Fatal("lost response was treated as a definite failure")
	}
	outcome, _ = provider.Reconcile(context.Background(), job, plan[0])
	if outcome.State != "succeeded" || writes != 1 {
		t.Fatalf("recovery=%+v writes=%d", outcome, writes)
	}
}

func TestServiceIncomeAmbiguousEvidenceRemainsUnknown(t *testing.T) {
	job := jobFixture()
	body, _ := json.Marshal(financialWrite{AccountID: 11, ClientID: 42, ExpenseID: 5, MasterID: 9, Amount: rubles(250001), Comment: "unique"})
	request, _ := json.Marshal(operationRequest{AccountID: 11, AmountKopecks: 250001, Comment: "unique", Body: body})
	client := goyclients.NewYClients("partner", providerHTTP(func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" {
			t.Fatal("reconcile repeated a write")
		}
		return response([]any{map[string]any{"id": 1, "account_id": 11, "client_id": 42, "amount": json.Number("2500.01"), "comment": "unique"}, map[string]any{"id": 2, "account_id": 11, "client_id": 42, "amount": json.Number("2500.01"), "comment": "unique"}}), nil
	}))
	outcome, _ := NewYClientsProvider(client).Reconcile(context.Background(), job, Operation{ID: "service-1", Kind: KindServiceIncome, Request: request})
	if outcome.State != "unknown" {
		t.Fatal("ambiguous financial evidence accepted")
	}
}

func TestGoodsSaleUsesStandaloneDocumentThenAccountPayment(t *testing.T) {
	job := jobFixture()
	job.Order.Items[0].Kind = checkout.KindGood
	job.Order.Items[0].Quantity = "1.25"
	job.Order.Items[0].AmountKopecks = 12500
	job.Order.Items[0].UnitPriceKopecks = 10000
	job.Order.PayableKopecks = 12500
	job.Order.SubtotalKopecks = 12500
	var stored goodsOperationWrite
	writes := 0
	client := goyclients.NewYClients("partner", providerHTTP(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/v1/accounts/781898":
			return response([]any{map[string]any{"id": 11, "type": 1}}), nil
		case "/api/v1/storages/781898":
			return response([]any{map[string]any{"id": 22}}), nil
		case "/api/v1/storage_operations/operation/781898":
			writes++
			if req.Method != "POST" {
				t.Fatal("goods mutation method")
			}
			if err := json.NewDecoder(req.Body).Decode(&stored); err != nil {
				t.Fatal(err)
			}
			if stored.TypeID != 1 || stored.StorageID != 22 || len(stored.GoodsTransactions) != 1 || stored.GoodsTransactions[0].Amount != "1.25" {
				t.Fatalf("goods request=%+v", stored)
			}
			return response(map[string]any{"document": map[string]any{"id": 99, "type_id": 1, "company_id": 781898, "comment": stored.Comment}, "goods_transactions": []any{map[string]any{"id": 33, "document_id": 99, "company_id": 781898, "storage_id": 22, "good_id": 7, "amount": json.Number("-1.25"), "cost": 125, "comment": stored.GoodsTransactions[0].Comment}}}), nil
		case "/api/v1/company/781898/sale/99/payment":
			writes++
			if req.Method != "POST" {
				t.Fatal("payment mutation method")
			}
			return response(map[string]any{"state": map[string]any{"payment_transactions": []any{map[string]any{"id": 101, "document_id": 99, "account_id": 11, "amount": 125}}}}), nil
		default:
			t.Fatalf("unexpected endpoint %s", req.URL.Path)
			return nil, nil
		}
	}))
	provider := NewYClientsProvider(client)
	plan, err := provider.Plan(context.Background(), job)
	if err != nil || len(plan) != 2 || plan[0].Kind != KindGoodsCreate || plan[1].Kind != KindGoodsPayment {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	outcome, _ := provider.Execute(context.Background(), job, plan[0])
	if outcome.State != "succeeded" {
		t.Fatalf("creation=%+v", outcome)
	}
	plan[0].State = "succeeded"
	plan[0].Result = outcome.Result
	job.Operations = plan
	outcome, _ = provider.Execute(context.Background(), job, plan[1])
	if outcome.State != "succeeded" || writes != 2 {
		t.Fatalf("payment=%+v writes=%d", outcome, writes)
	}
}

func TestVisitPaymentRecoveryDoesNotAttributeAnotherOperatorsPayment(t *testing.T) {
	job := jobFixture()
	request, _ := json.Marshal(operationRequest{DocumentID: 55, AccountID: 11, AmountKopecks: 250001, BaselineTransactionIDs: []int64{77}})
	provider := NewYClientsProvider(goyclients.NewYClients("partner", providerHTTP(func(*http.Request) (*http.Response, error) {
		t.Fatal("uncertain visit payment must not be resubmitted or inferred from equal amounts")
		return nil, nil
	})))
	outcome, _ := provider.Reconcile(context.Background(), job, Operation{Kind: KindVisitPayment, State: "unknown", Request: request})
	if outcome.State != "unknown" || outcome.ErrorCode != "settlement_payment_requires_review" {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestSettlementPolicyAndCompanyIsolationBeforeCharging(t *testing.T) {
	job := jobFixture()
	if err := ValidateCheckout(job.Order, job.Config); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Profile){func(p *Profile) { p.CompanyID++ }, func(p *Profile) { p.Enabled = false }, func(p *Profile) { p.CashlessAccountID = 0 }, func(p *Profile) { p.ServiceIncomeExpenseID = 0 }, func(p *Profile) { p.ServicePolicy = "require_visit" }} {
		profile := job.Config
		mutate(&profile)
		if ValidateCheckout(job.Order, profile) == nil {
			t.Fatal("invalid settlement profile accepted")
		}
	}
}

func TestGoodsEvidenceRequiresQuantityAndAllLines(t *testing.T) {
	profile := jobFixture().Config
	expected := goodsOperationWrite{GoodsTransactions: []goodsWrite{{GoodID: 7, Amount: "1.25", Cost: "125.00", Comment: "ref"}}}
	rows := []stockRow{{ID: 1, DocumentID: 99, GoodID: 7, CompanyID: 781898, StorageID: 22, Amount: "-1.25", Cost: "125.00", Comment: "ref"}}
	if !goodsRowsMatch(rows, expected, 99, profile) {
		t.Fatal("exact goods evidence rejected")
	}
	rows[0].Amount = "-2.5"
	if goodsRowsMatch(rows, expected, 99, profile) {
		t.Fatal("different quantity matched goods evidence")
	}
}

func TestGoodsRecoveryReadsDocumentedNestedReferencesWithoutAnotherWrite(t *testing.T) {
	job := jobFixture()
	comment := reference(job.Order.ID, "goods-create")
	body, _ := json.Marshal(goodsOperationWrite{TypeID: 1, StorageID: 22, Comment: comment, GoodsTransactions: []goodsWrite{{GoodID: 7, Amount: "1.25", Cost: "125.00", Comment: comment + " line=1"}}})
	request, _ := json.Marshal(operationRequest{AccountID: 11, AmountKopecks: 12500, Comment: comment, Body: body})
	operation := Operation{Kind: KindGoodsCreate, Request: request, State: "unknown"}
	for _, warehouse := range []int64{22, 23} {
		provider := NewYClientsProvider(goyclients.NewYClients("partner", providerHTTP(func(req *http.Request) (*http.Response, error) {
			if req.Method != "GET" || req.URL.Path != "/api/v1/storages/transactions/781898" {
				t.Fatal("goods recovery attempted a write or escaped company scope")
			}
			return response([]any{map[string]any{"id": 33, "document_id": 99, "good": map[string]any{"id": "7"}, "storage": map[string]any{"id": warehouse}, "amount": "-1.25", "cost": "125.00", "comment": comment + " line=1"}}), nil
		})))
		outcome, err := provider.Reconcile(context.Background(), job, operation)
		if err != nil || warehouse == 22 && outcome.State != "succeeded" || warehouse != 22 && outcome.State != "unknown" {
			t.Fatalf("warehouse=%d outcome=%+v err=%v", warehouse, outcome, err)
		}
	}
}

func TestPaymentConfirmationRejectsRepeatedTransactionIdentifiers(t *testing.T) {
	request := operationRequest{AccountID: 11, AmountKopecks: 20000}
	transaction := goyclients.SalePaymentTransaction{ID: 77, DocumentID: 99, AccountID: 11, Amount: "100.00"}
	if _, confirmed := newPaymentIDs(goyclients.Sale{State: goyclients.SaleState{PaymentTransactions: []goyclients.SalePaymentTransaction{transaction, transaction}}}, request, 99); confirmed {
		t.Fatal("repeated transaction ID fabricated a payment total")
	}
}

func TestVisitPreflightResolvesDocumentAndVerifiesCashlessCapacityWithoutWriting(t *testing.T) {
	job := jobFixture()
	job.Order.VisitID, job.Order.RecordID = 55, 66
	job.Order.Items[0].Source = checkout.SourceVisit
	for _, available := range []string{"2500.01", "2499.99"} {
		provider := NewYClientsProvider(goyclients.NewYClients("partner", providerHTTP(func(req *http.Request) (*http.Response, error) {
			if req.Method != "GET" {
				t.Fatal("preflight attempted an external mutation")
			}
			switch req.URL.Path {
			case "/api/v1/accounts/781898":
				return response([]any{map[string]any{"id": 11, "type": 1}}), nil
			case "/api/v1/visits/55":
				return response(map[string]any{"records": []any{map[string]any{"id": 66, "company_id": 781898, "documents": []any{map[string]any{"id": 99, "type_id": 7, "company_id": 781898, "visit_id": 55, "record_id": 66}}}}}), nil
			case "/api/v1/company/781898/sale/99":
				return response(map[string]any{"state": map[string]any{}, "payment_methods": []any{map[string]any{"slug": "account", "account_id": 11, "is_applicable": true, "applicable_amount": available}}}), nil
			default:
				t.Fatalf("unexpected preflight read %s", req.URL.Path)
				return nil, nil
			}
		})))
		err := provider.Preflight(context.Background(), job.Order, job.Config)
		if available == "2500.01" && err != nil || available != "2500.01" && err == nil {
			t.Fatalf("cashless capacity %s: %v", available, err)
		}
	}
}
