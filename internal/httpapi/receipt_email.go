package httpapi

import (
	"context"
	"net/http"

	"github.com/dronm/pronogti/internal/kiosk"
)

type receiptEmailInput struct{ Nonce, ID, Email string }

type receiptEmailResult struct {
	Sent bool `json:"sent"`
}

func receiptEmailBinder(r *http.Request) (any, error) {
	if r.Body != nil {
		r.Body = http.MaxBytesReader(nil, r.Body, 1024)
	}
	input, err := decodeCheckoutBody[struct {
		Email string `json:"email"`
	}](r)
	if err != nil {
		return nil, err
	}
	return receiptEmailInput{Nonce: r.Header.Get(kiosk.CustomerHeader), ID: r.PathValue("id"), Email: input.Email}, nil
}

func (s *service) SendReceiptEmail(ctx context.Context, input receiptEmailInput) (receiptEmailResult, error) {
	actor, err := s.checkoutActor(ctx, input.Nonce)
	if err != nil {
		return receiptEmailResult{}, err
	}
	payments, err := s.paymentService()
	if err != nil {
		return receiptEmailResult{}, err
	}
	if err := payments.SendReceiptEmail(ctx, actor, input.ID, input.Email); err != nil {
		return receiptEmailResult{}, err
	}
	return receiptEmailResult{Sent: true}, nil
}
