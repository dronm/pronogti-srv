package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/dronm/session"
	redisprovider "github.com/dronm/session/redis"
	websession "github.com/dronm/webapp/session"
	"github.com/redis/go-redis/v9"
)

const adminSessionCreatedKey = "pronogti_admin_session_created"

var adminSessionIDPattern = regexp.MustCompile(`^a[0-9a-f]{32}$`)

type AdminSessionManager interface {
	SessionStart(string) (session.Session, error)
	SessionDestroy(string) error
}

// AdminSessions uses the application's session library and its independent Redis
// provider. Explicit crypto-random IDs also avoid the legacy library ID generator.
type AdminSessions struct {
	manager    AdminSessionManager
	config     websession.Config
	cookiePath string
	invalidate func(context.Context, session.Session) error
	now        func() time.Time
}

func NewAdminSessions(manager AdminSessionManager, cfg websession.Config, cookiePath string, invalidate func(context.Context, session.Session) error) *AdminSessions {
	if cookiePath == "" {
		cookiePath = "/"
	}
	return &AdminSessions{manager: manager, config: cfg, cookiePath: cookiePath, invalidate: invalidate, now: time.Now}
}

type adminSessionStore struct {
	session.Session
	destroyed bool
}

func (s *adminSessionStore) Flush() error {
	if s.destroyed {
		return nil
	}
	return s.Session.Flush()
}

func missingAdminSessionValue(err error) bool {
	return errors.Is(err, redis.Nil) || errors.Is(err, redisprovider.ErrKeyNotFound)
}

func (s *AdminSessions) Resolver(w http.ResponseWriter, r *http.Request) (session.Session, error) {
	if s == nil || s.manager == nil {
		return nil, errors.New("administrator session manager is not configured")
	}
	w.Header().Set("Cache-Control", "no-store")
	var id string
	if cookie, err := r.Cookie(s.config.CookieName); err == nil && adminSessionIDPattern.MatchString(cookie.Value) {
		id = cookie.Value
	}
	var stored session.Session
	var created time.Time
	if id != "" {
		var err error
		stored, err = s.manager.SessionStart(id)
		if err != nil {
			return nil, fmt.Errorf("read administrator session: %w", err)
		}
		var accessed time.Time
		if err = stored.Get("time_accessed", &accessed); err != nil && !missingAdminSessionValue(err) {
			return nil, err
		}
		markerErr := stored.Get(adminSessionCreatedKey, &created)
		if markerErr != nil && !missingAdminSessionValue(markerErr) {
			return nil, markerErr
		}
		now := s.now()
		expired := markerErr != nil || created.IsZero() || created.After(now.Add(time.Minute)) || now.Sub(created) >= time.Duration(s.config.MaxLifeTime)*time.Second
		expired = expired || accessed.IsZero() || now.Sub(accessed) >= time.Duration(s.config.MaxIdleTime)*time.Second
		rotate := r.Method == http.MethodPost && r.URL.Path == "/admin/login"
		if expired || rotate {
			var invalidateErr error
			if markerErr == nil && s.invalidate != nil {
				invalidateErr = s.invalidate(r.Context(), stored)
			}
			destroyErr := s.manager.SessionDestroy(id)
			if err = errors.Join(invalidateErr, destroyErr); err != nil {
				return nil, err
			}
			id = ""
		}
	}
	if id == "" {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, fmt.Errorf("create administrator session ID: %w", err)
		}
		id = "a" + hex.EncodeToString(random[:])
		var err error
		stored, err = s.manager.SessionStart(id)
		if err != nil {
			return nil, err
		}
		created = s.now()
		for _, key := range []string{adminSessionCreatedKey, "time_created", "time_accessed"} {
			if err = stored.Set(key, created); err != nil {
				return nil, err
			}
		}
		if err = stored.Flush(); err != nil {
			return nil, err
		}
		// Existing cookies already expire at the fixed session deadline. Only
		// send a new cookie when creating or replacing a session.
		s.cookie(w, id, created)
	}
	return &adminSessionStore{Session: stored}, nil
}

func (s *AdminSessions) cookie(w http.ResponseWriter, id string, created time.Time) {
	expires := created.Add(time.Duration(s.config.MaxLifeTime) * time.Second)
	maxAge := int(expires.Sub(s.now()) / time.Second)
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{Name: s.config.CookieName, Value: id, Path: s.cookiePath, Domain: s.config.CookieDomain, Secure: s.config.Secure, HttpOnly: true, SameSite: s.config.SameSite, MaxAge: maxAge, Expires: expires})
}

func (s *AdminSessions) End(w http.ResponseWriter, _ *http.Request, sess session.Session) error {
	if s == nil {
		return errors.New("administrator session manager is not configured")
	}
	http.SetCookie(w, &http.Cookie{Name: s.config.CookieName, Value: "", Path: s.cookiePath, Domain: s.config.CookieDomain, Secure: s.config.Secure, HttpOnly: true, SameSite: s.config.SameSite, MaxAge: -1, Expires: time.Unix(1, 0)})
	if sess != nil && adminSessionIDPattern.MatchString(sess.SessionID()) {
		if stored, ok := sess.(*adminSessionStore); ok {
			stored.destroyed = true
		}
		return s.manager.SessionDestroy(sess.SessionID())
	}
	return nil
}
