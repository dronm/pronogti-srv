// Package refund coordinates a bank refund and its return-of-sale receipt.
package refund

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"regexp"
	"time"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/fiscal"
	"github.com/dronm/pronogti/internal/payment"
	"github.com/dronm/webapp"
	"github.com/google/uuid"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type Sale struct {
	Bank          BankPayment
	OrderID       string
	CompanyID     int64
	PaymentState  string
	OrderState    string
	FiscalState   string
	Items         []checkout.Item
	FiscalProfile fiscal.Profile
	SaleReceipt   json.RawMessage
}

type CreateRequest struct {
	Items []Selection `json:"items"`
}

type Refund struct {
	ID             string          `json:"id"`
	PaymentID      string          `json:"payment_id"`
	OrderID        string          `json:"order_id"`
	CompanyID      int64           `json:"company_id"`
	OperatorID     int64           `json:"operator_id"`
	AmountKopecks  int64           `json:"amount_kopecks"`
	Lines          []Line          `json:"lines"`
	BankState      string          `json:"bank_state"`
	FiscalState    string          `json:"fiscal_state"`
	BankReference  string          `json:"bank_reference,omitempty"`
	ErrorCode      string          `json:"error_code,omitempty"`
	ReceiptNumber  int64           `json:"receipt_number,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	ReceiptPayload json.RawMessage `json:"-"`
}

type QuoteLine struct {
	LineIndex              int    `json:"line_index"`
	ItemID                 int64  `json:"item_id"`
	Kind                   string `json:"kind"`
	Title                  string `json:"title"`
	Quantity               string `json:"quantity"`
	UnitPriceKopecks       int64  `json:"unit_price_kopecks"`
	AmountKopecks          int64  `json:"amount_kopecks"`
	RemainingQuantityMilli int64  `json:"remaining_quantity_milli"`
	RemainingAmountKopecks int64  `json:"remaining_amount_kopecks"`
}

type Quote struct {
	PaymentID        string      `json:"payment_id"`
	OrderID          string      `json:"order_id"`
	Environment      string      `json:"environment"`
	Method           string      `json:"method"`
	PaidKopecks      int64       `json:"paid_kopecks"`
	AvailableKopecks int64       `json:"available_kopecks"`
	Eligible         bool        `json:"eligible"`
	Reason           string      `json:"reason,omitempty"`
	Items            []QuoteLine `json:"items"`
	Refunds          []Refund    `json:"refunds"`
}

type Job struct {
	Refund     Refund
	Sale       Sale
	Step       string
	Baseline   []string
	ClaimToken string
}

type Outcome struct {
	State         string
	Reference     string
	ErrorCode     string
	Baseline      []string
	ReceiptNumber int64
	RetryAfter    time.Duration
}

type Repository interface {
	LoadSale(context.Context, string) (Sale, []Refund, error)
	Reserve(context.Context, string, int64, string, string, func(Sale, []Refund) (Refund, error)) (Refund, error)
	Get(context.Context, string, string) (Refund, error)
	ClaimNext(context.Context) (*Job, error)
	Finish(context.Context, Job, Outcome) error
}

type Bank interface {
	Validate(BankPayment) error
	Prepare(context.Context, BankPayment, int64) ([]string, error)
	Submit(context.Context, BankPayment, int64, []string) (BankObservation, error)
	Check(context.Context, BankPayment, int64, []string, string) (BankObservation, error)
}

type Service struct {
	repo      Repository
	bank      Bank
	driver    fiscal.Driver
	allowLive bool
}

func NewService(repo Repository, bank Bank, driver fiscal.Driver, allowLive bool) *Service {
	return &Service{repo: repo, bank: bank, driver: driver, allowLive: allowLive}
}

func (s *Service) Quote(ctx context.Context, paymentID string) (Quote, error) {
	sale, refunds, err := s.repo.LoadSale(ctx, paymentID)
	if err != nil {
		return Quote{}, err
	}
	used := consumed(refunds)
	result := Quote{
		PaymentID: sale.Bank.PaymentID, OrderID: sale.OrderID,
		Environment: sale.Bank.Profile.Environment, Method: sale.Bank.Method,
		PaidKopecks: sale.Bank.AmountKopecks, Items: make([]QuoteLine, 0, len(sale.Items)),
		Refunds: append([]Refund{}, refunds...),
	}
	if sale.Bank.Method == payment.MethodTerminalCard {
		result.Reason = "terminal_refunds_unsupported"
		return result, nil
	}
	for index, item := range sale.Items {
		_, quantity, err := checkout.ParseQuantity(item.Quantity)
		if err != nil {
			return Quote{}, webapp.Conflict("original sale quantity is invalid", nil)
		}
		remaining := item.AmountKopecks - used[index].AmountKopecks
		result.AvailableKopecks += remaining
		result.Items = append(result.Items, QuoteLine{
			LineIndex: index, ItemID: item.ID, Kind: item.Kind, Title: item.Title,
			Quantity: item.Quantity, UnitPriceKopecks: item.UnitPriceKopecks,
			AmountKopecks: item.AmountKopecks, RemainingQuantityMilli: quantity - used[index].QuantityMilli,
			RemainingAmountKopecks: remaining,
		})
	}
	result.Reason = s.ineligible(sale, refunds)
	if result.Reason == "" && result.AvailableKopecks <= 0 {
		result.Reason = "payment_fully_refunded"
	}
	result.Eligible = result.Reason == ""
	return result, nil
}

func (s *Service) Create(ctx context.Context, paymentID string, operatorID int64, key string, request CreateRequest) (Refund, error) {
	if !keyPattern.MatchString(key) {
		return Refund{}, webapp.BadRequest("a valid Idempotency-Key is required", nil)
	}
	items, err := normalizeSelections(request.Items)
	if err != nil {
		return Refund{}, webapp.BadRequest(err.Error(), nil)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return Refund{}, err
	}
	digest := sha256.Sum256(encoded)
	return s.repo.Reserve(ctx, paymentID, operatorID, key, hex.EncodeToString(digest[:]), func(sale Sale, previous []Refund) (Refund, error) {
		if reason := s.ineligible(sale, previous); reason != "" {
			return Refund{}, webapp.Conflict("payment is unavailable for refund", map[string]any{"code": reason})
		}
		lines, total, err := buildLines(sale.Items, sale.Bank.AmountKopecks, items, consumed(previous))
		if err != nil {
			return Refund{}, webapp.BadRequest(err.Error(), nil)
		}
		payload, err := PrepareReturnReceipt(sale.FiscalProfile, sale.Bank.Profile.Environment,
			sale.FiscalState, sale.SaleReceipt, lines, total, sale.Bank.AmountKopecks, s.allowLive)
		if err != nil {
			return Refund{}, webapp.Conflict("return receipt could not be prepared", map[string]any{"code": "refund_receipt_invalid"})
		}
		id, err := uuid.NewRandom()
		if err != nil {
			return Refund{}, err
		}
		return Refund{ID: id.String(), Lines: lines, AmountKopecks: total, ReceiptPayload: payload}, nil
	})
}

func (s *Service) Get(ctx context.Context, paymentID, refundID string) (Refund, error) {
	return s.repo.Get(ctx, paymentID, refundID)
}

func (s *Service) ineligible(sale Sale, previous []Refund) string {
	if sale.Bank.Method == payment.MethodTerminalCard {
		return "terminal_refunds_unsupported"
	}
	if sale.PaymentState != "paid" || sale.OrderState != "paid" {
		return "payment_not_paid"
	}
	for _, result := range previous {
		if result.BankState != "rejected" &&
			(result.BankState != "confirmed" || (result.FiscalState != "issued" && result.FiscalState != "demo")) {
			return "refund_already_open"
		}
		if result.BankState == "confirmed" && sale.Bank.Method == "card" && sale.Bank.Profile.Environment != "mock" {
			return "card_refund_already_completed"
		}
	}
	if sale.Bank.Profile.Environment == "production" {
		if !s.allowLive {
			return "live_refunds_disabled"
		}
		if sale.FiscalState != "issued" || sale.FiscalProfile.Mode != "live" {
			return "original_receipt_not_issued"
		}
	} else if sale.FiscalState != "demo" {
		return "original_demo_receipt_missing"
	}
	if len(sale.SaleReceipt) == 0 {
		return "original_receipt_payload_missing"
	}
	if s.bank.Validate(sale.Bank) != nil {
		return "refund_bank_identity_unavailable"
	}
	return ""
}

func consumed(previous []Refund) map[int]ConsumedLine {
	result := make(map[int]ConsumedLine)
	for _, refund := range previous {
		if refund.BankState == "rejected" {
			continue
		}
		for _, line := range refund.Lines {
			total := result[line.LineIndex]
			total.QuantityMilli += line.QuantityMilli
			total.AmountKopecks += line.AmountKopecks
			result[line.LineIndex] = total
		}
	}
	return result
}

// Run owns the refund queue independently of any administrator request.
func (s *Service) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		worked, err := s.processOne(ctx)
		if err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "refund worker could not persist progress", "error", err)
		}
		if worked && err == nil {
			continue
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

func (s *Service) processOne(ctx context.Context) (bool, error) {
	job, err := s.repo.ClaimNext(ctx)
	if err != nil || job == nil {
		return false, err
	}
	result := s.process(ctx, *job)
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return true, s.repo.Finish(save, *job, result)
}

const refundCheckDelay = 30 * time.Second

func (s *Service) process(ctx context.Context, job Job) Outcome {
	bank := job.Sale.Bank
	bank.RefundID = job.Refund.ID
	total := job.Refund.AmountKopecks
	blocked := bank.Profile.Environment == "production" && !s.allowLive
	switch job.Step {
	case "bank_prepare":
		if blocked {
			return Outcome{State: "queued", ErrorCode: "live_refunds_disabled", RetryAfter: refundCheckDelay}
		}
		baseline, err := s.bank.Prepare(ctx, bank, total)
		if err != nil {
			return Outcome{State: "rejected", ErrorCode: "bank_refund_preflight_failed"}
		}
		return Outcome{State: "prepared", Baseline: baseline}
	case "bank_submit":
		if blocked {
			return Outcome{State: "prepared", ErrorCode: "live_refunds_disabled", RetryAfter: refundCheckDelay}
		}
		observation, err := s.bank.Submit(ctx, bank, total, job.Baseline)
		return bankOutcome(bank.Method, observation, err)
	case "bank_check":
		if bank.Profile.Environment != "mock" && bank.Method == "card" && job.Refund.BankState != "pending" {
			// Without a recorded acknowledgement a matching portal refund can be
			// unrelated to our request, even when its amount is identical.
			return Outcome{State: "unknown", ErrorCode: "bank_refund_acknowledgement_missing"}
		}
		observation, err := s.bank.Check(ctx, bank, total, job.Baseline, job.Refund.BankReference)
		if bank.Method == "card" && err != nil {
			return Outcome{State: "pending", ErrorCode: "bank_refund_status_unavailable", RetryAfter: refundCheckDelay}
		}
		if observation.ExternalID == "" {
			observation.ExternalID = job.Refund.BankReference
		}
		return bankOutcome(bank.Method, observation, err)
	case "fiscal_submit", "fiscal_check":
		recovering := job.Step == "fiscal_check"
		if blocked && !recovering {
			return Outcome{State: "queued", ErrorCode: "live_refunds_disabled", RetryAfter: refundCheckDelay}
		}
		result := ProcessReturnReceipt(ctx, s.driver, job.Sale.FiscalProfile, bank.Profile.Environment,
			job.Refund.ID, job.Refund.ReceiptPayload, s.allowLive, recovering)
		outcome := Outcome{State: result.State, ErrorCode: result.ErrorCode}
		if result.State == "unknown" || result.State == "queued" {
			outcome.RetryAfter = refundCheckDelay
		}
		if result.State == "issued" {
			var response struct {
				Data struct {
					Receipt struct {
						DocumentNumber int64 `json:"documentNumber"`
					} `json:"receipt"`
				} `json:"data"`
			}
			if json.Unmarshal(result.Response, &response) == nil {
				outcome.ReceiptNumber = response.Data.Receipt.DocumentNumber
			}
		}
		return outcome
	default:
		return Outcome{State: "unknown", ErrorCode: "refund_step_invalid"}
	}
}

func bankOutcome(method string, observation BankObservation, err error) Outcome {
	result := Outcome{State: observation.State, Reference: observation.ExternalID, ErrorCode: observation.Code}
	if result.State == "" || (err != nil && result.State != "rejected") {
		result.State = "unknown"
		if result.ErrorCode == "" {
			result.ErrorCode = "bank_refund_outcome_unknown"
		}
	}
	if (result.State == "pending" && (method == "card" || result.Reference != "")) ||
		(result.State == "unknown" && method == "sbp" && result.Reference != "") {
		result.RetryAfter = refundCheckDelay
	}
	return result
}
