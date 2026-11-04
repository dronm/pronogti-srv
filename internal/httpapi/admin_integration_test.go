package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/ds/v4/pgds"
	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/session"
	websession "github.com/dronm/webapp/session"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// Administrator integration tests use fresh databases and explicitly configured
// loopback Redis. They never connect to the application's configured services.
func adminIntegrationDatabase(t *testing.T, beforeAdminMigration func(*pgx.Conn)) (ds.Provider, *pgx.Conn) {
	t.Helper()
	value := os.Getenv("PRONOGTI_TEST_DATABASE_URL")
	if value == "" {
		t.Skip("set PRONOGTI_TEST_DATABASE_URL to an isolated local PostgreSQL server")
	}
	config, err := pgx.ParseConfig(value)
	if err != nil {
		t.Fatal("invalid PRONOGTI_TEST_DATABASE_URL")
	}
	if config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1" {
		t.Fatal("administrator integration tests require loopback PostgreSQL")
	}
	ctx := context.Background()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	name := fmt.Sprintf("pronogti_admin_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP DATABASE "+quoted+" WITH (FORCE)") })
	config.Database = name
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil || len(files) < 10 {
		t.Fatalf("administrator migration inventory: %d, %v", len(files), err)
	}
	for _, file := range files {
		if strings.HasPrefix(filepath.Base(file), "000010_") && beforeAdminMigration != nil {
			beforeAdminMigration(conn)
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Exec(ctx, string(body)); err != nil {
			t.Fatalf("migration %s: %v", filepath.Base(file), err)
		}
	}
	poolURL := url.URL{Scheme: "postgres", User: url.UserPassword(config.User, config.Password), Host: net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port))), Path: "/" + name, RawQuery: "sslmode=disable"}
	provider, err := pgds.New(&pgds.Config{PrimaryConnStr: poolURL.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.(*pgds.Provider).Close() })
	return provider, conn
}

func adminIntegrationRedisURL(t *testing.T) string {
	t.Helper()
	value := os.Getenv("PRONOGTI_TEST_REDIS_URL")
	if value == "" {
		t.Skip("set PRONOGTI_TEST_REDIS_URL to an isolated local Redis protocol server")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "redis" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		t.Fatal("administrator integration tests require a loopback Redis URL")
	}
	return value
}

func adminIntegrationManager(t *testing.T, suffix string) *websession.Manager {
	t.Helper()
	manager, err := websession.NewRedisManager(websession.Config{CookieName: "test_admin", HTTPOnly: true, MaxLifeTime: 3600, MaxIdleTime: 600}, websession.RedisConfig{
		Connect: adminIntegrationRedisURL(t), Namespace: fmt.Sprintf("pronogti-admin-test-%s-%d", suffix, time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Core().CloseProvider() })
	return manager
}

func adminIntegrationSession(t *testing.T, manager *websession.Manager) session.Session {
	t.Helper()
	sess, err := manager.Core().SessionStart("")
	if err != nil {
		t.Fatal(err)
	}
	if err = sess.Put("time_created", time.Now()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Core().SessionDestroy(sess.SessionID()) })
	return sess
}

func adminIntegrationHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(hash)
}

