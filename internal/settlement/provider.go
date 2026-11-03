package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dronm/goyclients"
	"github.com/dronm/pronogti/internal/checkout"
)

const (
	KindVisitPayment  = "visit_payment"
	KindGoodsCreate   = "goods_create"
	KindGoodsPayment  = "goods_payment"
	KindServiceIncome = "service_income"
)

type YClientsProvider struct{ client *goyclients.YClients }

func NewYClientsProvider(client *goyclients.YClients) *YClientsProvider {
	return &YClientsProvider{client: client}
}

type operationRequest struct {
	DocumentID             int64           `json:"document_id,omitempty"`
	DependsOn              string          `json:"depends_on,omitempty"`
	AccountID              int64           `json:"account_id"`
	AmountKopecks          int64           `json:"amount_kopecks"`
	Comment                string          `json:"comment,omitempty"`
	BaselineTransactionIDs []int64         `json:"baseline_transaction_ids,omitempty"`
	Body                   json.RawMessage `json:"body,omitempty"`
}

type externalResult struct {
	DocumentID     int64   `json:"document_id,omitempty"`
	TransactionID  int64   `json:"transaction_id,omitempty"`
	TransactionIDs []int64 `json:"transaction_ids,omitempty"`
}

type financialWrite struct {
	ExpenseID  int64       `json:"expense_id"`
	Amount     json.Number `json:"amount"`
	AccountID  int64       `json:"account_id"`
	ClientID   int64       `json:"client_id"`
	SupplierID int64       `json:"supplier_id"`
	MasterID   int64       `json:"master_id"`
	Comment    string      `json:"comment"`
	Date       string      `json:"date"`
}

type goodsWrite struct {
	GoodID            int64       `json:"good_id"`
	OperationUnitType int         `json:"operation_unit_type"`
	Amount            json.Number `json:"amount"`
	CostPerUnit       json.Number `json:"cost_per_unit"`
	Discount          int         `json:"discount"`
	Cost              json.Number `json:"cost"`
	ClientID          int64       `json:"client_id"`
	MasterID          int64       `json:"master_id"`
	SupplierID        int64       `json:"supplier_id"`
	Comment           string      `json:"comment"`
}

type goodsOperationWrite struct {
	TypeID            int          `json:"type_id"`
	Comment           string       `json:"comment"`
	CreateDate        string       `json:"create_date"`
	StorageID         int64        `json:"storage_id"`
	GoodsTransactions []goodsWrite `json:"goods_transactions"`
}

// Preflight performs reads only. Runtime calls it before bank registration for
// live settlement profiles. The monetary plan is persisted by the worker after
// the bank confirms payment, using the immutable checkout and policy snapshot.
func (p *YClientsProvider) Preflight(ctx context.Context, order checkout.Order, profile Profile) error {
	if err := ValidateCheckout(order, profile); err != nil {
		return err
	}
	if profile.Mode != "live" {
		return nil
	}
	if err := p.validateTargets(ctx, order, profile); err != nil {
		return err
	}
	var visitAmount int64
	for _, item := range order.Items {
		if item.Source == checkout.SourceVisit {
			visitAmount += item.AmountKopecks
		}
	}
	if visitAmount > 0 {
		_, _, err := p.visitSale(ctx, order, profile, visitAmount)
		return err
	}
	return nil
}

func (p *YClientsProvider) validateTargets(ctx context.Context, order checkout.Order, profile Profile) error {
	if err := p.validateAccount(ctx, profile); err != nil {
		return err
	}
	for _, item := range order.Items {
		if item.Source != checkout.SourceCatalog || item.Kind != checkout.KindGood {
			continue
		}
		var storages []struct {
			ID flexibleID `json:"id"`
		}
		if err := p.client.SendRequestContext(ctx, fmt.Sprintf("/api/v1/storages/%d", profile.CompanyID), http.MethodGet, nil, nil, &storages, nil); err != nil {
			return errors.New("YCLIENTS goods warehouse could not be verified")
		}
		for _, storage := range storages {
			if int64(storage.ID) == profile.StorageID {
				return nil
			}
		}
		return errors.New("YCLIENTS goods warehouse is unavailable")
	}
	return nil
}

