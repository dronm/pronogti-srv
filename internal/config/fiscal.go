package config

import (
	"errors"
	"strings"
	"time"
)

// FiscalConfig controls the durable receipt worker. Live hardware requires an
// explicit application switch in addition to a live profile and production pay.
type FiscalConfig struct {
	ShiftOperatorToken string `json:"shift_operator_token,omitempty"`
	CorrectionTimeZone string `json:"correction_time_zone,omitempty"`
	WorkerEnabled      bool   `json:"worker_enabled"`
	AllowLive          bool   `json:"allow_live"`
	PollInterval       string `json:"poll_interval"`
}

// CorrectionLocation interprets the calendar date of a historical KKT
// transaction. Local matches a driver on the same host; separate hosts can
// explicitly select the KKT's IANA time zone.
func (c FiscalConfig) CorrectionLocation() (*time.Location, error) {
	if c.CorrectionTimeZone == "" || c.CorrectionTimeZone == "Local" {
		return time.Local, nil
	}
	location, err := time.LoadLocation(c.CorrectionTimeZone)
	if err != nil {
		return nil, errors.New("fiscal.correction_time_zone must be a valid IANA time zone")
	}
	return location, nil
}

func (c FiscalConfig) PollDuration() (time.Duration, error) {
	return parseDuration("fiscal.poll_interval", c.PollInterval)
}

func (c FiscalConfig) Validate() error {
	if _, err := c.CorrectionLocation(); err != nil {
		return err
	}
	if c.ShiftOperatorToken != "" && (len(c.ShiftOperatorToken) < 16 || len(c.ShiftOperatorToken) > 512 || strings.ContainsAny(c.ShiftOperatorToken, "\r\n")) {
		return errors.New("fiscal.shift_operator_token must contain 16 to 512 bytes without line breaks")
	}
	duration, err := c.PollDuration()
	if err != nil {
		return err
	}
	if duration < time.Second || duration > time.Minute {
		return errors.New("fiscal.poll_interval must be between 1s and 1m")
	}
	return nil
}