func TestAdminIntegrationLegacyMigrationAndDurableSession(t *testing.T) {
	adminIntegrationRedisURL(t)
	ctx := context.Background()
	provider, conn := adminIntegrationDatabase(t, func(conn *pgx.Conn) {
		_, err := conn.Exec(ctx, `INSERT INTO public.users(name,role_id,pwd,locale_id) VALUES('Legacy Administrator','admin','0123456789abcdef0123456789abcdef','ru')`)
		if err != nil {
			t.Fatal(err)
		}
	})
	var legacyPassword string
	var passwordHash *string
	if err := conn.QueryRow(ctx, `SELECT pwd,password_hash FROM public.users WHERE name='Legacy Administrator'`).Scan(&legacyPassword, &passwordHash); err != nil || legacyPassword != "0123456789abcdef0123456789abcdef" || passwordHash != nil {
		t.Fatalf("legacy account was rewritten: %v hash=%v pwd=%q", err, passwordHash, legacyPassword)
	}
	repository := adminauth.NewPostgresRepository(provider)
	manager := adminIntegrationManager(t, "durable")
	service := adminauth.NewService(repository, adminauth.Options{MaxLifeTime: time.Hour, BcryptCost: bcrypt.MinCost})
	sess := adminIntegrationSession(t, manager)
	if _, err := service.Login(ctx, sess, adminauth.Credentials{Name: "Legacy Administrator", Pwd: legacyPassword}, adminauth.RequestInfo{}); err == nil {
		t.Fatal("legacy password was accepted as administrator credentials")
	}
	const password = "administrator-integration-password"
	user, err := repository.Provision(ctx, "Operator", adminIntegrationHash(t, password), false)
	if err != nil {
		t.Fatal(err)
	}
	response, err := service.Login(ctx, sess, adminauth.Credentials{Name: "  oPeRaToR  ", Pwd: password}, adminauth.RequestInfo{IP: "[2001:db8:1:2::abcd]:54321", UserAgent: "Integration browser"})
	if err != nil || response.User == nil || response.Auth == nil || response.User.ID != user.ID {
		t.Fatalf("administrator login: %+v %v", response, err)
	}
	if response.Auth.Token == sess.SessionID() || response.Auth.Token == "" {
		t.Fatal("the public nonce exposed the session credential")
	}
	var recordedIP, recordedSession string
	var headers string
	if err = conn.QueryRow(ctx, `SELECT ip,session_id,headers::text FROM public.logins WHERE user_id=$1 AND auth_kind='admin' AND date_time_out IS NULL`, user.ID).Scan(&recordedIP, &recordedSession, &headers); err != nil {
		t.Fatal(err)
	}
	if recordedIP != "2001:db8:1:2::abcd" || recordedSession == sess.SessionID() || len(recordedSession) != 64 || headers != "{}" {
		t.Fatalf("unsafe or invalid login evidence: IP=%q sessionLength=%d headers=%q", recordedIP, len(recordedSession), headers)
	}
	// A fresh service and Redis session handle must restore the persisted identity.
	restored, err := manager.Core().SessionStart(sess.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	service = adminauth.NewService(repository, adminauth.Options{MaxLifeTime: time.Hour, BcryptCost: bcrypt.MinCost})
	current, err := service.Current(ctx, restored)
	if err != nil || current.User == nil || current.User.ID != user.ID || current.Auth == nil || current.Auth.Token != response.Auth.Token {
		t.Fatalf("restore administrator: %+v %v", current, err)
	}
	if err = service.Logout(ctx, restored, "incorrect-nonce"); err == nil {
		t.Fatal("logout accepted an incorrect nonce")
	}
	if err = service.Logout(ctx, restored, response.Auth.Token); err != nil {
		t.Fatal(err)
	}
	current, err = service.Current(ctx, restored)
	if err != nil || current.Auth != nil || current.User != nil {
		t.Fatalf("logged out session restored: %+v %v", current, err)
	}
	var open int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM public.logins WHERE user_id=$1 AND auth_kind='admin' AND date_time_out IS NULL`, user.ID).Scan(&open); err != nil || open != 0 {
		t.Fatalf("login evidence remains open after logout: %d %v", open, err)
	}
}

func TestAdminIntegrationRevocationAndRoleIsolation(t *testing.T) {
	adminIntegrationRedisURL(t)
	ctx := context.Background()
	provider, conn := adminIntegrationDatabase(t, nil)
	repository := adminauth.NewPostgresRepository(provider)
	manager := adminIntegrationManager(t, "revocation")
	service := adminauth.NewService(repository, adminauth.Options{MaxLifeTime: time.Hour, BcryptCost: bcrypt.MinCost})
	const password = "administrator-integration-password"
	hash := adminIntegrationHash(t, password)
	user, err := repository.Provision(ctx, "Operator", hash, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `INSERT INTO public.users(name,role_id,password_hash,banned,locale_id) VALUES('Customer','client',$1,false,'ru'),('Blocked','admin',$1,true,'ru')`, hash); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Customer", "Blocked", "Missing"} {
		sess := adminIntegrationSession(t, manager)
		if _, err = service.Login(ctx, sess, adminauth.Credentials{Name: name, Pwd: password}, adminauth.RequestInfo{}); err == nil {
			t.Fatalf("non-admin/blocked/missing account %q logged in", name)
		}
	}
	if _, err = repository.Provision(ctx, "Customer", hash, true); err == nil {
		t.Fatal("password reset promoted a customer account")
	}
	if _, err = repository.Provision(ctx, "Blocked", adminIntegrationHash(t, "blocked-administrator-replacement"), true); err != nil {
		t.Fatal(err)
	}
	var stillBanned bool
	if err = conn.QueryRow(ctx, `SELECT banned FROM public.users WHERE name='Blocked'`).Scan(&stillBanned); err != nil || !stillBanned {
		t.Fatalf("password reset unexpectedly unblocked administrator: banned=%v %v", stillBanned, err)
	}
	for _, mutation := range []string{
		`UPDATE public.users SET banned=true WHERE id=$1`,
		`UPDATE public.users SET role_id='client' WHERE id=$1`,
		`UPDATE public.users SET password_hash=$2 WHERE id=$1`,
		`UPDATE public.users SET session_version=session_version+1 WHERE id=$1`,
	} {
		if _, err = conn.Exec(ctx, `UPDATE public.users SET banned=false,role_id='admin',password_hash=$2 WHERE id=$1`, user.ID, hash); err != nil {
			t.Fatal(err)
		}
		sess := adminIntegrationSession(t, manager)
		response, err := service.Login(ctx, sess, adminauth.Credentials{Name: user.Name, Pwd: password}, adminauth.RequestInfo{})
		if err != nil || response.Auth == nil {
			t.Fatal(err)
		}
		args := []any{user.ID}
		if strings.Contains(mutation, "$2") {
			args = append(args, adminIntegrationHash(t, "replacement-integration-password"))
		}
		if _, err = conn.Exec(ctx, mutation, args...); err != nil {
			t.Fatal(err)
		}
		current, err := service.Current(ctx, sess)
		if err != nil || current.User != nil || current.Auth != nil {
			t.Fatalf("revoked session survived %q: %+v %v", mutation, current, err)
		}
		if _, err = service.Require(ctx, sess, response.Auth.Token); err == nil {
			t.Fatal("revoked session authorized a protected operation")
		}
	}
}

func TestAdminIntegrationIndependentRedisNamespacesAndExpiry(t *testing.T) {
	adminIntegrationRedisURL(t)
	ctx := context.Background()
	provider, _ := adminIntegrationDatabase(t, nil)
	repository := adminauth.NewPostgresRepository(provider)
	const password = "administrator-integration-password"
	if _, err := repository.Provision(ctx, "Operator", adminIntegrationHash(t, password), false); err != nil {
		t.Fatal(err)
	}
	publicManager := adminIntegrationManager(t, "public")
	publicSession := adminIntegrationSession(t, publicManager)
	if err := publicSession.Set("customer", "retained-customer-context"); err != nil {
		t.Fatal(err)
	}
	adminManager := adminIntegrationManager(t, "administrator")
	adminSession := adminIntegrationSession(t, adminManager)
	service := adminauth.NewService(repository, adminauth.Options{MaxLifeTime: 30 * time.Millisecond, BcryptCost: bcrypt.MinCost})
	response, err := service.Login(ctx, adminSession, adminauth.Credentials{Name: "Operator", Pwd: password}, adminauth.RequestInfo{})
	if err != nil || response.Auth == nil {
		t.Fatal(err)
	}
	// Even deliberately copying the same SID into the other namespace does not
	// copy its administrator identity, and starting the admin manager must not
	// redirect the first manager's existing SessionStore into a new namespace.
	publicSameSID, err := publicManager.Core().SessionStart(adminSession.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = publicManager.Core().SessionDestroy(publicSameSID.SessionID()) })
	if result, err := service.Current(ctx, publicSameSID); err != nil || result.Auth != nil || result.User != nil {
		t.Fatalf("public namespace recovered administrator identity: %+v %v", result, err)
	}
	var retained string
	if err := publicSession.Get("customer", &retained); err != nil || retained != "retained-customer-context" {
		t.Fatalf("admin startup changed public session provider: %q %v", retained, err)
	}
	time.Sleep(40 * time.Millisecond)
	if result, err := service.Current(ctx, adminSession); err != nil || result.Auth != nil || result.User != nil {
		t.Fatalf("expired administrator identity restored: %+v %v", result, err)
	}
	if err := publicSession.Get("customer", &retained); err != nil || retained != "retained-customer-context" {
		t.Fatal("administrator expiry removed customer context")
	}
}

func adminIntegrationHTTP(t *testing.T, handler http.Handler, method, path, body, origin, nonce string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "[2001:db8:1:2::abcd]:54321"
	r.Header.Set("User-Agent", "Administrator integration browser")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if nonce != "" {
		r.Header.Set(adminauth.SessionHeader, nonce)
	}
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func adminIntegrationCookie(t *testing.T, response *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	var found *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == name {
			found = cookie
		}
	}
	if found == nil {
		t.Fatalf("administrator response has no %s cookie: %d %s", name, response.Code, response.Body)
	}
	return found
}

func adminIntegrationResponse(t *testing.T, response *httptest.ResponseRecorder) adminauth.Response {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("administrator response: %d %s", response.Code, response.Body)
	}
	var value adminauth.Response
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestAdminIntegrationHTTPRotationOriginsAndCookieIsolation(t *testing.T) {
	adminIntegrationRedisURL(t)
	ctx := context.Background()
	provider, conn := adminIntegrationDatabase(t, nil)
	repository := adminauth.NewPostgresRepository(provider)
	const password = "administrator-integration-password"
	hash := adminIntegrationHash(t, password)
	if _, err := repository.Provision(ctx, "Operator", hash, false); err != nil {
		t.Fatal(err)
	}
	manager := adminIntegrationManager(t, "http")
	const cookieName = "test_admin"
	cfg := websession.Config{CookieName: cookieName, HTTPOnly: true, MaxLifeTime: 3600, MaxIdleTime: 600, SameSite: http.SameSiteLaxMode}
	service := adminauth.NewService(repository, adminauth.Options{MaxLifeTime: time.Hour, BcryptCost: bcrypt.MinCost})
	sessions := NewAdminSessions(manager.Core(), cfg, "/", service.Invalidate)
	adminHandler := NewAdminHandler(service, sessions, AdminHTTPOptions{AllowedOrigins: []string{"http://admin.local"}, RequestTimeout: 5 * time.Second})
	publicCalls := 0
	handler := WithAdministrator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicCalls++
		w.WriteHeader(http.StatusNoContent)
	}), adminHandler)
	anonymous := adminIntegrationHTTP(t, handler, http.MethodGet, "/admin/session", "", "", "")
	if value := adminIntegrationResponse(t, anonymous); value.User != nil || value.Auth != nil {
		t.Fatal("anonymous browser received administrator identity")
	}
	beforeLogin := adminIntegrationCookie(t, anonymous, cookieName)
	login := adminIntegrationHTTP(t, handler, http.MethodPost, "/admin/login", `{"name":"Operator","pwd":"`+password+`"}`, "http://admin.local", "", beforeLogin)
	value := adminIntegrationResponse(t, login)
	if value.User == nil || value.Auth == nil || value.Auth.Token == "" {
		t.Fatalf("missing administrator identity: %+v", value)
	}
	logged := adminIntegrationCookie(t, login, cookieName)
	if logged.Value == beforeLogin.Value || logged.Value == value.Auth.Token || !logged.HttpOnly || logged.Path != "/" || logged.SameSite != http.SameSiteLaxMode || logged.MaxAge <= 0 {
		t.Fatalf("unsafe or unrotated administrator cookie: %+v", logged)
	}
	for _, secret := range []string{password, hash, logged.Value, "password_hash", "session_hash"} {
		if strings.Contains(login.Body.String(), secret) {
			t.Fatal("administrator login response exposed a credential")
		}
	}
	if login.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("administrator credentials response was cacheable")
	}
	if replay := adminIntegrationResponse(t, adminIntegrationHTTP(t, handler, http.MethodGet, "/admin/session", "", "", "", beforeLogin)); replay.User != nil || replay.Auth != nil {
		t.Fatal("pre-login SID authenticated after rotation")
	}
	if res := adminIntegrationHTTP(t, handler, http.MethodGet, "/admin/permissions", "", "", "", logged); res.Code != http.StatusOK {
		t.Fatalf("authenticated permissions: %d %s", res.Code, res.Body)
	}
	if res := adminIntegrationHTTP(t, handler, http.MethodGet, "/admin/permissions", "", "", ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous permissions: %d %s", res.Code, res.Body)
	}
	if res := adminIntegrationHTTP(t, handler, http.MethodPost, "/admin/logout", `{}`, "http://admin.local", "wrong-nonce", logged); res.Code != http.StatusForbidden {
		t.Fatalf("logout without matching nonce: %d %s", res.Code, res.Body)
	}
	if res := adminIntegrationHTTP(t, handler, http.MethodPost, "/admin/login", `{"name":"Operator","pwd":"`+password+`"}`, "https://attacker.invalid", "", logged); res.Code != http.StatusForbidden {
		t.Fatalf("cross-origin login: %d %s", res.Code, res.Body)
	}
	if restored := adminIntegrationResponse(t, adminIntegrationHTTP(t, handler, http.MethodGet, "/admin/session", "", "", "", logged)); restored.User == nil || restored.Auth == nil || restored.Auth.Token != value.Auth.Token {
		t.Fatal("rejected request invalidated a legitimate administrator")
	}
	if res := adminIntegrationHTTP(t, handler, http.MethodGet, "/admin/unknown", "", "", "", logged); res.Code != http.StatusNotFound || publicCalls != 0 {
		t.Fatal("unknown administrator route reached public middleware")
	}
	// Cookie names do not let the public browser supply an administrator SID.
	publicCookie := &http.Cookie{Name: "public_customer_session", Value: logged.Value}
	if swapped := adminIntegrationResponse(t, adminIntegrationHTTP(t, handler, http.MethodGet, "/admin/session", "", "", "", publicCookie)); swapped.Auth != nil || swapped.User != nil {
		t.Fatal("a public cookie authenticated as administrator")
	}
	publicManager := adminIntegrationManager(t, "http-public")
	publicSession, err := publicManager.Core().SessionStart("a" + strings.Repeat("1", 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = publicManager.Core().SessionDestroy(publicSession.SessionID()) })
	if err = publicSession.Set("customer", "retained public context"); err != nil {
		t.Fatal(err)
	}
	if swapped := adminIntegrationResponse(t, adminIntegrationHTTP(t, handler, http.MethodGet, "/admin/session", "", "", "", &http.Cookie{Name: cookieName, Value: publicSession.SessionID()})); swapped.Auth != nil || swapped.User != nil {
		t.Fatal("a customer SID copied into the admin cookie authenticated")
	}
	logout := adminIntegrationHTTP(t, handler, http.MethodPost, "/admin/logout", `{}`, "http://admin.local", value.Auth.Token, logged)
	if logout.Code != http.StatusOK || adminIntegrationCookie(t, logout, cookieName).MaxAge >= 0 {
		t.Fatalf("logout did not clear administrator cookie: %d %s", logout.Code, logout.Body)
	}
	if replay := adminIntegrationResponse(t, adminIntegrationHTTP(t, handler, http.MethodGet, "/admin/session", "", "", "", logged)); replay.Auth != nil || replay.User != nil {
		t.Fatal("logged out SID restored administrator identity")
	}
	var retained string
	if err = publicSession.Get("customer", &retained); err != nil || retained != "retained public context" {
		t.Fatal("administrator logout affected the public customer context")
	}
	var open int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM public.logins WHERE auth_kind='admin' AND date_time_out IS NULL`).Scan(&open); err != nil || open != 0 {
		t.Fatalf("HTTP logout left an open administrator login: %d %v", open, err)
	}
}

