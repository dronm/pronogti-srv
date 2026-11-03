package httpapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/pronogti/internal/payment"
)

type paymentGetInput struct{ Nonce, ID string }

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
	return payments.Get(ctx, actor, input.ID)
}
