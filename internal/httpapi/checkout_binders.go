package httpapi

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/webapp"
)

type checkoutPrepareInput struct {
	Nonce          string
	IdempotencyKey string
	Request        checkout.PrepareRequest
}

type checkoutGetInput struct {
	Nonce string
	ID    string
}

func checkoutPrepareBinder(r *http.Request) (any, error) {
	request, err := decodeCheckoutBody[checkout.PrepareRequest](r)
	if err != nil {
		return nil, err
	}
	return checkoutPrepareInput{Nonce: r.Header.Get(kiosk.CustomerHeader), IdempotencyKey: r.Header.Get("Idempotency-Key"), Request: request}, nil
}

func checkoutGetBinder(r *http.Request) (any, error) {
	return checkoutGetInput{Nonce: r.Header.Get(kiosk.CustomerHeader), ID: r.PathValue("id")}, nil
}

// Checkout requests intentionally have a strict contract. User-provided price,
// company, customer, total, or bank configuration must never be accepted here.
func decodeCheckoutBody[T any](r *http.Request) (T, error) {
	var input T
	if err := requireSessionJSON(r); err != nil {
		return input, err
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, webapp.BadRequest("invalid checkout request", nil)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return input, webapp.BadRequest("request must contain exactly one JSON object", nil)
	}
	return input, nil
}
