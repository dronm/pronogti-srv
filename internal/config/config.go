// Package config loads and validates application configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/webapp"
	websession "github.com/dronm/webapp/session"
)

const DefaultConfigPath = "config.json"

type Config struct {
	mu       sync.Mutex
	fileName string

	HTTP               HTTPConfig       `json:"http"`
	Database           DatabaseConfig   `json:"database"`
	Session            SessionConfig    `json:"session"`
	Checkout           CheckoutConfig   `json:"checkout"`
	Fiscal             FiscalConfig     `json:"fiscal"`
	Payments           PaymentsConfig   `json:"payments"`
	Settlement         SettlementConfig `json:"settlement"`
	CORS               CORSConfig       `json:"cors"`
	Debug              DebugConfig      `json:"debug"`
	YClients           YClients         `json:"yClients"`
	Cache              CacheConfig      `json:"cache"`
	ClientTimeoutValue string           `json:"client_timeout"`
	ShutdownTimeout    string           `json:"shutdown_timeout"`
}

type HTTPConfig struct {
	Addr           string `json:"addr"`
	ReadTimeout    string `json:"read_timeout"`
	WriteTimeout   string `json:"write_timeout"`
	IdleTimeout    string `json:"idle_timeout"`
	RequestTimeout string `json:"request_timeout"`
}

// CheckoutConfig controls how long an immutable price quote can be used.
type CheckoutConfig struct {
	MaxLifeTime int64 `json:"max_life_time"`
}

// DatabaseConfig retains environment-variable names rather than resolved secrets.
type DatabaseConfig struct {
	Primary     string            `json:"primary,omitempty"`
	PrimaryEnv  string            `json:"primary_env,omitempty"`
	Secondaries map[string]string `json:"secondaries,omitempty"`
}

type ResolvedDatabase struct {
	primary     string
	secondaries map[string]string
}

func (c ResolvedDatabase) GetPrimary() string                { return c.primary }
func (c ResolvedDatabase) GetSecondaries() map[string]string { return c.secondaries }

func (c DatabaseConfig) Resolve() (ResolvedDatabase, error) {
	primary, err := resolveSecret("database.primary", c.Primary, c.PrimaryEnv)
	if err != nil {
		return ResolvedDatabase{}, err
	}
	if strings.TrimSpace(primary) == "" {
		return ResolvedDatabase{}, errors.New("database.primary or database.primary_env must provide a PostgreSQL connection string")
	}
	secondaries := make(map[string]string, len(c.Secondaries))
	for key, value := range c.Secondaries {
		secondaries[key] = value
	}
	return ResolvedDatabase{primary: primary, secondaries: secondaries}, nil
}

type SessionConfig struct {
	Enabled             bool        `json:"enabled"`
	CookieName          string      `json:"cookie_name"`
	KioskCookieName     string      `json:"kiosk_cookie_name"`
	KioskCookieMaxAge   int         `json:"kiosk_cookie_max_age"`
	CookieDomain        string      `json:"cookie_domain"`
	Secure              bool        `json:"secure"`
	SameSite            string      `json:"same_site"`
	MaxLifeTime         int64       `json:"max_life_time"`
	MaxIdleTime         int64       `json:"max_idle_time"`
	DestroyAllTime      string      `json:"destroy_all_time"`
	CustomerMaxLifeTime int64       `json:"customer_max_life_time"`
	CustomerMaxIdleTime int64       `json:"customer_max_idle_time"`
	Redis               RedisConfig `json:"redis"`
}

type RedisConfig struct {
	Connect    string `json:"connect,omitempty"`
	ConnectEnv string `json:"connect_env,omitempty"`
	Namespace  string `json:"namespace"`
}

func (c SessionConfig) WebappConfig() websession.Config {
	sameSite := http.SameSiteLaxMode
	switch c.SameSite {
	case "strict":
		sameSite = http.SameSiteStrictMode
	case "none":
		sameSite = http.SameSiteNoneMode
	}
	return websession.Config{CookieName: c.CookieName, CookieDomain: c.CookieDomain, Secure: c.Secure, HTTPOnly: true, SameSite: sameSite, MaxLifeTime: c.MaxLifeTime, MaxIdleTime: c.MaxIdleTime, DestroyAllTime: c.DestroyAllTime}
}

