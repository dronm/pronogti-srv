package payment

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"regexp"
	"strconv"
)

type EnvironmentTokens struct{ KeyEnv string }

func NewEnvironmentTokens(keyEnv string) *EnvironmentTokens {
	return &EnvironmentTokens{KeyEnv: keyEnv}
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Token resolves an explicit per-profile environment reference first. Existing
// ciphertext uses version 1 || 12-byte nonce || AES-256-GCM ciphertext/tag, with
// the decimal company/profile identity as additional authenticated data.
func (t *EnvironmentTokens) Token(ctx context.Context, profile Profile) (string, error) {
	if ctx == nil {
		return "", errors.New("payment credential context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if profile.JWTEnv != "" {
		if !environmentName.MatchString(profile.JWTEnv) {
			return "", errors.New("invalid payment credential reference")
		}
		value, ok := os.LookupEnv(profile.JWTEnv)
		if !ok || !validToken(value) {
			return "", errors.New("payment credential is unavailable")
		}
		return value, nil
	}
	if t == nil || !environmentName.MatchString(t.KeyEnv) {
		return "", errors.New("payment encryption key reference is unavailable")
	}
	value, ok := os.LookupEnv(t.KeyEnv)
	if !ok {
		return "", errors.New("payment encryption key is unavailable")
	}
	key, err := hex.DecodeString(value)
	if err != nil || len(key) != 32 {
		key, err = base64.StdEncoding.DecodeString(value)
	}
	if err != nil || len(key) != 32 {
		return "", errors.New("payment encryption key must contain 32 bytes encoded as hex or base64")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", errors.New("invalid payment encryption key")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", errors.New("payment cipher is unavailable")
	}
	data := profile.JWTCiphertext
	if len(data) < 1+aead.NonceSize()+aead.Overhead() || data[0] != 1 {
		return "", errors.New("payment ciphertext is invalid")
	}
	plaintext, err := aead.Open(nil, data[1:1+aead.NonceSize()], data[1+aead.NonceSize():], tokenAAD(profile))
	if err != nil || !validToken(string(plaintext)) {
		return "", errors.New("payment ciphertext could not be authenticated")
	}
	return string(plaintext), nil
}

func validToken(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func tokenAAD(profile Profile) []byte {
	return []byte("pronogti:tochka:" + strconv.FormatInt(profile.CompanyID, 10) + ":" + strconv.FormatInt(profile.ID, 10))
}
