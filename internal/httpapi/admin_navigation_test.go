package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dronm/modelbind"
	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/pronogti/internal/adminnav"
	"github.com/dronm/session"
	wmodels "github.com/dronm/webapp/models"
	websession "github.com/dronm/webapp/session"
)

type navigationTestService struct {
	calls    []string
	id       int
	patch    adminnav.Patch
	created  adminnav.MainMenu
	params   modelbind.CollectionParams
	user     adminauth.User
	manifest adminnav.ApplicationRouteSyncInput
	query    string
	limit    int
	err      error
}

func (s *navigationTestService) ListMenus(_ context.Context, params modelbind.CollectionParams) (wmodels.CollectionResponse[*adminnav.MainMenuList], error) {
	s.calls = append(s.calls, "menus")
	s.params = params
	return wmodels.CollectionResponse[*adminnav.MainMenuList]{Rows: []*adminnav.MainMenuList{}, Agg: &wmodels.TotCount{}}, s.err
}
func (s *navigationTestService) Menu(_ context.Context, id int) (*adminnav.MainMenu, error) {
	s.calls = append(s.calls, "menu")
	s.id = id
	return &adminnav.MainMenu{ID: id, Caption: "Test"}, s.err
}
func (s *navigationTestService) CreateMenu(_ context.Context, input adminnav.MainMenu) (map[string]any, error) {
	s.calls = append(s.calls, "create")
	s.created = input
	return map[string]any{"id": 17}, s.err
}
func (s *navigationTestService) UpdateMenu(_ context.Context, id int, patch adminnav.Patch) (wmodels.RowsAffectedResponse, error) {
	s.calls = append(s.calls, "update")
	s.id, s.patch = id, patch
	return wmodels.RowsAffectedResponse{RowsAffected: 1}, s.err
}
func (s *navigationTestService) DeleteMenu(_ context.Context, id int) (wmodels.RowsAffectedResponse, error) {
	s.calls = append(s.calls, "delete")
	s.id = id
	return wmodels.RowsAffectedResponse{RowsAffected: 1}, s.err
}
func (s *navigationTestService) ForUser(_ context.Context, user adminauth.User) ([]*adminnav.MenuItem, error) {
	s.calls = append(s.calls, "for-user")
	s.user = user
	return []*adminnav.MenuItem{{ID: 17, Caption: "Test", Route: "main", Children: []*adminnav.MenuItem{}}}, s.err
}
func (s *navigationTestService) ListRoutes(_ context.Context, params modelbind.CollectionParams) (wmodels.CollectionResponse[*adminnav.ApplicationRoute], error) {
	s.calls = append(s.calls, "routes")
	s.params = params
	return wmodels.CollectionResponse[*adminnav.ApplicationRoute]{Rows: []*adminnav.ApplicationRoute{}, Agg: &wmodels.TotCount{}}, s.err
}
func (s *navigationTestService) Route(_ context.Context, id int) (*adminnav.ApplicationRoute, error) {
	s.calls = append(s.calls, "route")
	s.id = id
	return &adminnav.ApplicationRoute{ID: id, Name: "main", Path: "/", Descr: "Test", Section: "General"}, s.err
}
func (s *navigationTestService) UpdateRoute(_ context.Context, id int, patch adminnav.Patch) (wmodels.RowsAffectedResponse, error) {
	s.calls = append(s.calls, "update-route")
	s.id, s.patch = id, patch
	return wmodels.RowsAffectedResponse{RowsAffected: 1}, s.err
}
func (s *navigationTestService) SyncRoutes(_ context.Context, input adminnav.ApplicationRouteSyncInput) (adminnav.ApplicationRouteSyncResult, error) {
	s.calls = append(s.calls, "sync")
	s.manifest = input
	return adminnav.ApplicationRouteSyncResult{Registered: len(input.Items)}, s.err
}
func (s *navigationTestService) RouteAutocomplete(_ context.Context, query string, limit int) ([]*adminnav.ApplicationRouteOption, error) {
	s.calls = append(s.calls, "autocomplete")
	s.query, s.limit = query, limit
	return []*adminnav.ApplicationRouteOption{}, s.err
}
func (s *navigationTestService) ListUsers(_ context.Context, params modelbind.CollectionParams) (wmodels.CollectionResponse[*adminnav.UserReference], error) {
	s.calls = append(s.calls, "users")
	s.params = params
	return wmodels.CollectionResponse[*adminnav.UserReference]{Rows: []*adminnav.UserReference{{ID: 1, Name: "Admin", RoleID: "admin"}}, Agg: &wmodels.TotCount{TotCount: 1}}, s.err
}
func (s *navigationTestService) User(_ context.Context, id int) (*adminnav.UserReference, error) {
	s.calls = append(s.calls, "user")
	s.id = id
	return &adminnav.UserReference{ID: id, Name: "Admin", RoleID: "admin"}, s.err
}