func (p *YClientsProvider) validateAccount(ctx context.Context, profile Profile) error {
	if p == nil || p.client == nil {
		return errors.New("YCLIENTS settlement client is not initialized")
	}
	accounts, err := p.client.AccountsContext(ctx, profile.CompanyID)
	if err != nil {
		return errors.New("YCLIENTS cashless account could not be verified")
	}
	for _, account := range accounts {
		if account.ID == profile.CashlessAccountID && account.Type == 1 {
			return nil
		}
	}
	return errors.New("YCLIENTS settlement cashless account is unavailable")
}

func (p *YClientsProvider) Plan(ctx context.Context, job Job) ([]Operation, error) {
	if err := ValidateCheckout(job.Order, job.Config); err != nil {
		return nil, err
	}
	if err := p.validateTargets(ctx, job.Order, job.Config); err != nil {
		return nil, err
	}
	if job.PaidAt.IsZero() {
		return nil, errors.New("a verified payment timestamp is required")
	}
	operations := make([]Operation, 0)
	var visitAmount int64
	var goods []goodsWrite
	var goodsAmount int64
	for index, item := range job.Order.Items {
		if item.Source == checkout.SourceVisit {
			visitAmount += item.AmountKopecks
			continue
		}
		id := fmt.Sprintf("service-%d", index+1)
		comment := reference(job.Order.ID, id)
		if item.Kind == checkout.KindService {
			title := []rune(item.Title)
			if len(title) > 100 {
				title = title[:100]
			}
			comment += fmt.Sprintf(" service=%d qty=%s %s", item.ID, item.Quantity, string(title))
			body, _ := json.Marshal(financialWrite{ExpenseID: job.Config.ServiceIncomeExpenseID, Amount: rubles(item.AmountKopecks), AccountID: job.Config.CashlessAccountID,
				ClientID: job.Order.Owner.ClientID, MasterID: item.StaffID, Comment: comment, Date: job.PaidAt.Format(time.RFC3339)})
			request, _ := json.Marshal(operationRequest{AccountID: job.Config.CashlessAccountID, AmountKopecks: item.AmountKopecks, Comment: comment, Body: body})
			operations = append(operations, Operation{ID: id, Kind: KindServiceIncome, State: "pending", Request: request})
		} else {
			goods = append(goods, goodsWrite{GoodID: item.ID, OperationUnitType: 1, Amount: json.Number(item.Quantity), CostPerUnit: rubles(item.UnitPriceKopecks), Cost: rubles(item.AmountKopecks),
				ClientID: job.Order.Owner.ClientID, MasterID: item.StaffID, Comment: reference(job.Order.ID, "goods-create") + fmt.Sprintf(" line=%d", index+1)})
			goodsAmount += item.AmountKopecks
		}
	}
	if visitAmount > 0 {
		documentID, sale, err := p.visitSale(ctx, job.Order, job.Config, visitAmount)
		if err != nil {
			return nil, err
		}
		baseline := make([]int64, 0, len(sale.State.PaymentTransactions))
		for _, transaction := range sale.State.PaymentTransactions {
			baseline = append(baseline, transaction.ID)
		}
		request, _ := json.Marshal(operationRequest{DocumentID: documentID, AccountID: job.Config.CashlessAccountID, AmountKopecks: visitAmount, BaselineTransactionIDs: baseline})
		operations = append([]Operation{{ID: "visit-payment", Kind: KindVisitPayment, State: "pending", Request: request}}, operations...)
	}
	if len(goods) > 0 {
		comment := reference(job.Order.ID, "goods-create")
		body, _ := json.Marshal(goodsOperationWrite{TypeID: 1, Comment: comment, CreateDate: job.PaidAt.Format(time.RFC3339), StorageID: job.Config.StorageID, GoodsTransactions: goods})
		request, _ := json.Marshal(operationRequest{AccountID: job.Config.CashlessAccountID, AmountKopecks: goodsAmount, Comment: comment, Body: body})
		operations = append(operations, Operation{ID: "goods-create", Kind: KindGoodsCreate, State: "pending", Request: request})
		request, _ = json.Marshal(operationRequest{DependsOn: "goods-create", AccountID: job.Config.CashlessAccountID, AmountKopecks: goodsAmount})
		operations = append(operations, Operation{ID: "goods-payment", Kind: KindGoodsPayment, State: "pending", Request: request})
	}
	return operations, nil
}

