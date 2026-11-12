// Package receiptmail sends already-issued receipt links through an SMTP relay.
package receiptmail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dronm/pronogti/internal/config"
)

var (
	ErrInvalidAddress = errors.New("a bare email address is required")
	ErrInvalidLink    = errors.New("an issued Saby receipt link is required")
	ErrDelivery       = errors.New("receipt email SMTP delivery failed")
	sabyReceiptURL    = regexp.MustCompile(`^https://saby\.ru/ofd/check/rec/([0-9]{1,20})/([0-9]{6})/([0-9]{1,10})$`)
)

const fiscalDateTimeLayout = "2006-01-02T15:04"

type Sender struct {
	address     string
	from        string
	host        string
	username    string
	password    string
	timeout     time.Duration
	implicitTLS bool
	tlsConfig   *tls.Config
}

// New creates a sender for an enabled configuration. Credentials are loaded
// from the process environment and SMTP is always protected by verified TLS.
func New(cfg config.ReceiptEmailConfig) (*Sender, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	password := os.Getenv(cfg.SMTPPasswordEnv)
	if password == "" || strings.ContainsRune(password, '\x00') {
		return nil, fmt.Errorf("receipt_email.smtp_password_env requires a usable value in %s", cfg.SMTPPasswordEnv)
	}
	from, err := NormalizeAddress(cfg.From)
	if err != nil {
		return nil, errors.New("receipt_email.from must be a bare ASCII email address")
	}
	timeout, err := cfg.SendTimeout()
	if err != nil {
		return nil, err
	}
	return &Sender{
		address:     net.JoinHostPort(cfg.SMTPHost, strconv.Itoa(cfg.SMTPPort)),
		from:        from,
		host:        cfg.SMTPHost,
		username:    cfg.SMTPUsername,
		password:    password,
		timeout:     timeout,
		implicitTLS: cfg.SMTPTLSMode == "implicit",
		tlsConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: cfg.SMTPHost,
		},
	}, nil
}

// NormalizeAddress accepts one bare ASCII mailbox. It rejects display names,
// multiple recipients and anything that could introduce a header or SMTP command.
func NormalizeAddress(raw string) (string, error) {
	address := strings.TrimSpace(raw)
	if strings.ContainsAny(raw, "\r\n") || len(address) > 254 ||
		strings.IndexFunc(address, func(r rune) bool { return r < '!' || r > '~' }) >= 0 {
		return "", ErrInvalidAddress
	}
	parsed, err := mail.ParseAddress(address)
	at := strings.LastIndexByte(address, '@')
	if err != nil || parsed.Address != address || at < 1 || at > 64 {
		return "", ErrInvalidAddress
	}
	return address[:at+1] + strings.ToLower(address[at+1:]), nil
}

func (s *Sender) Send(ctx context.Context, email, receiptURL, dateTime string) error {
	to, err := NormalizeAddress(email)
	if err != nil {
		return err
	}
	match := sabyReceiptURL.FindStringSubmatch(receiptURL)
	date, err := time.Parse(fiscalDateTimeLayout, dateTime)
	if match == nil || err != nil || date.Format(fiscalDateTimeLayout) != dateTime || date.Format("020106") != match[2] {
		return ErrInvalidLink
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", s.address)
	if err != nil {
		return deliveryError("connect")
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return deliveryError("deadline")
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	var smtpConn net.Conn = conn
	if s.implicitTLS {
		tlsConn := tls.Client(conn, s.tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return deliveryError("TLS verification")
		}
		smtpConn = tlsConn
	}
	client, err := smtp.NewClient(smtpConn, s.host)
	if err != nil {
		return deliveryError("greeting")
	}
	defer client.Close()
	if !s.implicitTLS {
		if err := client.StartTLS(s.tlsConfig); err != nil {
			return deliveryError("STARTTLS verification")
		}
	}
	if err := client.Auth(smtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
		return deliveryError("authentication")
	}
	if err := client.Mail(s.from); err != nil {
		return deliveryError("sender")
	}
	if err := client.Rcpt(to); err != nil {
		return deliveryError("recipient")
	}
	body, err := client.Data()
	if err != nil {
		return deliveryError("message envelope")
	}
	if err := writeMessage(body, s.from, to, receiptURL, match[1], date.Format("02.01.2006 15:04"), match[3]); err != nil {
		return deliveryError("message body")
	}
	if err := body.Close(); err != nil {
		return deliveryError("message acceptance")
	}
	return nil
}

// Relay responses may echo addresses or authentication data. Keep them out of
// both logs and API responses, while retaining the failed stage for diagnosis.
func deliveryError(stage string) error {
	return fmt.Errorf("%w at %s", ErrDelivery, stage)
}

func writeMessage(out io.Writer, from, to, receiptURL, registrationNumber, dateTime, fiscalSign string) error {
	_, domain, _ := strings.Cut(from, "@")
	subject := mime.QEncoding.Encode("UTF-8", "Ваш кассовый чек")
	if _, err := fmt.Fprintf(out, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n",
		from, to, subject, time.Now().UTC().Format(time.RFC1123Z), rand.Text(), domain); err != nil {
		return err
	}
	quoted := quotedprintable.NewWriter(out)
	if _, err := fmt.Fprintf(quoted, "Здравствуйте!\r\n\r\nВаш кассовый чек в Saby ОФД:\r\n%s\r\n\r\nЕсли чек не открывается, найдите его на https://saby.ru/ofd/check по реквизитам:\r\nРегистрационный номер ККТ: %s\r\nДата и время: %s\r\nФискальный признак: %s\r\n\r\nЕсли чек ещё обрабатывается, попробуйте позднее.\r\n",
		receiptURL, registrationNumber, dateTime, fiscalSign); err != nil {
		return err
	}
	return quoted.Close()
}
