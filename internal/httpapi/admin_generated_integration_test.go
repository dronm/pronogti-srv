package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dronm/goyclients"
	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/session"
	"github.com/dronm/webapp"
	websession "github.com/dronm/webapp/session"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type generatedAdministratorIntegration struct {
	handler http.Handler
	public  *testRuntime
	conn    *pgx.Conn
	user    adminauth.User
	cookie  *http.Cookie
	nonce   string
}

// This fixture deliberately builds the generated handler twice. Reusing the
// process must not register the same generated service twice in webapp's global
// registry, and both builds must retain all compiled route permissions.
func newGeneratedAdministratorIntegration(t *testing.T) generatedAdministratorIntegration {
	t.Helper()
	ctx := context.Background()
	provider, conn := adminIntegrationDatabase(t, nil)
	repository := adminauth.NewPostgresRepository(provider)
	const password = "generated-administrator-password"
	user, err := repository.Provision(ctx, "Generated Operator", adminIntegrationHash(t, password), false)
	if err != nil {
		t.Fatal(err)
	}
	auth := adminauth.NewService(repository, adminauth.Options{MaxLifeTime: time.Hour, BcryptCost: bcrypt.MinCost})
	generated, permissions, err := NewGeneratedAdminHandler(auth, provider, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, again, err := NewGeneratedAdminHandler(auth, provider, 5*time.Second)
	if err != nil || len(again) != len(permissions) {
		t.Fatalf("rebuilding generated administrator handler: permissions=%v error=%v", again, err)
	}
	for _, permission := range []string{"salon.create", "salon.list", "salon.detail", "salon.update"} {
		found := false
		for _, actual := range permissions {
			found = found || actual == permission
		}
		if !found {
			t.Fatalf("missing compiled permission %q: %v", permission, permissions)
		}
	}
	manager := &adminTestManager{}
	sessions := NewAdminSessions(manager, websession.Config{
		CookieName: "admin_test", HTTPOnly: true, SameSite: http.SameSiteLaxMode,
		MaxLifeTime: 3600, MaxIdleTime: 600,
	}, "/", auth.Invalidate)
	admin := NewAdminHandler(auth, sessions, AdminHTTPOptions{
		AllowedOrigins: []string{"http://localhost:5174"}, RequestTimeout: 5 * time.Second,
		GeneratedRoutes: generated, GeneratedPermissions: permissions,
	})
	public := newRuntime(&fakeBackend{clientResult: goyclients.Client{ID: 45, Name: "Anna"}})
	handler := WithAdministrator(public.handler, admin)
	public.handler = handler
	login := adminRequest(handler, http.MethodPost, "/admin/login", `{"name":"Generated Operator","pwd":"`+password+`"}`, nil, "")
	identity := adminIntegrationResponse(t, login)
	if identity.Auth == nil || identity.User == nil {
		t.Fatal("generated administrator login has no identity")
	}
	return generatedAdministratorIntegration{
		handler: handler, public: public, conn: conn, user: user,
		cookie: adminIntegrationCookie(t, login, "admin_test"), nonce: identity.Auth.Token,
	}
}

func generatedIntegrationSalon(t *testing.T, fixture generatedAdministratorIntegration, companyID int64) models.Salon {
	t.Helper()
	response := adminRequest(fixture.handler, http.MethodGet, "/admin/salons/"+strconv.FormatInt(companyID, 10), "", fixture.cookie, "")
	var salon models.Salon
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &salon) != nil {
		t.Fatalf("generated salon detail: %d %s", response.Code, response.Body)
	}
	return salon
}

