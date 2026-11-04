package config

import (
	"net/http"
	"testing"
)

func TestAdministratorConfigurationDefaultsAndSecureInheritance(t *testing.T) {
	cfg := defaultConfig()
	if err := cfg.Admin.Validate(cfg.Session); err != nil {
		t.Fatal(err)
	}
	if !cfg.Admin.Enabled || cfg.Admin.CookieName == cfg.Session.CookieName || cfg.Admin.RedisNamespace == cfg.Session.Redis.Namespace || cfg.Admin.MaxLifeTime != 28800 || cfg.Admin.MaxIdleTime != 1800 {
		t.Fatalf("administrator defaults are not isolated: %+v", cfg.Admin)
	}
	cfg.Session.Secure = true
	if wire := cfg.Admin.WebappConfig(cfg.Session); !wire.Secure || !wire.HTTPOnly || wire.SameSite != http.SameSiteLaxMode {
		t.Fatalf("secure cookie inheritance: %+v", wire)
	}
	value := false
	cfg.Admin.Secure = &value
	if cfg.Admin.WebappConfig(cfg.Session).Secure {
		t.Fatal("explicit local development override was ignored")
	}
}

func TestAdministratorConfigurationRejectsSharedIdentityAndUnsafeOrigins(t *testing.T) {
	for name, change := range map[string]func(*AdminConfig, SessionConfig){
		"customer cookie":            func(a *AdminConfig, s SessionConfig) { a.CookieName = s.CookieName },
		"device cookie":              func(a *AdminConfig, s SessionConfig) { a.CookieName = s.KioskCookieName },
		"shared namespace":           func(a *AdminConfig, s SessionConfig) { a.RedisNamespace = s.Redis.Namespace },
		"namespace glob":             func(a *AdminConfig, _ SessionConfig) { a.RedisNamespace = "admin:*" },
		"unbounded session":          func(a *AdminConfig, _ SessionConfig) { a.MaxLifeTime = 0 },
		"unbounded idle":             func(a *AdminConfig, _ SessionConfig) { a.MaxIdleTime = 0 },
		"idle exceeds lifetime":      func(a *AdminConfig, _ SessionConfig) { a.MaxIdleTime = a.MaxLifeTime + 1 },
		"wildcard origin":            func(a *AdminConfig, _ SessionConfig) { a.AllowedOrigins = []string{"*"} },
		"origin with credentials":    func(a *AdminConfig, _ SessionConfig) { a.AllowedOrigins = []string{"https://secret@example.org"} },
		"origin with path":           func(a *AdminConfig, _ SessionConfig) { a.AllowedOrigins = []string{"https://example.org/admin"} },
		"insecure cross site cookie": func(a *AdminConfig, _ SessionConfig) { a.SameSite = "none"; value := false; a.Secure = &value },
		"invalid cookie path":        func(a *AdminConfig, _ SessionConfig) { a.CookiePath = "/;other=value" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := defaultConfig()
			change(&cfg.Admin, cfg.Session)
			if err := cfg.Admin.Validate(cfg.Session); err == nil {
				t.Fatal("invalid administrator boundary was accepted")
			}
		})
	}
}

func TestAdministratorConfigurationLoadsWithoutChangingCustomerSettings(t *testing.T) {
	path := writeConfig(t, `{"admin":{"cookie_name":"admin_custom","redis_namespace":"admin_custom","max_life_time":3600,"max_idle_time":600,"allowed_origins":["https://admin.example.org"]},`+testCredentials+`}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Admin.CookieName != "admin_custom" || cfg.Admin.MaxLifeTime != 3600 || cfg.Session.CookieName != "_pronogti_s" || cfg.Session.Redis.Namespace != "pronogti" {
		t.Fatalf("independent settings were not preserved: admin=%+v customer=%+v", cfg.Admin, cfg.Session)
	}
}