type navigationTestAuth struct {
	*adminTestService
	currentCalls int
	role         string
	revoked      bool
}

func (s *navigationTestAuth) Current(ctx context.Context, sess session.Session) (adminauth.Response, error) {
	s.currentCalls++
	response, err := s.adminTestService.Current(ctx, sess)
	if s.revoked {
		return adminauth.Response{}, nil
	}
	if response.User != nil && s.role != "" {
		response.User.RoleID = s.role
	}
	return response, err
}

func navigationTestHandler(t *testing.T) (http.Handler, *http.Cookie, *adminTestManager, *navigationTestAuth, *navigationTestService) {
	t.Helper()
	manager := &adminTestManager{}
	auth := &navigationTestAuth{adminTestService: &adminTestService{}}
	navigation := &navigationTestService{}
	sessions := NewAdminSessions(manager, websession.Config{CookieName: "admin_test", HTTPOnly: true, SameSite: http.SameSiteLaxMode, MaxLifeTime: 3600, MaxIdleTime: 600}, "/", auth.Invalidate)
	handler := NewAdminHandler(auth, sessions, AdminHTTPOptions{AllowedOrigins: []string{"http://localhost:5174"}, RequestTimeout: time.Second, Navigation: navigation})
	login := adminRequest(handler, http.MethodPost, "/admin/login", `{"name":"admin","pwd":"password"}`, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login: %d %s", login.Code, login.Body)
	}
	return handler, login.Result().Cookies()[0], manager, auth, navigation
}

