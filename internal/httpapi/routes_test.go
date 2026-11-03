package httpapi

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dronm/goyclients"
	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/pronogti/internal/salon"
	"github.com/dronm/session"
	"github.com/dronm/webapp"
)

type fakeBackend struct {
	call         string
	companyID    int64
	clientID     int64
	phone        string
	staffID      *int64
	categoryID   *int64
	term         string
	client       models.NewClient
	clientResult goyclients.Client
	err          error
}

func (b *fakeBackend) FetchCompanies(context.Context) ([]*goyclients.Company, error) {
	b.call = "companies"
	return []*goyclients.Company{{ID: 12}, {ID: 99}}, b.err
}
func (b *fakeBackend) FindClientByPhone(_ context.Context, c int64, p string) (goyclients.Client, error) {
	b.call, b.companyID, b.phone = "findClient", c, p
	return b.clientResult, b.err
}
func (b *fakeBackend) FetchPayDraft(_ context.Context, c, id int64) (models.PayDraft, error) {
	b.call, b.companyID, b.clientID = "payDraft", c, id
	return models.PayDraft{ClientID: id, Items: []models.PayDraftItem{}}, b.err
}
func (b *fakeBackend) AddClient(_ context.Context, c int64, v models.NewClient) (models.ClientKey, error) {
	b.call, b.companyID, b.client = "registerClient", c, v
	return models.ClientKey{ID: 42}, b.err
}
func (b *fakeBackend) FetchStaffList(_ context.Context, c int64) ([]goyclients.Staff, error) {
	b.call, b.companyID = "staff", c
	return []goyclients.Staff{}, b.err
}
func (b *fakeBackend) FetchServiceCatList(_ context.Context, c int64, s *int64) ([]*goyclients.ServiceCategory, error) {
	b.call, b.companyID, b.staffID = "serviceCategories", c, s
	return []*goyclients.ServiceCategory{}, b.err
}
func (b *fakeBackend) FetchServiceList(_ context.Context, c int64, s, cat *int64, term string) ([]*goyclients.Service, error) {
	b.call, b.companyID, b.staffID, b.categoryID, b.term = "services", c, s, cat, term
	return []*goyclients.Service{}, b.err
}
func (b *fakeBackend) FetchGoodsList(_ context.Context, c int64, term string) ([]*goyclients.Good, error) {
	b.call, b.companyID, b.term = "goods", c, term
	return []*goyclients.Good{}, b.err
}

type memorySession struct {
	session.Session
	mu     sync.Mutex
	values map[string][]byte
}

func newMemorySession() *memorySession { return &memorySession{values: map[string][]byte{}} }
func (s *memorySession) Set(key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b bytes.Buffer
	if err := gob.NewEncoder(&b).Encode(value); err != nil {
		return err
	}
	s.values[key] = b.Bytes()
	return nil
}
func (s *memorySession) Get(key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.values[key]
	if !ok {
		return errors.New("missing")
	}
	return gob.NewDecoder(bytes.NewReader(b)).Decode(value)
}
func (s *memorySession) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}
func (s *memorySession) Flush() error      { return nil }
func (s *memorySession) SessionID() string { return "framework-sid-must-not-be-returned" }

type fakeRegistry struct{ device kiosk.Kiosk }

func (r *fakeRegistry) FindByToken(_ context.Context, token string) (kiosk.Kiosk, error) {
	if token != "valid-token" {
		return kiosk.Kiosk{}, kiosk.ErrKioskNotFound
	}
	return r.device, nil
}
func (r *fakeRegistry) FindByID(_ context.Context, id int64) (kiosk.Kiosk, error) {
	if id != r.device.ID {
		return kiosk.Kiosk{}, kiosk.ErrKioskNotFound
	}
	return r.device, nil
}

type testRuntime struct {
	handler  http.Handler
	backend  *fakeBackend
	registry *fakeRegistry
	sess     *memorySession
	salons   *fakeSalons
	scopeID  string
}

func newRuntime(backend *fakeBackend, checkoutOptions ...CheckoutDependencies) *testRuntime {
	r := &testRuntime{backend: backend, registry: &fakeRegistry{device: kiosk.Kiosk{ID: 1, Name: "Salon kiosk", CompanyID: 12, IsActive: true, SessionVersion: 1}}, sess: newMemorySession()}
	r.salons = &fakeSalons{entries: []salon.Entry{
		{SalonIdentity: models.SalonIdentity{CompanyID: 12, Slug: "salon-one", Name: "First salon"}, IsActive: true, SessionVersion: 1},
		{SalonIdentity: models.SalonIdentity{CompanyID: 99, Slug: "salon-two", Name: "Second salon"}, IsActive: true, SessionVersion: 1},
		{SalonIdentity: models.SalonIdentity{CompanyID: 100, Slug: "disabled-salon", Name: "Disabled salon"}, IsActive: false, SessionVersion: 1},
	}}
	RegisterServices(backend, r.registry, kiosk.Options{Salons: r.salons}, checkoutOptions...)
	r.handler = webapp.MustBuildRouter(&webapp.AppContext{}, BuildRoutes(), webapp.RecoverMiddleware(), webapp.SessionMiddleware(func(http.ResponseWriter, *http.Request) (session.Session, error) { return r.sess, nil }), SalonScopeMiddleware())
	return r
}

