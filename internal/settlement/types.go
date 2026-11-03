// Package settlement synchronizes confirmed purchases with YCLIENTS without
// making shopping-cart edits into appointments.
package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dronm/pronogti/internal/checkout"
)

var ErrNoJob = errors.New("no YCLIENTS settlement job available")

type VisitReference struct{ VisitID, RecordID int64 }

type Profile struct {
	ID                     int64  `json:"id"`
	CompanyID              int64  `json:"company_id"`
	Name                   string `json:"name"`
	Mode                   string `json:"mode"`
	Enabled                bool   `json:"enabled"`
	CashlessAccountID      int64  `json:"cashless_account_id"`
	StorageID              int64  `json:"storage_id"`
	ServicePolicy          string `json:"service_policy"`
	ServiceIncomeExpenseID int64  `json:"service_income_expense_id"`
	ConfigVersion          int64  `json:"config_version"`
}

func (p Profile) Validate() error {
	if !p.Enabled || p.CompanyID <= 0 || p.Mode != "demo" && p.Mode != "live" {
		return errors.New("YCLIENTS settlement profile is unavailable")
	}
	if p.ServicePolicy != "require_visit" && p.ServicePolicy != "financial_income" {
		return errors.New("YCLIENTS service purchase policy is invalid")
	}
	if p.Mode == "live" && p.CashlessAccountID <= 0 {
		return errors.New("YCLIENTS cashless account must be configured")
	}
	return nil
}

// ValidateCheckout is safe to call before charging. It checks the frozen policy
// without creating appointments, goods movements or financial transactions.
func ValidateCheckout(order checkout.Order, profile Profile) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	if profile.CompanyID != order.Owner.CompanyID || order.Currency != "RUB" || order.PayableKopecks <= 0 || len(order.Items) == 0 {
		return errors.New("checkout does not match its YCLIENTS settlement profile")
	}
	var total int64
	for _, item := range order.Items {
		if item.AmountKopecks <= 0 || item.AmountKopecks > checkout.MaxMoney-total {
			return errors.New("invalid YCLIENTS settlement amount")
		}
		total += item.AmountKopecks
		if item.Source == checkout.SourceVisit {
			if order.VisitID <= 0 || order.RecordID <= 0 {
				return errors.New("visit settlement requires complete visit references")
			}
			continue
		}
		if item.Source != checkout.SourceCatalog {
			return errors.New("unsupported settlement item source")
		}
		switch item.Kind {
		case checkout.KindGood:
			if profile.Mode == "live" && profile.StorageID <= 0 {
				return errors.New("YCLIENTS goods-sale warehouse must be configured")
			}
		case checkout.KindService:
			if profile.ServicePolicy != "financial_income" {
				return errors.New("new service purchases require the financial_income policy; no appointment is fabricated")
			}
			if profile.Mode == "live" && profile.ServiceIncomeExpenseID <= 0 {
				return errors.New("YCLIENTS service-income article must be configured")
			}
		default:
			return errors.New("unsupported settlement item kind")
		}
	}
	if total != order.PayableKopecks || order.BonusKopecks != 0 {
		return errors.New("YCLIENTS settlement total does not match the paid checkout")
	}
	return nil
}

type Job struct {
	Order           checkout.Order
	PaymentID       string
	Environment     string
	CustomerContact string
	Config          Profile
	PaidAt          time.Time
	ClaimedAt       time.Time
	Operations      []Operation
}

type Operation struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	State        string          `json:"state"`
	Request      json.RawMessage `json:"request"`
	Result       json.RawMessage `json:"result,omitempty"`
	DispatchedAt *time.Time      `json:"dispatched_at,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
}

type Outcome struct {
	State     string
	Result    json.RawMessage
	ErrorCode string
}

type Repository interface {
	Claim(context.Context) (Job, error)
	SavePlan(context.Context, Job, []Operation) error
	BeginDispatch(context.Context, Job, string) error
	SaveOutcome(context.Context, Job, Operation, Outcome) error
	Finish(context.Context, Job, string, string) error
}

type Provider interface {
	Plan(context.Context, Job) ([]Operation, error)
	Execute(context.Context, Job, Operation) (Outcome, error)
	Reconcile(context.Context, Job, Operation) (Outcome, error)
}

type Options struct {
	AllowLive    bool
	PollInterval time.Duration
}