func TestAdministratorNavigationUsesSharedCollectionAndLookupContracts(t *testing.T) {
	h, cookie, _, auth, navigation := navigationTestHandler(t)
	paths := []string{
		"/admin/main-menu?user_id=999&role_id=client", "/admin/main-menus/for-user",
		"/admin/main-menus", "/admin/main-menus/17", "/admin/main-menus/routes/autocomplete?q=main&limit=7",
		"/admin/application-routes", "/admin/application-routes/17", "/admin/users", "/admin/users/1",
	}
	for _, path := range paths {
		before := auth.currentCalls
		got := adminRequest(h, http.MethodGet, path, "", cookie, "")
		if got.Code != http.StatusOK || auth.currentCalls != before+1 {
			t.Fatalf("read did not verify fresh identity %s: %d %s", path, got.Code, got.Body)
		}
		if strings.Contains(got.Body.String(), "password") || strings.Contains(got.Body.String(), "session") {
			t.Fatalf("lookup exposed credential fields: %s", got.Body)
		}
	}
	if navigation.user.ID != 1 || navigation.user.RoleID != "admin" || navigation.query != "main" || navigation.limit != 7 {
		t.Fatalf("client values influenced authenticated lookup: user=%+v query=%q limit=%d", navigation.user, navigation.query, navigation.limit)
	}
	params := url.QueryEscape(`{"from":4,"count":15,"filter":[],"sorter":[]}`)
	got := adminRequest(h, http.MethodGet, "/admin/main-menus?params="+params, "", cookie, "")
	if got.Code != http.StatusOK || navigation.params.From != 4 || navigation.params.Count != 15 {
		t.Fatalf("shared collection parameters were not bound: %d %s %+v", got.Code, got.Body, navigation.params)
	}
	var collection struct {
		Rows []json.RawMessage `json:"rows"`
		Agg  struct {
			Total int `json:"tot_count"`
		} `json:"agg"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &collection); err != nil || collection.Rows == nil {
		t.Fatalf("invalid collection envelope: %s %v", got.Body, err)
	}
}

func TestAdministratorNavigationRejectsPublicSessionsAndRevokedRoles(t *testing.T) {
	h, cookie, manager, auth, navigation := navigationTestHandler(t)
	paths := []string{"/admin/main-menu", "/admin/main-menus", "/admin/main-menus/for-user", "/admin/main-menus/17", "/admin/main-menus/routes/autocomplete", "/admin/application-routes", "/admin/application-routes/17", "/admin/users", "/admin/users/1"}
	for _, path := range paths {
		if got := adminRequest(h, http.MethodGet, path, "", nil, ""); got.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous read %s: %d %s", path, got.Code, got.Body)
		}
	}
	for i, kind := range []string{"customer", "kiosk"} {
		id := "a" + strings.Repeat(strconvDigit(i+1), 32)
		sess, _ := manager.SessionStart(id)
		_ = sess.Set(kind, map[string]any{"company_id": 1, "role_id": "admin"})
		got := adminRequest(h, http.MethodGet, "/admin/main-menus?role_id=admin", "", &http.Cookie{Name: "admin_test", Value: id}, "")
		if got.Code != http.StatusUnauthorized {
			t.Fatalf("%s session authenticated administrator: %d %s", kind, got.Code, got.Body)
		}
	}
	for _, role := range []string{"client", "kiosk"} {
		auth.role = role
		if got := adminRequest(h, http.MethodGet, "/admin/main-menus", "", cookie, ""); got.Code != http.StatusUnauthorized {
			t.Fatalf("fresh role %s accepted: %d", role, got.Code)
		}
	}
	auth.role, auth.revoked = "", true
	if got := adminRequest(h, http.MethodGet, "/admin/main-menu", "", cookie, ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session accepted: %d", got.Code)
	}
	if len(navigation.calls) != 0 {
		t.Fatalf("unauthorized request reached navigation storage: %v", navigation.calls)
	}
}

func strconvDigit(value int) string {
	return string(rune('0' + value))
}

func TestAdministratorNavigationRequiresNonceOnEveryMutation(t *testing.T) {
	h, cookie, _, auth, navigation := navigationTestHandler(t)
	mutations := []struct{ method, path, body string }{
		{http.MethodPost, "/admin/main-menus", `{"role_id":"admin","user_id":null,"parent_id":null,"route_id":null,"caption":"Group","icon":null,"sort_order":0,"is_active":true}`},
		{http.MethodPatch, "/admin/main-menus/17", `{"caption":"Changed","icon":null}`},
		{http.MethodDelete, "/admin/main-menus/17", ""},
		{http.MethodPatch, "/admin/application-routes/17", `{"descr":"Changed","icon":null}`},
		{http.MethodPost, "/admin/application-routes/sync", `{"items":[{"name":"main","path":"/","descr":"Home","section":"General","icon":null,"menu_available":true}]}`},
	}
	for _, operation := range mutations {
		if got := adminRequest(h, operation.method, operation.path, operation.body, nil, strings.Repeat("a", 64)); got.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous mutation %s %s: %d %s", operation.method, operation.path, got.Code, got.Body)
		}
		auth.role = "client"
		if got := adminRequest(h, operation.method, operation.path, operation.body, cookie, strings.Repeat("a", 64)); got.Code != http.StatusUnauthorized {
			t.Fatalf("client role mutation %s %s: %d %s", operation.method, operation.path, got.Code, got.Body)
		}
		auth.role = ""
		for _, nonce := range []string{"", "incorrect", strings.Repeat("a", 63)} {
			before := auth.currentCalls
			got := adminRequest(h, operation.method, operation.path, operation.body, cookie, nonce)
			if got.Code != http.StatusForbidden || auth.currentCalls != before+1 {
				t.Fatalf("mutation accepted invalid nonce %s %s: %d %s", operation.method, operation.path, got.Code, got.Body)
			}
		}
	}
	if len(navigation.calls) != 0 {
		t.Fatalf("mutation without nonce reached storage: %v", navigation.calls)
	}
	for _, operation := range mutations {
		got := adminRequest(h, operation.method, operation.path, operation.body, cookie, strings.Repeat("a", 64))
		expected := http.StatusOK
		if operation.method == http.MethodPost && operation.path == "/admin/main-menus" {
			expected = http.StatusCreated
		}
		if got.Code != expected {
			t.Fatalf("shared library request %s %s: %d %s", operation.method, operation.path, got.Code, got.Body)
		}
		if operation.method == http.MethodPatch || operation.method == http.MethodDelete {
			if !strings.Contains(got.Body.String(), `"affectedRows":1`) {
				t.Fatalf("shared library mutation response: %s", got.Body)
			}
		}
	}
	if len(navigation.calls) != len(mutations) || navigation.created.Caption != "Group" || navigation.created.RoleID == nil || *navigation.created.RoleID != "admin" || navigation.created.UserID != nil || len(navigation.manifest.Items) != 1 {
		t.Fatalf("incorrect bound mutation values: calls=%v created=%+v manifest=%+v", navigation.calls, navigation.created, navigation.manifest)
	}
	auth.revoked = true
	if got := adminRequest(h, http.MethodDelete, "/admin/main-menus/17", "", cookie, strings.Repeat("a", 64)); got.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session nonce authorized mutation: %d", got.Code)
	}
}

func TestAdministratorNavigationUsesPathKeysAndPreservesNullablePatch(t *testing.T) {
	h, cookie, _, _, navigation := navigationTestHandler(t)
	got := adminRequest(h, http.MethodPatch, "/admin/main-menus/17?id=999&caption=ignored", `{"caption":"Changed","parent_id":null,"icon":null}`, cookie, strings.Repeat("a", 64))
	if got.Code != http.StatusOK || navigation.id != 17 || string(navigation.patch["caption"]) != `"Changed"` || string(navigation.patch["parent_id"]) != "null" || string(navigation.patch["icon"]) != "null" {
		t.Fatalf("path or nullable patch changed: %d %s id=%d patch=%+v", got.Code, got.Body, navigation.id, navigation.patch)
	}
	if _, present := navigation.patch["route_id"]; present {
		t.Fatal("absent PATCH field became an update")
	}
	got = adminRequest(h, http.MethodPatch, "/admin/application-routes/21?id=999", `{"section":"Changed"}`, cookie, strings.Repeat("a", 64))
	if got.Code != http.StatusOK || navigation.id != 21 {
		t.Fatalf("route path key was overridden: %d %s id=%d", got.Code, got.Body, navigation.id)
	}
}

func TestAdministratorNavigationRejectsInvalidBodiesAndImmutableFields(t *testing.T) {
	h, cookie, _, _, navigation := navigationTestHandler(t)
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/admin/main-menus", `{"model":{"caption":"Envelope"}}`},
		{http.MethodPost, "/admin/main-menus", `{"caption":"Test","id":9}`},
		{http.MethodPost, "/admin/main-menus", `{"role_id":"admin"}`},
		{http.MethodPost, "/admin/main-menus", `{"caption":"Test","is_active":null}`},
		{http.MethodPost, "/admin/main-menus", `{"caption":"Test","sort_order":null}`},
		{http.MethodPatch, "/admin/main-menus/17", `{"id":999,"caption":"Test"}`},
		{http.MethodPatch, "/admin/main-menus/17", `{"updated_at":"2026-01-01"}`},
		{http.MethodPatch, "/admin/main-menus/17", `{"caption":"Test"}{}`},
		{http.MethodPatch, "/admin/main-menus/17", `{"caption":42}`},
		{http.MethodPatch, "/admin/main-menus/17", `null`},
		{http.MethodPatch, "/admin/main-menus/17", `[]`},
		{http.MethodPatch, "/admin/main-menus/17", `{"caption":"` + strings.Repeat("x", 17000) + `"}`},
		{http.MethodPatch, "/admin/application-routes/17", `{"name":"unsafe"}`},
		{http.MethodPatch, "/admin/application-routes/17", `{"path":"https://attacker.invalid"}`},
		{http.MethodPatch, "/admin/application-routes/17", `{"menu_available":true}`},
		{http.MethodPatch, "/admin/application-routes/17", `{"is_active":true}`},
		{http.MethodPost, "/admin/application-routes/sync", `{"items":[{"name":"main","path":"/","role_id":"admin"}]}`},
		{http.MethodDelete, "/admin/main-menus/-1", ""},
		{http.MethodPatch, "/admin/application-routes/nope", `{}`},
	}
	for _, item := range cases {
		got := adminRequest(h, item.method, item.path, item.body, cookie, strings.Repeat("a", 64))
		if got.Code != http.StatusBadRequest {
			t.Fatalf("invalid request reached service %s %s %s: %d %s", item.method, item.path, item.body[:min(len(item.body), 100)], got.Code, got.Body)
		}
	}
	if len(navigation.calls) != 0 {
		t.Fatalf("invalid request reached navigation storage: %v", navigation.calls)
	}
	for _, path := range []string{"/admin/main-menus?params=invalid", "/admin/users?count=invalid", "/admin/main-menus/routes/autocomplete?limit=101"} {
		if got := adminRequest(h, http.MethodGet, path, "", cookie, ""); got.Code != http.StatusBadRequest {
			t.Fatalf("invalid collection/autocomplete accepted %s: %d", path, got.Code)
		}
	}
}

func TestAdministratorNavigationMapsDomainErrorsAndAllowsMutationPreflight(t *testing.T) {
	h, cookie, _, _, navigation := navigationTestHandler(t)
	navigation.err = adminnav.ErrNotFound
	if got := adminRequest(h, http.MethodGet, "/admin/main-menus/17", "", cookie, ""); got.Code != http.StatusNotFound {
		t.Fatalf("missing menu error: %d %s", got.Code, got.Body)
	}
	navigation.err = adminnav.ErrInvalid
	if got := adminRequest(h, http.MethodPatch, "/admin/main-menus/17", `{"caption":"Changed"}`, cookie, strings.Repeat("a", 64)); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid menu error: %d %s", got.Code, got.Body)
	}
	r := httptest.NewRequest(http.MethodOptions, "/admin/main-menus/17", nil)
	r.Header.Set("Origin", "http://localhost:5174")
	r.Header.Set("Access-Control-Request-Method", http.MethodPatch)
	r.Header.Set("Access-Control-Request-Headers", "Content-Type, X-Admin-Session")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || !strings.Contains(w.Header().Get("Access-Control-Allow-Methods"), http.MethodPatch) || !strings.Contains(w.Header().Get("Access-Control-Allow-Methods"), http.MethodDelete) {
		t.Fatalf("administrator mutation preflight: %d %v", w.Code, w.Header())
	}
}
