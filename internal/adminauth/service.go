package adminauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dronm/session"
	"github.com/dronm/webapp"
	"golang.org/x/crypto/bcrypt"
)

const (
	PWD_MIN_LEN = 6
	PWD_MAX_LEN = 72
)

var errInvalidPassword = fmt.Errorf("password must be valid UTF-8, contain at least %d characters, and fit within %d bytes", PWD_MIN_LEN, PWD_MAX_LEN)

type identity struct {
	UserID         int64     `json:"user_id"`
	SessionVersion int64     `json:"session_version"`
	LoginID        int64     `json:"login_id"`
	Nonce          string    `json:"nonce"`
	ExpiresAt      time.Time `json:"expires_at"`
	SessionHash    string    `json:"session_hash"`
}

type Service struct {
	repository Repository
	options    Options
	dummyHash  []byte
	now        func() time.Time
}

func NewService(repository Repository, options Options) *Service {
	if options.MaxLifeTime <= 0 {
		options.MaxLifeTime = 8 * time.Hour
	}
	if options.BcryptCost < bcrypt.MinCost || options.BcryptCost > bcrypt.MaxCost {
		options.BcryptCost = PWD_MIN_LEN
	}
	// This is not an account password. It equalizes the expensive comparison for
	// nonexistent, disabled and non-administrator accounts.
	dummy, _ := bcrypt.GenerateFromPassword([]byte("pronogti-invalid-account-comparison"), options.BcryptCost)
	return &Service{repository: repository, options: options, dummyHash: dummy, now: time.Now}
}

// Login must receive the freshly rotated administrator session supplied by the
// administrator HTTP resolver. Customer and kiosk sessions must never be passed.
func (s *Service) Login(ctx context.Context, sess session.Session, credentials Credentials, info RequestInfo) (Response, error) {
	if err := s.ready(ctx, sess); err != nil {
		return Response{}, err
	}
	name := strings.TrimSpace(credentials.Name)
	if !validName(name) || credentials.Pwd == "" || len(credentials.Pwd) > 72 {
		return Response{}, webapp.BadRequest("name and password are required", nil)
	}
	account, err := s.repository.FindByName(ctx, name)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Response{}, fmt.Errorf("read administrator: %w", err)
	}
	valid := err == nil && validAccount(account)
	hash := s.dummyHash
	if valid {
		hash = []byte(account.PasswordHash)
	}
	passwordErr := bcrypt.CompareHashAndPassword(hash, []byte(credentials.Pwd))
	if !valid || passwordErr != nil {
		return Response{}, invalidCredentials()
	}
	nonce, err := randomNonce()
	if err != nil {
		return Response{}, errors.New("administrator session nonce could not be generated")
	}
	sessionHash := hashSession(sess.SessionID())
	loginID, err := s.repository.OpenLogin(ctx, account, sessionHash, info)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrCredentials) {
		return Response{}, invalidCredentials()
	}
	if err != nil {
		return Response{}, fmt.Errorf("record administrator login: %w", err)
	}
	value := identity{
		UserID: account.ID, SessionVersion: account.SessionVersion, LoginID: loginID, Nonce: nonce,
		ExpiresAt: s.now().UTC().Add(s.options.MaxLifeTime), SessionHash: sessionHash,
	}
	if err = sess.Set(SessionKey, value); err == nil {
		err = sess.Flush()
	}
	if err != nil {
		_ = sess.Delete(SessionKey)
		_ = sess.Flush()
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.repository.CloseLogin(cleanup, loginID, account.ID, sessionHash)
		return Response{}, errors.New("administrator session could not be saved")
	}
	return responseFor(account.User, value), nil
}

func (s *Service) Current(ctx context.Context, sess session.Session) (Response, error) {
	account, value, err := s.current(ctx, sess)
	if errors.Is(err, ErrNotFound) {
		return Response{}, nil
	}
	if err != nil {
		return Response{}, err
	}
	return responseFor(account.User, value), nil
}

func (s *Service) Require(ctx context.Context, sess session.Session, nonce string) (User, error) {
	account, _, err := s.require(ctx, sess, nonce)
	return account.User, err
}

func (s *Service) require(ctx context.Context, sess session.Session, nonce string) (Account, identity, error) {
	account, value, err := s.current(ctx, sess)
	if errors.Is(err, ErrNotFound) {
		return Account{}, identity{}, sessionRequired()
	}
	if err != nil {
		return Account{}, identity{}, err
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(value.Nonce), []byte(nonce)) != 1 {
		return Account{}, identity{}, webapp.Forbidden("administrator session nonce is required", map[string]any{"code": "admin_session_nonce_required"})
	}
	return account, value, nil
}

