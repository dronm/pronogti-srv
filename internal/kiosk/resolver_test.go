package kiosk

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	core "github.com/dronm/session"
	websession "github.com/dronm/webapp/session"
	"github.com/redis/go-redis/v9"
)

type resolverTestSession struct {
	core.Session
	id     string
	values map[string]time.Time
	err    error
}

func (s *resolverTestSession) SessionID() string { return s.id }
func (s *resolverTestSession) Get(key string, out any) error {
	if s.err != nil {
		return s.err
	}
	value, exists := s.values[key]
	if !exists {
		return redis.Nil
	}
	*out.(*time.Time) = value
	return nil
}
func (s *resolverTestSession) Set(key string, value any) error {
	if s.err != nil {
		return s.err
	}
	s.values[key] = value.(time.Time)
	return nil
}
func (s *resolverTestSession) Flush() error { return s.err }

func TestResolverRejectsSuppliedUnknownIDsAndBearer(t *testing.T) {
	for _, input := range []string{"", "chosen-session", "../../*", "11111111-1111-4111-8111-111111111111"} {
		t.Run(input, func(t *testing.T) {
			stores := map[string]*resolverTestSession{}
			var received string
			resolver := newSessionResolver(resolverBackend{
				start: func(id string) (core.Session, error) {
					if stores[id] == nil {
						stores[id] = &resolverTestSession{id: id, values: map[string]time.Time{}}
					}
					return stores[id], nil
				},
				destroy: func(string) error { t.Fatal("destroy called for untrusted ID"); return nil },
				resolve: func(w http.ResponseWriter, r *http.Request) (core.Session, error) {
					cookie, err := r.Cookie("_test")
					if err != nil {
						t.Fatal(err)
					}
					received = cookie.Value
					if r.Header.Get("Authorization") != "" {
						t.Fatal("bearer fallback still enabled")
					}
					if cookie, err := r.Cookie("other"); err != nil || cookie.Value != "keep" {
						t.Fatal("unrelated cookie lost")
					}
					return stores[received], nil
				},
			}, websession.Config{CookieName: "_test", MaxLifeTime: 60})
			r := httptest.NewRequest(http.MethodGet, "/kiosk/session", nil)
			if input != "" {
				r.AddCookie(&http.Cookie{Name: "_test", Value: input})
			}
			r.AddCookie(&http.Cookie{Name: "other", Value: "keep"})
			r.Header.Set("Authorization", "Bearer chosen-session")
			w := httptest.NewRecorder()
			sess, err := resolver(w, r)
			if err != nil {
				t.Fatal(err)
			}
			if !sessionIDPattern.MatchString(received) || received == input || received != sess.SessionID() {
				t.Fatalf("selected ID %q", received)
			}
			if stores[received].values[sessionCreatedKey].IsZero() {
				t.Fatal("missing creation marker")
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("session response is cacheable")
			}
		})
	}
}

func TestResolverRestoresRotatesAndExpiresKnownSessions(t *testing.T) {
	for _, tt := range []struct {
		name              string
		created, accessed time.Time
		method            string
		rotate            bool
	}{
		{"restore", time.Now().Add(-time.Second), time.Now(), http.MethodGet, false},
		{"life expiry", time.Now().Add(-2 * time.Hour), time.Now(), http.MethodGet, true},
		{"idle expiry", time.Now().Add(-time.Minute), time.Now().Add(-time.Hour), http.MethodGet, true},
		{"provisioning rotation", time.Now(), time.Now(), http.MethodPost, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id, _ := secureSessionID()
			stores := map[string]*resolverTestSession{id: {id: id, values: map[string]time.Time{sessionCreatedKey: tt.created, "time_accessed": tt.accessed}}}
			destroyed := false
			resolver := newSessionResolver(resolverBackend{
				start: func(value string) (core.Session, error) {
					if stores[value] == nil {
						stores[value] = &resolverTestSession{id: value, values: map[string]time.Time{}}
					}
					return stores[value], nil
				},
				destroy: func(value string) error { destroyed = value == id; delete(stores, value); return nil },
				resolve: func(w http.ResponseWriter, r *http.Request) (core.Session, error) {
					cookie, _ := r.Cookie("_test")
					return stores[cookie.Value], nil
				},
			}, websession.Config{CookieName: "_test", MaxLifeTime: 3600, MaxIdleTime: 1800})
			r := httptest.NewRequest(tt.method, "/kiosk/session", nil)
			r.AddCookie(&http.Cookie{Name: "_test", Value: id})
			sess, err := resolver(httptest.NewRecorder(), r)
			if err != nil {
				t.Fatal(err)
			}
			if destroyed != tt.rotate || (sess.SessionID() != id) != tt.rotate {
				t.Fatalf("destroyed=%t new=%q", destroyed, sess.SessionID())
			}
		})
	}
}

func TestResolverDoesNotHideRedisFailures(t *testing.T) {
	sentinel := errors.New("Redis unavailable")
	id, _ := secureSessionID()
	sess := &resolverTestSession{id: id, err: sentinel}
	resolver := newSessionResolver(resolverBackend{
		start:   func(string) (core.Session, error) { return sess, nil },
		destroy: func(string) error { t.Fatal("destroy on infrastructure failure"); return nil },
		resolve: func(http.ResponseWriter, *http.Request) (core.Session, error) {
			t.Fatal("resolved despite failure")
			return nil, nil
		},
	}, websession.Config{CookieName: "_test"})
	r := httptest.NewRequest(http.MethodGet, "/kiosk/session", nil)
	r.AddCookie(&http.Cookie{Name: "_test", Value: id})
	if _, err := resolver(httptest.NewRecorder(), r); !errors.Is(err, sentinel) {
		t.Fatalf("error=%v", err)
	}
}
