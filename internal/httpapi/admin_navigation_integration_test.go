package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dronm/modelbind"
	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/pronogti/internal/adminnav"
	websession "github.com/dronm/webapp/session"
	"golang.org/x/crypto/bcrypt"
)

func adminNavigationPatch(t *testing.T, value string) adminnav.Patch {
	t.Helper()
	var patch adminnav.Patch
	if err := json.Unmarshal([]byte(value), &patch); err != nil {
		t.Fatal(err)
	}
	return patch
}

func adminNavigationCreate(t *testing.T, service *adminnav.Service, menu adminnav.MainMenu) int {
	t.Helper()
	result, err := service.CreateMenu(context.Background(), menu)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := result["id"].(int)
	if !ok || id <= 0 {
		t.Fatalf("insert returned an invalid menu key: %#v", result)
	}
	return id
}

func TestAdminNavigationIntegrationManifestPersistence(t *testing.T) {
	ctx := context.Background()
	provider, conn := adminIntegrationDatabase(t, nil)
	service := adminnav.NewService(provider)
	manifest := adminnav.ApplicationRouteSyncInput{Items: []adminnav.ApplicationRouteManifestItem{
		{Name: "main", Path: "/", Descr: "Home from manifest", Section: "Forms", MenuAvailable: true},
		{Name: "salons", Path: "/salons", Descr: "Salons", Section: "Forms", MenuAvailable: true},
		{Name: "salonEdit", Path: "/salons/:id", Descr: "Edit salon", Section: "Forms", MenuAvailable: false},
	}}
	if result, err := service.SyncRoutes(ctx, manifest); err != nil || result.Registered != 3 || result.Deactivated != 3 {
		t.Fatalf("initial manifest synchronization: %+v %v", result, err)
	}
	var routeID int
	if err := conn.QueryRow(ctx, `SELECT id FROM public.application_routes WHERE name='salons'`).Scan(&routeID); err != nil {
		t.Fatal(err)
	}
	if changed, err := service.UpdateRoute(ctx, routeID, adminNavigationPatch(t, `{"descr":"Administrator label","section":"Custom section","icon":"pi pi-star"}`)); err != nil || changed.RowsAffected != 1 {
		t.Fatalf("edit route metadata: %+v %v", changed, err)
	}
	manifest.Items[1].Path = "/salon-list"
	manifest.Items[1].Descr = "Replacement manifest label"
	manifest.Items = manifest.Items[:2]
	if result, err := service.SyncRoutes(ctx, manifest); err != nil || result.Registered != 2 || result.Deactivated != 1 {
		t.Fatalf("repeat manifest synchronization: %+v %v", result, err)
	}
	route, err := service.Route(ctx, routeID)
	if err != nil || route.ID != routeID || route.Path != "/salon-list" || route.Descr != "Administrator label" || route.Section != "Custom section" || route.Icon == nil || *route.Icon != "pi pi-star" || !route.IsActive {
		t.Fatalf("sync lost stable identity or administrator metadata: %+v %v", route, err)
	}
	if _, err := service.UpdateRoute(ctx, routeID, adminNavigationPatch(t, `{"path":"/injected"}`)); !errors.Is(err, adminnav.ErrInvalid) {
		t.Fatalf("technical registry fields were editable: %v", err)
	}
	options, err := service.RouteAutocomplete(ctx, "Administrator", 20)
	if err != nil || len(options) != 1 || options[0].ID != routeID {
		t.Fatalf("route autocomplete: %+v %v", options, err)
	}
	options, err = service.RouteAutocomplete(ctx, "salonEdit", 20)
	if err != nil || len(options) != 0 {
		t.Fatalf("deactivated or parameterized route offered for menus: %+v %v", options, err)
	}
	var hiddenID int
	if err := conn.QueryRow(ctx, `SELECT id FROM public.application_routes WHERE name='salonEdit'`).Scan(&hiddenID); err != nil {
		t.Fatal(err)
	}
	role := "admin"
	if _, err := service.CreateMenu(ctx, adminnav.MainMenu{RoleID: &role, Caption: "Inactive route", RouteID: &hiddenID, IsActive: true}); !errors.Is(err, adminnav.ErrInvalid) {
		t.Fatalf("menu accepted an unavailable route: %v", err)
	}
	before := route.Path
	invalidManifest := adminnav.ApplicationRouteSyncInput{Items: []adminnav.ApplicationRouteManifestItem{
		{Name: "salons", Path: "//attacker.invalid", Descr: "Injected", Section: "Forms", MenuAvailable: true},
	}}
	if _, err := service.SyncRoutes(ctx, invalidManifest); !errors.Is(err, adminnav.ErrInvalid) {
		t.Fatalf("external SPA route was accepted: %v", err)
	}
	route, err = service.Route(ctx, routeID)
	if err != nil || route.Path != before || !route.IsActive {
		t.Fatalf("rejected sync partially changed registry: %+v %v", route, err)
	}
	boundMenuID := adminNavigationCreate(t, service, adminnav.MainMenu{RoleID: &role, Caption: "Bound salon menu", RouteID: &routeID, IsActive: true})
	manifest.Items = manifest.Items[:1]
	if _, err := service.SyncRoutes(ctx, manifest); err != nil {
		t.Fatal(err)
	}
	if changed, err := service.UpdateMenu(ctx, boundMenuID, adminNavigationPatch(t, `{"caption":"Unavailable route","is_active":false}`)); err != nil || changed.RowsAffected != 1 {
		t.Fatalf("existing stale route reference prevented menu maintenance: %+v %v", changed, err)
	}
}