func (c SessionConfig) KioskCookieConfig() kiosk.DeviceCookieConfig {
	wire := c.WebappConfig()
	return kiosk.DeviceCookieConfig{
		Name: c.KioskCookieName, MaxAge: c.KioskCookieMaxAge,
		Domain: wire.CookieDomain, Secure: wire.Secure, SameSite: wire.SameSite,
	}
}

func (c RedisConfig) Resolve() (websession.RedisConfig, error) {
	if c.ConnectEnv != "" {
		c.Connect = ""
	}
	connect, err := resolveSecret("session.redis.connect", c.Connect, c.ConnectEnv)
	if err != nil {
		return websession.RedisConfig{}, err
	}
	parsed, err := url.Parse(connect)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "redis" && parsed.Scheme != "rediss") {
		return websession.RedisConfig{}, errors.New("session.redis.connect must be a redis:// or rediss:// URL")
	}
	return websession.RedisConfig{Connect: connect, Namespace: c.Namespace}, nil
}

func resolveSecret(name, value, env string) (string, error) {
	// An explicit value takes precedence over the default environment reference.
	if value != "" {
		return value, nil
	}
	if env == "" {
		return "", nil
	}
	resolved, ok := os.LookupEnv(env)
	if !ok || strings.TrimSpace(resolved) == "" {
		return "", fmt.Errorf("%s requires environment variable %s", name, env)
	}
	return resolved, nil
}

type CORSConfig struct {
	AllowedOrigins   []string `json:"allowed_origins"`
	AllowedMethods   []string `json:"allowed_methods"`
	AllowedHeaders   []string `json:"allowed_headers"`
	ExposedHeaders   []string `json:"exposed_headers"`
	AllowCredentials bool     `json:"allow_credentials"`
	MaxAgeSeconds    int      `json:"max_age_seconds"`
}

type DebugConfig struct {
	LogConfig bool   `json:"log_config"`
	LogLevel  string `json:"log_level"`
	LogFormat string `json:"log_format"`
}

type CacheConfig struct {
	TTL string `json:"ttl"`
}

type YClients struct {
	PartnerToken string     `json:"partnerToken"`
	Login        string     `json:"login"`
	Password     string     `json:"password"`
	UserToken    string     `json:"UserToken,omitempty"`
	AuthedAt     *time.Time `json:"authedAt,omitempty"`
}