func TestGeneratedAdministratorFiscalReceiptDetails(t *testing.T) {
	fixture := newGeneratedAdministratorIntegration(t)
	const create = `{"company_id":781898,"name":"Receipt details","enabled":true,"mode":"demo",
		"timeout_seconds":100,"cashier_name":"Cashier","tax_system":1,"service_vat_rate":6,
		"good_vat_rate":6,"payment_method":4,"internet_payment":false,"suppress_print":false,
		"settlement_address":"Тюмень","settlement_place":"Салон","automatic_device_number":"0007",
		"time_zone":4,"send_settlement_details":true}`
	response := adminRequest(fixture.handler, http.MethodPost, "/admin/fiscal-profiles", create, fixture.cookie, fixture.nonce)
	var created models.FiscalProfile
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &created) != nil || created.ID <= 0 {
		t.Fatalf("create fiscal profile: %d %s", response.Code, response.Body)
	}
	path := "/admin/fiscal-profiles/" + strconv.FormatInt(created.ID, 10)
	get := func() models.FiscalProfile {
		t.Helper()
		response := adminRequest(fixture.handler, http.MethodGet, path, "", fixture.cookie, "")
		var profile models.FiscalProfile
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &profile) != nil {
			t.Fatalf("read fiscal profile: %d %s", response.Code, response.Body)
		}
		return profile
	}
	profile := get()
	if !profile.SendSettlementDetails || profile.TimeZone == nil || *profile.TimeZone != 4 ||
		profile.AutomaticDeviceNumber == nil || *profile.AutomaticDeviceNumber != "0007" {
		t.Fatalf("receipt details did not round-trip: %+v", profile)
	}
	const clear = `{"automatic_device_number":null,"time_zone":null,"send_settlement_details":false}`
	response = adminRequest(fixture.handler, http.MethodPatch, path, clear, fixture.cookie, fixture.nonce)
	if response.Code != http.StatusOK {
		t.Fatalf("clear fiscal receipt details: %d %s", response.Code, response.Body)
	}
	profile = get()
	if profile.SendSettlementDetails || profile.TimeZone != nil || profile.AutomaticDeviceNumber != nil {
		t.Fatalf("receipt details not cleared: %+v", profile)
	}
}

