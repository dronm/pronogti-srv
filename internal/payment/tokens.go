package payment

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"regexp"
	"strconv"
)

type EnvironmentTokens struct {
	KeyEnv           string
	configuredCipher cipher.AEAD
}

func NewEnvironmentTokens(keyEnv string) *EnvironmentTokens {
	return &EnvironmentTokens{KeyEnv: keyEnv}
}

// NewConfiguredTokens validates a key supplied in config before startup.
// An empty key preserves lazy resolution through the environment variable.
func NewConfiguredTokens(keyEnv, key string) (*EnvironmentTokens, error) {
	tokens := NewEnvironmentTokens(keyEnv)
	if key == "" {
		return tokens, nil
	}
	aead, err := newPaymentCipher(key)
	if err != nil {
		return nil, err
	}
	tokens.configuredCipher = aead
	return tokens, nil
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var ErrInvalidPaymentToken = errors.New("invalid payment credential")

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
	aead, err := t.encryptionCipher()
	if err != nil {
		return "", err
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

// Encrypt seals a write-only profile credential using the existing on-disk
// format. The credential is tied to the immutable company and profile IDs.
func (t *EnvironmentTokens) Encrypt(profile Profile, value string) ([]byte, error) {
	if profile.CompanyID <= 0 || profile.ID <= 0 || len(value) > 8192 || !validToken(value) {
		return nil, ErrInvalidPaymentToken
	}
	aead, err := t.encryptionCipher()
	if err != nil {
		return nil, err
	}
	encoded := make([]byte, 1+aead.NonceSize(), 1+aead.NonceSize()+len(value)+aead.Overhead())
	encoded[0] = 1
	if _, err := rand.Read(encoded[1:]); err != nil {
		return nil, errors.New("payment credential nonce is unavailable")
	}
	return aead.Seal(encoded, encoded[1:], []byte(value), tokenAAD(profile)), nil
}

func (t *EnvironmentTokens) encryptionCipher() (cipher.AEAD, error) {
	if t == nil {
		return nil, errors.New("payment encryption key reference is unavailable")
	}
	if t.configuredCipher != nil {
		return t.configuredCipher, nil
	}
	if !environmentName.MatchString(t.KeyEnv) {
		return nil, errors.New("payment encryption key reference is unavailable")
	}
	value, ok := os.LookupEnv(t.KeyEnv)
	if !ok {
		return nil, errors.New("payment encryption key is unavailable")
	}
	return newPaymentCipher(value)
}

func newPaymentCipher(value string) (cipher.AEAD, error) {
	key, err := hex.DecodeString(value)
	if err != nil || len(key) != 32 {
		key, err = base64.StdEncoding.DecodeString(value)
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("payment encryption key must contain 32 bytes encoded as hex or base64")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("invalid payment encryption key")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("payment cipher is unavailable")
	}
	return aead, nil
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
