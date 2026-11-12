package config

import (
	"errors"
	"net"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

// ReceiptEmailConfig controls delivery of links to already-issued receipts.
type ReceiptEmailConfig struct {
	Enabled         bool               `json:"enabled"`
	SMTPHost        string             `json:"smtp_host"`
	SMTPPort        int                `json:"smtp_port"`
	SMTPTLSMode     string             `json:"smtp_tls_mode"`
	SMTPUsername    string             `json:"smtp_username"`
	SMTPPasswordEnv string             `json:"smtp_password_env"`
	From            string             `json:"from"`
	Timeout         string             `json:"timeout"`
	SabyDevices     []SabyDeviceConfig `json:"saby_devices"`
}

var (
	smtpHostnamePattern    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]{0,251}[a-zA-Z0-9]$`)
	smtpPasswordEnvPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

func (c ReceiptEmailConfig) SendTimeout() (time.Duration, error) {
	if c.Timeout == "" {
		return 10 * time.Second, nil
	}
	return parseDuration("receipt_email.timeout", c.Timeout)
}

func (c ReceiptEmailConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if net.ParseIP(c.SMTPHost) == nil && !smtpHostnamePattern.MatchString(c.SMTPHost) {
		return errors.New("receipt_email.smtp_host must be a bare host name or IP address")
	}
	if c.SMTPPort < 1 || c.SMTPPort > 65535 {
		return errors.New("receipt_email.smtp_port must be a valid TCP port")
	}
	if c.SMTPTLSMode != "starttls" && c.SMTPTLSMode != "implicit" {
		return errors.New("receipt_email.smtp_tls_mode must be starttls or implicit")
	}
	if strings.TrimSpace(c.SMTPUsername) == "" || strings.ContainsAny(c.SMTPUsername, "\x00\r\n") {
		return errors.New("receipt_email.smtp_username must contain a valid SMTP login")
	}
	if !smtpPasswordEnvPattern.MatchString(c.SMTPPasswordEnv) {
		return errors.New("receipt_email.smtp_password_env must be an environment variable name")
	}
	from, err := mail.ParseAddress(c.From)
	if err != nil || from.Address != c.From {
		return errors.New("receipt_email.from must be a bare email address")
	}
	timeout, err := c.SendTimeout()
	if err != nil {
		return err
	}
	if timeout < time.Second || timeout > 30*time.Second {
		return errors.New("receipt_email.timeout must be between 1s and 30s")
	}
	return c.validateSabyDevices()
}
