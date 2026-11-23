package httpapi

import (
	"context"
	"net/http"

	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/pronogti/internal/terminalbridge"
	"github.com/dronm/webapp"
)

type paymentMethodsInput struct{ Nonce, CheckoutID string }

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

func (s *service) PaymentMethods(ctx context.Context, input paymentMethodsInput) (terminalbridge.Availability, error) {
	if !s.kioskOnly {
		return terminalbridge.Availability{}, webapp.Forbidden("terminal payments require a bound kiosk", nil)
	}
	actor, err := s.checkoutActor(ctx, input.Nonce)
	if err != nil {
		return terminalbridge.Availability{}, err
	}
	if s.checkout == nil || s.checkout.TerminalBridge == nil {
		return terminalbridge.Availability{TerminalCard: terminalbridge.TerminalAvailability{Status: "disabled", ReasonCode: "terminal_payments_disabled"}}, nil
	}
	result, err := s.checkout.TerminalBridge.Availability(ctx, terminalOwner(actor), input.CheckoutID)
	if err != nil {
		return terminalbridge.Availability{}, err
	}
	current, err := s.checkoutActor(ctx, input.Nonce)
	if err != nil {
		return terminalbridge.Availability{}, err
	}
	if !sameTerminalActor(actor, current) {
		return terminalbridge.Availability{}, kiosk.CustomerRequired()
	}
	return result, nil
}
