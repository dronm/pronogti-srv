package goyclients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// MaxPaymentKopecks keeps the integer payment amount exactly representable in
// applications that exchange JSON with JavaScript. No floating point conversion
// is used to encode the corresponding ruble amount.
const MaxPaymentKopecks int64 = 9007199254740991

// AccountPayment records money already received in a YCLIENTS financial account.
// AccountID is a YCLIENTS cashdesk ID, not an acquiring bank account identifier.
// AmountKopecks is the incremental amount of this payment, including partial pay.
type AccountPayment struct {
	AccountID     int64
	AmountKopecks int64
}

func (p AccountPayment) Validate() error {
	if p.AccountID <= 0 {
		return errors.New("YCLIENTS payment account ID must be positive")
	}
	if p.AmountKopecks <= 0 || p.AmountKopecks > MaxPaymentKopecks {
		return errors.New("payment amount must be positive integer kopecks within the supported range")
	}
	return nil
}

// SaleAPI addresses existing sale documents. A document may belong to a visit
// or a standalone goods sale; document IDs are distinct from visit/record IDs.
type SaleAPI struct {
	client    *YClients
	companyID int64
}

func (c *YClients) NewSaleAPI(companyID int64) *SaleAPI {
	return &SaleAPI{client: c, companyID: companyID}
}

// Sale is the sale-operation response returned by both Get and Pay. Money and
// quantities use json.Number so the API's decimal values are preserved exactly.
type Sale struct {
	State          SaleState           `json:"state"`
	KKMState       json.RawMessage     `json:"kkm_state"`
	PaymentMethods []SalePaymentMethod `json:"payment_methods"`
}

type SaleState struct {
	Items               []SaleItem               `json:"items"`
	PaymentTransactions []SalePaymentTransaction `json:"payment_transactions"`
	LoyaltyTransactions []json.RawMessage        `json:"loyalty_transactions"`
}

type SaleItem struct {
	ID                    int64       `json:"id"`
	Type                  string      `json:"type"`
	DocumentID            int64       `json:"document_id,omitempty"`
	Title                 string      `json:"title"`
	Amount                json.Number `json:"amount"`
	DefaultCostPerUnit    json.Number `json:"default_cost_per_unit"`
	DefaultCostTotal      json.Number `json:"default_cost_total"`
	ClientDiscountPercent json.Number `json:"client_discount_percent"`
	CostToPayTotal        json.Number `json:"cost_to_pay_total"`
}

type SalePaymentTransaction struct {
	ID           int64           `json:"id"`
	DocumentID   int64           `json:"document_id"`
	SaleItemID   int64           `json:"sale_item_id"`
	SaleItemType string          `json:"sale_item_type"`
	ExpenseID    int64           `json:"expense_id"`
	AccountID    int64           `json:"account_id"`
	Amount       json.Number     `json:"amount"`
	CreatedAt    json.RawMessage `json:"created_at"`
	Account      *Account        `json:"account"`
	Expense      *CatalogRef     `json:"expense"`
}

type SalePaymentMethod struct {
	Slug             string          `json:"slug"`
	IsApplicable     bool            `json:"is_applicable"`
	ApplicableAmount json.Number     `json:"applicable_amount"`
	ApplicableCount  json.Number     `json:"applicable_count"`
	ApplicableValue  json.Number     `json:"applicable_value"`
	AccountID        int64           `json:"account_id"`
	Account          *Account        `json:"account"`
	DepositID        int64           `json:"deposit_id,omitempty"`
	LoyaltyCardID    int64           `json:"loyalty_card_id,omitempty"`
	LoyaltyProgramID int64           `json:"loyalty_program_id,omitempty"`
	Deposit          json.RawMessage `json:"deposit,omitempty"`
}

// UnmarshalJSON rejects an envelope that cannot identify sale-operation state.
// After a write, SendRequestContext classifies this decode failure as uncertain.
func (s *Sale) UnmarshalJSON(data []byte) error {
	type saleAlias Sale
	var stateProbe struct {
		State json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(data, &stateProbe); err != nil {
		return err
	}
	if len(stateProbe.State) == 0 || string(stateProbe.State) == "null" {
		return errors.New("YCLIENTS sale response has no state")
	}
	var decoded saleAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*s = Sale(decoded)
	return nil
}

func (c *SaleAPI) validate(documentID int64) error {
	if c == nil || c.client == nil {
		return errors.New("YCLIENTS sale client is not initialized")
	}
	if c.companyID <= 0 || documentID <= 0 {
		return errors.New("company and sale document IDs must be positive")
	}
	return nil
}

func (c *SaleAPI) Get(documentID int64) (Sale, error) {
	return c.GetContext(context.Background(), documentID)
}

// GetContext reads the current items, transaction history and available account
// methods. Applications must check these before charging and when reconciling.
func (c *SaleAPI) GetContext(ctx context.Context, documentID int64) (Sale, error) {
	if err := c.validate(documentID); err != nil {
		return Sale{}, err
	}
	var response Sale
	err := c.client.SendRequestContext(ctx,
		fmt.Sprintf("%s/company/%d/sale/%d", apiVersionEndpoint, c.companyID, documentID),
		http.MethodGet, nil, nil, &response, nil)
	if err != nil {
		return Sale{}, err
	}
	return response, nil
}

func (c *SaleAPI) Pay(documentID int64, payment AccountPayment) (Sale, error) {
	return c.PayContext(context.Background(), documentID, payment)
}

// PayContext records one account payment using the documented sale-payment API.
// It does not charge a bank, create a visit or print a receipt. The caller must
// supply an amount already confirmed by the bank and serialize concurrent writes.
// There is no automatic retry: reconcile OutcomeUnknownError through GetContext
// before deciding whether another write is appropriate.
func (c *SaleAPI) PayContext(ctx context.Context, documentID int64, payment AccountPayment) (Sale, error) {
	if err := c.validate(documentID); err != nil {
		return Sale{}, err
	}
	if err := payment.Validate(); err != nil {
		return Sale{}, err
	}
	request := struct {
		Payment struct {
			Method struct {
				Slug      string `json:"slug"`
				AccountID int64  `json:"account_id"`
			} `json:"method"`
			Amount json.Number `json:"amount"`
		} `json:"payment"`
	}{}
	request.Payment.Method.Slug = "account"
	request.Payment.Method.AccountID = payment.AccountID
	request.Payment.Amount = json.Number(fmt.Sprintf("%d.%02d", payment.AmountKopecks/100, payment.AmountKopecks%100))
	var response Sale
	err := c.client.SendRequestContext(ctx,
		fmt.Sprintf("%s/company/%d/sale/%d/payment", apiVersionEndpoint, c.companyID, documentID),
		http.MethodPost, nil, request, &response, nil)
	if err != nil {
		return Sale{}, err
	}
	return response, nil
}
