package config

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dronm/webapp"
)

const testCredentials = `"yClients":{"login":"test","password":"test","partnerToken":"test"}`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadLegacyConfigAndDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, "\xef\xbb\xbf"+`{"httpServer":":59000",`+testCredentials+`}`))
	if err != nil {
		t.Fatal(err)
	}
	cacheTTL, err := cfg.CacheTTLDuration()
	if err != nil {
		t.Fatal(err)
	}
	requestTimeout, err := cfg.RequestTimeout()
	if err != nil {
		t.Fatal(err)
	}
	clientTimeout, err := cfg.ClientTimeout()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != ":59000" || cacheTTL != 3*time.Hour || requestTimeout != 30*time.Second {
		t.Fatalf("unexpected legacy defaults: addr=%q cache=%s timeout=%s", cfg.HTTP.Addr, cacheTTL, requestTimeout)
	}
	if clientTimeout != 10*time.Second || cfg.Debug.LogLevel != "info" || cfg.Debug.LogFormat != "text" {
		t.Fatal("missing client/logger defaults")
	}
	if cfg.HTTP.WriteTimeout != "35s" {
		t.Fatalf("write timeout should exceed request timeout: %s", cfg.HTTP.WriteTimeout)
	}
}

func TestCanonicalFieldsOverrideLegacySettings(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{
		"httpServer":":59000",
		"middlewareTimeout":60000,
		"logLevel":"debug",
		"allowedOrigins":["http://old.example.org"],
		"http":{"addr":"127.0.0.1:59100","request_timeout":"2s"},
		"debug":{"log_level":"warn","log_format":"json"},
		"cors":{"allowed_origins":["https://app.example.org"]},
		"cache":{"ttl":"20m"},
		"client_timeout":"8s",
		"shutdown_timeout":"12s",
		`+testCredentials+`
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != "127.0.0.1:59100" || cfg.HTTP.RequestTimeout != "2s" || cfg.HTTP.WriteTimeout != "7s" {
		t.Fatalf("nested HTTP configuration did not take precedence: %#v", cfg.HTTP)
	}
	if cfg.Debug.LogLevel != "warn" || cfg.Debug.LogFormat != "json" || cfg.Cache.TTL != "20m" || cfg.ClientTimeoutValue != "8s" || cfg.ShutdownTimeout != "12s" {
		t.Fatal("nested settings were lost")
	}
	if !reflect.DeepEqual(cfg.CORS.AllowedOrigins, []string{"https://app.example.org"}) {
		t.Fatalf("CORS origins = %v", cfg.CORS.AllowedOrigins)
	}
}

func TestConfiguredCORSUsesLibraryMiddleware(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{
		"cors":{
			"allowed_origins":["https://app.example.org"],
			"allowed_methods":["GET","POST"],
			"allowed_headers":["Content-Type","X-Custom"],
			"exposed_headers":["X-Reply"],
			"allow_credentials":false,
			"max_age_seconds":120
		},
		`+testCredentials+`
	}`))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler := webapp.CORSMiddleware(cfg.CORS.WebappConfig())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	request := httptest.NewRequest(http.MethodOptions, "/companies", nil)
	request.Header.Set("Origin", "https://app.example.org")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || called {
		t.Fatal("library did not handle preflight before the endpoint")
	}
	for header, expected := range map[string]string{
		"Access-Control-Allow-Origin":      "https://app.example.org",
		"Access-Control-Allow-Methods":     "GET, POST, DELETE",
		"Access-Control-Allow-Headers":     "Content-Type, X-Custom, X-Customer-Session, X-Salon-Session, Idempotency-Key",
		"Access-Control-Expose-Headers":    "X-Reply",
		"Access-Control-Allow-Credentials": "",
		"Access-Control-Max-Age":           "120",
	} {
		if value := response.Header().Get(header); value != expected {
			t.Errorf("%s = %q, expected %q", header, value, expected)
		}
	}
	request.Header.Set("Origin", "https://unlisted.example.org")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unconfigured origin was allowed")
	}
}

func TestSessionConfigurationUsesLibraryAndKioskDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{`+testCredentials+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Session.Enabled || cfg.Session.CookieName != "_pronogti_s" || cfg.Session.Redis.Namespace != "pronogti" || cfg.Session.CustomerMaxLifeTime != 1800 || cfg.Session.CustomerMaxIdleTime != 180 {
		t.Fatalf("session defaults are incomplete: %+v", cfg.Session)
	}
	wire := cfg.Session.WebappConfig()
	if !wire.HTTPOnly || wire.SameSite != http.SameSiteLaxMode || wire.MaxLifeTime != 2592000 {
		t.Fatalf("library configuration=%+v", wire)
	}
	if cfg.Database.PrimaryEnv != "PRONOGTI_DATABASE_URL" {
		t.Fatal("database secret reference missing")
	}
	deviceCookie := cfg.Session.KioskCookieConfig()
	if deviceCookie.Name != "_pronogti_kiosk" || deviceCookie.MaxAge != 34560000 || deviceCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("durable kiosk defaults=%+v", deviceCookie)
	}
}

func TestSessionConfigurationRejectsInvalidActiveValues(t *testing.T) {
	for _, session := range []string{
		`{"cookie_name":"bad cookie"}`,
		`{"kiosk_cookie_name":"bad cookie"}`,
		`{"kiosk_cookie_name":"_pronogti_s"}`,
		`{"kiosk_cookie_max_age":0}`,
		`{"kiosk_cookie_max_age":34560001}`,
		`{"same_site":"unknown"}`,
		`{"same_site":"none","secure":false}`,
		`{"max_life_time":0}`,
		`{"max_idle_time":-1}`,
		`{"customer_max_life_time":0}`,
		`{"customer_max_idle_time":2000,"customer_max_life_time":1000}`,
		`{"redis":{"namespace":"other:*"}}`,
		`{"destroy_all_time":"25:99"}`,
	} {
		if _, err := Load(writeConfig(t, `{"session":`+session+`,`+testCredentials+`}`)); err == nil {
			t.Errorf("accepted %s", session)
		}
	}
}

