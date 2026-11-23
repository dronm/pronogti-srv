package config

import (
	"encoding/json"
	"os"
	"strings"
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

func TestShiftOperatorTokenValidation(t *testing.T) {
	for _, value := range []string{"", "0123456789abcdef"} {
		if err := (FiscalConfig{PollInterval: "2s", ShiftOperatorToken: value}).Validate(); err != nil {
			t.Fatalf("valid shift operator token rejected: %v", err)
		}
	}
	for _, value := range []string{"short", "0123456789abcde\n", strings.Repeat("a", 513)} {
		if err := (FiscalConfig{PollInterval: "2s", ShiftOperatorToken: value}).Validate(); err == nil {
			t.Fatal("invalid shift operator token accepted")
		}
	}
}

func TestCorrectionTimeZone(t *testing.T) {
	location, err := (FiscalConfig{CorrectionTimeZone: "Asia/Yekaterinburg"}).CorrectionLocation()
	if err != nil || location.String() != "Asia/Yekaterinburg" {
		t.Fatalf("correction location: %v %v", location, err)
	}
	if _, err := (FiscalConfig{CorrectionTimeZone: "Not/A_Time_Zone"}).CorrectionLocation(); err == nil {
		t.Fatal("invalid correction time zone accepted")
	}
}
