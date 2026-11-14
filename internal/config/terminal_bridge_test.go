package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTerminalBridgeDefaultsRemainDisabledWithoutJSON(t *testing.T) {
	t.Setenv("PRONOGTI_TERMINAL_BRIDGE_ENABLED", "true")
	t.Setenv("PRONOGTI_TERMINAL_BRIDGE_ALLOW_LIVE", "true")
	cfg, err := Load(writeConfig(t, `{`+testCredentials+`}`))
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultTerminalBridgeConfig()
	if cfg.TerminalBridge != want {
		t.Fatalf("terminal bridge defaults = %+v, want %+v", cfg.TerminalBridge, want)
	}
	if cfg.TerminalBridge.Enabled || cfg.TerminalBridge.AllowLive || cfg.TerminalBridge.AllowSimulated {
		t.Fatal("terminal integration was enabled without JSON opt-in")
	}
}

func TestTerminalBridgeEnabledPollRequiresResponseBudget(t *testing.T) {
	for _, test := range []struct {
		name      string
		json      string
		wantError bool
	}{
		{"disabled short HTTP", `"http":{"write_timeout":"2s"}`, false},
		{"enabled insufficient HTTP", `"terminal_bridge":{"enabled":true},"http":{"write_timeout":"29s"}`, true},
		{"enabled exact margin", `"terminal_bridge":{"enabled":true},"http":{"write_timeout":"30s"}`, false},
		{"enabled custom poll", `"terminal_bridge":{"enabled":true,"max_poll_seconds":40,"lease_seconds":45},"http":{"write_timeout":"45s"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, `{`+test.json+`,`+testCredentials+`}`))
			if (err != nil) != test.wantError {
				t.Fatalf("Load error = %v; want error %v", err, test.wantError)
			}
		})
	}
}

func TestTerminalBridgeRangesAreValidatedEvenWhenDisabled(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*TerminalBridgeConfig)
		field  string
	}{
		{"short deadline", func(c *TerminalBridgeConfig) { c.CommandTTLSeconds = 9 }, "command_ttl_seconds"},
		{"long deadline", func(c *TerminalBridgeConfig) { c.CommandTTLSeconds = 901 }, "command_ttl_seconds"},
		{"zero poll", func(c *TerminalBridgeConfig) { c.MaxPollSeconds = 0 }, "max_poll_seconds"},
		{"long poll", func(c *TerminalBridgeConfig) { c.MaxPollSeconds = 61 }, "max_poll_seconds"},
		{"lease too short", func(c *TerminalBridgeConfig) { c.LeaseSeconds = 29 }, "lease_seconds"},
		{"lease too long", func(c *TerminalBridgeConfig) { c.LeaseSeconds = 901 }, "lease_seconds"},
		{"heartbeat too short", func(c *TerminalBridgeConfig) { c.HeartbeatMaxAgeSeconds = 9 }, "heartbeat_max_age_seconds"},
		{"heartbeat too long", func(c *TerminalBridgeConfig) { c.HeartbeatMaxAgeSeconds = 3601 }, "heartbeat_max_age_seconds"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultTerminalBridgeConfig()
			test.mutate(&cfg)
			err := cfg.Validate(time.Second)
			if err == nil || !strings.Contains(err.Error(), "terminal_bridge."+test.field) {
				t.Fatalf("error = %v; want terminal_bridge.%s", err, test.field)
			}
		})
	}
}

func TestTerminalBridgeJSONSurvivesYClientsTokenRefresh(t *testing.T) {
	path := writeConfig(t, `{"terminal_bridge":{"enabled":true,"allow_simulated":true,"command_ttl_seconds":240,"max_poll_seconds":30,"lease_seconds":50,"heartbeat_max_age_seconds":90},"http":{"write_timeout":"40s"},`+testCredentials+`}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := cfg.TerminalBridge
	if err := cfg.SaveYClientsToken("test-refreshed-token", time.Now()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		TerminalBridge TerminalBridgeConfig `json:"terminal_bridge"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.TerminalBridge != want {
		t.Fatalf("terminal bridge settings changed on token refresh: got %+v, want %+v", saved.TerminalBridge, want)
	}
}
