package payment

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestEnvironmentTokensAcceptsPhonyJWTWithoutEncryptionKey(t *testing.T) {
	t.Setenv("PRONOGTI_TEST_TOCHKA_TOKEN", "phony.jwt.token")
	profile := Profile{ID: 1, CompanyID: 781898, JWTEnv: "PRONOGTI_TEST_TOCHKA_TOKEN", JWTCiphertext: []byte("invalid ignored ciphertext")}
	token, err := NewEnvironmentTokens("MISSING_KEY").Token(context.Background(), profile)
	if err != nil || token != "phony.jwt.token" {
		t.Fatalf("token lookup failed: %v", err)
	}
}

func TestEnvironmentTokensRejectsMissingOrMalformedReferencesSafely(t *testing.T) {
	for _, name := range []string{"PRONOGTI_TEST_TOCHKA_MISSING", "TOKEN;echo", "PATH TO TOKEN", "TOKEN\nVALUE"} {
		if name == "PRONOGTI_TEST_TOCHKA_MISSING" {
			t.Setenv(name, "")
		}
		value, err := NewEnvironmentTokens("KEY").Token(context.Background(), Profile{JWTEnv: name})
		if err == nil || value != "" || strings.Contains(err.Error(), name) {
			t.Fatalf("unsafe or accepted reference: value=%q error=%v", value, err)
		}
	}
	for _, value := range []string{"", " phony.jwt.token", "phony.jwt.token ", "phony\r\ntoken", "phony token", "phony\ttoken", "phony.токен"} {
		t.Run(hex.EncodeToString([]byte(value)), func(t *testing.T) {
			t.Setenv("PRONOGTI_TEST_TOCHKA_INVALID", value)
			token, err := NewEnvironmentTokens("KEY").Token(context.Background(), Profile{JWTEnv: "PRONOGTI_TEST_TOCHKA_INVALID"})
			if err == nil || token != "" || strings.Contains(err.Error(), "phony") {
				t.Fatalf("bad credential was accepted or exposed: %v", err)
			}
		})
	}
	if validToken("phony\x00token") {
		t.Fatal("NUL in decrypted credential accepted")
	}
}

func sealedTokenFixture(t *testing.T, profile Profile, key []byte, token string) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	for i := range nonce {
		nonce[i] = byte(i + 1)
	}
	result := append([]byte{1}, nonce...)
	return aead.Seal(result, nonce, []byte(token), tokenAAD(profile))
}

func TestEnvironmentTokensDecryptsVersionedCompanyProfileBoundCiphertext(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	for _, format := range []string{"hex", "base64"} {
		t.Run(format, func(t *testing.T) {
			encoded := hex.EncodeToString(key)
			if format == "base64" {
				encoded = base64.StdEncoding.EncodeToString(key)
			}
			t.Setenv("PRONOGTI_TEST_TOCHKA_KEY", encoded)
			profile := Profile{ID: 9, CompanyID: 781898}
			profile.JWTCiphertext = sealedTokenFixture(t, profile, key, "encrypted.phony.jwt")
			source := NewEnvironmentTokens("PRONOGTI_TEST_TOCHKA_KEY")
			token, err := source.Token(context.Background(), profile)
			if err != nil || token != "encrypted.phony.jwt" {
				t.Fatalf("decrypt failed: %v", err)
			}
			for _, mutate := range []func(*Profile){func(p *Profile) { p.ID++ }, func(p *Profile) { p.CompanyID++ }, func(p *Profile) {
				p.JWTCiphertext = append([]byte(nil), p.JWTCiphertext...)
				p.JWTCiphertext[len(p.JWTCiphertext)-1] ^= 1
			}, func(p *Profile) { p.JWTCiphertext = append([]byte(nil), p.JWTCiphertext...); p.JWTCiphertext[0] = 2 }} {
				changed := profile
				mutate(&changed)
				token, err = source.Token(context.Background(), changed)
				if err == nil || token != "" || strings.Contains(err.Error(), "encrypted.phony.jwt") {
					t.Fatalf("wrong identity/ciphertext authenticated: %v", err)
				}
			}
		})
	}
}

func TestEnvironmentTokensRejectsInvalidKeysCiphertextAndCancellation(t *testing.T) {
	profile := Profile{ID: 9, CompanyID: 781898, JWTCiphertext: []byte("short")}
	for _, key := range []string{"", "secret.invalid.key", hex.EncodeToString(make([]byte, 16))} {
		t.Setenv("PRONOGTI_TEST_TOCHKA_BAD_KEY", key)
		token, err := NewEnvironmentTokens("PRONOGTI_TEST_TOCHKA_BAD_KEY").Token(context.Background(), profile)
		if err == nil || token != "" || strings.Contains(err.Error(), "secret.invalid.key") {
			t.Fatalf("bad key accepted or exposed: %v", err)
		}
	}
	t.Setenv("PRONOGTI_TEST_TOCHKA_BAD_KEY", hex.EncodeToString(make([]byte, 32)))
	if _, err := NewEnvironmentTokens("PRONOGTI_TEST_TOCHKA_BAD_KEY").Token(context.Background(), profile); err == nil {
		t.Fatal("short ciphertext accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewEnvironmentTokens("KEY").Token(ctx, profile); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not preserved: %v", err)
	}
	if _, err := NewEnvironmentTokens("KEY").Token(nil, profile); err == nil {
		t.Fatal("nil context accepted")
	}
	var source *EnvironmentTokens
	if _, err := source.Token(context.Background(), profile); err == nil {
		t.Fatal("nil key resolver accepted")
	}
}
