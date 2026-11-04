package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/session"
	"github.com/dronm/webapp"
	websession "github.com/dronm/webapp/session"
	"github.com/redis/go-redis/v9"
)

type adminTestStore struct {
	*memorySession
	id string
}

func (s *adminTestStore) SessionID() string { return s.id }
func (s *adminTestStore) Get(key string, value any) error {
	if err := s.memorySession.Get(key, value); err != nil {
		return redis.Nil
	}
	return nil
}

type adminTestManager struct {
	stores    map[string]*adminTestStore
	started   []string
	destroyed []string
}

func (m *adminTestManager) SessionStart(id string) (session.Session, error) {
	m.started = append(m.started, id)
	if m.stores == nil {
		m.stores = map[string]*adminTestStore{}
	}
	if m.stores[id] == nil {
		m.stores[id] = &adminTestStore{memorySession: newMemorySession(), id: id}
	}
	return m.stores[id], nil
}
func (m *adminTestManager) SessionDestroy(id string) error {
	m.destroyed = append(m.destroyed, id)
	if store := m.stores[id]; store != nil {
		store.values = map[string][]byte{}
	}
	delete(m.stores, id)
	return nil
}

type adminTestService struct {
	loginCalls    int
	invalidations int
}

func (s *adminTestService) Login(_ context.Context, sess session.Session, c adminauth.Credentials, _ adminauth.RequestInfo) (adminauth.Response, error) {
	s.loginCalls++
	if c.Name != "admin" || c.Pwd != "password" {
		return adminauth.Response{}, webapp.Unauthorized("invalid credentials", nil)
	}
	_ = sess.Set("test_admin", true)
	return adminTestResponse(), nil
}
func (s *adminTestService) Current(_ context.Context, sess session.Session) (adminauth.Response, error) {
	var active bool
	if sess == nil || sess.Get("test_admin", &active) != nil || !active {
		return adminauth.Response{}, nil
	}
	return adminTestResponse(), nil
}
func (s *adminTestService) Logout(ctx context.Context, sess session.Session, nonce string) error {
	value, _ := s.Current(ctx, sess)
	if value.Auth != nil && nonce != value.Auth.Token {
		return webapp.Forbidden("administrator session nonce is required", nil)
	}
	return sess.Delete("test_admin")
}
func (s *adminTestService) Invalidate(_ context.Context, sess session.Session) error {
	s.invalidations++
	return sess.Delete("test_admin")
}
func adminTestResponse() adminauth.Response {
	return adminauth.Response{Auth: &adminauth.Auth{Token: strings.Repeat("a", 64)}, User: &adminauth.User{ID: 1, Name: "Admin", RoleID: "admin", LocaleID: "ru"}}
}
func adminTestHandler() (http.Handler, *adminTestManager, *adminTestService) {
	manager, service := &adminTestManager{}, &adminTestService{}
	sessions := NewAdminSessions(manager, websession.Config{CookieName: "admin_test", HTTPOnly: true, SameSite: http.SameSiteLaxMode, MaxLifeTime: 3600, MaxIdleTime: 600}, "/", service.Invalidate)
	return NewAdminHandler(service, sessions, AdminHTTPOptions{AllowedOrigins: []string{"http://localhost:5174"}, RequestTimeout: time.Second}), manager, service
}
func adminRequest(handler http.Handler, method, path, body string, cookie *http.Cookie, nonce string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://localhost:5174")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if nonce != "" {
		r.Header.Set(adminauth.SessionHeader, nonce)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestAdministratorHTTPRotatesCookieAndNeverAuthenticatesReturnFlags(t *testing.T) {
	h, manager, service := adminTestHandler()
	anonymous := adminRequest(h, http.MethodGet, "/admin/session", "", nil, "")
	old := anonymous.Result().Cookies()[0]
	response := adminRequest(h, http.MethodPost, "/admin/login", `{"name":"admin","pwd":"password"}`, old, "")
	if response.Code != 200 {
		t.Fatalf("login: %d %s", response.Code, response.Body)
	}
	cookie := response.Result().Cookies()[0]
	if old.Value == cookie.Value || !adminSessionIDPattern.MatchString(cookie.Value) || !cookie.HttpOnly || len(manager.destroyed) != 1 || service.invalidations != 1 || strings.Contains(response.Body.String(), cookie.Value) {
		t.Fatalf("session was not securely rotated: old=%s new=%s destroyed=%v", old.Value, cookie.Value, manager.destroyed)
	}
	if got := adminRequest(h, http.MethodGet, "/admin/main-menu", "", cookie, ""); got.Code != 200 {
		t.Fatalf("authenticated shell read: %d %s", got.Code, got.Body)
	}
	replay := adminRequest(h, http.MethodGet, "/admin/session?success=true&role=admin", "", old, "")
	var value adminauth.Response
	_ = json.Unmarshal(replay.Body.Bytes(), &value)
	if replay.Code != 200 || value.Auth != nil || value.User != nil {
		t.Fatalf("old cookie/query flags restored identity: %d %s", replay.Code, replay.Body)
	}
	if got := adminRequest(h, http.MethodPost, "/admin/logout", `{}`, cookie, ""); got.Code != 403 {
		t.Fatalf("logout accepted missing nonce: %d", got.Code)
	}
	logout := adminRequest(h, http.MethodPost, "/admin/logout", `{}`, cookie, strings.Repeat("a", 64))
	if logout.Code != 200 || logout.Result().Cookies()[len(logout.Result().Cookies())-1].MaxAge != -1 {
		t.Fatalf("logout did not clear cookie: %d %s", logout.Code, logout.Body)
	}
	if got := adminRequest(h, http.MethodGet, "/admin/main-menu", "", cookie, ""); got.Code != 401 {
		t.Fatalf("logged-out cookie regained access: %d", got.Code)
	}
}

func TestAdministratorHTTPRejectsInvalidRequestsBeforeCredentials(t *testing.T) {
	h, manager, service := adminTestHandler()
	for _, body := range []string{"null", "[]", "", `{"name":"admin","pwd":"password","role_id":"admin"}`, `{"name":"admin","pwd":"password"}{}`, strings.Repeat("x", 5000)} {
		if got := adminRequest(h, http.MethodPost, "/admin/login", body, nil, ""); got.Code != 400 {
			t.Fatalf("accepted invalid request: %d %s", got.Code, got.Body)
		}
	}
	if service.loginCalls != 0 {
		t.Fatal("invalid JSON reached credential verification")
	}
	before := len(manager.started)
	r := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(`{"name":"admin","pwd":"password"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://attacker.invalid")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || len(manager.started) != before {
		t.Fatalf("foreign origin created a session: %d", w.Code)
	}
	if got := adminRequest(h, http.MethodGet, "/admin/prog-about", "", nil, ""); got.Code != 401 {
		t.Fatalf("anonymous shell metadata: %d", got.Code)
	}
}

func TestAdministratorNamespaceDoesNotUsePublicResolverOrWildcardIDs(t *testing.T) {
	h, manager, _ := adminTestHandler()
	publicCalls := 0
	combined := WithAdministrator(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { publicCalls++; w.WriteHeader(204) }), h)
	for _, id := range []string{"*", "a*", "a" + strings.Repeat("b", 31) + "?", "fd6a7a34-d216-4f93-92d5-69ece7cdf99f"} {
		before := len(manager.started)
		got := adminRequest(combined, http.MethodGet, "/admin/session", "", &http.Cookie{Name: "admin_test", Value: id}, "")
		if got.Code != 200 {
			t.Fatal(got.Body)
		}
		if len(manager.started) != before+1 || manager.started[len(manager.started)-1] == id || !adminSessionIDPattern.MatchString(manager.started[len(manager.started)-1]) {
			t.Fatal("untrusted supplied ID reached the session provider")
		}
	}
	if got := adminRequest(combined, http.MethodGet, "/admin/unknown", "", nil, ""); got.Code != 404 || publicCalls != 0 {
		t.Fatalf("admin namespace fell into customer stack: %d calls=%d", got.Code, publicCalls)
	}
	if got := adminRequest(combined, http.MethodGet, "/session", "", nil, ""); got.Code != 204 || publicCalls != 1 {
		t.Fatal("public routing changed")
	}
}

func TestAdministratorLoginLimiterIsBoundedAndExpires(t *testing.T) {
	limiter := newAdminLoginLimiter()
	now := time.Now()
	limiter.now = func() time.Time { return now }
	calls := 0
	h := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(204) }))
	for i := 0; i < 16; i++ {
		got := adminRequest(h, http.MethodPost, "/admin/login", `{}`, nil, "")
		expected := 204
		if i == 15 {
			expected = 429
		}
		if got.Code != expected {
			t.Fatalf("attempt %d status=%d", i, got.Code)
		}
	}
	now = now.Add(time.Minute)
	if got := adminRequest(h, http.MethodPost, "/admin/login", `{}`, nil, ""); got.Code != 204 || calls != 16 {
		t.Fatal("login rate limit did not expire")
	}
}
