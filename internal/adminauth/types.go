// Package adminauth authenticates administrators independently of public and
// kiosk customer identities, using the application's existing session interface.
package adminauth

import (
	"context"
	"errors"
	"time"
)

const (
	SessionKey    = "pronogti_admin_identity"
	SessionHeader = "X-Admin-Session"
)

var (
	ErrNotFound    = errors.New("administrator not found")
	ErrCredentials = errors.New("administrator credentials changed")
)

type Credentials struct {
	Name string `json:"name"`
	Pwd  string `json:"pwd"`
}

type PasswordChange struct {
	NewPassword     string `json:"new_password"`
	ConfirmPassword string `json:"confirm_password"`
}

type User struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	RoleID   string `json:"role_id"`
	LocaleID string `json:"locale_id"`
}

// Auth.Token is only a CSRF/interaction nonce. The actual session credential is
// carried by the separate HttpOnly administrator cookie.
type Auth struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Response struct {
	Auth *Auth `json:"auth"`
	User *User `json:"user"`
}

type Account struct {
	User
	PasswordHash   string
	SessionVersion int64
	Banned         bool
}

type RequestInfo struct {
	IP        string
	UserAgent string
}

type Repository interface {
	FindByName(context.Context, string) (Account, error)
	OpenLogin(context.Context, Account, string, RequestInfo) (int64, error)
	CurrentAccount(context.Context, int64, int64, string) (Account, error)
	CloseLogin(context.Context, int64, int64, string) error
	ChangePassword(ctx context.Context, expected Account, loginID int64, sessionHash, passwordHash string) error
}

type Options struct {
	MaxLifeTime time.Duration
	BcryptCost  int
}