func TestAdminIntegrationHTTPIdleExpiryClosesLogin(t *testing.T) {
	adminIntegrationRedisURL(t)
	ctx := context.Background()
	provider, conn := adminIntegrationDatabase(t, nil)
	repository := adminauth.NewPostgresRepository(provider)
	const password = "administrator-integration-password"
	if _, err := repository.Provision(ctx, "Operator", adminIntegrationHash(t, password), false); err != nil {
		t.Fatal(err)
	}
	manager := adminIntegrationManager(t, "http-expiry")
	cfg := websession.Config{CookieName: "test_admin", HTTPOnly: true, MaxLifeTime: 3600, MaxIdleTime: 600, SameSite: http.SameSiteLaxMode}
	service := adminauth.NewService(repository, adminauth.Options{MaxLifeTime: time.Hour, BcryptCost: bcrypt.MinCost})
	sessions := NewAdminSessions(manager.Core(), cfg, "/", service.Invalidate)
	handler := NewAdminHandler(service, sessions, AdminHTTPOptions{AllowedOrigins: []string{"http://admin.local"}, RequestTimeout: 5 * time.Second})
	login := adminIntegrationHTTP(t, handler, http.MethodPost, "/admin/login", `{"name":"Operator","pwd":"`+password+`"}`, "http://admin.local", "")
	_ = adminIntegrationResponse(t, login)
	cookie := adminIntegrationCookie(t, login, cfg.CookieName)
	stored, err := manager.Core().SessionStart(cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if err = stored.Set("time_accessed", time.Now().Add(-11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	current := adminIntegrationResponse(t, adminIntegrationHTTP(t, handler, http.MethodGet, "/admin/session", "", "", "", cookie))
	if current.User != nil || current.Auth != nil {
		t.Fatal("idle administrator session restored")
	}
	if replacement := adminIntegrationCookie(t, adminIntegrationHTTP(t, handler, http.MethodGet, "/admin/session", "", "", "", cookie), cfg.CookieName); replacement.Value == cookie.Value {
		t.Fatal("expired administrator SID was reused")
	}
	var open int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM public.logins WHERE auth_kind='admin' AND date_time_out IS NULL`).Scan(&open); err != nil || open != 0 {
		t.Fatalf("idle expiry did not close login evidence: %d %v", open, err)
	}
}