func (p *YClientsProvider) visitSale(ctx context.Context, order checkout.Order, profile Profile, amount int64) (int64, goyclients.Sale, error) {
	documentID, err := p.client.NewVisitAPI(profile.CompanyID).PaymentDocumentIDContext(ctx, order.VisitID, order.RecordID)
	if err != nil {
		return 0, goyclients.Sale{}, errors.New("YCLIENTS visit payment document could not be resolved")
	}
	sale, err := p.client.NewSaleAPI(profile.CompanyID).GetContext(ctx, documentID)
	if err != nil {
		return 0, goyclients.Sale{}, errors.New("YCLIENTS visit sale could not be read")
	}
	if err := checkSaleAccount(sale, profile.CashlessAccountID, amount); err != nil {
		return 0, goyclients.Sale{}, err
	}
	return documentID, sale, nil
}

func (p *YClientsProvider) Execute(ctx context.Context, job Job, operation Operation) (Outcome, error) {
	request, err := decodeOperation(job, operation)
	if err != nil {
		return failed("settlement_request_invalid"), nil
	}
	switch operation.Kind {
	case KindServiceIncome:
		var response financialRow
		err = p.client.SendRequestContext(ctx, fmt.Sprintf("/api/v1/finance_transactions/%d", job.Config.CompanyID), http.MethodPost, nil, request.Body, &response, nil)
		if err != nil {
			return writeFailure(err), nil
		}
		if !financialMatches(response, request, job.Order.Owner.ClientID) {
			return unknown("settlement_response_mismatch"), nil
		}
		return succeeded(externalResult{TransactionID: int64(response.ID), DocumentID: int64(response.DocumentID)}), nil
	case KindGoodsCreate:
		var response goodsOperationResponse
		err = p.client.SendRequestContext(ctx, fmt.Sprintf("/api/v1/storage_operations/operation/%d", job.Config.CompanyID), http.MethodPost, nil, request.Body, &response, nil)
		if err != nil {
			return writeFailure(err), nil
		}
		if int64(response.Document.ID) <= 0 || response.Document.TypeID != 1 || response.Document.CompanyID != job.Config.CompanyID || response.Document.Comment != request.Comment || len(response.GoodsTransactions) == 0 {
			return unknown("settlement_response_mismatch"), nil
		}
		var expected goodsOperationWrite
		if json.Unmarshal(request.Body, &expected) != nil || !goodsRowsMatch(response.GoodsTransactions, expected, int64(response.Document.ID), job.Config) {
			return unknown("settlement_response_mismatch"), nil
		}
		return succeeded(externalResult{DocumentID: int64(response.Document.ID)}), nil
	case KindVisitPayment, KindGoodsPayment:
		documentID, err := resolveDocument(job, request)
		if err != nil {
			return failed("settlement_document_unavailable"), nil
		}
		// The upstream payment response is the operation state, including existing
		// and new transactions. It is not an acquiring-bank response.
		sale, err := p.client.NewSaleAPI(job.Config.CompanyID).PayContext(ctx, documentID, goyclients.AccountPayment{AccountID: request.AccountID, AmountKopecks: request.AmountKopecks})
		if err != nil {
			return writeFailure(err), nil
		}
		ids, ok := newPaymentIDs(sale, request, documentID)
		if !ok {
			return unknown("settlement_payment_confirmation_missing"), nil
		}
		return succeeded(externalResult{DocumentID: documentID, TransactionIDs: ids}), nil
	default:
		return failed("settlement_operation_kind_invalid"), nil
	}
}

// PrepareDispatch is a read-only barrier before the durable mutation marker.
// Its failure can be retried because no financial/stock write has been sent.
func (p *YClientsProvider) PrepareDispatch(ctx context.Context, job Job, operation Operation) error {
	request, err := decodeOperation(job, operation)
	if err != nil {
		return err
	}
	if operation.Kind == KindVisitPayment || operation.Kind == KindGoodsPayment {
		documentID, err := resolveDocument(job, request)
		if err != nil {
			return err
		}
		sale, err := p.client.NewSaleAPI(job.Config.CompanyID).GetContext(ctx, documentID)
		if err != nil {
			return errors.New("YCLIENTS sale could not be verified before payment")
		}
		return checkSaleAccount(sale, request.AccountID, request.AmountKopecks)
	}
	return nil
}

