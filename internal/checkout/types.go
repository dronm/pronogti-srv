// Package checkout prepares durable, server-priced customer checkouts.
// It does not contact a bank or mutate YClients visits or loyalty balances.
package checkout

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("checkout not found")

const (
	SourceVisit   = "visit"
	SourceCatalog = "catalog"
	KindService   = "service"
	KindGood      = "good"
	StatePrepared = "prepared"
)

// Selection contains identifiers and quantity only. Prices and customer/company
// identity are deliberately absent from the client request contract.
type Selection struct {
	Source   string `json:"source"`
	Kind     string `json:"kind"`
	ID       int64  `json:"id"`
	LineID   int64  `json:"line_id,omitempty"`
	StaffID  int64  `json:"staff_id"`
	Quantity string `json:"quantity"`
}

type PrepareRequest struct {
	VisitID             int64       `json:"visit_id"`
	RecordID            int64       `json:"record_id"`
	Items               []Selection `json:"items"`
	BonusToApplyKopecks int64       `json:"bonus_to_apply_kopecks"`
}

// Actor is constructed exclusively from the current server-side session.
type Actor struct {
	CompanyID         int64
	KioskID           *int64
	Mode              string
	ScopeID           string
	CustomerSessionID string
	ClientID          int64
	Guest             bool
	// Customer details come from the identified server session.
	CustomerName string
	Contact      string
}

// Owner stores hashes of interaction nonces, not the browser's session cookie.
type Owner struct {
	CompanyID           int64
	KioskID             *int64
	Mode                string
	ClientID            int64
	ScopeHash           string
	CustomerSessionHash string
}

type Item struct {
	Selection
	Title            string `json:"title"`
	StaffName        string `json:"staff_name"`
	UnitPriceKopecks int64  `json:"unit_price_kopecks"`
	AmountKopecks    int64  `json:"amount_kopecks"`
}

// Order is an immutable pricing snapshot. Later payment work will create bank
// attempts referencing this order rather than accepting an arbitrary amount.
type Order struct {
	ID                 string    `json:"id"`
	Owner              Owner     `json:"-"`
	VisitID            int64     `json:"visit_id"`
	RecordID           int64     `json:"record_id"`
	Items              []Item    `json:"items"`
	SubtotalKopecks    int64     `json:"subtotal_kopecks"`
	BonusKopecks       int64     `json:"bonus_kopecks"`
	PayableKopecks     int64     `json:"payable_kopecks"`
	Currency           string    `json:"currency"`
	State              string    `json:"state"`
	IdempotencyKey     string    `json:"-"`
	RequestHash        string    `json:"-"`
	ExpiresAt          time.Time `json:"expires_at"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	YClientsSyncStatus string    `json:"yclients_sync_status"`
	CustomerName       string    `json:"-"`
	CustomerContact    string    `json:"-"`
}

// VisitLine retains the full outstanding amount. In particular, a partially
// paid goods line must not be reconstructed from a rounded unit price.
type VisitLine struct {
	Kind             string
	ID               int64
	LineID           int64
	StaffID          int64
	StaffName        string
	Title            string
	Quantity         string
	UnitPriceKopecks int64
	AmountKopecks    int64
}

type Visit struct {
	VisitID  int64
	RecordID int64
	Items    []VisitLine
}

// CatalogItem is supplied by a fresh company/staff-specific catalog lookup.
// The provider rejects unsupported or variable prices before returning it.
type CatalogItem struct {
	Selection
	Title            string
	StaffName        string
	UnitPriceKopecks int64
}

type Provider interface {
	LoadVisit(context.Context, int64, int64) (Visit, error)
	LoadCatalog(context.Context, int64, []Selection) ([]CatalogItem, error)
}

type Repository interface {
	FindByKey(context.Context, Owner, string) (Order, error)
	Get(context.Context, string) (Order, error)
	// Create returns the existing winner when the same owner/key is inserted
	// concurrently. The service checks its request hash before returning it.
	Create(context.Context, Order) (Order, error)
}

type Options struct {
	MaxLifeTime int64
	// ValidateOrder checks frozen downstream requirements after ownership checks.
	ValidateOrder func(context.Context, Order) error
	// ValidateActor rechecks the live scope/customer after outbound reads.
	ValidateActor func(context.Context, Actor) error
}