// Load supports the nested configuration format and existing flat files.
// Explicit nested fields take precedence over their legacy equivalents.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	cfg := defaultConfig()
	if err := cfg.applyLegacy(body); err != nil {
		return nil, fmt.Errorf("parse legacy config %q: %w", path, err)
	}
	if err := json.Unmarshal(body, cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	if cfg.Session.Enabled {
		cfg.CORS.AllowedHeaders = appendUniqueFold(cfg.CORS.AllowedHeaders, "X-Customer-Session")
		cfg.CORS.AllowedHeaders = appendUniqueFold(cfg.CORS.AllowedHeaders, "X-Salon-Session")
		cfg.CORS.AllowedHeaders = appendUniqueFold(cfg.CORS.AllowedHeaders, "Idempotency-Key")
		cfg.CORS.AllowedMethods = appendUniqueFold(cfg.CORS.AllowedMethods, http.MethodDelete)
	}
	if cfg.Debug.LogLevel == "" || cfg.Debug.LogLevel == "note" {
		cfg.Debug.LogLevel = "info"
	}
	if cfg.HTTP.WriteTimeout == "" {
		requestTimeout, err := cfg.RequestTimeout()
		if err != nil {
			return nil, err
		}
		cfg.HTTP.WriteTimeout = (requestTimeout + 5*time.Second).String()
	}
	cfg.fileName = path
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaultConfig() *Config {
	return &Config{
		Database:   DatabaseConfig{PrimaryEnv: "PRONOGTI_DATABASE_URL"},
		Checkout:   CheckoutConfig{MaxLifeTime: 900},
		Fiscal:     FiscalConfig{WorkerEnabled: true, PollInterval: "2s"},
		Payments:   PaymentsConfig{JWTKeyEnv: "PRONOGTI_PAYMENT_JWT_KEY", CreatingTimeoutSeconds: 120, WorkerEnabled: true, PollInterval: "2s", MockAutoPayAfter: "2s", WebhookPublicKeyEnv: "PRONOGTI_TOCHKA_WEBHOOK_PUBLIC_KEY"},
		Settlement: SettlementConfig{WorkerEnabled: true, PollInterval: "2s"},
		Session: SessionConfig{
			Enabled: true, CookieName: "_pronogti_s", SameSite: "lax",
			KioskCookieName: kiosk.DefaultDeviceCookieName, KioskCookieMaxAge: kiosk.MaxDeviceCookieAge,
			MaxLifeTime: 2592000, MaxIdleTime: 604800,
			CustomerMaxLifeTime: 1800, CustomerMaxIdleTime: 180,
			Redis: RedisConfig{Connect: "redis://127.0.0.1:6379/0", Namespace: "pronogti"},
		},
		HTTP: HTTPConfig{
			Addr:           "127.0.0.1:59000",
			ReadTimeout:    "15s",
			IdleTimeout:    "60s",
			RequestTimeout: "30s",
		},
		CORS: CORSConfig{
			AllowedOrigins: []string{"http://localhost:3000"},
			AllowedMethods: []string{
				http.MethodGet,
				http.MethodPost,
				http.MethodPatch,
				http.MethodPut,
				http.MethodDelete,
				http.MethodOptions,
			},
			AllowedHeaders:   []string{"Content-Type", "Authorization", "X-Customer-Session", "X-Salon-Session", "Idempotency-Key", webapp.DefaultQueryIDHeader},
			ExposedHeaders:   []string{webapp.DefaultQueryIDHeader},
			AllowCredentials: true,
			MaxAgeSeconds:    86400,
		},
		Debug: DebugConfig{
			LogLevel:  "info",
			LogFormat: "text",
		},
		Cache:              CacheConfig{TTL: "3h"},
		ClientTimeoutValue: "10s",
		ShutdownTimeout:    "10s",
	}
}

func (c *Config) applyLegacy(body []byte) error {
	var legacy struct {
		CORS struct {
			AllowedOrigins json.RawMessage `json:"allowed_origins"`
		} `json:"cors"`
		BaseURL            string        `json:"baseURL"`
		AllowedOrigins     []string      `json:"allowedOrigins"`
		HTTPServer         string        `json:"httpServer"`
		MiddlewareTimeout  int           `json:"middlewareTimeout"`
		ClientQueryTimeout int           `json:"clientQueryTimeout"`
		HTTPReadTimeout    string        `json:"httpReadTimeout"`
		HTTPWriteTimeout   string        `json:"httpWriteTimeout"`
		HTTPIdleTimeout    string        `json:"httpIdleTimeout"`
		AppShutdownTimeout int           `json:"appShutdownTimeout"`
		CacheTime          time.Duration `json:"cacheTime"`
		LogLevel           string        `json:"logLevel"`
		LogFormat          string        `json:"logFormat"`
	}
	if err := json.Unmarshal(body, &legacy); err != nil {
		return err
	}
	if legacy.HTTPServer != "" {
		c.HTTP.Addr = legacy.HTTPServer
	}
	if legacy.MiddlewareTimeout != 0 {
		c.HTTP.RequestTimeout = (time.Duration(legacy.MiddlewareTimeout) * time.Millisecond).String()
	}
	if legacy.ClientQueryTimeout != 0 {
		c.ClientTimeoutValue = (time.Duration(legacy.ClientQueryTimeout) * time.Millisecond).String()
	}
	if legacy.AppShutdownTimeout != 0 {
		c.ShutdownTimeout = (time.Duration(legacy.AppShutdownTimeout) * time.Second).String()
	}
	if legacy.CacheTime != 0 {
		c.Cache.TTL = legacy.CacheTime.String()
	}
	if legacy.HTTPReadTimeout != "" {
		c.HTTP.ReadTimeout = legacy.HTTPReadTimeout
	}
	if legacy.HTTPWriteTimeout != "" {
		c.HTTP.WriteTimeout = legacy.HTTPWriteTimeout
	}
	if legacy.HTTPIdleTimeout != "" {
		c.HTTP.IdleTimeout = legacy.HTTPIdleTimeout
	}
	if legacy.LogLevel != "" {
		c.Debug.LogLevel = legacy.LogLevel
	}
	if legacy.LogFormat != "" {
		c.Debug.LogFormat = legacy.LogFormat
	}
	if len(legacy.CORS.AllowedOrigins) > 0 {
		// The canonical origin list supersedes legacy URL settings, including
		// obsolete values that would no longer be valid frontend URLs.
		return nil
	}
	if len(legacy.AllowedOrigins) > 0 {
		c.CORS.AllowedOrigins = legacy.AllowedOrigins
	} else if legacy.BaseURL != "" {
		// Older files store the frontend page URL rather than its origin.
		parsed, err := url.Parse(legacy.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return errors.New("baseURL must be an absolute frontend URL")
		}
		c.CORS.AllowedOrigins = []string{parsed.Scheme + "://" + parsed.Host}
	}
	return nil
}

func (c *Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.HTTP.Addr); err != nil {
		return fmt.Errorf("http.addr must be a host:port address: %w", err)
	}
	for _, item := range []struct {
		name  string
		value string
	}{
		{"http.read_timeout", c.HTTP.ReadTimeout},
		{"http.write_timeout", c.HTTP.WriteTimeout},
		{"http.idle_timeout", c.HTTP.IdleTimeout},
		{"http.request_timeout", c.HTTP.RequestTimeout},
		{"client_timeout", c.ClientTimeoutValue},
		{"shutdown_timeout", c.ShutdownTimeout},
		{"cache.ttl", c.Cache.TTL},
	} {
		duration, err := parseDuration(item.name, item.value)
		if err != nil {
			return err
		}
		if duration <= 0 {
			return fmt.Errorf("%s must be a positive duration", item.name)
		}
	}
	if strings.TrimSpace(c.YClients.Login) == "" || c.YClients.Password == "" || strings.TrimSpace(c.YClients.PartnerToken) == "" {
		return errors.New("yClients login, password and partnerToken are required")
	}
	switch strings.ToLower(c.Debug.LogLevel) {
	case "debug", "info", "warn", "warning", "error":
	default:
		return errors.New("debug.log_level must be debug, info, warn or error")
	}
	if c.Debug.LogFormat != "text" && c.Debug.LogFormat != "json" {
		return errors.New("debug.log_format must be text or json")
	}
	if c.CORS.MaxAgeSeconds < 0 {
		return errors.New("cors.max_age_seconds must not be negative")
	}
	if c.Checkout.MaxLifeTime <= 0 || c.Checkout.MaxLifeTime > 3600 {
		return errors.New("checkout.max_life_time must be between 1 and 3600 seconds")
	}
	if err := c.Payments.Validate(); err != nil {
		return err
	}
	if err := c.Fiscal.Validate(); err != nil {
		return err
	}
	if err := c.Settlement.Validate(); err != nil {
		return err
	}
	if err := c.Session.Validate(); err != nil {
		return err
	}
	return nil
}

