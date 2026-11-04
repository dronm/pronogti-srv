package adminauth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type memorySession struct {
	id       string
	values   map[string][]byte
	flushErr error
}

func newSession(id string) *memorySession { return &memorySession{id: id, values: map[string][]byte{}} }
func (s *memorySession) Set(key string, value any) error {
	encoded, err := json.Marshal(value)
	s.values[key] = encoded
	return err
}
func (s *memorySession) Put(key string, value any) error {
	if err := s.Set(key, value); err != nil {
		return err
	}
	return s.Flush()
}
func (s *memorySession) Get(key string, value any) error {
	encoded, ok := s.values[key]
	if !ok {
		return errors.New("missing")
	}
	return json.Unmarshal(encoded, value)
}
func (s *memorySession) Delete(key string) error     { delete(s.values, key); return nil }
func (s *memorySession) SessionID() string           { return s.id }
func (s *memorySession) Flush() error                { return s.flushErr }
func (s *memorySession) TimeCreated() time.Time      { return time.Time{} }
func (s *memorySession) TimeAccessed() time.Time     { return time.Time{} }
func (s *memorySession) GetBool(key string) bool     { var v bool; _ = s.Get(key, &v); return v }
func (s *memorySession) GetString(key string) string { var v string; _ = s.Get(key, &v); return v }
func (s *memorySession) GetInt(key string) int64     { var v int64; _ = s.Get(key, &v); return v }
func (s *memorySession) GetFloat(key string) float64 { var v float64; _ = s.Get(key, &v); return v }

type memoryRepository struct {
	account     Account
	open        bool
	openCalls   int
	closeCalls  int
	sessionHash string
	readErr     error
	openErr     error
}

func (r *memoryRepository) FindByName(_ context.Context, name string) (Account, error) {
	if r.readErr != nil {
		return Account{}, r.readErr
	}
	if !strings.EqualFold(name, r.account.Name) {
		return Account{}, ErrNotFound
	}
	return r.account, nil
}
func (r *memoryRepository) OpenLogin(_ context.Context, account Account, hash string, _ RequestInfo) (int64, error) {
	if r.openErr != nil {
		return 0, r.openErr
	}
	r.open = true
	r.openCalls++
	r.sessionHash = hash
	return 7, nil
}
func (r *memoryRepository) CurrentAccount(_ context.Context, userID, loginID int64, hash string) (Account, error) {
	if r.readErr != nil {
		return Account{}, r.readErr
	}
	if !r.open || userID != r.account.ID || loginID != 7 || hash != r.sessionHash {
		return Account{}, ErrNotFound
	}
	return r.account, nil
}
func (r *memoryRepository) CloseLogin(_ context.Context, loginID, userID int64, hash string) error {
	r.closeCalls++
	if loginID == 7 && userID == r.account.ID && hash == r.sessionHash {
		r.open = false
	}
	return nil
}

func setup(t *testing.T) (*Service, *memoryRepository, *memorySession) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("proper password 123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	repo := &memoryRepository{account: Account{User: User{ID: 4, Name: "admin", RoleID: "admin", LocaleID: "ru"}, PasswordHash: string(hash), SessionVersion: 1}}
	svc := NewService(repo, Options{MaxLifeTime: time.Hour, BcryptCost: bcrypt.MinCost})
	svc.now = func() time.Time { return time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC) }
	return svc, repo, newSession("fresh-admin-session")
}

func login(t *testing.T, svc *Service, sess *memorySession) Response {
	t.Helper()
	response, err := svc.Login(context.Background(), sess, Credentials{Name: "admin", Pwd: "proper password 123"}, RequestInfo{IP: "[2001:db8::1]:4321", UserAgent: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestLoginUsesBcryptAndDoesNotExposeCredentials(t *testing.T) {
	svc, repo, sess := setup(t)
	response := login(t, svc, sess)
	if response.Auth == nil || response.User == nil || response.User.ID != 4 || len(response.Auth.Token) < 40 {
		t.Fatalf("response=%+v", response)
	}
	if repo.sessionHash == sess.id || len(repo.sessionHash) != 64 {
		t.Fatal("audit stores raw session credential")
	}
	encoded, _ := json.Marshal(response)
	for _, secret := range []string{repo.account.PasswordHash, "proper password 123", sess.id, "session_version", "login_id"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("response leaked %s", secret)
		}
	}
	current, err := svc.Current(context.Background(), sess)
	if err != nil || current.Auth.Token != response.Auth.Token {
		t.Fatalf("restore=%+v %v", current, err)
	}
}

func TestRejectedCredentialsCannotOpenAudit(t *testing.T) {
	for _, kind := range []string{"wrong-password", "unknown-user", "banned", "customer", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			svc, repo, sess := setup(t)
			credentials := Credentials{Name: "admin", Pwd: "proper password 123"}
			switch kind {
			case "wrong-password":
				credentials.Pwd = "wrong"
			case "unknown-user":
				credentials.Name = "unknown"
			case "banned":
				repo.account.Banned = true
			case "customer":
				repo.account.RoleID = "client"
			case "legacy":
				repo.account.PasswordHash = ""
			}
			_, err := svc.Login(context.Background(), sess, credentials, RequestInfo{})
			if err == nil || repo.openCalls != 0 || len(sess.values) != 0 {
				t.Fatal("unauthorized account acquired a session")
			}
		})
	}
}

