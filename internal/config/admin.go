package config

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	websession "github.com/dronm/webapp/session"
)

// AdminConfig isolates administrator cookies, Redis data and browser origins.
// The Redis connection is shared with Session.Redis; its namespace is separate.
type AdminConfig struct {
	Enabled        bool     `json:"enabled"`
	CookieName     string   `json:"cookie_name"`
	CookieDomain   string   `json:"cookie_domain"`
	CookiePath     string   `json:"cookie_path"`
	Secure         *bool    `json:"secure,omitempty"`
	SameSite       string   `json:"same_site"`
	MaxLifeTime    int64    `json:"max_life_time"`
	MaxIdleTime    int64    `json:"max_idle_time"`
	RedisNamespace string   `json:"redis_namespace"`
	AllowedOrigins []string `json:"allowed_origins"`
}

func defaultAdminConfig() AdminConfig {
	return AdminConfig{Enabled: true, CookieName: "pronogti_admin_session", CookiePath: "/", SameSite: "lax", MaxLifeTime: 28800, MaxIdleTime: 1800, RedisNamespace: "pronogti_admin", AllowedOrigins: []string{"http://localhost:5174", "http://127.0.0.1:5174"}}
}

func (c AdminConfig) WebappConfig(parent SessionConfig) websession.Config {
	secure := parent.Secure
	if c.Secure != nil {
		secure = *c.Secure
	}
	sameSite := http.SameSiteLaxMode
	if c.SameSite == "strict" {
		sameSite = http.SameSiteStrictMode
	} else if c.SameSite == "none" {
		sameSite = http.SameSiteNoneMode
	}
	return websession.Config{CookieName: c.CookieName, CookieDomain: c.CookieDomain, Secure: secure, HTTPOnly: true, SameSite: sameSite, MaxLifeTime: c.MaxLifeTime, MaxIdleTime: c.MaxIdleTime}
}

func (c AdminConfig) Validate(parent SessionConfig) error {
	if !c.Enabled {
		return nil
	}
	if !parent.Enabled {
		return errors.New("admin requires session.enabled")
	}
	if strings.TrimSpace(c.CookieName) == "" || strings.ContainsAny(c.CookieName, " \t\r\n;=,") || c.CookieName == parent.CookieName || c.CookieName == parent.KioskCookieName {
		return errors.New("admin.cookie_name must be valid and different from customer/device cookies")
	}
	if !strings.HasPrefix(c.CookiePath, "/") || strings.ContainsAny(c.CookiePath, ";\r\n") {
		return errors.New("admin.cookie_path must be an absolute cookie path")
	}
	if c.SameSite != "lax" && c.SameSite != "strict" && c.SameSite != "none" {
		return errors.New("admin.same_site must be lax, strict or none")
	}
	if c.SameSite == "none" && !c.WebappConfig(parent).Secure {
		return errors.New("admin.secure is required for same_site none")
	}
	if c.MaxLifeTime <= 0 || c.MaxLifeTime > 604800 || c.MaxIdleTime <= 0 || c.MaxIdleTime > c.MaxLifeTime {
		return errors.New("admin lifetimes must be positive, ordered and no longer than seven days")
	}
	if strings.TrimSpace(c.RedisNamespace) == "" || strings.ContainsAny(c.RedisNamespace, ":*?[] \t\r\n") || c.RedisNamespace == parent.Redis.Namespace {
		return errors.New("admin.redis_namespace must be valid and different from customer sessions")
	}
	if len(c.AllowedOrigins) == 0 {
		return errors.New("admin.allowed_origins must contain at least one trusted frontend origin")
	}
	for _, value := range c.AllowedOrigins {
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(value, "\r\n") {
			return errors.New("admin.allowed_origins must contain exact HTTP or HTTPS origins")
		}
	}
	return nil
}
