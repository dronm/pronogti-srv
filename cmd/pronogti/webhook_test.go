package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/dronm/pronogti/internal/config"
)

func TestDisabledWebhookDoesNotRequireAKey(t *testing.T) {
	handler, err := configuredWebhook(config.PaymentsConfig{}, nil)
	if err != nil || handler != nil {
		t.Fatalf("disabled callback initialization=%v handler=%v", err, handler)
	}
}

func TestEnabledWebhookRejectsMissingOrInvalidKeyWithoutEchoingMaterial(t *testing.T) {
	const keyEnv = "PRONOGTI_RUNTIME_TEST_WEBHOOK_KEY"
	for _, value := range []string{"", "not-valid-private-or-public-key"} {
		t.Setenv(keyEnv, value)
		if handler, err := configuredWebhook(config.PaymentsConfig{WebhookEnabled: true, WebhookPublicKeyEnv: keyEnv}, nil); err == nil || handler != nil || value != "" && strings.Contains(err.Error(), value) {
			t.Fatalf("invalid key handling: handler=%v error=%v", handler, err)
		}
	}
}

func TestEnabledWebhookPublicKeySources(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey),
	}))
	const keyEnv = "PRONOGTI_RUNTIME_TEST_WEBHOOK_KEY"
	for _, tc := range []struct {
		name      string
		configKey string
		envKey    string
		wantError bool
	}{
		{name: "config key only", configKey: publicKey},
		{name: "config key takes precedence", configKey: publicKey, envKey: "invalid"},
		{name: "environment fallback", envKey: publicKey},
		{name: "invalid config key fails startup", configKey: "invalid", envKey: publicKey, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(keyEnv, tc.envKey)
			handler, err := configuredWebhook(config.PaymentsConfig{
				WebhookEnabled: true, WebhookPublicKey: tc.configKey, WebhookPublicKeyEnv: keyEnv,
			}, nil)
			if (err != nil) != tc.wantError || (handler == nil) != tc.wantError {
				t.Fatalf("callback initialization: handler=%v error=%v", handler, err)
			}
		})
	}
}
