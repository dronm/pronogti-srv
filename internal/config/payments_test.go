package config

import (
	"strings"
	"testing"
	"time"
)

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

func TestPaymentJWTKeySurvivesConfigRewrite(t *testing.T) {
	key := strings.Repeat("7f", 32)
	path := writeConfig(t, `{"payments":{"jwt_key":"`+key+`"},`+testCredentials+`}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Payments.JWTKey != key {
		t.Fatal("inline payment key was not loaded")
	}
	if err := cfg.SaveYClientsToken("fixture", time.Now()); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Payments.JWTKey != key {
		t.Fatal("config rewrite dropped the inline payment key")
	}
}
