package payment

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestEncryptTokenIsRuntimeCompatibleAndProfileBound(t *testing.T) {
	t.Setenv("PRONOGTI_TEST_PROFILE_ENCRYPT_KEY", hex.EncodeToString(bytes.Repeat([]byte{53}, 32)))
	store := NewEnvironmentTokens("PRONOGTI_TEST_PROFILE_ENCRYPT_KEY")
	profile := Profile{ID: 27, CompanyID: 781898}
	const token = "opaque.fixture.token"
	ciphertext, err := store.Encrypt(profile, token)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.Encrypt(profile, token)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ciphertext, repeated) || bytes.Contains(ciphertext, []byte(token)) || ciphertext[0] != 1 {
		t.Fatal("encryption did not use the protected, randomized versioned format")
	}
	profile.JWTCiphertext = ciphertext
	value, err := store.Token(context.Background(), profile)
	if err != nil || value != token {
		t.Fatal("runtime could not resolve the stored credential")
	}
	profile.CompanyID++
	if _, err := store.Token(context.Background(), profile); err == nil {
		t.Fatal("credential could be used with another company")
	}
	profile.CompanyID--
	profile.ID++
	if _, err := store.Token(context.Background(), profile); err == nil {
		t.Fatal("credential could be used with another profile")
	}
}

func TestEncryptTokenRejectsInvalidInputAndMissingKey(t *testing.T) {
	t.Setenv("PRONOGTI_TEST_PROFILE_NO_KEY", "")
	store := NewEnvironmentTokens("PRONOGTI_TEST_PROFILE_NO_KEY")
	profile := Profile{ID: 27, CompanyID: 781898}
	for _, token := range []string{"", "embedded space", "newline\nsecret", strings.Repeat("x", 8193)} {
		if _, err := store.Encrypt(profile, token); !errors.Is(err, ErrInvalidPaymentToken) {
			t.Error("invalid token accepted")
		}
	}
	if _, err := store.Encrypt(profile, "fixture.token"); err == nil {
		t.Error("encryption accepted a missing key")
	}
}

func TestConfiguredTokensReadsExistingCredentials(t *testing.T) {
	const (
		keyEnv = "PRONOGTI_TEST_CONFIGURED_KEY"
		token  = "stored.fixture.token"
	)
	key := bytes.Repeat([]byte{53}, 32)
	hexKey := hex.EncodeToString(key)
	otherKey := hex.EncodeToString(make([]byte, 32))
	t.Setenv(keyEnv, hexKey)
	legacy := NewEnvironmentTokens(keyEnv)
	profile := Profile{ID: 27, CompanyID: 781898}
	ciphertext, err := legacy.Encrypt(profile, token)
	if err != nil {
		t.Fatal(err)
	}
	profile.JWTCiphertext = ciphertext

	for _, tc := range []struct {
		name   string
		key    string
		envKey string
	}{
		{name: "configured hex takes precedence", key: hexKey, envKey: otherKey},
		{name: "configured base64 ignores malformed env", key: base64.StdEncoding.EncodeToString(key), envKey: "invalid-env-key"},
		{name: "empty configured key uses env", envKey: hexKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(keyEnv, tc.envKey)
			store, err := NewConfiguredTokens(keyEnv, tc.key)
			if err != nil {
				t.Fatal(err)
			}
			got, err := store.Token(context.Background(), profile)
			if err != nil || got != token {
				t.Fatalf("configured store could not resolve existing ciphertext: %v", err)
			}
		})
	}

	t.Setenv(keyEnv, otherKey)
	store, err := NewConfiguredTokens(keyEnv, hexKey)
	if err != nil {
		t.Fatal(err)
	}
	const replacement = "replacement.fixture.token"
	profile.JWTCiphertext, err = store.Encrypt(profile, replacement)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(keyEnv, hexKey)
	got, err := legacy.Token(context.Background(), profile)
	if err != nil || got != replacement {
		t.Fatalf("legacy store could not resolve new ciphertext: %v", err)
	}

	profile.JWTEnv = "PRONOGTI_TEST_CONFIGURED_LEGACY_TOKEN"
	const envToken = "env.fixture.token"
	t.Setenv(profile.JWTEnv, envToken)
	got, err = store.Token(context.Background(), profile)
	if err != nil || got != envToken {
		t.Fatalf("configured store could not resolve a profile JWT reference: %v", err)
	}
}

func TestConfiguredTokensRejectsInvalidKeyWithoutFallingBack(t *testing.T) {
	const keyEnv = "PRONOGTI_TEST_CONFIGURED_KEY"
	t.Setenv(keyEnv, hex.EncodeToString(make([]byte, 32)))
	for _, key := range []string{"invalid.fixture.key", base64.StdEncoding.EncodeToString(make([]byte, 31))} {
		if _, err := NewConfiguredTokens(keyEnv, key); err == nil || strings.Contains(err.Error(), key) {
			t.Error("invalid configured key was accepted or exposed")
		}
	}
}
