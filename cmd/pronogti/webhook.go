package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/dronm/paytochkabank"
	"github.com/dronm/pronogti/internal/config"
	"github.com/dronm/pronogti/internal/payment"
)

// Parse the verification key before starting workers. A malformed startup
// configuration must not start upstream financial writes and then exit.
func configuredWebhook(cfg config.PaymentsConfig, receive payment.WebhookReceiver, cards ...payment.CardWebhookReceiver) (http.Handler, error) {
	if !cfg.WebhookEnabled {
		return nil, nil
	}
	value, ok := os.LookupEnv(cfg.WebhookPublicKeyEnv)
	if !ok || value == "" {
		return nil, fmt.Errorf("payments.webhook_public_key_env requires environment variable %s", cfg.WebhookPublicKeyEnv)
	}
	publicKey, err := paytochkabank.ParseWebhookPublicKey([]byte(value))
	if err != nil {
		return nil, errors.New("bank webhook public key could not be parsed")
	}
	verifier, err := paytochkabank.NewWebhookVerifier(publicKey)
	if err != nil {
		return nil, errors.New("bank webhook verifier could not be initialized")
	}
	if len(cards) > 0 {
		return payment.NewCombinedWebhookHandler(verifier, receive, cards[0]), nil
	}
	return payment.NewWebhookHandler(verifier, receive), nil
}