func TestAdminNavigationIntegrationHierarchyAndOwnerIsolation(t *testing.T) {
	ctx := context.Background()
	provider, conn := adminIntegrationDatabase(t, nil)
	service := adminnav.NewService(provider)
	var userID, otherID, routeID int
	if err := conn.QueryRow(ctx, `INSERT INTO public.users(name,role_id,locale_id) VALUES('Menu Operator','admin','ru') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `INSERT INTO public.users(name,role_id,locale_id) VALUES('Other Operator','admin','ru') RETURNING id`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT id FROM public.application_routes WHERE name='main'`).Scan(&routeID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `DELETE FROM public.main_menus`); err != nil {
		t.Fatal(err)
	}
	role := "admin"
	groupID := adminNavigationCreate(t, service, adminnav.MainMenu{RoleID: &role, Caption: "Role group", IsActive: true})
	childID := adminNavigationCreate(t, service, adminnav.MainMenu{RoleID: &role, ParentID: &groupID, RouteID: &routeID, Caption: "Later", SortOrder: 20, IsActive: true})
	firstID := adminNavigationCreate(t, service, adminnav.MainMenu{RoleID: &role, ParentID: &groupID, RouteID: &routeID, Caption: "Earlier", SortOrder: 10, IsActive: true})
	nestedID := adminNavigationCreate(t, service, adminnav.MainMenu{RoleID: &role, ParentID: &groupID, Caption: "Nested group", SortOrder: 30, IsActive: true})
	personalID := adminNavigationCreate(t, service, adminnav.MainMenu{UserID: &userID, Caption: "Private menu", RouteID: &routeID, IsActive: true})
	personal, err := service.ForUser(ctx, adminauth.User{ID: int64(userID), RoleID: "admin"})
	if err != nil || len(personal) != 1 || personal[0].ID != personalID {
		t.Fatalf("personal menu did not replace role menu: %+v %v", personal, err)
	}
	other, err := service.ForUser(ctx, adminauth.User{ID: int64(otherID), RoleID: "admin"})
	if err != nil || len(other) != 1 || other[0].ID != groupID || len(other[0].Children) != 3 || other[0].Children[0].ID != firstID || other[0].Children[1].ID != childID {
		t.Fatalf("role fallback or ordering: %+v %v", other, err)
	}
	if _, err := service.CreateMenu(ctx, adminnav.MainMenu{UserID: &userID, ParentID: &groupID, Caption: "Cross-owner", IsActive: true}); !errors.Is(err, adminnav.ErrInvalid) {
		t.Fatalf("cross-owner parent accepted: %v", err)
	}
	if _, err := service.UpdateMenu(ctx, groupID, adminNavigationPatch(t, `{"parent_id":`+jsonNumber(nestedID)+`}`)); !errors.Is(err, adminnav.ErrInvalid) {
		t.Fatalf("cycle accepted: %v", err)
	}
	if _, err := service.UpdateMenu(ctx, groupID, adminNavigationPatch(t, `{"route_id":`+jsonNumber(routeID)+`}`)); !errors.Is(err, adminnav.ErrInvalid) {
		t.Fatalf("group with children converted to a route: %v", err)
	}
	if _, err := service.UpdateMenu(ctx, groupID, adminNavigationPatch(t, `{"role_id":null,"user_id":`+jsonNumber(userID)+`}`)); !errors.Is(err, adminnav.ErrInvalid) {
		t.Fatalf("group changed owner without its descendants: %v", err)
	}
	if changed, err := service.UpdateMenu(ctx, childID, adminNavigationPatch(t, `{"parent_id":null,"sort_order":-10,"caption":"Detached"}`)); err != nil || changed.RowsAffected != 1 {
		t.Fatalf("explicit-null reparent and reorder: %+v %v", changed, err)
	}
	// A new service instance must observe the edit; no state belongs to a handler.
	service = adminnav.NewService(provider)
	child, err := service.Menu(ctx, childID)
	if err != nil || child.ParentID != nil || child.SortOrder != -10 || child.Caption != "Detached" {
		t.Fatalf("menu change was not persisted: %+v %v", child, err)
	}
	if changed, err := service.DeleteMenu(ctx, groupID); err != nil || changed.RowsAffected != 1 {
		t.Fatalf("delete menu group: %+v %v", changed, err)
	}
	for _, id := range []int{groupID, firstID, nestedID} {
		if _, err := service.Menu(ctx, id); !errors.Is(err, adminnav.ErrNotFound) {
			t.Fatalf("deleted group descendant %d survived: %v", id, err)
		}
	}
	if _, err := service.Menu(ctx, childID); err != nil {
		t.Fatalf("deleting a group removed a detached menu item: %v", err)
	}
	if changed, err := service.UpdateMenu(ctx, personalID, adminNavigationPatch(t, `{"is_active":false}`)); err != nil || changed.RowsAffected != 1 {
		t.Fatalf("disable personal menu: %+v %v", changed, err)
	}
	personal, err = service.ForUser(ctx, adminauth.User{ID: int64(userID), RoleID: "admin"})
	if err != nil || len(personal) != 1 || personal[0].ID != childID {
		t.Fatalf("inactive personal menu prevented role fallback: %+v %v", personal, err)
	}
}