func (p *YClientsProvider) Reconcile(ctx context.Context, job Job, operation Operation) (Outcome, error) {
	request, err := decodeOperation(job, operation)
	if err != nil {
		return unknown("settlement_request_invalid"), nil
	}
	switch operation.Kind {
	case KindServiceIncome:
		rows, err := p.financialRows(ctx, job)
		if err != nil {
			return unknown("settlement_reconciliation_unavailable"), nil
		}
		var matches []financialRow
		for _, row := range rows {
			if financialMatches(row, request, job.Order.Owner.ClientID) {
				matches = append(matches, row)
			}
		}
		if len(matches) == 1 {
			return succeeded(externalResult{TransactionID: int64(matches[0].ID), DocumentID: int64(matches[0].DocumentID)}), nil
		}
		return unknown("settlement_financial_outcome_unconfirmed"), nil
	case KindGoodsCreate:
		return p.reconcileGoods(ctx, job, request)
	case KindVisitPayment, KindGoodsPayment:
		// Account payments have no documented external request reference. A new
		// transaction with the same amount could belong to a different operator.
		// Without the original confirmed response this requires operator review.
		return unknown("settlement_payment_requires_review"), nil
	default:
		return unknown("settlement_operation_kind_invalid"), nil
	}
}

func decodeOperation(job Job, operation Operation) (operationRequest, error) {
	var request operationRequest
	if err := json.Unmarshal(operation.Request, &request); err != nil {
		return request, err
	}
	if job.Config.CompanyID != job.Order.Owner.CompanyID || request.AmountKopecks <= 0 || request.AccountID != job.Config.CashlessAccountID || request.AccountID <= 0 {
		return request, errors.New("settlement operation does not match its frozen profile")
	}
	return request, nil
}

func resolveDocument(job Job, request operationRequest) (int64, error) {
	if request.DocumentID > 0 {
		return request.DocumentID, nil
	}
	for _, operation := range job.Operations {
		if operation.ID != request.DependsOn || operation.State != "succeeded" {
			continue
		}
		var result externalResult
		if json.Unmarshal(operation.Result, &result) == nil && result.DocumentID > 0 {
			return result.DocumentID, nil
		}
	}
	return 0, errors.New("settlement document dependency is not complete")
}

func checkSaleAccount(sale goyclients.Sale, accountID, amount int64) error {
	for _, method := range sale.PaymentMethods {
		if method.Slug != "account" || method.AccountID != accountID {
			continue
		}
		available, err := kopecks(method.ApplicableAmount)
		if err == nil && method.IsApplicable && available >= amount {
			return nil
		}
	}
	return errors.New("YCLIENTS sale cannot accept the confirmed amount into the configured cashless account")
}

func newPaymentIDs(sale goyclients.Sale, request operationRequest, documentID int64) ([]int64, bool) {
	baseline := make(map[int64]bool)
	for _, id := range request.BaselineTransactionIDs {
		baseline[id] = true
	}
	var ids []int64
	var amount int64
	seen := make(map[int64]bool)
	for _, transaction := range sale.State.PaymentTransactions {
		if transaction.ID <= 0 || baseline[transaction.ID] || transaction.DocumentID != documentID || transaction.AccountID != request.AccountID {
			continue
		}
		if seen[transaction.ID] {
			return nil, false
		}
		seen[transaction.ID] = true
		value, err := kopecks(transaction.Amount)
		if err != nil || value <= 0 || value > checkout.MaxMoney-amount {
			return nil, false
		}
		amount += value
		ids = append(ids, transaction.ID)
	}
	return ids, len(ids) > 0 && amount == request.AmountKopecks
}

func reference(orderID, operationID string) string { return "pronogti:" + orderID + ":" + operationID }
func rubles(value int64) json.Number {
	return json.Number(fmt.Sprintf("%d.%02d", value/100, value%100))
}
func kopecks(value json.Number) (int64, error) { return checkout.ParseMoney(value.String()) }
func succeeded(result externalResult) Outcome {
	encoded, _ := json.Marshal(result)
	return Outcome{State: "succeeded", Result: encoded}
}
func failed(code string) Outcome  { return Outcome{State: "failed", ErrorCode: code} }
func unknown(code string) Outcome { return Outcome{State: "unknown", ErrorCode: code} }
func writeFailure(err error) Outcome {
	if goyclients.IsOutcomeUnknown(err) {
		return unknown("settlement_write_outcome_unknown")
	}
	return failed("settlement_write_rejected")
}

type flexibleID int64