func TestNonceRequiredAndLogoutRevokesStaleSessionCopies(t *testing.T) {
	svc, repo, sess := setup(t)
	response := login(t, svc, sess)
	stale := newSession(sess.id)
	stale.values[SessionKey] = append([]byte(nil), sess.values[SessionKey]...)
	if _, err := svc.Require(context.Background(), sess, "wrong"); err == nil {
		t.Fatal("accepted wrong nonce")
	}
	if err := svc.Logout(context.Background(), sess, "wrong"); err == nil || !repo.open {
		t.Fatal("invalid logout revoked session")
	}
	if _, err := svc.Require(context.Background(), sess, response.Auth.Token); err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(context.Background(), sess, response.Auth.Token); err != nil || repo.open {
		t.Fatalf("logout=%v open=%v", err, repo.open)
	}
	if current, err := svc.Current(context.Background(), stale); err != nil || current.Auth != nil || current.User != nil {
		t.Fatalf("stale session survived logout: %+v %v", current, err)
	}
}

func TestBannedRoleChangedResetAndExpiredSessionsFailClosed(t *testing.T) {
	for _, kind := range []string{"banned", "role", "password-reset", "closed-login", "expired", "different-session"} {
		t.Run(kind, func(t *testing.T) {
			svc, repo, sess := setup(t)
			login(t, svc, sess)
			switch kind {
			case "banned":
				repo.account.Banned = true
			case "role":
				repo.account.RoleID = "client"
			case "password-reset":
				repo.account.SessionVersion++
			case "closed-login":
				repo.open = false
			case "expired":
				svc.now = func() time.Time { return time.Date(2026, 11, 4, 11, 0, 0, 0, time.UTC) }
			case "different-session":
				sess.id = "another-session"
			}
			current, err := svc.Current(context.Background(), sess)
			if err != nil || current.Auth != nil || current.User != nil {
				t.Fatalf("revoked session restored: %+v %v", current, err)
			}
		})
	}
}

func TestDatabaseFailureDoesNotBecomeAuthenticatedOrAnonymousSuccess(t *testing.T) {
	svc, repo, sess := setup(t)
	login(t, svc, sess)
	repo.readErr = errors.New("database unavailable")
	if _, err := svc.Current(context.Background(), sess); err == nil {
		t.Fatal("database failure was hidden")
	}
	if _, err := svc.Require(context.Background(), sess, "nonce"); err == nil {
		t.Fatal("database failure granted access")
	}
}

func TestSessionWriteFailureClosesSuccessfulAudit(t *testing.T) {
	svc, repo, sess := setup(t)
	sess.flushErr = errors.New("redis failed")
	if _, err := svc.Login(context.Background(), sess, Credentials{Name: "admin", Pwd: "proper password 123"}, RequestInfo{}); err == nil || repo.open || repo.closeCalls == 0 {
		t.Fatal("failed session write left active login")
	}
}

func TestPasswordHashBoundariesAndIPv6(t *testing.T) {
	for _, password := range []string{"short", strings.Repeat("x", 73), string([]byte{0xff, 0xfe})} {
		if _, err := HashPassword(password); err == nil {
			t.Fatal("accepted invalid password")
		}
	}
	for input, want := range map[string]string{"[2001:db8::1]:443": "2001:db8::1", "127.0.0.1:5440": "127.0.0.1", "2001:db8::2": "2001:db8::2", "not an IP": ""} {
		if got := normalizedIP(input); got != want {
			t.Fatalf("%q -> %q, want %q", input, got, want)
		}
	}
}