type fakeSalons struct{ entries []salon.Entry }

func (r *fakeSalons) List(context.Context) ([]salon.Entry, error) { return r.entries, nil }
func (r *fakeSalons) FindBySlug(_ context.Context, slug string) (salon.Entry, error) {
	for _, entry := range r.entries {
		if entry.Slug == slug {
			return entry, nil
		}
	}
	return salon.Entry{}, salon.ErrSalonNotFound
}
func (r *fakeSalons) FindByCompanyID(_ context.Context, companyID int64) (salon.Entry, error) {
	for _, entry := range r.entries {
		if entry.CompanyID == companyID {
			return entry, nil
		}
	}
	return salon.Entry{}, salon.ErrSalonNotFound
}
func (r *testRuntime) request(method, path, body, nonce string) *httptest.ResponseRecorder {
	return r.requestInScope(method, path, body, nonce, r.scopeID)
}
func (r *testRuntime) requestInScope(method, path, body, nonce, scopeID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if nonce != "" {
		req.Header.Set(kiosk.CustomerHeader, nonce)
	}
	if strings.HasPrefix(path, "/customer/") && scopeID != "" {
		req.Header.Set(kiosk.ScopeHeader, scopeID)
	}
	res := httptest.NewRecorder()
	r.handler.ServeHTTP(res, req)
	if res.Code == http.StatusOK {
		var response map[string]json.RawMessage
		if json.Unmarshal(res.Body.Bytes(), &response) == nil {
			if id, ok := response["scope_id"]; ok {
				_ = json.Unmarshal(id, &r.scopeID)
			}
		}
	}
	return res
}
func (r *testRuntime) bind(t *testing.T) {
	t.Helper()
	res := r.request(http.MethodPost, "/kiosk/session", `{"token":"valid-token"}`, "")
	if res.Code != 200 {
		t.Fatalf("bind: %d %s", res.Code, res.Body)
	}
}
func (r *testRuntime) start(t *testing.T, body string) models.SessionResponse {
	t.Helper()
	res := r.request(http.MethodPost, "/kiosk/customer-session", body, "")
	if res.Code != 200 {
		t.Fatalf("start: %d %s", res.Code, res.Body)
	}
	var out models.SessionResponse
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSessionFlowUsesKioskCompanyAndCustomerIdentity(t *testing.T) {
	r := newRuntime(&fakeBackend{clientResult: goyclients.Client{ID: 45, Name: "Anna", Email: "anna@example.org"}})
	if res := r.request(http.MethodGet, "/kiosk/services", "", ""); res.Code != 401 {
		t.Fatalf("unauthenticated: %d", res.Code)
	}
	r.bind(t)
	start := r.start(t, `{"phone":"+7 (999) 123-45-67","company_id":99,"client_id":999}`)
	if start.Customer == nil || start.Customer.ClientID != 45 || !start.Customer.Registered || start.Customer.Phone != "79991234567" || r.backend.companyID != 12 {
		t.Fatalf("session=%+v backend=%+v", start, r.backend)
	}
	if start.Customer.SessionID == r.sess.SessionID() {
		t.Fatal("framework SID leaked")
	}
	for _, path := range []string{"/kiosk/me/pay-draft", "/kiosk/me/pay-draft?client_id=999&company_id=99"} {
		res := r.request(http.MethodGet, path, "", start.Customer.SessionID)
		if res.Code != 200 || r.backend.companyID != 12 || r.backend.clientID != 45 {
			t.Fatalf("draft %d %s backend=%+v", res.Code, res.Body, r.backend)
		}
	}
	res := r.request(http.MethodGet, "/kiosk/company", "", "")
	if res.Code != 200 {
		t.Fatalf("company: %d %s", res.Code, res.Body)
	}
	var company goyclients.Company
	_ = json.Unmarshal(res.Body.Bytes(), &company)
	if company.ID != 12 {
		t.Fatalf("company=%+v", company)
	}
}

func TestCustomerLifecycleRejectsPreviousInteraction(t *testing.T) {
	r := newRuntime(&fakeBackend{clientResult: goyclients.Client{ID: 45}})
	r.bind(t)
	first := r.start(t, `{"phone":"79991234567"}`)
	second := r.start(t, `{"guest":true}`)
	if first.Customer.SessionID == second.Customer.SessionID {
		t.Fatal("interaction ID reused")
	}
	for _, op := range []struct{ method, path, body string }{{http.MethodGet, "/kiosk/me/pay-draft", ""}, {http.MethodPost, "/kiosk/customer-session/touch", ""}, {http.MethodDelete, "/kiosk/customer-session", ""}, {http.MethodPost, "/kiosk/client/register", `{"phone":"79991234567"}`}} {
		res := r.request(op.method, op.path, op.body, first.Customer.SessionID)
		if res.Code != 401 {
			t.Fatalf("old nonce %s: %d %s", op.path, res.Code, res.Body)
		}
	}
	res := r.request(http.MethodDelete, "/kiosk/customer-session", "", second.Customer.SessionID)
	if res.Code != 200 || strings.TrimSpace(res.Body.String()) != "null" {
		t.Fatalf("logout=%d %s", res.Code, res.Body)
	}
	res = r.request(http.MethodGet, "/kiosk/session", "", "")
	var current models.SessionResponse
	_ = json.Unmarshal(res.Body.Bytes(), &current)
	if res.Code != 200 || current.Customer != nil || current.Kiosk.ID != 1 {
		t.Fatalf("postlogout=%d %s", res.Code, res.Body)
	}
	if res := r.request(http.MethodGet, "/kiosk/me/pay-draft", "", second.Customer.SessionID); res.Code != 401 {
		t.Fatal("logged out customer accepted")
	}
}

func TestRegistrationBoundToSessionPhoneAndCompany(t *testing.T) {
	r := newRuntime(&fakeBackend{})
	r.bind(t)
	start := r.start(t, `{"phone":"79991234567"}`)
	if start.Customer.Registered {
		t.Fatal("missing client marked registered")
	}
	if res := r.request(http.MethodPost, "/kiosk/client/register", `{"phone":"79990000000"}`, start.Customer.SessionID); res.Code != 403 {
		t.Fatalf("phone mismatch=%d %s", res.Code, res.Body)
	}
	res := r.request(http.MethodPost, "/kiosk/client/register", `{"name":"Anna","phone":"89991234567","email":"anna@example.org","birth_date":"1990-04-05"}`, start.Customer.SessionID)
	if res.Code != 200 {
		t.Fatalf("register=%d %s", res.Code, res.Body)
	}
	var out models.SessionResponse
	_ = json.Unmarshal(res.Body.Bytes(), &out)
	if out.Customer == nil || out.Customer.ClientID != 42 || !out.Customer.Registered || r.backend.client.Phone != "79991234567" || r.backend.companyID != 12 {
		t.Fatalf("result=%+v backend=%+v", out, r.backend)
	}
}

func TestLegacyDraftSavingRoutesAreRemoved(t *testing.T) {
	r := newRuntime(&fakeBackend{clientResult: goyclients.Client{ID: 45}})
	r.bind(t)
	start := r.start(t, `{"phone":"79991234567"}`)
	for _, path := range []string{"/customer/me/save-pay-draft", "/kiosk/me/save-pay-draft", "/12/client/45/save-pay-draft"} {
		res := r.request(http.MethodPost, path, `{"items":[]}`, start.Customer.SessionID)
		if res.Code != http.StatusNotFound {
			t.Fatalf("removed endpoint %s returned %d: %s", path, res.Code, res.Body)
		}
	}
}

func TestKioskRevocationAndOldURLs(t *testing.T) {
	r := newRuntime(&fakeBackend{})
	r.bind(t)
	r.registry.device.SessionVersion++
	if res := r.request(http.MethodGet, "/kiosk/session", "", ""); res.Code != 401 {
		t.Fatalf("rotated kiosk=%d", res.Code)
	}
	for _, path := range []string{"/companies", "/12/services", "/12/client/find/79991234567", "/12/client/45/pay-draft"} {
		if res := r.request(http.MethodGet, path, "", ""); res.Code != 404 {
			t.Errorf("legacy URL %s still mounted: %d", path, res.Code)
		}
	}
}

func TestCatalogFiltersAndSanitizedErrors(t *testing.T) {
	r := newRuntime(&fakeBackend{})
	r.bind(t)
	res := r.request(http.MethodGet, "/kiosk/services?staff=5&cat=6&term=hair&companyID=99", "", "")
	if res.Code != 200 || strings.TrimSpace(res.Body.String()) != "[]" || r.backend.staffID == nil || *r.backend.staffID != 5 || r.backend.categoryID == nil || *r.backend.categoryID != 6 || r.backend.companyID != 12 {
		t.Fatalf("catalog=%d %s", res.Code, res.Body)
	}
	r.backend.call = ""
	if res := r.request(http.MethodGet, "/kiosk/services?staff=bad", "", ""); res.Code != 400 || r.backend.call != "" {
		t.Fatalf("bad filter=%d", res.Code)
	}
	r.backend.err = errors.New("secret credentials")
	res = r.request(http.MethodGet, "/kiosk/goods", "", "")
	if res.Code != 500 || strings.Contains(res.Body.String(), "secret credentials") {
		t.Fatalf("unsanitized=%d %s", res.Code, res.Body)
	}
}