func jsonNumber(value int) string {
	result, _ := json.Marshal(value)
	return string(result)
}

func TestAdminNavigationIntegrationCollectionReferences(t *testing.T) {
	ctx := context.Background()
	provider, conn := adminIntegrationDatabase(t, nil)
	service := adminnav.NewService(provider)
	var userID, customerID, routeID int
	if err := conn.QueryRow(ctx, `INSERT INTO public.users(name,role_id,password_hash,locale_id) VALUES('Menu Owner','admin',$1,'ru') RETURNING id`, adminIntegrationHash(t, "reference-password")).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `INSERT INTO public.users(name,role_id,locale_id) VALUES('End Customer','client','ru') RETURNING id`).Scan(&customerID); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT id FROM public.application_routes WHERE name='main'`).Scan(&routeID); err != nil {
		t.Fatal(err)
	}
	groupID := adminNavigationCreate(t, service, adminnav.MainMenu{UserID: &userID, Caption: "Owner group", IsActive: true})
	childID := adminNavigationCreate(t, service, adminnav.MainMenu{UserID: &userID, ParentID: &groupID, RouteID: &routeID, Caption: "Owner child", IsActive: true})
	menus, err := service.ListMenus(ctx, modelbind.CollectionParams{Count: 100})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, menu := range menus.Rows {
		if menu.ID == childID {
			found = menu.User != nil && menu.User.Keys.ID == userID && menu.Parent != nil && menu.Parent.Keys.ID == groupID && menu.ApplicationRoute != nil && menu.ApplicationRoute.Keys.ID == routeID && menu.RouteName != nil && *menu.RouteName == "main"
		}
	}
	if !found {
		t.Fatalf("menu collection omitted reference projections: %+v", menus.Rows)
	}
	users, err := service.ListUsers(ctx, modelbind.CollectionParams{Count: 100})
	if err != nil || len(users.Rows) != 1 || users.Rows[0].ID != userID {
		t.Fatalf("user lookup included non-administrator: %+v %v", users, err)
	}
	encoded, err := json.Marshal(users)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"password", "session_version", "pwd", "banned", "logins"} {
		if strings.Contains(string(encoded), field) {
			t.Fatalf("user reference exposed %q", field)
		}
	}
	if _, err := service.User(ctx, customerID); !errors.Is(err, adminnav.ErrNotFound) {
		t.Fatalf("non-administrator available as menu owner: %v", err)
	}
	if _, err := service.CreateMenu(ctx, adminnav.MainMenu{UserID: &customerID, Caption: "Customer menu", IsActive: true}); !errors.Is(err, adminnav.ErrInvalid) {
		t.Fatalf("non-administrator accepted as menu owner: %v", err)
	}
}

func TestAdminNavigationIntegrationConcurrentReparentCannotCreateCycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider, _ := adminIntegrationDatabase(t, nil)
	service := adminnav.NewService(provider)
	role := "admin"
	firstID := adminNavigationCreate(t, service, adminnav.MainMenu{RoleID: &role, Caption: "First group", IsActive: true})
	secondID := adminNavigationCreate(t, service, adminnav.MainMenu{RoleID: &role, Caption: "Second group", IsActive: true})
	firstPatch := adminNavigationPatch(t, `{"parent_id":`+jsonNumber(secondID)+`}`)
	secondPatch := adminNavigationPatch(t, `{"parent_id":`+jsonNumber(firstID)+`}`)
	start := make(chan struct{})
	errorsReturned := make(chan error, 2)
	go func() {
		<-start
		_, err := service.UpdateMenu(ctx, firstID, firstPatch)
		errorsReturned <- err
	}()
	go func() {
		<-start
		_, err := service.UpdateMenu(ctx, secondID, secondPatch)
		errorsReturned <- err
	}()
	close(start)
	succeeded, rejected := 0, 0
	for range 2 {
		err := <-errorsReturned
		if err == nil {
			succeeded++
		} else if errors.Is(err, adminnav.ErrInvalid) {
			rejected++
		} else {
			t.Fatalf("concurrent reparent failed unexpectedly: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent inverse moves produced %d successes and %d rejections", succeeded, rejected)
	}
	first, firstErr := service.Menu(ctx, firstID)
	second, secondErr := service.Menu(ctx, secondID)
	if firstErr != nil || secondErr != nil || (first.ParentID != nil && second.ParentID != nil) {
		t.Fatalf("concurrent moves persisted a cycle: %+v %+v %v %v", first, second, firstErr, secondErr)
	}
}

// Real PostgreSQL services pass through the same HTTP modelbind adapters used
// by the shared Vue APIs. Session persistence itself is covered by the separate
// Redis integration tests; this test needs no configured Redis instance.
func TestAdminNavigationIntegrationHTTPRealAdministrator(t *testing.T) {
	ctx := context.Background()
	provider, conn := adminIntegrationDatabase(t, nil)
	repository := adminauth.NewPostgresRepository(provider)
	const password = "navigation-integration-password"
	operator, err := repository.Provision(ctx, "Operator", adminIntegrationHash(t, password), false)
	if err != nil {
		t.Fatal(err)
	}
	var routeID int
	if err := conn.QueryRow(ctx, `SELECT id FROM public.application_routes WHERE name='main'`).Scan(&routeID); err != nil {
		t.Fatal(err)
	}
	auth := adminauth.NewService(repository, adminauth.Options{MaxLifeTime: time.Hour, BcryptCost: bcrypt.MinCost})
	manager := &adminTestManager{}
	sessions := NewAdminSessions(manager, websession.Config{
		CookieName: "admin_test", HTTPOnly: true, SameSite: http.SameSiteLaxMode,
		MaxLifeTime: 3600, MaxIdleTime: 600,
	}, "/", auth.Invalidate)
	handler := NewAdminHandler(auth, sessions, AdminHTTPOptions{
		AllowedOrigins: []string{"http://localhost:5174"}, RequestTimeout: 5 * time.Second,
		Navigation: adminnav.NewService(provider),
	})
	if response := adminRequest(handler, http.MethodGet, "/admin/main-menus", "", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous navigation read: %d %s", response.Code, response.Body)
	}
	login := adminRequest(handler, http.MethodPost, "/admin/login", `{"name":"Operator","pwd":"`+password+`"}`, nil, "")
	identity := adminIntegrationResponse(t, login)
	if identity.Auth == nil || identity.User == nil {
		t.Fatal("login did not issue an administrator identity")
	}
	cookie := adminIntegrationCookie(t, login, "admin_test")
	createBody := `{"role_id":"admin","user_id":null,"parent_id":null,"caption":"HTTP menu","route_id":` + jsonNumber(routeID) + `,"icon":"pi pi-home","sort_order":40,"is_active":true}`
	if response := adminRequest(handler, http.MethodPost, "/admin/main-menus", createBody, cookie, ""); response.Code != http.StatusForbidden {
		t.Fatalf("write without nonce: %d %s", response.Code, response.Body)
	}
	create := adminRequest(handler, http.MethodPost, "/admin/main-menus", createBody, cookie, identity.Auth.Token)
	var key struct {
		ID int `json:"id"`
	}
	if create.Code != http.StatusCreated || json.Unmarshal(create.Body.Bytes(), &key) != nil || key.ID <= 0 {
		t.Fatalf("shared API flat create body: %d %s", create.Code, create.Body)
	}
	path := "/admin/main-menus/" + jsonNumber(key.ID)
	update := adminRequest(handler, http.MethodPatch, path, `{"icon":null,"caption":"HTTP edited","sort_order":-1}`, cookie, identity.Auth.Token)
	if update.Code != http.StatusOK || !strings.Contains(update.Body.String(), `"rows_affected":1`) {
		t.Fatalf("shared API partial PATCH body: %d %s", update.Code, update.Body)
	}
	detail := adminRequest(handler, http.MethodGet, path, "", cookie, "")
	var menu adminnav.MainMenu
	if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &menu) != nil || menu.Icon != nil || menu.Caption != "HTTP edited" || menu.SortOrder != -1 || menu.RoleID == nil || *menu.RoleID != "admin" {
		t.Fatalf("flat PATCH failed to retain omitted fields or explicit null: %d %s", detail.Code, detail.Body)
	}
	// ApplicationRoutesView requests the complete registry in this exact shape.
	allFormsQuery := url.QueryEscape(`{"from":0,"count":10000,"sorter":[{"f":"section","d":"a"},{"f":"descr","d":"a"},{"f":"name","d":"a"}]}`)
	constructorQuery := url.QueryEscape(`{"from":0,"count":10000,"sorter":[{"f":"sort_order","d":"a"},{"f":"id","d":"a"}]}`)
	for _, endpoint := range []string{"/admin/main-menu", "/admin/main-menus/for-user", "/admin/main-menus?params=" + constructorQuery, "/admin/application-routes?params=" + allFormsQuery, "/admin/users", "/admin/users/" + jsonNumber(int(operator.ID))} {
		response := adminRequest(handler, http.MethodGet, endpoint, "", cookie, "")
		if response.Code != http.StatusOK {
			t.Fatalf("administrator navigation read %s: %d %s", endpoint, response.Code, response.Body)
		}
		if strings.Contains(response.Body.String(), "password_hash") || strings.Contains(response.Body.String(), "session_version") {
			t.Fatalf("navigation read %s exposed account secrets", endpoint)
		}
	}
	remove := adminRequest(handler, http.MethodDelete, path, "", cookie, identity.Auth.Token)
	if remove.Code != http.StatusOK || !strings.Contains(remove.Body.String(), `"rows_affected":1`) {
		t.Fatalf("shared API DELETE without request body: %d %s", remove.Code, remove.Body)
	}
	if _, err := conn.Exec(ctx, `UPDATE public.users SET banned=true WHERE id=$1`, operator.ID); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/admin/main-menu", "/admin/main-menus", "/admin/application-routes", "/admin/users"} {
		if response := adminRequest(handler, http.MethodGet, endpoint, "", cookie, ""); response.Code != http.StatusUnauthorized {
			t.Fatalf("revoked account read %s: %d %s", endpoint, response.Code, response.Body)
		}
	}
}
