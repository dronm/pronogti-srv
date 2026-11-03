package config

import (
	"errors"
	"time"
)

type SettlementConfig struct {
	WorkerEnabled bool   `json:"worker_enabled"`
	AllowLive     bool   `json:"allow_live"`
	PollInterval  string `json:"poll_interval"`
}

func (c SettlementConfig) PollDuration() (time.Duration, error) {
	return parseDuration("settlement.poll_interval", c.PollInterval)
}

func (c SettlementConfig) Validate() error {
	value, err := c.PollDuration()
	if err != nil {
		return err
	}
	if value < time.Second || value > time.Minute {
		return errors.New("settlement.poll_interval must be between 1s and 1m")
	}
	return nil
}
