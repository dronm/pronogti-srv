package payment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net/url"
	"regexp"
	"time"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/webapp"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type Service struct {
	dependencies Dependencies
	options      Options
	now          func() time.Time
}

func NewService(dependencies Dependencies, options Options) *Service {
	if dependencies.CreatingTimeout <= 0 {
		dependencies.CreatingTimeout = 120 * time.Second
	}
	return &Service{dependencies: dependencies, options: options, now: time.Now}
}

func (s *Service) Init(ctx context.Context, actor checkout.Actor, key, orderID string) (View, error) {
	return s.initMethod(ctx, actor, key, orderID, MethodSBP)
}

func (s *Service) InitCard(ctx context.Context, actor checkout.Actor, key, orderID string) (View, error) {
	return s.initMethod(ctx, actor, key, orderID, MethodCard)
}

func (s *Service) initMethod(ctx context.Context, actor checkout.Actor, key, orderID, method string) (View, error) {
	if err := s.ready(ctx, actor); err != nil {
		return View{}, err
	}
	cardRepository, cardSupported := s.dependencies.Repository.(CardRepository)
	if method == MethodCard && (s.dependencies.CardBank == nil || !cardSupported) || method == MethodSBP && s.dependencies.Bank == nil {
		return View{}, fmt.Errorf("%s payments are not configured", method)
	}
	if !uuidPattern.MatchString(orderID) {
		return View{}, webapp.BadRequest("checkout id must be a valid UUID", nil)
	}
	if !validKey(key) {
		return View{}, webapp.BadRequest("Idempotency-Key must contain 1 to 128 visible ASCII characters", nil)
	}
	order, err := s.dependencies.Repository.GetOrder(ctx, orderID)
	if errors.Is(err, checkout.ErrNotFound) {
		return View{}, webapp.NotFound("checkout not found", nil)
	}
	if err != nil {
		return View{}, err
	}
	owner := ownerFor(actor)
	if !owns(owner, order.Owner) {
		return View{}, webapp.NotFound("checkout not found", nil)
	}
	if s.options.ValidateOrder != nil {
		if err := s.options.ValidateOrder(ctx, order); err != nil {
			return View{}, err
		}
	}
	if err := s.validateActor(ctx, actor); err != nil {
		return View{}, err
	}
	prefix := "sbp-qr:"
	if method == MethodCard {
		prefix = "card:"
	}
	digest := sha256.Sum256([]byte(prefix + order.ID))
	var reservation Reservation
	if method == MethodCard {
		reservation, err = cardRepository.ReserveCard(ctx, owner, orderID, key, hex.EncodeToString(digest[:]), s.now().UTC())
	} else {
		reservation, err = s.dependencies.Repository.Reserve(ctx, owner, orderID, key, hex.EncodeToString(digest[:]), s.now().UTC())
	}
	if err != nil {
		return View{}, err
	}
	if methodFor(reservation.Attempt) != method {
		return View{}, paymentConflict("checkout already has a payment using another method", "payment_method_conflict")
	}
	if !reservation.Created {
		return s.Get(ctx, actor, reservation.Attempt.ID)
	}
	attempt := reservation.Attempt
	// All failures before Bank.Create are known not to have contacted the bank.
	failBeforeBank := func(code string, cause error) (View, error) {
		saveCtx, cancel := persistenceContext(ctx)
		defer cancel()
		if err := s.dependencies.Repository.Fail(saveCtx, attempt.ID, false, code); err != nil {
			return View{}, err
		}
		return View{}, cause
	}
	if s.dependencies.ValidatePayable != nil {
		if err := s.dependencies.ValidatePayable(ctx, order); err != nil {
			return failBeforeBank("checkout_revalidation_failed", err)
		}
	}
	token := ""
	if reservation.Profile.Environment != "mock" || s.dependencies.MockExternal {
		if s.dependencies.Tokens == nil {
			return failBeforeBank("payment_credentials_unavailable", webapp.Conflict("payment credentials are not configured", nil))
		}
		token, err = s.dependencies.Tokens.Token(ctx, reservation.Profile)
		if err != nil {
			return failBeforeBank("payment_credentials_unavailable", webapp.Conflict("payment credentials are not configured", map[string]any{"code": "payment_credentials_unavailable"}))
		}
	}
	if err := s.validateActor(ctx, actor); err != nil {
		return failBeforeBank("customer_session_changed", err)
	}
	now := s.now().UTC()
	if attempt.ExpiresAt == nil {
		return failBeforeBank("invalid_payment_expiry", fmt.Errorf("payment reservation has no expiry"))
	}
	remaining := int(attempt.ExpiresAt.Sub(now) / time.Minute)
	ttl := reservation.Profile.QRTTLMinutes
	if method == MethodCard {
		ttl = reservation.Profile.CardTTLMinutes
	}
	if remaining > ttl {
		remaining = ttl
	}
	if remaining < 1 {
		return failBeforeBank("checkout_expired", webapp.Conflict("checkout has too little time remaining; prepare it again", map[string]any{"code": "checkout_expired"}))
	}
	// Complete an authorized, journaled operation even when its HTTP caller
	// disconnects. The profile timeout bounds this detached bank request.
	timeout := time.Duration(reservation.Profile.HTTPTimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > 300*time.Second {
		return failBeforeBank("invalid_payment_timeout", fmt.Errorf("invalid payment profile timeout"))
	}
	bankCtx, cancelBank := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	request := BankRequest{
		AttemptID: attempt.ID, OrderID: order.ID, Profile: reservation.Profile, Token: token,
		AmountKopecks: attempt.AmountKopecks, Purpose: "Checkout " + order.ID,
		TTLMinutes: remaining, StartedAt: now,
	}
	var qrResult QRResult
	var cardResult CardResult
	var bankErr error
	if method == MethodCard {
		cardResult, bankErr = s.dependencies.CardBank.CreateCard(bankCtx, request)
	} else {
		qrResult, bankErr = s.dependencies.Bank.Create(bankCtx, request)
	}
	cancelBank()
	saveCtx, cancelSave := persistenceContext(ctx)
	defer cancelSave()
	if bankErr != nil {
		unknown, code := true, "bank_outcome_unknown"
		var failure *BankError
		if errors.As(bankErr, &failure) {
			unknown, code = failure.Unknown, failure.Code
		}
		if err := s.dependencies.Repository.Fail(saveCtx, attempt.ID, unknown, code); err != nil {
			return View{}, err
		}
	} else {
		valid := validResult(qrResult, now, attempt.ExpiresAt)
		if method == MethodCard {
			valid = validCardResult(cardResult, now, attempt.ExpiresAt)
		}
		if !valid {
			if err := s.dependencies.Repository.Fail(saveCtx, attempt.ID, true, "invalid_bank_response"); err != nil {
				return View{}, err
			}
		} else if method == MethodCard {
			if err := cardRepository.CompleteCard(saveCtx, attempt.ID, cardResult); err != nil {
				return View{}, err
			}
		} else if err := s.dependencies.Repository.Complete(saveCtx, attempt.ID, qrResult); err != nil {
			return View{}, err
		}
	}
	// Reload through an owner-authorized read; never return to a changed session.
	return s.Get(ctx, actor, attempt.ID)
}

func (s *Service) Get(ctx context.Context, actor checkout.Actor, id string) (View, error) {
	if err := s.ready(ctx, actor); err != nil {
		return View{}, err
	}
	if !uuidPattern.MatchString(id) {
		return View{}, webapp.BadRequest("payment id must be a valid UUID", nil)
	}
	attempt, err := s.dependencies.Repository.Get(ctx, ownerFor(actor), id)
	if err != nil {
		return View{}, err
	}
	now := s.now().UTC()
	if attempt.State == "creating" {
		wait := s.dependencies.CreatingTimeout
		var profile Profile
		if json.Unmarshal(attempt.ProfileSnapshot, &profile) == nil {
			profileWait := time.Duration(profile.HTTPTimeoutSeconds+30) * time.Second
			if wait < profileWait {
				wait = profileWait
			}
		}
		if !attempt.CreatedAt.Add(wait).After(now) {
			attempt, err = s.dependencies.Repository.AbandonStale(ctx, attempt.ID, now)
			if err != nil {
				return View{}, err
			}
		}
	}
	if attempt.State == "pending" && attempt.ExpiresAt != nil && !attempt.ExpiresAt.After(now) {
		attempt, err = s.dependencies.Repository.UpdateExpired(ctx, attempt.ID, now)
		if err != nil {
			return View{}, err
		}
	}
	if err := s.validateActor(ctx, actor); err != nil {
		return View{}, err
	}
	result := viewFor(attempt, now)
	if reader, ok := s.dependencies.Repository.(interface {
		GetCompletion(context.Context, checkout.Owner, string) (Completion, error)
	}); ok {
		completion, err := reader.GetCompletion(ctx, ownerFor(actor), id)
		if err != nil {
			return View{}, err
		}
		result.OrderState, result.YClientsSyncStatus, result.FiscalStatus = completion.OrderState, completion.YClientsSyncStatus, completion.FiscalStatus
		result.PaidAt, result.BankReference = completion.PaidAt, completion.BankReference
		if result.State == "paid" {
			result.Receipt = completion.Receipt
			if result.Environment == "production" && result.OrderState == "paid" && result.FiscalStatus == "issued" {
				result.receiptEmailURL = s.sabyReceiptURL(completion)
				if result.receiptEmailURL != "" {
					receipt := *result.Receipt
					receipt.EmailAvailable = true
					result.Receipt = &receipt
				}
			}
		}
	}
	if err := s.validateActor(ctx, actor); err != nil {
		return View{}, err
	}
	return result, nil
}

func (s *Service) ready(ctx context.Context, actor checkout.Actor) error {
	if ctx == nil {
		return fmt.Errorf("payment context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.dependencies.Repository == nil || s.dependencies.Bank == nil && s.dependencies.CardBank == nil {
		return fmt.Errorf("payments are not configured")
	}
	if actor.CompanyID <= 0 || actor.ScopeID == "" || actor.CustomerSessionID == "" ||
		actor.Mode != "public" && actor.Mode != "kiosk" || actor.Guest && actor.ClientID != 0 || !actor.Guest && actor.ClientID <= 0 ||
		actor.Mode == "kiosk" && (actor.KioskID == nil || *actor.KioskID <= 0) || actor.Mode == "public" && actor.KioskID != nil {
		return webapp.Unauthorized("a current customer session is required", nil)
	}
	return s.validateActor(ctx, actor)
}

func (s *Service) validateActor(ctx context.Context, actor checkout.Actor) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.options.ValidateActor != nil {
		return s.options.ValidateActor(ctx, actor)
	}
	return nil
}

func persistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func validResult(result QRResult, now time.Time, limit *time.Time) bool {
	u, err := url.Parse(result.Payload)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || len(result.Payload) > 2083 || len(result.Image) > 1024*1024 {
		return false
	}
	size, err := png.DecodeConfig(bytes.NewReader(result.Image))
	return err == nil && size.Width > 0 && size.Width <= 2048 && size.Height > 0 && size.Height <= 2048 && result.QRCID != "" && result.ImageMediaType == "image/png" &&
		result.ExpiresAt.After(now) && limit != nil && !result.ExpiresAt.After(*limit)
}

func viewFor(attempt Attempt, now time.Time) View {
	result := View{PaymentID: attempt.ID, CheckoutID: attempt.OrderID, State: attempt.State,
		AmountKopecks: attempt.AmountKopecks, Currency: attempt.Currency, Environment: attempt.Environment,
		ExpiresAt: attempt.ExpiresAt, ErrorCode: attempt.ErrorCode, Method: methodFor(attempt), OrderState: "payment_pending", YClientsSyncStatus: "not_started", FiscalStatus: "not_started"}
	if attempt.State == "paid" {
		result.OrderState = "paid"
	}
	if attempt.State == "failed" {
		result.OrderState = "prepared"
	}
	if attempt.State == "pending" && attempt.ExpiresAt != nil && attempt.ExpiresAt.After(now) {
		if result.Method == MethodCard {
			result.CheckoutURL = attempt.Payload
			return result
		}
		result.Payload = attempt.Payload
		if len(attempt.Image) > 0 && attempt.ImageMediaType == "image/png" {
			result.QRImageURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(attempt.Image)
		}
	}
	return result
}

func validKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}
func ownerFor(actor checkout.Actor) checkout.Owner {
	scope := sha256.Sum256([]byte(actor.ScopeID))
	customer := sha256.Sum256([]byte(actor.CustomerSessionID))
	return checkout.Owner{CompanyID: actor.CompanyID, KioskID: actor.KioskID, Mode: actor.Mode, ClientID: actor.ClientID, ScopeHash: hex.EncodeToString(scope[:]), CustomerSessionHash: hex.EncodeToString(customer[:])}
}
func owns(a, b checkout.Owner) bool {
	return a.CompanyID == b.CompanyID && a.Mode == b.Mode && a.ClientID == b.ClientID && a.ScopeHash == b.ScopeHash && a.CustomerSessionHash == b.CustomerSessionHash &&
		(a.KioskID == nil && b.KioskID == nil || a.KioskID != nil && b.KioskID != nil && *a.KioskID == *b.KioskID)
}

func methodFor(attempt Attempt) string {
	if attempt.Method == "" {
		return MethodSBP
	}
	return attempt.Method
}
