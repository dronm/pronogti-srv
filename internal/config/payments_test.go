package config

import "testing"

func TestPaymentConfigDefaultsAndValidation(t *testing.T) {
	cfg := defaultConfig()
	if cfg.Payments.JWTKeyEnv != "PRONOGTI_PAYMENT_JWT_KEY" || cfg.Payments.MockExternal || cfg.Payments.CreatingTimeoutSeconds != 120 {
		t.Fatalf("unexpected defaults: %+v", cfg.Payments)
	}
	if err := cfg.Payments.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []PaymentsConfig{
		{JWTKeyEnv: "not an env", CreatingTimeoutSeconds: 120},
		{JWTKeyEnv: "KEY", CreatingTimeoutSeconds: 0},
		{JWTKeyEnv: "KEY", CreatingTimeoutSeconds: 3601},
	} {
		if err := c.Validate(); err == nil {
			t.Fatalf("invalid payment settings accepted: %+v", c)
		}
	}
}