func TestGeneratedAdministratorIntegrationSalonCRUD(t *testing.T) {
	fixture := newGeneratedAdministratorIntegration(t)
	const companyID int64 = 880010
	const create = `{"company_id":880010,"slug":"generated-salon","name":"Generated Salon","is_active":false,"sort_order":0,"default_payment_profile_id":null,"default_fiscal_profile_id":null,"default_yclients_profile_id":null}`
	for _, nonce := range []string{"", "wrong-nonce"} {
		response := adminRequest(fixture.handler, http.MethodPost, "/admin/salons", create, fixture.cookie, nonce)
		if response.Code != http.StatusForbidden {
			t.Fatalf("generated salon create without matching nonce: %d %s", response.Code, response.Body)
		}
	}
	response := adminRequest(fixture.handler, http.MethodPost, "/admin/salons", create, fixture.cookie, fixture.nonce)
	var calculated struct {
		CreatedAt      time.Time `json:"created_at"`
		UpdatedAt      time.Time `json:"updated_at"`
		SessionVersion int64     `json:"session_version"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &calculated) != nil || calculated.CreatedAt.IsZero() || calculated.UpdatedAt.IsZero() || calculated.SessionVersion != 1 {
		t.Fatalf("generated salon create with false/zero/null fields: %d %s", response.Code, response.Body)
	}
	salon := generatedIntegrationSalon(t, fixture, companyID)
	if salon.CompanyID != companyID || salon.Name != "Generated Salon" || salon.IsActive || salon.SortOrder != 0 || salon.SessionVersion != 1 || salon.CreatedAt.IsZero() || salon.UpdatedAt.IsZero() || salon.DefaultPaymentProfileID != nil || salon.DefaultFiscalProfileID != nil || salon.DefaultYclientsProfileID != nil {
		t.Fatalf("generated create changed required false/zero or server-generated values: %+v", salon)
	}
	query := url.QueryEscape(`{"from":0,"count":30,"sorter":[{"f":"company_id","d":"d"}]}`)
	response = adminRequest(fixture.handler, http.MethodGet, "/admin/salons?params="+query, "", fixture.cookie, "")
	var collection struct {
		Rows []models.Salon `json:"rows"`
		Agg  struct {
			TotCount int64 `json:"tot_count"`
		} `json:"agg"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &collection) != nil || len(collection.Rows) != 2 || collection.Rows[0].CompanyID != companyID {
		t.Fatalf("generated collection is not registered or sorted: %d %s", response.Code, response.Body)
	}
	path := "/admin/salons/" + strconv.FormatInt(companyID, 10)
	var profileID int64
	if err := fixture.conn.QueryRow(context.Background(), `INSERT INTO public.fiscal_profiles(company_id,name) VALUES($1,'Integration demo') RETURNING id`, companyID).Scan(&profileID); err != nil {
		t.Fatal(err)
	}
	response = adminRequest(fixture.handler, http.MethodPatch, path, `{"default_fiscal_profile_id":`+strconv.FormatInt(profileID, 10)+`}`, fixture.cookie, fixture.nonce)
	if response.Code != http.StatusOK {
		t.Fatalf("generated nullable reference assignment: %d %s", response.Code, response.Body)
	}
	bound := generatedIntegrationSalon(t, fixture, companyID)
	if bound.DefaultFiscalProfileID == nil || *bound.DefaultFiscalProfileID != profileID {
		t.Fatalf("nullable reference assignment was not persisted: %+v", bound)
	}
	response = adminRequest(fixture.handler, http.MethodPatch, path+"?company_id=880011", `{"name":"Updated Salon","default_fiscal_profile_id":null}`, fixture.cookie, fixture.nonce)
	if response.Code != http.StatusOK {
		t.Fatalf("generated partial salon update: %d %s", response.Code, response.Body)
	}
	updated := generatedIntegrationSalon(t, fixture, companyID)
	if updated.Name != "Updated Salon" || updated.CompanyID != companyID || updated.Slug != salon.Slug || updated.IsActive || updated.SortOrder != 0 || updated.DefaultFiscalProfileID != nil || updated.SessionVersion != bound.SessionVersion+1 || !updated.CreatedAt.Equal(salon.CreatedAt) {
		t.Fatalf("generated partial PATCH lost omitted fields or ignored path key: before=%+v after=%+v", salon, updated)
	}
	// Client-provided metadata and the body/query company key must not replace
	// the authoritative path key or overwrite database-managed version/time.
	for _, body := range []string{
		`{"company_id":880011}`,
		`{"session_version":900}`,
		`{"created_at":"2000-01-01T00:00:00Z"}`,
		`{"updated_at":"2000-01-01T00:00:00Z"}`,
	} {
		response = adminRequest(fixture.handler, http.MethodPatch, path, body, fixture.cookie, fixture.nonce)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("generated immutable-field update was not rejected: %d %s", response.Code, response.Body)
		}
		retained := generatedIntegrationSalon(t, fixture, companyID)
		if retained.CompanyID != companyID || retained.SessionVersion != updated.SessionVersion || !retained.CreatedAt.Equal(salon.CreatedAt) || retained.UpdatedAt.Year() == 2000 {
			t.Fatalf("generated PATCH wrote an immutable field: %+v", retained)
		}
	}
	response = adminRequest(fixture.handler, http.MethodPost, "/admin/salons", `{"company_id":880012,"slug":"injected-created-at","name":"Injected","is_active":true,"sort_order":0,"created_at":"2000-01-01T00:00:00Z"}`, fixture.cookie, fixture.nonce)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("generated create accepted server-calculated timestamp: %d %s", response.Code, response.Body)
	}
	var injected int
	if err := fixture.conn.QueryRow(context.Background(), `SELECT count(*) FROM public.salons WHERE company_id IN (880011,880012)`).Scan(&injected); err != nil || injected != 0 {
		t.Fatalf("body/query key moved the salon: count=%d error=%v", injected, err)
	}
	response = adminRequest(fixture.handler, http.MethodGet, "/admin/salons/99999999", "", fixture.cookie, "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing generated salon detail: %d %s", response.Code, response.Body)
	}
	response = adminRequest(fixture.handler, http.MethodGet, "/admin/permissions", "", fixture.cookie, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"salon.list"`) {
		t.Fatalf("generated permissions are not exposed to administrator: %d %s", response.Code, response.Body)
	}
}

func TestGeneratedAdministratorIntegrationIsolationAndRevocation(t *testing.T) {
	fixture := newGeneratedAdministratorIntegration(t)
	if response := adminRequest(fixture.handler, http.MethodGet, "/admin/salons", "", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous generated collection: %d %s", response.Code, response.Body)
	}
	if response := fixture.public.request(http.MethodGet, "/salons", "", ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "salon-one") {
		t.Fatalf("public salon discovery changed: %d %s", response.Code, response.Body)
	}
	fixture.public.bind(t)
	customer := fixture.public.start(t, `{"phone":"79991234567"}`)
	for _, path := range []string{"/admin/salons", "/admin/salons/781898"} {
		response := fixture.public.request(http.MethodGet, path, "", customer.Customer.SessionID)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("kiosk/customer session authorized generated endpoint %s: %d %s", path, response.Code, response.Body)
		}
	}
	response := adminRequest(fixture.handler, http.MethodPatch, "/admin/salons/781898", `{"name":"Unauthorized"}`, fixture.cookie, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("generated update accepted missing nonce: %d %s", response.Code, response.Body)
	}
	if _, err := fixture.conn.Exec(context.Background(), `UPDATE public.users SET banned=true WHERE id=$1`, fixture.user.ID); err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/admin/salons", ""},
		{http.MethodGet, "/admin/salons/781898", ""},
		{http.MethodPatch, "/admin/salons/781898", `{"name":"Revoked"}`},
	} {
		response := adminRequest(fixture.handler, request.method, request.path, request.body, fixture.cookie, fixture.nonce)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("revoked administrator reached %s %s: %d %s", request.method, request.path, response.Code, response.Body)
		}
	}
	var name string
	if err := fixture.conn.QueryRow(context.Background(), `SELECT name FROM public.salons WHERE company_id=781898`).Scan(&name); err != nil || name != "Salon 781898" {
		t.Fatalf("unauthorized/revoked write changed persisted salon: %q %v", name, err)
	}
	if response := adminRequest(fixture.handler, http.MethodGet, "/admin/unknown-generated-path", "", fixture.cookie, ""); response.Code != http.StatusNotFound {
		t.Fatalf("unknown administrator path no longer returns404: %d %s", response.Code, response.Body)
	}
}

func TestGeneratedAdministratorPermissionsFailClosed(t *testing.T) {
	for _, item := range []struct {
		name          string
		method        string
		permission    string
		authenticated bool
		nonce         string
		expected      int
	}{
		{"anonymous", http.MethodGet, "salon.list", false, "", http.StatusUnauthorized},
		{"administrator read", http.MethodGet, "salon.list", true, "", http.StatusNoContent},
		{"unknown permission", http.MethodGet, "salon.delete", true, "", http.StatusForbidden},
		{"mutation missing nonce", http.MethodPatch, "salon.update", true, "", http.StatusForbidden},
		{"authorized mutation", http.MethodPatch, "salon.update", true, adminTestResponse().Auth.Token, http.StatusNoContent},
	} {
		t.Run(item.name, func(t *testing.T) {
			stored := newMemorySession()
			if item.authenticated {
				if err := stored.Set("test_admin", true); err != nil {
					t.Fatal(err)
				}
			}
			checker := generatedAdminPermissionChecker(&adminTestService{}, map[string]struct{}{"salon.list": {}, "salon.update": {}})
			handler := webapp.BuildRouter(&webapp.AppContext{PermissionChecker: checker}, []webapp.Route{
				webapp.NewRoute(item.method, "/admin/permission-test", webapp.WithPermission(item.permission), webapp.WithHandler(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })),
			}, webapp.SessionMiddleware(func(http.ResponseWriter, *http.Request) (session.Session, error) { return stored, nil }))
			request := httptest.NewRequest(item.method, "/admin/permission-test", nil)
			request.Header.Set(adminauth.SessionHeader, item.nonce)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != item.expected {
				t.Fatalf("permission check status=%d want=%d body=%s", response.Code, item.expected, response.Body)
			}
		})
	}
}
