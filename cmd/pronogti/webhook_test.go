package main

import (
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
