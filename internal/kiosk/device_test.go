package kiosk

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/pronogti/internal/salon"
	"github.com/dronm/session"
)

func deviceCookieRequest(method, path, token string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.AddCookie(&http.Cookie{Name: DefaultDeviceCookieName, Value: base64.RawURLEncoding.EncodeToString([]byte(token))})
	return r
}

func sessionResolverFor(sess session.Session) func(http.ResponseWriter, *http.Request) (session.Session, error) {
	return func(http.ResponseWriter, *http.Request) (session.Session, error) { return sess, nil }
}

func TestPersistentDeviceRecoversBindingAfterRedisStateLoss(t *testing.T) {
	m, original, registry, _ := newFixture(t)
	if _, _, err := m.StartCustomer(context.Background(), original, "", true); err != nil {
		t.Fatal(err)
	}
	// This empty store represents a newly issued framework session after Redis
	// expiration/cleanup. The durable device credential is still in the browser.
	fresh := &testSession{values: map[string][]byte{}}
	resolver := NewDeviceSessionResolver(sessionResolverFor(fresh), m, registry, DeviceCookieConfig{})
	w := httptest.NewRecorder()
	if _, err := resolver(w, deviceCookieRequest(http.MethodGet, "/session", "token")); err != nil {
		t.Fatal(err)
	}
	current, err := m.Current(context.Background(), fresh)
	if err != nil || current.Kiosk == nil || current.Kiosk.ID != 1 || current.Customer != nil || current.Mode != "kiosk" {
		t.Fatalf("restored=%+v error=%v", current, err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != DefaultDeviceCookieName || cookies[0].MaxAge != MaxDeviceCookieAge || !cookies[0].HttpOnly {
		t.Fatalf("renewed cookies=%+v", cookies)
	}
}

func TestDeviceReassignmentNeverRestoresPreviousCustomer(t *testing.T) {
	m, sess, registry, _ := newFixture(t)
	_, old, err := m.StartCustomer(context.Background(), sess, "", true)
	if err != nil {
		t.Fatal(err)
	}
	registry.device.CompanyID = 99
	registry.device.SessionVersion++
	resolver := NewDeviceSessionResolver(sessionResolverFor(sess), m, registry, DeviceCookieConfig{})
	if _, err := resolver(httptest.NewRecorder(), deviceCookieRequest(http.MethodGet, "/session", "token")); err != nil {
		t.Fatal(err)
	}
	current, err := m.Current(context.Background(), sess)
	if err != nil || current.Kiosk == nil || current.Kiosk.CompanyID != 99 || current.Customer != nil {
		t.Fatalf("reassigned=%+v error=%v", current, err)
	}
	if _, _, err := m.RequireCustomer(context.Background(), sess, old.SessionID); err == nil {
		t.Fatal("previous company customer nonce survived reassignment")
	}
}

func TestRevokedDeviceCredentialClearsCachedKiosk(t *testing.T) {
	for _, invalid := range []string{"rotation", "disabled", "malformed"} {
		t.Run(invalid, func(t *testing.T) {
			m, sess, registry, _ := newFixture(t)
			_, old, err := m.StartCustomer(context.Background(), sess, "", true)
			if err != nil {
				t.Fatal(err)
			}
			r := deviceCookieRequest(http.MethodGet, "/session", "token")
			switch invalid {
			case "rotation":
				registry.err = ErrKioskNotFound
			case "disabled":
				registry.device.IsActive = false
			case "malformed":
				r.Header.Set("Cookie", DefaultDeviceCookieName+"=%%%")
			}
			resolver := NewDeviceSessionResolver(sessionResolverFor(sess), m, registry, DeviceCookieConfig{})
			w := httptest.NewRecorder()
			if _, err := resolver(w, r); err != nil {
				t.Fatal(err)
			}
			var cached Kiosk
			if err := sess.Get(kioskKey, &cached); err == nil {
				t.Fatal("cached kiosk survived invalid durable credential")
			}
			if _, _, err := m.RequireCustomer(context.Background(), sess, old.SessionID); err == nil {
				t.Fatal("customer survived device revocation")
			}
			cookies := w.Result().Cookies()
			if len(cookies) != 1 || cookies[0].MaxAge != -1 {
				t.Fatalf("credential was not cleared: %+v", cookies)
			}
		})
	}
}

type deviceSalonRegistry struct{ entry salon.Entry }

func (r deviceSalonRegistry) List(context.Context) ([]salon.Entry, error) {
	return []salon.Entry{r.entry}, nil
}
func (r deviceSalonRegistry) FindBySlug(_ context.Context, slug string) (salon.Entry, error) {
	if slug != r.entry.Slug {
		return salon.Entry{}, salon.ErrSalonNotFound
	}
	return r.entry, nil
}
func (r deviceSalonRegistry) FindByCompanyID(_ context.Context, id int64) (salon.Entry, error) {
	if id != r.entry.CompanyID {
		return salon.Entry{}, salon.ErrSalonNotFound
	}
	return r.entry, nil
}

func TestDeviceRenewalPreservesAnExplicitPublicCustomer(t *testing.T) {
	m, sess, registry, _ := newFixture(t)
	m.options.Salons = deviceSalonRegistry{entry: salon.Entry{SalonIdentity: models.SalonIdentity{CompanyID: 99, Slug: "public-salon", Name: "Public salon"}, IsActive: true, SessionVersion: 1}}
	if _, err := m.SelectSalon(context.Background(), sess, "public-salon"); err != nil {
		t.Fatal(err)
	}
	_, customer, err := m.StartCustomer(context.Background(), sess, "", true)
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewDeviceSessionResolver(sessionResolverFor(sess), m, registry, DeviceCookieConfig{})
	for _, revoke := range []bool{false, true} {
		if revoke {
			registry.err = ErrKioskNotFound
		}
		if _, err := resolver(httptest.NewRecorder(), deviceCookieRequest(http.MethodGet, "/session", "token")); err != nil {
			t.Fatal(err)
		}
		current, err := m.Current(context.Background(), sess)
		if err != nil || current.Mode != "public" || current.Salon == nil || current.Salon.CompanyID != 99 || current.Customer == nil || current.Customer.SessionID != customer.SessionID {
			t.Fatalf("public interaction changed after revoke=%t: %+v error=%v", revoke, current, err)
		}
	}
}

func TestDeviceDatabaseFailureDoesNotEraseCredential(t *testing.T) {
	m, sess, registry, _ := newFixture(t)
	registry.err = errors.New("database unavailable")
	resolver := NewDeviceSessionResolver(sessionResolverFor(sess), m, registry, DeviceCookieConfig{})
	w := httptest.NewRecorder()
	if _, err := resolver(w, deviceCookieRequest(http.MethodGet, "/session", "token")); !errors.Is(err, registry.err) {
		t.Fatalf("error=%v", err)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("temporary database failure erased the durable credential")
	}
}

func TestPartialRedisEvictionPreservesPublicScopeAndCustomer(t *testing.T) {
	m, sess, registry, _ := newFixture(t)
	m.options.Salons = deviceSalonRegistry{entry: salon.Entry{SalonIdentity: models.SalonIdentity{CompanyID: 99, Slug: "public-salon", Name: "Public salon"}, IsActive: true, SessionVersion: 1}}
	selected, err := m.SelectSalon(context.Background(), sess, "public-salon")
	if err != nil {
		t.Fatal(err)
	}
	_, customer, err := m.StartCustomer(context.Background(), sess, "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, partial := range []string{"missing cache", "stale cache"} {
		t.Run(partial, func(t *testing.T) {
			if partial == "missing cache" {
				if err := sess.Delete(kioskKey); err != nil {
					t.Fatal(err)
				}
			} else {
				stale := registry.device
				stale.SessionVersion--
				if err := sess.Set(kioskKey, stale); err != nil {
					t.Fatal(err)
				}
			}
			resolver := NewDeviceSessionResolver(sessionResolverFor(sess), m, registry, DeviceCookieConfig{})
			if _, err := resolver(httptest.NewRecorder(), deviceCookieRequest(http.MethodGet, "/session", "token")); err != nil {
				t.Fatal(err)
			}
			current, err := m.Current(context.Background(), sess)
			if err != nil || current.Mode != "public" || current.ScopeID != selected.ScopeID || current.Salon == nil || current.Salon.CompanyID != 99 || current.Customer == nil || current.Customer.SessionID != customer.SessionID {
				t.Fatalf("public interaction changed: %+v error=%v", current, err)
			}
			var cached Kiosk
			if err := sess.Get(kioskKey, &cached); err != nil || cached != registry.device {
				t.Fatalf("device cache not refreshed: %+v error=%v", cached, err)
			}
		})
	}
}

func TestSetupWritesCredentialOnlyAfterSuccessfulHandler(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			const body = `{"token":"setup-token"}`
			cfg := DeviceCookieConfig{Name: "_device", Domain: "example.test", MaxAge: 3600, Secure: true, SameSite: http.SameSiteStrictMode}
			handler := DeviceCookieMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				actual, err := io.ReadAll(r.Body)
				if err != nil || string(actual) != body {
					t.Fatalf("binder body=%q error=%v", actual, err)
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`null`))
			}))
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/kiosk/session", strings.NewReader(body))
			handler.ServeHTTP(w, r)
			cookies := w.Result().Cookies()
			if status != http.StatusOK {
				if len(cookies) != 0 {
					t.Fatal("failed setup wrote a device credential")
				}
				return
			}
			if len(cookies) != 1 {
				t.Fatalf("setup cookies=%+v", cookies)
			}
			cookie := cookies[0]
			token, err := base64.RawURLEncoding.DecodeString(cookie.Value)
			if err != nil || string(token) != "setup-token" || cookie.Name != "_device" || cookie.Domain != "example.test" || cookie.Path != "/" || cookie.MaxAge != 3600 || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || !cookie.Expires.After(time.Now()) {
				t.Fatalf("setup cookie=%+v token=%q error=%v", cookie, token, err)
			}
		})
	}
}

func TestNormalVisitorsDoNotReceiveDeviceCredentials(t *testing.T) {
	m, _, registry, _ := newFixture(t)
	blank := &testSession{values: map[string][]byte{}}
	resolver := NewDeviceSessionResolver(sessionResolverFor(blank), m, registry, DeviceCookieConfig{})
	w := httptest.NewRecorder()
	if _, err := resolver(w, httptest.NewRequest(http.MethodGet, "/session", nil)); err != nil {
		t.Fatal(err)
	}
	current, err := m.Current(context.Background(), blank)
	if err != nil || current.Kiosk != nil || current.Customer != nil || len(w.Result().Cookies()) != 0 {
		t.Fatalf("visitor unexpectedly became kiosk: %+v error=%v", current, err)
	}
}
