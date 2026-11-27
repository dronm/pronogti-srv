package httpapi

import (
	"context"
	"net/http"

	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/pronogti/internal/terminalbridge"
	"github.com/dronm/webapp"
)

type paymentMethodsInput struct{ Nonce, CheckoutID string }

type paymentMethodsResponse struct {
	terminalbridge.Availability
	OnlineCardEnabled bool `json:"online_card_enabled"`
}

func paymentMethodsBinder(r *http.Request) (any, error) {
	values, err := r.URL.Query(), r.ParseForm()
	if err != nil || len(values["checkout_id"]) > 1 {
		return nil, webapp.BadRequest("invalid payment-method query", nil)
	}
	for key := range values {
		if key != "checkout_id" {
			return nil, webapp.BadRequest("unknown payment-method query parameter", nil)
		}
	}
	return paymentMethodsInput{Nonce: r.Header.Get(kiosk.CustomerHeader), CheckoutID: values.Get("checkout_id")}, nil
}

func (s *service) PaymentMethods(ctx context.Context, input paymentMethodsInput) (paymentMethodsResponse, error) {
	if !s.kioskOnly {
		return paymentMethodsResponse{}, webapp.Forbidden("terminal payments require a bound kiosk", nil)
	}
	actor, err := s.checkoutActor(ctx, input.Nonce)
	if err != nil {
		return paymentMethodsResponse{}, err
	}
	var result paymentMethodsResponse
	result.TerminalCard = terminalbridge.TerminalAvailability{Status: "disabled", ReasonCode: "terminal_payments_disabled"}
	if s.checkout == nil {
		return result, nil
	}
	if s.checkout.TerminalBridge != nil {
		result.Availability, err = s.checkout.TerminalBridge.Availability(ctx, terminalOwner(actor), input.CheckoutID)
		if err != nil {
			return paymentMethodsResponse{}, err
		}
	}
	if s.checkout.OnlineCardEnabled != nil {
		result.OnlineCardEnabled, err = s.checkout.OnlineCardEnabled(ctx, actor.CompanyID, actor.KioskID)
		if err != nil {
			return paymentMethodsResponse{}, err
		}
	}
	current, err := s.checkoutActor(ctx, input.Nonce)
	if err != nil {
		return paymentMethodsResponse{}, err
	}
	if !sameTerminalActor(actor, current) {
		return paymentMethodsResponse{}, kiosk.CustomerRequired()
	}
	return result, nil
}
