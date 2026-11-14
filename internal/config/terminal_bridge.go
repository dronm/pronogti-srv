package config

import (
	"errors"
	"time"
)

// TerminalBridgeConfig controls the physical-terminal integration. Settings are
// read only from JSON; agent tokens are issued and hashed in PostgreSQL rather
// than stored in deployment configuration or environment variables.
type TerminalBridgeConfig struct {
	Enabled                bool `json:"enabled"`
	AllowLive              bool `json:"allow_live"`
	AllowSimulated         bool `json:"allow_simulated"`
	CommandTTLSeconds      int  `json:"command_ttl_seconds"`
	MaxPollSeconds         int  `json:"max_poll_seconds"`
	LeaseSeconds           int  `json:"lease_seconds"`
	HeartbeatMaxAgeSeconds int  `json:"heartbeat_max_age_seconds"`
}

func DefaultTerminalBridgeConfig() TerminalBridgeConfig {
	return TerminalBridgeConfig{
		CommandTTLSeconds:      120,
		MaxPollSeconds:         25,
		LeaseSeconds:           45,
		HeartbeatMaxAgeSeconds: 60,
	}
}

// Validate always checks ranges so a disabled deployment cannot retain invalid
// settings unnoticed. The HTTP timeout requirement applies only when enabled.
func (c TerminalBridgeConfig) Validate(httpWriteTimeout time.Duration) error {
	if c.CommandTTLSeconds < 10 || c.CommandTTLSeconds > 900 {
		return errors.New("terminal_bridge.command_ttl_seconds must be between 10 and 900")
	}
	if c.MaxPollSeconds < 1 || c.MaxPollSeconds > 60 {
		return errors.New("terminal_bridge.max_poll_seconds must be between 1 and 60")
	}
	if c.LeaseSeconds < c.MaxPollSeconds+5 || c.LeaseSeconds > 900 {
		return errors.New("terminal_bridge.lease_seconds must be at least max_poll_seconds + 5 and no greater than 900")
	}
	if c.HeartbeatMaxAgeSeconds < 10 || c.HeartbeatMaxAgeSeconds > 3600 {
		return errors.New("terminal_bridge.heartbeat_max_age_seconds must be between 10 and 3600")
	}
	if c.Enabled && httpWriteTimeout < time.Duration(c.MaxPollSeconds+5)*time.Second {
		return errors.New("http.write_timeout must be at least terminal_bridge.max_poll_seconds + 5 seconds when terminal_bridge.enabled is true")
	}
	return nil
}
