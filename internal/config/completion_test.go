package config

import (
	"testing"
	"time"
)

func TestCompletionConfigurationDefaultsDoNotEnableLiveWrites(t *testing.T) {
	c := defaultConfig()
	if !c.Payments.WorkerEnabled || !c.Settlement.WorkerEnabled || !c.Fiscal.WorkerEnabled {
		t.Fatal("completion workers must be enabled by default")
	}
	if c.Payments.WebhookEnabled || c.Settlement.AllowLive || c.Fiscal.AllowLive {
		t.Fatal("live integrations require explicit configuration")
	}
	if delay, err := c.Payments.MockPaymentDuration(); err != nil || delay != 2*time.Second {
		t.Fatalf("demo completion delay=%s error=%v", delay, err)
	}
	if err := c.Settlement.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := c.Payments.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCompletionConfigurationRejectsInvalidIntervalsAndWebhookKeys(t *testing.T) {
	for _, change := range []func(*PaymentsConfig){
		func(c *PaymentsConfig) { c.PollInterval = "0s" },
		func(c *PaymentsConfig) { c.PollInterval = "2m" },
		func(c *PaymentsConfig) { c.MockAutoPayAfter = "-1s" },
		func(c *PaymentsConfig) { c.MockAutoPayAfter = "25h" },
		func(c *PaymentsConfig) { c.WebhookEnabled = true; c.WebhookPublicKeyEnv = "" },
		func(c *PaymentsConfig) { c.WebhookPublicKeyEnv = "not an env" },
	} {
		c := defaultConfig().Payments
		change(&c)
		if err := c.Validate(); err == nil {
			t.Fatalf("invalid confirmation config accepted: %+v", c)
		}
	}
	for _, interval := range []string{"", "invalid", "0s", "90s"} {
		if err := (SettlementConfig{PollInterval: interval}).Validate(); err == nil {
			t.Fatalf("invalid settlement polling accepted: %q", interval)
		}
	}
}
