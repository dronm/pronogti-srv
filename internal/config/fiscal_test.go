package config

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestFiscalDefaultsAndValidation(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{`+testCredentials+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Fiscal.WorkerEnabled || cfg.Fiscal.AllowLive || cfg.Fiscal.PollInterval != "2s" {
		t.Fatalf("unsafe fiscal defaults: %+v", cfg.Fiscal)
	}
	for _, value := range []string{"0", "500ms", "61s", "bad"} {
		if _, err := Load(writeConfig(t, `{"fiscal":{"poll_interval":"`+value+`"},`+testCredentials+`}`)); err == nil {
			t.Errorf("accepted fiscal interval %q", value)
		}
	}
}

func TestFiscalConfigurationSurvivesTokenRefresh(t *testing.T) {
	path := writeConfig(t, `{"fiscal":{"worker_enabled":false,"allow_live":true,"poll_interval":"10s"},`+testCredentials+`}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveYClientsToken("refreshed", time.Now()); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Fiscal FiscalConfig `json:"fiscal"`
	}
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Fiscal.WorkerEnabled || !saved.Fiscal.AllowLive || saved.Fiscal.PollInterval != "10s" {
		t.Fatalf("fiscal config lost on token refresh: %+v", saved.Fiscal)
	}
}