func (s *Service) ChangePassword(ctx context.Context, sess session.Session, nonce string, change PasswordChange) error {
	account, value, err := s.require(ctx, sess, nonce)
	if err != nil {
		return err
	}
	if change.NewPassword != change.ConfirmPassword {
		return webapp.BadRequest("password confirmation does not match", map[string]any{"code": "admin_password_mismatch"})
	}
	hash, err := HashPassword(change.NewPassword)
	if errors.Is(err, errInvalidPassword) {
		return webapp.BadRequest(err.Error(), map[string]any{"code": "admin_password_invalid"})
	}
	if err != nil {
		return fmt.Errorf("hash administrator password: %w", err)
	}
	if !s.now().Before(value.ExpiresAt) {
		return sessionRequired()
	}
	err = s.repository.ChangePassword(ctx, account, value.LoginID, value.SessionHash, hash)
	if errors.Is(err, ErrCredentials) {
		return sessionRequired()
	}
	if err != nil {
		return fmt.Errorf("change administrator password: %w", err)
	}
	return nil
}

func (s *Service) Logout(ctx context.Context, sess session.Session, nonce string) error {
	if sess == nil {
		return nil
	}
	var value identity
	if err := sess.Get(SessionKey, &value); err != nil || value.UserID == 0 {
		return nil
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(value.Nonce), []byte(nonce)) != 1 {
		return webapp.Forbidden("administrator session nonce is required", map[string]any{"code": "admin_session_nonce_required"})
	}
	return s.Invalidate(ctx, sess)
}

// Invalidate is for trusted server lifecycle operations, including rotating the
// administrator cookie before login. It must not be exposed as an HTTP action.
func (s *Service) Invalidate(ctx context.Context, sess session.Session) error {
	if sess == nil {
		return nil
	}
	var value identity
	var closeErr error
	if err := sess.Get(SessionKey, &value); err == nil && value.UserID > 0 && value.LoginID > 0 && s != nil && s.repository != nil {
		closeErr = s.repository.CloseLogin(ctx, value.LoginID, value.UserID, hashSession(sess.SessionID()))
	}
	deleteErr := sess.Delete(SessionKey)
	flushErr := sess.Flush()
	return errors.Join(closeErr, deleteErr, flushErr)
}

func (s *Service) current(ctx context.Context, sess session.Session) (Account, identity, error) {
	if err := s.ready(ctx, sess); err != nil {
		if sess == nil {
			return Account{}, identity{}, ErrNotFound
		}
		return Account{}, identity{}, err
	}
	var value identity
	if err := sess.Get(SessionKey, &value); err != nil || value.UserID <= 0 || value.LoginID <= 0 || value.Nonce == "" || value.SessionVersion <= 0 || value.SessionHash != hashSession(sess.SessionID()) {
		return Account{}, identity{}, ErrNotFound
	}
	if !s.now().Before(value.ExpiresAt) {
		_ = s.Invalidate(ctx, sess)
		return Account{}, identity{}, ErrNotFound
	}
	account, err := s.repository.CurrentAccount(ctx, value.UserID, value.LoginID, value.SessionHash)
	if errors.Is(err, ErrNotFound) || err == nil && (!validAccount(account) || account.SessionVersion != value.SessionVersion) {
		_ = s.Invalidate(ctx, sess)
		return Account{}, identity{}, ErrNotFound
	}
	if err != nil {
		return Account{}, identity{}, fmt.Errorf("restore administrator session: %w", err)
	}
	return account, value, nil
}

func (s *Service) ready(ctx context.Context, sess session.Session) error {
	if ctx == nil || s == nil || s.repository == nil {
		return errors.New("administrator authentication is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if sess == nil || sess.SessionID() == "" {
		return sessionRequired()
	}
	return nil
}

func validAccount(account Account) bool {
	return account.ID > 0 && account.RoleID == "admin" && !account.Banned && account.SessionVersion > 0 && account.PasswordHash != ""
}

func validName(name string) bool {
	return name != "" && utf8.RuneCountInString(name) <= 50 && strings.IndexFunc(name, unicode.IsControl) < 0
}

func responseFor(user User, value identity) Response {
	return Response{Auth: &Auth{Token: value.Nonce, ExpiresAt: value.ExpiresAt}, User: &user}
}

func randomNonce() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func hashSession(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func invalidCredentials() error {
	return webapp.Unauthorized("invalid administrator credentials", map[string]any{"code": "admin_credentials_invalid"})
}

func sessionRequired() error {
	return webapp.Unauthorized("administrator session required", map[string]any{"code": "admin_session_required"})
}

// HashPassword accepts the full UTF-8 password without trimming it. Bcrypt's
// byte limit is explicit to avoid silently treating different passwords alike.
func HashPassword(password string) (string, error) {
	if len(password) > PWD_MAX_LEN || !utf8.ValidString(password) || utf8.RuneCountInString(password) < PWD_MIN_LEN {
		return "", errInvalidPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), PWD_MIN_LEN)
	return string(hash), err
}
