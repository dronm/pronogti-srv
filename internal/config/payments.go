package config

import (
	"errors"
	"regexp"
	"time"
)

// PaymentsConfig supplies deployment-level settings. Bank profiles and kiosk
// bindings live in PostgreSQL; token values stay in environment variables.
type PaymentsConfig struct {
	JWTKeyEnv              string `json:"jwt_key_env"`
	MockExternal           bool   `json:"mock_external"`
	CreatingTimeoutSeconds int    `json:"creating_timeout_seconds"`
	WorkerEnabled          bool   `json:"worker_enabled"`
	PollInterval           string `json:"poll_interval"`
	MockAutoPayAfter       string `json:"mock_auto_pay_after"`
	WebhookEnabled         bool   `json:"webhook_enabled"`
	WebhookPublicKeyEnv    string `json:"webhook_public_key_env"`
}

func (c PaymentsConfig) PollDuration() (time.Duration, error) {
	return parseDuration("payments.poll_interval", c.PollInterval)
}

func (c PaymentsConfig) MockPaymentDuration() (time.Duration, error) {
	return parseDuration("payments.mock_auto_pay_after", c.MockAutoPayAfter)
}

func (c PaymentsConfig) Validate() error {
	if c.JWTKeyEnv != "" && !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(c.JWTKeyEnv) {
		return errors.New("payments.jwt_key_env must name an environment variable")
	}
	if c.CreatingTimeoutSeconds < 1 || c.CreatingTimeoutSeconds > 3600 {
		return errors.New("payments.creating_timeout_seconds must be between 1 and 3600")
	}
	poll, err := c.PollDuration()
	if err != nil {
		return err
	}
	if poll < time.Second || poll > time.Minute {
		return errors.New("payments.poll_interval must be between 1s and 1m")
	}
	mockDelay, err := c.MockPaymentDuration()
	if err != nil {
		return err
	}
	if mockDelay < 0 || mockDelay > 24*time.Hour {
		return errors.New("payments.mock_auto_pay_after must be between 0s and 24h")
	}
	if c.WebhookPublicKeyEnv != "" && !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(c.WebhookPublicKeyEnv) {
		return errors.New("payments.webhook_public_key_env must name an environment variable")
	}
	if c.WebhookEnabled && c.WebhookPublicKeyEnv == "" {
		return errors.New("payments.webhook_public_key_env is required when the bank webhook is enabled")
	}
	return nil
}