func (c SessionConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.CookieName) == "" || strings.ContainsAny(c.CookieName, " \t\r\n;=,") {
		return errors.New("session.cookie_name must be a valid cookie name")
	}
	if strings.TrimSpace(c.KioskCookieName) == "" || strings.ContainsAny(c.KioskCookieName, " \t\r\n;=,") || c.KioskCookieName == c.CookieName {
		return errors.New("session.kiosk_cookie_name must be a valid cookie name different from session.cookie_name")
	}
	if c.KioskCookieMaxAge <= 0 || c.KioskCookieMaxAge > kiosk.MaxDeviceCookieAge {
		return errors.New("session.kiosk_cookie_max_age must be between 1 and 34560000 seconds")
	}
	if c.SameSite != "lax" && c.SameSite != "strict" && c.SameSite != "none" {
		return errors.New("session.same_site must be lax, strict or none")
	}
	if c.SameSite == "none" && !c.Secure {
		return errors.New("session.secure is required for same_site none")
	}
	if c.MaxLifeTime <= 0 || c.MaxIdleTime < 0 || c.MaxIdleTime > c.MaxLifeTime {
		return errors.New("session lifetimes must be positive with max_idle_time no greater than max_life_time")
	}
	if c.CustomerMaxLifeTime <= 0 || c.CustomerMaxIdleTime <= 0 || c.CustomerMaxIdleTime > c.CustomerMaxLifeTime || c.CustomerMaxLifeTime > c.MaxLifeTime {
		return errors.New("customer session lifetimes must be positive, ordered and no longer than the Redis session lifetime")
	}
	if strings.TrimSpace(c.Redis.Namespace) == "" || strings.ContainsAny(c.Redis.Namespace, ":*?[] \t\r\n") {
		return errors.New("session.redis.namespace must be nonempty and contain no Redis glob characters, colon or whitespace")
	}
	if c.Redis.Connect == "" && c.Redis.ConnectEnv == "" {
		return errors.New("session.redis.connect or connect_env is required")
	}
	if c.DestroyAllTime != "" {
		layout := "15:04"
		if len(c.DestroyAllTime) == 8 {
			layout = "15:04:05"
		}
		if _, err := time.Parse(layout, c.DestroyAllTime); err != nil {
			return errors.New("session.destroy_all_time must use HH:MM or HH:MM:SS")
		}
	}
	return nil
}

