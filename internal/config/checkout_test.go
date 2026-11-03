package config

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestCheckoutConfigurationDefaultsAndValidation(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{`+testCredentials+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Checkout.MaxLifeTime != 900 {
		t.Fatalf("checkout lifetime=%d", cfg.Checkout.MaxLifeTime)
	}
	for _, value := range []string{"0", "-1", "3601"} {
		if _, err := Load(writeConfig(t, `{"checkout":{"max_life_time":`+value+`},`+testCredentials+`}`)); err == nil {
			t.Errorf("accepted invalid checkout lifetime %s", value)
		}
	}
	path := writeConfig(t, `{"checkout":{"max_life_time":600},`+testCredentials+`}`)
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveYClientsToken("refreshed", time.Now()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Checkout CheckoutConfig `json:"checkout"`
	}
	if err := json.Unmarshal(data, &saved); err != nil || saved.Checkout.MaxLifeTime != 600 {
		t.Fatalf("checkout configuration lost on token refresh: %+v error=%v", saved.Checkout, err)
	}
}