func (v *flexibleID) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*v = 0
		return nil
	}
	value := strings.Trim(string(data), "\"")
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number < 0 {
		return errors.New("invalid YCLIENTS identifier")
	}
	*v = flexibleID(number)
	return nil
}

type flexibleRef struct {
	ID flexibleID `json:"id"`
}

func (v *flexibleRef) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || string(data) == "[]" {
		v.ID = 0
		return nil
	}
	type alias flexibleRef
	return json.Unmarshal(data, (*alias)(v))
}

type financialRow struct {
	ID         flexibleID  `json:"id"`
	DocumentID flexibleID  `json:"document_id"`
	AccountID  flexibleID  `json:"account_id"`
	ClientID   flexibleID  `json:"client_id"`
	ExpenseID  flexibleID  `json:"expense_id"`
	MasterID   flexibleID  `json:"master_id"`
	Amount     json.Number `json:"amount"`
	Comment    string      `json:"comment"`
	Deleted    bool        `json:"deleted"`
	Account    flexibleRef `json:"account"`
	Client     flexibleRef `json:"client"`
}

func financialMatches(row financialRow, request operationRequest, clientID int64) bool {
	account := int64(row.AccountID)
	if account == 0 {
		account = int64(row.Account.ID)
	}
	client := int64(row.ClientID)
	if client == 0 {
		client = int64(row.Client.ID)
	}
	amount, err := kopecks(row.Amount)
	var sent financialWrite
	if json.Unmarshal(request.Body, &sent) != nil {
		return false
	}
	return err == nil && int64(row.ID) > 0 && !row.Deleted && row.Comment == request.Comment && account == request.AccountID && client == clientID && amount == request.AmountKopecks &&
		(int64(row.ExpenseID) == 0 || int64(row.ExpenseID) == sent.ExpenseID) && (int64(row.MasterID) == 0 || int64(row.MasterID) == sent.MasterID)
}

type stockRow struct {
	ID         flexibleID  `json:"id"`
	DocumentID flexibleID  `json:"document_id"`
	GoodID     flexibleID  `json:"good_id"`
	CompanyID  int64       `json:"company_id"`
	StorageID  int64       `json:"storage_id"`
	Amount     json.Number `json:"amount"`
	Cost       json.Number `json:"cost"`
	Comment    string      `json:"comment"`
	Deleted    bool        `json:"deleted"`
	Good       flexibleRef `json:"good"`
	Storage    flexibleRef `json:"storage"`
}

type goodsOperationResponse struct {
	Document struct {
		ID        flexibleID `json:"id"`
		TypeID    int        `json:"type_id"`
		CompanyID int64      `json:"company_id"`
		Comment   string     `json:"comment"`
	} `json:"document"`
	GoodsTransactions []stockRow `json:"goods_transactions"`
}

func dateQuery(paidAt time.Time, page int) url.Values {
	return url.Values{"page": {strconv.Itoa(page)}, "count": {"100"}, "start_date": {paidAt.Add(-24 * time.Hour).Format("2006-01-02")}, "end_date": {paidAt.Add(24 * time.Hour).Format("2006-01-02")}}
}

func (p *YClientsProvider) financialRows(ctx context.Context, job Job) ([]financialRow, error) {
	var all []financialRow
	seen := make(map[int64]bool)
	for page := 1; page <= 100; page++ {
		var part []financialRow
		query := dateQuery(job.PaidAt, page)
		query.Set("account_id", strconv.FormatInt(job.Config.CashlessAccountID, 10))
		if job.Order.Owner.ClientID > 0 {
			query.Set("client_id", strconv.FormatInt(job.Order.Owner.ClientID, 10))
		}
		if err := p.client.SendRequestContext(ctx, fmt.Sprintf("/api/v1/transactions/%d", job.Config.CompanyID), http.MethodGet, query, nil, &part, nil); err != nil {
			return nil, err
		}
		added := 0
		for _, row := range part {
			if !seen[int64(row.ID)] {
				seen[int64(row.ID)] = true
				all = append(all, row)
				added++
			}
		}
		if len(part) < 100 {
			return all, nil
		}
		if added == 0 {
			return nil, errors.New("YCLIENTS transaction pagination did not advance")
		}
	}
	return nil, errors.New("YCLIENTS reconciliation search exceeded its bound")
}