func (c *Config) HTTPReadTimeout() (time.Duration, error) {
	return parseDuration("http.read_timeout", c.HTTP.ReadTimeout)
}

func (c *Config) HTTPWriteTimeout() (time.Duration, error) {
	return parseDuration("http.write_timeout", c.HTTP.WriteTimeout)
}

func (c *Config) HTTPIdleTimeout() (time.Duration, error) {
	return parseDuration("http.idle_timeout", c.HTTP.IdleTimeout)
}

func (c *Config) RequestTimeout() (time.Duration, error) {
	return parseDuration("http.request_timeout", c.HTTP.RequestTimeout)
}

func (c *Config) ClientTimeout() (time.Duration, error) {
	return parseDuration("client_timeout", c.ClientTimeoutValue)
}

func (c *Config) CacheTTLDuration() (time.Duration, error) {
	return parseDuration("cache.ttl", c.Cache.TTL)
}

func (c *Config) ShutdownTimeoutDuration() (time.Duration, error) {
	return parseDuration("shutdown_timeout", c.ShutdownTimeout)
}

// WebappConfig adapts configuration fields to the library's middleware input.
func (c CORSConfig) WebappConfig() webapp.CORSConfig {
	return webapp.CORSConfig{
		AllowedOrigins:   c.AllowedOrigins,
		AllowedMethods:   c.AllowedMethods,
		AllowedHeaders:   c.AllowedHeaders,
		ExposedHeaders:   c.ExposedHeaders,
		AllowCredentials: c.AllowCredentials,
		MaxAgeSeconds:    c.MaxAgeSeconds,
	}
}

func parseDuration(name, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration: %w", name, err)
	}
	return duration, nil
}

// SaveYClientsToken atomically writes refreshed authentication state to the
// loaded configuration path without requiring a global config object.
func (c *Config) SaveYClientsToken(token string, at time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.YClients.UserToken = token
	c.YClients.AuthedAt = &at
	if c.fileName == "" {
		return errors.New("config file name is not set")
	}
	data, err := json.MarshalIndent(c, "", "\t")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.fileName), ".pronogti-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.fileName)
}

func appendUniqueFold(values []string, value string) []string {
	for _, current := range values {
		if strings.EqualFold(current, value) {
			return values
		}
	}
	return append(values, value)
}
