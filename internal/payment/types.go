// Package payment creates durable bank payment attempts for authoritative checkouts.
package payment

import (
	"context"
	"encoding/json"
	"time"

	"github.com/dronm/pronogti/internal/checkout"
)

const (
	MethodSBP  = "sbp"
	MethodCard = "card"
)

type Profile struct {
	ID                  int64  `json:"id"`
	CompanyID           int64  `json:"company_id"`
	ConfigVersion       int64  `json:"config_version"`
	Name                string `json:"name"`
	Environment         string `json:"environment"`
	APIBaseURL          string `json:"api_base_url"`
	JWTEnv              string `json:"-"`
	JWTCiphertext       []byte `json:"-"`
	ClientID            string `json:"client_id"`
	CustomerCode        string `json:"customer_code"`
	SBPMerchantID       string `json:"sbp_merchant_id"`
	AccountID           string `json:"account_id"`
	QRTTLMinutes        int    `json:"qr_ttl_minutes"`
	HTTPTimeoutSeconds  int    `json:"http_timeout_seconds"`
	Enabled             bool   `json:"enabled"`
	CardsEnabled        bool   `json:"cards_enabled"`
	AcquiringMerchantID string `json:"acquiring_merchant_id"`
	CardTTLMinutes      int    `json:"card_ttl_minutes"`
	CardRedirectURL     string `json:"card_redirect_url"`
	CardFailRedirectURL string `json:"card_fail_redirect_url"`
}

type Attempt struct {
	ID, OrderID                                                                     string
	CompanyID, PaymentProfileID                                                     int64
	Environment, State, IdempotencyKey, RequestHash, QRCID, Payload, ImageMediaType string
	Image                                                                           []byte
	ProfileSnapshot                                                                 json.RawMessage
	AmountKopecks                                                                   int64
	Currency                                                                        string
	ExpiresAt                                                                       *time.Time
	CreatedAt, UpdatedAt                                                            time.Time
	ErrorCode                                                                       string
	Method, OperationID                                                             string
}

type Reservation struct {
	Attempt Attempt
	Profile Profile
	Created bool
}

type QRResult struct {
	QRCID, Payload string
	Image          []byte
	ImageMediaType string
	ExpiresAt      time.Time
}

type BankRequest struct {
	AttemptID     string
	OrderID       string
	Profile       Profile
	Token         string
	AmountKopecks int64
	Purpose       string
	TTLMinutes    int
	StartedAt     time.Time
}

type Bank interface {
	Create(context.Context, BankRequest) (QRResult, error)
}

type CardResult struct {
	OperationID, CheckoutURL, Status string
	ExpiresAt                        time.Time
}

type CardBank interface {
	CreateCard(context.Context, BankRequest) (CardResult, error)
}

// CardRepository extends the QR repository without changing existing adapters.
type CardRepository interface {
	ReserveCard(context.Context, checkout.Owner, string, string, string, time.Time) (Reservation, error)
	CompleteCard(context.Context, string, CardResult) error
}

type Repository interface {
	GetOrder(context.Context, string) (checkout.Order, error)
	Reserve(context.Context, checkout.Owner, string, string, string, time.Time) (Reservation, error)
	Get(context.Context, checkout.Owner, string) (Attempt, error)
	Complete(context.Context, string, QRResult) error
	Fail(context.Context, string, bool, string) error
	UpdateExpired(context.Context, string, time.Time) (Attempt, error)
	AbandonStale(context.Context, string, time.Time) (Attempt, error)
}

type TokenSource interface {
	Token(context.Context, Profile) (string, error)
}

type Dependencies struct {
	Repository      Repository
	Bank            Bank
	CardBank        CardBank
	Tokens          TokenSource
	CreatingTimeout time.Duration
	MockExternal    bool
	ValidatePayable func(context.Context, checkout.Order) error
}

type Options struct {
	ValidateActor func(context.Context, checkout.Actor) error
	ValidateOrder func(context.Context, checkout.Order) error
}

// View intentionally omits provider/credential identifiers and internal errors.
type View struct {
	PaymentID          string     `json:"payment_id"`
	CheckoutID         string     `json:"checkout_id"`
	State              string     `json:"state"`
	AmountKopecks      int64      `json:"amount_kopecks"`
	Currency           string     `json:"currency"`
	Environment        string     `json:"environment"`
	ExpiresAt          *time.Time `json:"expires_at"`
	Payload            string     `json:"payload,omitempty"`
	QRImageURL         string     `json:"qr_image_url,omitempty"`
	ErrorCode          string     `json:"error_code,omitempty"`
	OrderState         string     `json:"order_state"`
	YClientsSyncStatus string     `json:"yclients_sync_status"`
	FiscalStatus       string     `json:"fiscal_status"`
	PaidAt             *time.Time `json:"paid_at,omitempty"`
	BankReference      string     `json:"bank_reference,omitempty"`
	Method             string     `json:"method"`
	CheckoutURL        string     `json:"checkout_url,omitempty"`
}

type Completion struct {
	OrderState, YClientsSyncStatus, FiscalStatus string
	PaidAt                                       *time.Time
	BankReference                                string
}