func (p *YClientsProvider) reconcileGoods(ctx context.Context, job Job, request operationRequest) (Outcome, error) {
	var expected goodsOperationWrite
	if json.Unmarshal(request.Body, &expected) != nil || len(expected.GoodsTransactions) == 0 {
		return unknown("settlement_request_invalid"), nil
	}
	matched := make(map[string]stockRow)
	seen := make(map[int64]bool)
	var documentID int64
	for page := 1; page <= 100; page++ {
		var rows []stockRow
		if err := p.client.SendRequestContext(ctx, fmt.Sprintf("/api/v1/storages/transactions/%d", job.Config.CompanyID), http.MethodGet, dateQuery(job.PaidAt, page), nil, &rows, nil); err != nil {
			return unknown("settlement_reconciliation_unavailable"), nil
		}
		added := 0
		for _, row := range rows {
			if !seen[int64(row.ID)] {
				seen[int64(row.ID)] = true
				added++
			}
			for _, line := range expected.GoodsTransactions {
				if row.Comment != line.Comment || row.Deleted {
					continue
				}
				cost, err := kopecks(row.Cost)
				// The list endpoint is scoped to the company in its URL and uses
				// nested good/storage references. Creation responses also expose
				// the flat IDs. Contradictory identities never confirm a write.
				goodID, goodOK := referenceID(row.GoodID, row.Good)
				storageID, storageOK := referenceID(flexibleID(row.StorageID), row.Storage)
				if err != nil || cost != moneyValue(line.Cost) || !sameQuantityMagnitude(row.Amount, line.Amount) || !goodOK || goodID != line.GoodID || !storageOK || storageID != job.Config.StorageID || row.CompanyID != 0 && row.CompanyID != job.Config.CompanyID || int64(row.DocumentID) <= 0 || int64(row.ID) <= 0 {
					return unknown("settlement_goods_evidence_mismatch"), nil
				}
				if old, exists := matched[line.Comment]; exists && old.ID != row.ID {
					return unknown("settlement_goods_evidence_ambiguous"), nil
				}
				if documentID != 0 && documentID != int64(row.DocumentID) {
					return unknown("settlement_goods_evidence_ambiguous"), nil
				}
				documentID = int64(row.DocumentID)
				matched[line.Comment] = row
			}
		}
		if len(rows) < 100 {
			if len(matched) == len(expected.GoodsTransactions) {
				return succeeded(externalResult{DocumentID: documentID}), nil
			}
			return unknown("settlement_goods_outcome_unconfirmed"), nil
		}
		if added == 0 {
			return unknown("settlement_reconciliation_pagination_stalled"), nil
		}
	}
	return unknown("settlement_reconciliation_search_limit"), nil
}

func referenceID(flat flexibleID, nested flexibleRef) (int64, bool) {
	if flat > 0 && nested.ID > 0 && flat != nested.ID {
		return 0, false
	}
	if flat > 0 {
		return int64(flat), true
	}
	return int64(nested.ID), nested.ID > 0
}

func moneyValue(value json.Number) int64 { result, _ := kopecks(value); return result }

func sameQuantityMagnitude(left, right json.Number) bool {
	a, ok := new(big.Rat).SetString(left.String())
	if !ok {
		return false
	}
	b, ok := new(big.Rat).SetString(right.String())
	if !ok {
		return false
	}
	return a.Abs(a).Cmp(b.Abs(b)) == 0 && a.Sign() > 0
}

func goodsRowsMatch(rows []stockRow, expected goodsOperationWrite, documentID int64, profile Profile) bool {
	if len(rows) != len(expected.GoodsTransactions) {
		return false
	}
	seen := make(map[string]bool)
	for _, row := range rows {
		found := false
		for _, line := range expected.GoodsTransactions {
			if row.Comment != line.Comment {
				continue
			}
			cost, err := kopecks(row.Cost)
			if err != nil || cost != moneyValue(line.Cost) || !sameQuantityMagnitude(row.Amount, line.Amount) ||
				int64(row.GoodID) != line.GoodID || row.CompanyID != profile.CompanyID || row.StorageID != profile.StorageID ||
				int64(row.DocumentID) != documentID || int64(row.ID) <= 0 || row.Deleted || seen[row.Comment] {
				return false
			}
			seen[row.Comment] = true
			found = true
			break
		}
		if !found {
			return false
		}
	}
	return len(seen) == len(expected.GoodsTransactions)
}

var _ Provider = (*YClientsProvider)(nil)
