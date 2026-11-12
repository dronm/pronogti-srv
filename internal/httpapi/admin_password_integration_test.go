package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dronm/pronogti/internal/adminauth"
	websession "github.com/dronm/webapp/session"
	"golang.org/x/crypto/bcrypt"
)

func TestAdminIntegrationProfilePasswordChange(t *testing.T) {
	adminIntegrationRedisURL(t)
	ctx := context.Background()
	provider, conn := adminIntegrationDatabase(t, nil)
	repository := adminauth.NewPostgresRepository(provider)
	const oldPassword = "administrator-integration-password"
	const newPassword = "abc123"
	if _, err := repository.Provision(ctx, "Operator", adminIntegrationHash(t, oldPassword), false); err != nil {
		t.Fatal(err)
	}
	manager := adminIntegrationManager(t, "profile-password")
	const cookieName = "test_admin"
	const origin = "http://admin.local"
	const changeRoute = "/admin/profile/password"
	cfg := websession.Config{CookieName: cookieName, MaxLifeTime: 3600, MaxIdleTime: 600}
	service := adminauth.NewService(repository, adminauth.Options{MaxLifeTime: time.Hour, BcryptCost: bcrypt.MinCost})
	sessions := NewAdminSessions(manager.Core(), cfg, "/", service.Invalidate)
	handler := NewAdminHandler(service, sessions, AdminHTTPOptions{AllowedOrigins: []string{origin}})
	login := func(password string) *httptest.ResponseRecorder {
		return adminIntegrationHTTP(t, handler, http.MethodPost, "/admin/login", `{"name":"Operator","pwd":"`+password+`"}`, origin, "")
	}
	first, second := login(oldPassword), login(oldPassword)
	firstIdentity := adminIntegrationResponse(t, first)
	secondIdentity := adminIntegrationResponse(t, second)
	if firstIdentity.Auth == nil || firstIdentity.User == nil || secondIdentity.Auth == nil {
		t.Fatal("both sessions must be authenticated before the password change")
	}
	firstCookie := adminIntegrationCookie(t, first, cookieName)
	secondCookie := adminIntegrationCookie(t, second, cookieName)
	const change = `{"new_password":"` + newPassword + `","confirm_password":"` + newPassword + `"}`
	if res := adminIntegrationHTTP(t, handler, http.MethodPost, changeRoute, change, origin, ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous password change: %d %s", res.Code, res.Body)
	}
	if res := adminIntegrationHTTP(t, handler, http.MethodPost, changeRoute, change, origin, secondIdentity.Auth.Token, firstCookie); res.Code != http.StatusForbidden {
		t.Fatalf("password change with another session's nonce: %d %s", res.Code, res.Body)
	}
	for _, body := range []string{
		`{"new_password":"abcdef","confirm_password":"abc123"}`,
		`{"new_password":"абвгд","confirm_password":"абвгд"}`,
		`{"new_password":"abcdef","confirm_password":"abcdef","name":"Another admin"}`,
	} {
		res := adminIntegrationHTTP(t, handler, http.MethodPost, changeRoute, body, origin, firstIdentity.Auth.Token, firstCookie)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("invalid password change: %d %s", res.Code, res.Body)
		}
	}
	changed := adminIntegrationHTTP(t, handler, http.MethodPost, changeRoute, change, origin, firstIdentity.Auth.Token, firstCookie)
	value := adminIntegrationResponse(t, changed)
	if value.Auth != nil || value.User != nil || adminIntegrationCookie(t, changed, cookieName).MaxAge >= 0 {
		t.Fatal("password change did not clear the initiating session")
	}
	var open int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM public.logins WHERE auth_kind='admin' AND date_time_out IS NULL`).Scan(&open); err != nil || open != 0 {
		t.Fatalf("password change left an open administrator login: %d %v", open, err)
	}
	for _, cookie := range []*http.Cookie{firstCookie, secondCookie} {
		res := adminIntegrationHTTP(t, handler, http.MethodGet, "/admin/permissions", "", "", "", cookie)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("pre-change session still authorized: %d %s", res.Code, res.Body)
		}
	}
	if res := login(oldPassword); res.Code != http.StatusUnauthorized {
		t.Fatalf("previous password still authorized: %d %s", res.Code, res.Body)
	}
	if value := adminIntegrationResponse(t, login(newPassword)); value.Auth == nil || value.User == nil || value.User.ID != firstIdentity.User.ID {
		t.Fatal("the new password did not authenticate the original account")
	}
}

func TestAdminIntegrationPasswordChangeRejectsConcurrentUpdate(t *testing.T) {
	ctx := context.Background()
	provider, _ := adminIntegrationDatabase(t, nil)
	repository := adminauth.NewPostgresRepository(provider)
	if _, err := repository.Provision(ctx, "Operator", adminIntegrationHash(t, "old-password"), false); err != nil {
		t.Fatal(err)
	}
	expected, err := repository.FindByName(ctx, "Operator")
	if err != nil {
		t.Fatal(err)
	}
	openLogin := func(hash string) int64 {
		t.Helper()
		id, err := repository.OpenLogin(ctx, expected, hash, adminauth.RequestInfo{})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	closedHash := strings.Repeat("c", 64)
	closedID := openLogin(closedHash)
	if err := repository.CloseLogin(ctx, closedID, expected.ID, closedHash); err != nil {
		t.Fatal(err)
	}
	if err := repository.ChangePassword(ctx, expected, closedID, closedHash, adminIntegrationHash(t, "closed-login-password")); !errors.Is(err, adminauth.ErrCredentials) {
		t.Fatalf("closed session password change: %v", err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, value := range []string{"a", "b"} {
		sessionHash := strings.Repeat(value, 64)
		loginID := openLogin(sessionHash)
		passwordHash := adminIntegrationHash(t, "new-password-"+value)
		go func() {
			<-start
			results <- repository.ChangePassword(ctx, expected, loginID, sessionHash, passwordHash)
		}()
	}
	close(start)
	first, second := <-results, <-results
	if !(first == nil && errors.Is(second, adminauth.ErrCredentials) || second == nil && errors.Is(first, adminauth.ErrCredentials)) {
		t.Fatalf("concurrent password changes: %v and %v", first, second)
	}
	current, err := repository.FindByName(ctx, "Operator")
	if err != nil || current.SessionVersion != expected.SessionVersion+1 {
		t.Fatalf("password changes did not increment the session version once: %d %v", current.SessionVersion, err)
	}
}
