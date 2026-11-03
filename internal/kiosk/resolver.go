package kiosk

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	core "github.com/dronm/session"
	redisprovider "github.com/dronm/session/redis"
	"github.com/dronm/webapp"
	websession "github.com/dronm/webapp/session"
	"github.com/redis/go-redis/v9"
)

const sessionCreatedKey = "pronogti_session_created"

var sessionIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type resolverBackend struct {
	start   func(string) (core.Session, error)
	destroy func(string) error
	resolve webapp.SessionResolver
}

// NewSessionResolver uses the webapp Redis manager, while ensuring that only
// server-issued session IDs are accepted. The pinned session package also
// supports arbitrary supplied IDs, so this adapter generates crypto-random IDs
// and checks a server-side creation marker before using a cookie.
// A durable kiosk credential is handled separately by NewDeviceSessionResolver;
// ordinary Redis session expiry therefore does not revoke the device registration.
func NewSessionResolver(manager *websession.Manager, cfg websession.Config) webapp.SessionResolver {
	if manager == nil || manager.Core() == nil {
		return func(http.ResponseWriter, *http.Request) (core.Session, error) {
			return nil, fmt.Errorf("session manager is not configured")
		}
	}
	return newSessionResolver(resolverBackend{
		start:   manager.Core().SessionStart,
		destroy: manager.Core().SessionDestroy,
		resolve: manager.Resolver,
	}, cfg)
}

func newSessionResolver(backend resolverBackend, cfg websession.Config) webapp.SessionResolver {
	cookieName := cfg.CookieName
	if cookieName == "" {
		cookieName = websession.DefaultCookieName
	}
	return func(w http.ResponseWriter, r *http.Request) (core.Session, error) {
		w.Header().Set("Cache-Control", "no-store")
		var sid string
		if cookie, err := r.Cookie(cookieName); err == nil && sessionIDPattern.MatchString(cookie.Value) {
			sid = cookie.Value
		}
		var stored core.Session
		if sid != "" {
			var err error
			stored, err = backend.start(sid)
			if err != nil {
				return nil, fmt.Errorf("read session: %w", err)
			}
			// Read the prior access time first: reads in the Redis provider update it.
			var accessed, created time.Time
			accessErr := stored.Get("time_accessed", &accessed)
			if accessErr != nil && !missingSessionValue(accessErr) {
				return nil, fmt.Errorf("read session access time: %w", accessErr)
			}
			if err := stored.Get(sessionCreatedKey, &created); err != nil {
				if !missingSessionValue(err) {
					return nil, fmt.Errorf("read session marker: %w", err)
				}
				sid = ""
			} else {
				now := time.Now()
				expired := created.IsZero() || created.After(now.Add(time.Minute))
				expired = expired || cfg.MaxLifeTime > 0 && now.Sub(created) >= time.Duration(cfg.MaxLifeTime)*time.Second
				expired = expired || cfg.MaxIdleTime > 0 && (accessed.IsZero() || now.Sub(accessed) >= time.Duration(cfg.MaxIdleTime)*time.Second)
				// Provisioning establishes a new device identity and rotates its ID.
				rotate := r.Method == http.MethodPost && r.URL.Path == "/kiosk/session"
				if expired || rotate {
					if err := backend.destroy(sid); err != nil {
						return nil, fmt.Errorf("destroy expired session: %w", err)
					}
					sid = ""
				}
			}
		}
		if sid == "" {
			var err error
			sid, err = secureSessionID()
			if err != nil {
				return nil, err
			}
			stored, err = backend.start(sid)
			if err != nil {
				return nil, fmt.Errorf("create session: %w", err)
			}
			now := time.Now()
			for _, key := range []string{sessionCreatedKey, "time_created", "time_accessed"} {
				if err := stored.Set(key, now); err != nil {
					return nil, fmt.Errorf("initialize session: %w", err)
				}
			}
			if err := stored.Flush(); err != nil {
				return nil, fmt.Errorf("flush new session: %w", err)
			}
		}

		// Let the library manage its HttpOnly cookie and context-compatible store.
		// Do not let its bearer fallback interpret a kiosk key as a session ID.
		request := r.Clone(r.Context())
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
		for _, cookie := range r.Cookies() {
			if cookie.Name != cookieName {
				request.AddCookie(cookie)
			}
		}
		request.AddCookie(&http.Cookie{Name: cookieName, Value: sid})
		return backend.resolve(w, request)
	}
}

func missingSessionValue(err error) bool {
	return errors.Is(err, redis.Nil) || errors.Is(err, redisprovider.ErrKeyNotFound)
}

func secureSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
