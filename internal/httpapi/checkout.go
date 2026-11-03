package httpapi

import (
	"context"
	"fmt"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/webapp"
)

func (s *service) PrepareCheckout(ctx context.Context, input checkoutPrepareInput) (checkout.Order, error) {
	actor, err := s.checkoutActor(ctx, input.Nonce)
	if err != nil {
		return checkout.Order{}, err
	}
	preparer, err := s.checkoutService()
	if err != nil {
		return checkout.Order{}, err
	}
	return preparer.Prepare(ctx, actor, input.IdempotencyKey, input.Request)
}

func (s *service) GetCheckout(ctx context.Context, input checkoutGetInput) (checkout.Order, error) {
	actor, err := s.checkoutActor(ctx, input.Nonce)
	if err != nil {
		return checkout.Order{}, err
	}
	preparer, err := s.checkoutService()
	if err != nil {
		return checkout.Order{}, err
	}
	return preparer.Get(ctx, actor, input.ID)
}

func (s *service) checkoutActor(ctx context.Context, nonce string) (checkout.Actor, error) {
	scope, customer, err := s.requireCustomer(ctx, nonce)
	if err != nil {
		return checkout.Actor{}, err
	}
	if !customer.Guest && (!customer.Registered || customer.ClientID <= 0) {
		return checkout.Actor{}, webapp.Conflict("customer registration is required", nil)
	}
	actor := checkout.Actor{
		CompanyID: scope.CompanyID, Mode: scope.Mode, ScopeID: scope.ScopeID,
		CustomerSessionID: customer.SessionID, ClientID: customer.ClientID, Guest: customer.Guest, Contact: customer.Phone,
	}
	if scope.Kiosk != nil {
		id := scope.Kiosk.ID
		actor.KioskID = &id
	}
	return actor, nil
}

func (s *service) checkoutService() (*checkout.Service, error) {
	if s.checkout == nil || s.checkout.Provider == nil || s.checkout.Repository == nil {
		return nil, fmt.Errorf("checkout preparation is not configured")
	}
	return checkout.NewService(s.checkout.Provider, s.checkout.Repository, checkout.Options{
		MaxLifeTime:   s.checkout.MaxLifeTime,
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

func sameKioskID(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
