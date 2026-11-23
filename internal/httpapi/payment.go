package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/pronogti/internal/payment"
	"github.com/dronm/pronogti/internal/terminalbridge"
	"github.com/dronm/webapp"
)

type paymentGetInput struct{ Nonce, ID string }

type TerminalBridgePayments interface {
	Availability(context.Context, checkout.Owner, string) (terminalbridge.Availability, error)
	ExistingAttempt(context.Context, checkout.Owner, string, string) (string, error)
	Initiate(context.Context, checkout.Owner, string, string) (string, error)
	PaymentDetails(context.Context, string) (terminalbridge.TerminalDetails, error)
	ExpirePayment(context.Context, string) (bool, error)
}

func paymentGetBinder(r *http.Request) (any, error) {
	return paymentGetInput{Nonce: r.Header.Get(kiosk.CustomerHeader), ID: r.PathValue("id")}, nil
}

func (s *service) paymentService() (*payment.Service, error) {
	if s.checkout == nil || s.checkout.Payment == nil {
		return nil, fmt.Errorf("payment is not configured")
	}
	return payment.NewService(*s.checkout.Payment, payment.Options{
		ValidateOrder: s.checkout.ValidateOrder,
		ValidateActor: func(ctx context.Context, original checkout.Actor) error {
			current, err := s.checkoutActor(ctx, original.CustomerSessionID)
			if err != nil {
				return err
			}
			if current.CompanyID != original.CompanyID || current.ScopeID != original.ScopeID || current.Mode != original.Mode || current.ClientID != original.ClientID || current.Guest != original.Guest || !sameKioskID(current.KioskID, original.KioskID) {
				return kiosk.CustomerRequired()
			}
			return nil
		},
	}), nil
}

func (s *service) InitQRPayment(ctx context.Context, input kioskQRRequest) (payment.View, error) {
	actor, err := s.checkoutActor(ctx, input.Nonce)
	if err != nil {
		return payment.View{}, err
	}
	payments, err := s.paymentService()
	if err != nil {
		return payment.View{}, err
	}
	return payments.Init(ctx, actor, input.IdempotencyKey, input.CheckoutID)
}

func (s *service) InitCardPayment(ctx context.Context, input kioskQRRequest) (payment.View, error) {
	actor, err := s.checkoutActor(ctx, input.Nonce)
	if err != nil {
		return payment.View{}, err
	}
	payments, err := s.paymentService()
	if err != nil {
		return payment.View{}, err
	}
	return payments.InitCard(ctx, actor, input.IdempotencyKey, input.CheckoutID)
}

func (s *service) GetPayment(ctx context.Context, input paymentGetInput) (payment.View, error) {
	actor, err := s.checkoutActor(ctx, input.Nonce)
	if err != nil {
		return payment.View{}, err
	}
	payments, err := s.paymentService()
	if err != nil {
		return payment.View{}, err
	}
	view, err := payments.Get(ctx, actor, input.ID)
	if err != nil {
		return payment.View{}, err
	}
	if view.Method == terminalbridge.Method && s.checkout.TerminalBridge != nil {
		changed, err := s.checkout.TerminalBridge.ExpirePayment(ctx, input.ID)
		if err != nil {
			return payment.View{}, err
		}
		if changed {
			view, err = payments.Get(ctx, actor, input.ID)
			if err != nil {
				return payment.View{}, err
			}
		}
		details, err := s.checkout.TerminalBridge.PaymentDetails(ctx, input.ID)
		if err != nil {
			return payment.View{}, err
		}
		view.TerminalID, view.CommandID, view.CommandState = details.TerminalID, details.CommandID, details.State
		view.TerminalResolutionPending = details.ResolutionPending
	}
	return view, nil
}

// InitTerminalPayment shares checkout ownership and downstream preflight with
// online payments, while reserving a durable device command instead of a URL.
func (s *service) InitTerminalPayment(ctx context.Context, input kioskQRRequest) (payment.View, error) {
	if !s.kioskOnly {
		return payment.View{}, webapp.Forbidden("terminal payments require a bound kiosk", nil)
	}
	actor, err := s.checkoutActor(ctx, input.Nonce)
	if err != nil {
		return payment.View{}, err
	}
	if s.checkout == nil || s.checkout.TerminalBridge == nil {
		// An unavailable store cannot prove that an earlier request did not
		// reserve a payment. Keep this ambiguous rather than returning the
		// definite disabled/new-operation response used below.
		return payment.View{}, webapp.Internal("terminal payment recovery is unavailable", nil)
	}
	owner := terminalOwner(actor)
	id, err := s.checkout.TerminalBridge.ExistingAttempt(ctx, owner, input.CheckoutID, input.IdempotencyKey)
	if err != nil {
		return payment.View{}, err
	}
	if id == "" {
		if !s.checkout.TerminalEnabled {
			return payment.View{}, webapp.Conflict("terminal payments are disabled", map[string]any{"code": "terminal_payments_disabled"})
		}
		preparer, err := s.checkoutService()
		if err != nil {
			return payment.View{}, err
		}
		order, err := preparer.Get(ctx, actor, input.CheckoutID)
		if err != nil {
			return payment.View{}, err
		}
		if s.checkout.Payment == nil || s.checkout.Payment.ValidatePayable == nil {
			return payment.View{}, webapp.Internal("terminal checkout preflight is not configured", nil)
		}
		if err = s.checkout.Payment.ValidatePayable(ctx, order); err != nil {
			return payment.View{}, err
		}
		current, err := s.checkoutActor(ctx, input.Nonce)
		if err != nil {
			return payment.View{}, err
		}
		if !sameTerminalActor(actor, current) {
			return payment.View{}, kiosk.CustomerRequired()
		}
		id, err = s.checkout.TerminalBridge.Initiate(ctx, owner, input.CheckoutID, input.IdempotencyKey)
		if err != nil {
			return payment.View{}, err
		}
	}
	return s.GetPayment(ctx, paymentGetInput{Nonce: input.Nonce, ID: id})
}

func terminalOwner(actor checkout.Actor) checkout.Owner {
	scope := sha256.Sum256([]byte(actor.ScopeID))
	customer := sha256.Sum256([]byte(actor.CustomerSessionID))
	return checkout.Owner{CompanyID: actor.CompanyID, KioskID: actor.KioskID, Mode: actor.Mode, ClientID: actor.ClientID, ScopeHash: hex.EncodeToString(scope[:]), CustomerSessionHash: hex.EncodeToString(customer[:])}
}

func sameTerminalActor(a, b checkout.Actor) bool {
	return a.CompanyID == b.CompanyID && a.Mode == b.Mode && a.ScopeID == b.ScopeID && a.CustomerSessionID == b.CustomerSessionID && a.ClientID == b.ClientID && a.Guest == b.Guest && sameKioskID(a.KioskID, b.KioskID)
}
