package config

import (
	"errors"
	"time"
)

// FiscalConfig controls the durable receipt worker. Live hardware requires an
// explicit application switch in addition to a live profile and production pay.
type FiscalConfig struct {
	WorkerEnabled bool   `json:"worker_enabled"`
	AllowLive     bool   `json:"allow_live"`
	PollInterval  string `json:"poll_interval"`
}

func (c FiscalConfig) PollDuration() (time.Duration, error) {
	return parseDuration("fiscal.poll_interval", c.PollInterval)
}

func (c FiscalConfig) Validate() error {
	duration, err := c.PollDuration()
	if err != nil {
		return err
	}
	if duration < time.Second || duration > time.Minute {
		return errors.New("fiscal.poll_interval must be between 1s and 1m")
	}
	return nil
}
