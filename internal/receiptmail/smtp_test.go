package receiptmail

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

const testReceiptURL = "https://saby.ru/ofd/check/rec/0000000000000001/061126/12345"

func TestNormalizeAddress(t *testing.T) {
	address, err := NormalizeAddress(" Customer+receipt@EXAMPLE.COM ")
	if err != nil || address != "Customer+receipt@example.com" {
		t.Fatalf("address %q, error %v", address, err)
	}
	for _, invalid := range []string{
		"customer@example.com\r\n",
		"Customer <customer@example.com>",
		"a@example.com,b@example.com",
		"customer@example.com\nBcc: copy@example.com",
		"покупатель@example.com",
	} {
		if _, err := NormalizeAddress(invalid); !errors.Is(err, ErrInvalidAddress) {
			t.Errorf("expected invalid address for %q, got %v", invalid, err)
		}
	}
}

func TestSenderRejectsInvalidReceiptLinkBeforeConnecting(t *testing.T) {
	sender := &Sender{}
	for _, tc := range []struct{ link, date string }{
		{testReceiptURL, "2026-11-07T10:15"},
		{testReceiptURL, "2026-11-06T10:15:00"},
		{testReceiptURL, "2026-11-06T10:15\r\n"},
		{testReceiptURL + "?redirect=elsewhere", "2026-11-06T10:15"},
		{strings.Replace(testReceiptURL, "saby.ru", "other.example", 1), "2026-11-06T10:15"},
	} {
		if err := sender.Send(context.Background(), "customer@example.com", tc.link, tc.date); !errors.Is(err, ErrInvalidLink) {
			t.Errorf("expected invalid link for %q and %q, got %v", tc.link, tc.date, err)
		}
	}
}

func TestMessageContainsSabyLinkAndPublicLookupDetails(t *testing.T) {
	var encoded strings.Builder
	if err := writeMessage(&encoded, "sender@example.com", "customer@example.com", testReceiptURL, "0000000000000001", "06.11.2026 10:15", "12345"); err != nil {
		t.Fatal(err)
	}
	message, err := mail.ReadMessage(strings.NewReader(encoded.String()))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := (&mime.WordDecoder{}).DecodeHeader(message.Header.Get("Subject"))
	if err != nil || subject != "Ваш кассовый чек" || message.Header.Get("To") != "customer@example.com" {
		t.Fatalf("unexpected header: %#v, subject %q, error %v", message.Header, subject, err)
	}
	body, err := io.ReadAll(quotedprintable.NewReader(message.Body))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		testReceiptURL,
		"https://saby.ru/ofd/check по реквизитам:",
		"Регистрационный номер ККТ: 0000000000000001",
		"Дата и время: 06.11.2026 10:15",
		"Фискальный признак: 12345",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("email body missing %q", want)
		}
	}
}

func TestSenderDeliversThroughVerifiedSTARTTLS(t *testing.T) {
	certificateServer := httptest.NewTLSServer(http.NotFoundHandler())
	defer certificateServer.Close()
	roots := x509.NewCertPool()
	roots.AddCert(certificateServer.Certificate())

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	relayed := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			relayed <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		relayed <- captureSTARTTLSDelivery(conn, certificateServer.TLS)
	}()

	sender := Sender{
		address: listener.Addr().String(), from: "sender@example.com", host: "127.0.0.1",
		username: "test-login", password: "test-password", timeout: 3 * time.Second,
		tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "127.0.0.1", RootCAs: roots},
	}
	if err := sender.Send(context.Background(), "customer@example.com", testReceiptURL, "2026-11-06T10:15"); err != nil {
		t.Fatal(err)
	}
	if err := <-relayed; err != nil {
		t.Fatal(err)
	}
}

func captureSTARTTLSDelivery(conn net.Conn, serverTLS *tls.Config) error {
	protocol := textproto.NewConn(conn)
	if err := protocol.PrintfLine("220 localhost ESMTP"); err != nil {
		return err
	}
	if err := expectSMTPCommand(protocol, "EHLO localhost", "250-localhost\r\n250 STARTTLS"); err != nil {
		return err
	}
	if err := expectSMTPCommand(protocol, "STARTTLS", "220 begin TLS"); err != nil {
		return err
	}
	secure := tls.Server(conn, serverTLS)
	protocol = textproto.NewConn(secure)
	auth := base64.StdEncoding.EncodeToString([]byte("\x00test-login\x00test-password"))
	for _, stage := range [][2]string{
		{"EHLO localhost", "250-localhost\r\n250 AUTH PLAIN"},
		{"AUTH PLAIN " + auth, "235 authenticated"},
		{"MAIL FROM:<sender@example.com>", "250 sender accepted"},
		{"RCPT TO:<customer@example.com>", "250 recipient accepted"},
		{"DATA", "354 send message"},
	} {
		if err := expectSMTPCommand(protocol, stage[0], stage[1]); err != nil {
			return err
		}
	}
	message, err := mail.ReadMessage(protocol.DotReader())
	if err != nil {
		return err
	}
	body, err := io.ReadAll(quotedprintable.NewReader(message.Body))
	if err != nil {
		return err
	}
	if !strings.Contains(string(body), testReceiptURL) {
		return errors.New("relay did not receive receipt link")
	}
	return protocol.PrintfLine("250 message accepted")
}

func expectSMTPCommand(protocol *textproto.Conn, command, response string) error {
	got, err := protocol.ReadLine()
	if err != nil {
		return err
	}
	if got != command {
		return fmt.Errorf("SMTP command %q, want %q", got, command)
	}
	return protocol.PrintfLine("%s", response)
}
