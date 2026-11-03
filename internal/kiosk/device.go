package kiosk

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/dronm/session"
	"github.com/dronm/webapp"
)

const (
	DefaultDeviceCookieName = "_pronogti_kiosk"
	MaxDeviceCookieAge      = 400 * 24 * 60 * 60
)

// DeviceCookieConfig describes the durable credential that can recover a kiosk
// binding after its ordinary framework Redis session is expired or removed.
// The database credential itself has no automatic expiry.
type DeviceCookieConfig struct {
	Name     string
	Domain   string
	MaxAge   int
	Secure   bool
	SameSite http.SameSite
}

func (c DeviceCookieConfig) normalized() DeviceCookieConfig {
	if c.Name == "" {
		c.Name = DefaultDeviceCookieName
	}
	if c.MaxAge <= 0 || c.MaxAge > MaxDeviceCookieAge {
		c.MaxAge = MaxDeviceCookieAge
	}
	if c.SameSite == 0 {
		c.SameSite = http.SameSiteLaxMode
	}
	return c
}

func (c DeviceCookieConfig) set(w http.ResponseWriter, token string) {
	c = c.normalized()
	// Base64 preserves the exact token bytes while using cookie-safe characters.
	// This is an opaque bearer credential, not an encryption mechanism.
	http.SetCookie(w, &http.Cookie{
		Name: c.Name, Value: base64.RawURLEncoding.EncodeToString([]byte(token)),
		Path: "/", Domain: c.Domain, MaxAge: c.MaxAge,
		Expires: time.Now().Add(time.Duration(c.MaxAge) * time.Second),
		Secure:  c.Secure, HttpOnly: true, SameSite: c.SameSite,
	})
}

func (c DeviceCookieConfig) clear(w http.ResponseWriter) {
	c = c.normalized()
	http.SetCookie(w, &http.Cookie{
		Name: c.Name, Value: "", Path: "/", Domain: c.Domain, MaxAge: -1,
		Expires: time.Unix(1, 0), Secure: c.Secure, HttpOnly: true, SameSite: c.SameSite,
	})
}

// NewDeviceSessionResolver wraps the framework session resolver. It validates
// the durable device key independently from the Redis/customer session and only
// restores a binding when the cached kiosk no longer matches that key.
func NewDeviceSessionResolver(base webapp.SessionResolver, manager *Manager, registry Registry, cfg DeviceCookieConfig) webapp.SessionResolver {
	cfg = cfg.normalized()
	return func(w http.ResponseWriter, r *http.Request) (session.Session, error) {
		if base == nil || manager == nil || registry == nil {
			return nil, errors.New("device session resolver is not configured")
		}
		sess, err := base(w, r)
		if err != nil || sess == nil {
			return sess, err
		}
		// A provisioning request establishes its own identity. Its successful
		// response is responsible for replacing the credential cookie.
		if r.Method == http.MethodPost && r.URL.Path == "/kiosk/session" {
			return sess, nil
		}
		cookie, err := r.Cookie(cfg.Name)
		if errors.Is(err, http.ErrNoCookie) {
			return sess, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read device cookie: %w", err)
		}
		token, decodeErr := base64.RawURLEncoding.DecodeString(cookie.Value)
		if decodeErr != nil || len(token) == 0 || len(token) > 512 {
			cfg.clear(w)
			return sess, manager.ClearKiosk(sess)
		}
		device, err := registry.FindByToken(r.Context(), string(token))
		if errors.Is(err, ErrKioskNotFound) || err == nil && (!device.IsActive || device.ID <= 0 || device.CompanyID <= 0) {
			cfg.clear(w)
			return sess, manager.ClearKiosk(sess)
		}
		if err != nil {
			return nil, fmt.Errorf("validate device credential: %w", err)
		}
		var cached Kiosk
		if err := sess.Get(kioskKey, &cached); err != nil || cached.ID != device.ID || cached.CompanyID != device.CompanyID || cached.SessionVersion != device.SessionVersion {
			var selected scopeState
			if err := sess.Get(scopeKey, &selected); err == nil && selected.Mode == "public" {
				// Redis may lose an individual device key while its independent
				// public interaction remains. Validate that context, then refresh
				// only the device cache instead of replacing the public scope.
				if _, err := manager.RequireScope(r.Context(), sess); err != nil {
					return nil, err
				}
				if err := sess.Set(kioskKey, device); err != nil {
					return nil, err
				}
				if err := sess.Flush(); err != nil {
					return nil, err
				}
			} else if _, err := manager.Bind(r.Context(), sess, string(token)); err != nil {
				return nil, err
			}
		}
		cfg.set(w, string(token))
		return sess, nil
	}
}

// DeviceCookieMiddleware records a provisioning key only after the actual
// setup handler successfully authenticates it. Place it after origin checks
// and before SessionMiddleware; the setup JSON body remains available to its
// normal modelbind binder.
func DeviceCookieMiddleware(cfg DeviceCookieConfig) webapp.Middleware {
	cfg = cfg.normalized()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/kiosk/session" {
				next.ServeHTTP(w, r)
				return
			}
			if r.Body == nil {
				r.Body = http.NoBody
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if err != nil {
				webapp.WriteServiceError(w, webapp.BadRequest("invalid kiosk setup request body", nil))
				return
			}
			if r.Body != nil {
				_ = r.Body.Close()
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var input struct {
				Token string `json:"token"`
			}
			// The binder owns all request validation. A malformed body cannot
			// result in a successful setup response and a durable credential.
			_ = json.Unmarshal(body, &input)
			writer := &deviceResponseWriter{
				ResponseWriter: w,
				beforeHeader: func(status int) {
					if status >= 200 && status < 300 && input.Token != "" && len(input.Token) <= 512 {
						cfg.set(w, input.Token)
					}
				},
			}
			next.ServeHTTP(writer, r)
			if !writer.written {
				writer.WriteHeader(http.StatusOK)
			}
		})
	}
}

type deviceResponseWriter struct {
	http.ResponseWriter
	beforeHeader func(int)
	written      bool
}

func (w *deviceResponseWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.written {
		return
	}
	w.written = true
	w.beforeHeader(status)
	w.ResponseWriter.WriteHeader(status)
}

func (w *deviceResponseWriter) Write(body []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *deviceResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