func TestResolvedSecretsDoNotLeakIntoPersistedConfiguration(t *testing.T) {
	t.Setenv("TEST_PRONOGTI_DB", "postgres://test:secret@127.0.0.1/pronogti")
	t.Setenv("TEST_PRONOGTI_REDIS", "redis://:secret@127.0.0.1:6379/4")
	path := writeConfig(t, `{"database":{"primary_env":"TEST_PRONOGTI_DB"},"session":{"redis":{"connect_env":"TEST_PRONOGTI_REDIS"}},`+testCredentials+`}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := cfg.Database.Resolve()
	if err != nil || db.GetPrimary() != os.Getenv("TEST_PRONOGTI_DB") {
		t.Fatalf("resolve database: %v", err)
	}
	redis, err := cfg.Session.Redis.Resolve()
	if err != nil || redis.Connect != os.Getenv("TEST_PRONOGTI_REDIS") {
		t.Fatalf("resolve Redis: %v", err)
	}
	if err := cfg.SaveYClientsToken("refreshed", time.Now()); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "test:secret") || strings.Contains(string(body), ":secret@") {
		t.Fatal("resolved environment secret was serialized")
	}
	if !strings.Contains(string(body), "TEST_PRONOGTI_DB") || !strings.Contains(string(body), "TEST_PRONOGTI_REDIS") {
		t.Fatal("secret references were discarded")
	}
	restored, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Session.CustomerMaxIdleTime != 180 || restored.Session.Redis.Namespace != "pronogti" {
		t.Fatal("session settings lost during token refresh")
	}
}

func TestSecretResolutionFailsClearlyWithoutEnvironment(t *testing.T) {
	t.Setenv("TEST_PRONOGTI_MISSING", "")
	if _, err := (DatabaseConfig{PrimaryEnv: "TEST_PRONOGTI_MISSING"}).Resolve(); err == nil {
		t.Fatal("accepted empty database environment")
	}
	if _, err := (RedisConfig{ConnectEnv: "TEST_PRONOGTI_MISSING"}).Resolve(); err == nil {
		t.Fatal("accepted empty Redis environment")
	}
	if _, err := (RedisConfig{Connect: "http://127.0.0.1", Namespace: "test"}).Resolve(); err == nil {
		t.Fatal("accepted non-Redis URL")
	}
	if _, err := (DatabaseConfig{Primary: "postgres://localhost/test", PrimaryEnv: "TEST_PRONOGTI_MISSING"}).Resolve(); err != nil {
		t.Fatal("explicit database primary should override default environment reference")
	}
}

func TestCanonicalOriginsOverrideInvalidLegacyURL(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{
		"baseURL":"obsolete-relative-path",
		"cors":{"allowed_origins":["https://app.example.org"]},
		`+testCredentials+`
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.CORS.AllowedOrigins, []string{"https://app.example.org"}) {
		t.Fatal("explicit canonical CORS origins were not retained")
	}
}

func TestLoadRejectsInvalidActiveSettings(t *testing.T) {
	for _, body := range []string{
		`{"http":{"addr":":59000"},"yClients":{}}`,
		`{"http":{"addr":"bad"},` + testCredentials + `}`,
		`{"middlewareTimeout":-1,` + testCredentials + `}`,
		`{"http":{"read_timeout":"wrong"},` + testCredentials + `}`,
		`{"cache":{"ttl":"-1s"},` + testCredentials + `}`,
		`{"client_timeout":"0s",` + testCredentials + `}`,
		`{"debug":{"log_level":"invalid"},` + testCredentials + `}`,
		`{"debug":{"log_format":"invalid"},` + testCredentials + `}`,
	} {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Fatal("invalid configuration was accepted")
		}
	}
}

func TestLegacyTokenPersistenceMigratesToCanonicalConfig(t *testing.T) {
	path := writeConfig(t, `{
		"httpServer":":59000",
		"baseURL":"http://localhost/pronogti",
		"middlewareTimeout":60000,
		"cacheTime":10800000000000,
		"appShutdownTimeout":12,
		"clientQueryTimeout":8000,
		"logLevel":"note",
		`+testCredentials+`
	}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := cfg.SaveYClientsToken("refreshed", time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatalf("configuration was corrupted: %v", err)
	}
	if _, legacy := stored["httpServer"]; legacy || stored["http"] == nil || stored["cors"] == nil {
		t.Fatal("token persistence did not write the nested configuration")
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.YClients.UserToken != "refreshed" || reloaded.YClients.AuthedAt == nil || reloaded.YClients.PartnerToken != "test" {
		t.Fatal("authentication state was not preserved")
	}
	if reloaded.HTTP.Addr != ":59000" || reloaded.HTTP.RequestTimeout != "1m0s" || reloaded.Cache.TTL != "3h0m0s" || reloaded.ClientTimeoutValue != "8s" || reloaded.ShutdownTimeout != "12s" {
		t.Fatal("legacy active settings were lost")
	}
	if !reflect.DeepEqual(reloaded.CORS.AllowedOrigins, []string{"http://localhost"}) || reloaded.Debug.LogLevel != "info" {
		t.Fatal("legacy origin/logger conversion was lost")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token file permissions: %o", info.Mode().Perm())
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".pronogti-config-*"))
	if err != nil || len(matches) != 0 {
		t.Fatal("temporary configuration files were left behind")
	}
}
