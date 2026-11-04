package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/dronm/modelbind"
	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/pronogti/internal/adminnav"
	"github.com/dronm/webapp"
	wmodels "github.com/dronm/webapp/models"
)

// AdminNavigation contains presentation metadata only. Every endpoint still
// checks the authoritative administrator session before accessing this service.
type AdminNavigation interface {
	ListMenus(context.Context, modelbind.CollectionParams) (wmodels.CollectionResponse[*adminnav.MainMenuList], error)
	Menu(context.Context, int) (*adminnav.MainMenu, error)
	CreateMenu(context.Context, adminnav.MainMenu) (map[string]any, error)
	UpdateMenu(context.Context, int, adminnav.Patch) (wmodels.RowsAffectedResponse, error)
	DeleteMenu(context.Context, int) (wmodels.RowsAffectedResponse, error)
	ForUser(context.Context, adminauth.User) ([]*adminnav.MenuItem, error)
	ListRoutes(context.Context, modelbind.CollectionParams) (wmodels.CollectionResponse[*adminnav.ApplicationRoute], error)
	Route(context.Context, int) (*adminnav.ApplicationRoute, error)
	UpdateRoute(context.Context, int, adminnav.Patch) (wmodels.RowsAffectedResponse, error)
	SyncRoutes(context.Context, adminnav.ApplicationRouteSyncInput) (adminnav.ApplicationRouteSyncResult, error)
	RouteAutocomplete(context.Context, string, int) ([]*adminnav.ApplicationRouteOption, error)
	ListUsers(context.Context, modelbind.CollectionParams) (wmodels.CollectionResponse[*adminnav.UserReference], error)
	User(context.Context, int) (*adminnav.UserReference, error)
}

func (h *adminHandler) registerNavigation(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/main-menus", h.navigationMenus)
	mux.HandleFunc("POST /admin/main-menus", h.navigationCreateMenu)
	mux.HandleFunc("GET /admin/main-menus/for-user", h.navigationForUser)
	mux.HandleFunc("GET /admin/main-menus/routes/autocomplete", h.navigationRouteAutocomplete)
	mux.HandleFunc("GET /admin/main-menus/{id}", h.navigationMenu)
	mux.HandleFunc("PATCH /admin/main-menus/{id}", h.navigationUpdateMenu)
	mux.HandleFunc("DELETE /admin/main-menus/{id}", h.navigationDeleteMenu)
	mux.HandleFunc("GET /admin/application-routes", h.navigationRoutes)
	mux.HandleFunc("GET /admin/application-routes/{id}", h.navigationRoute)
	mux.HandleFunc("PATCH /admin/application-routes/{id}", h.navigationUpdateRoute)
	mux.HandleFunc("POST /admin/application-routes/sync", h.navigationSyncRoutes)
	mux.HandleFunc("GET /admin/users", h.navigationUsers)
	mux.HandleFunc("GET /admin/users/{id}", h.navigationUser)
}

func (h *adminHandler) navigationIdentity(w http.ResponseWriter, r *http.Request, mutate bool) (adminauth.User, bool) {
	user, err := administratorIdentity(h.service, r, mutate)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return adminauth.User{}, false
	}
	return user, true
}

// administratorIdentity validates both the Redis session and fresh database
// evidence through Current. It is shared by handwritten and generated APIs.
func administratorIdentity(service AdminService, r *http.Request, mutate bool) (adminauth.User, error) {
	if service == nil {
		return adminauth.User{}, errors.New("administrator authentication is not configured")
	}
	response, err := service.Current(r.Context(), webapp.SessionFromContext(r.Context()))
	if err != nil {
		return adminauth.User{}, err
	}
	if response.User == nil || response.User.ID <= 0 || response.User.RoleID != "admin" || response.Auth == nil || response.Auth.Token == "" {
		return adminauth.User{}, webapp.Unauthorized("administrator session required", map[string]any{"code": "admin_session_required"})
	}
	if mutate && subtle.ConstantTimeCompare([]byte(response.Auth.Token), []byte(r.Header.Get(adminauth.SessionHeader))) != 1 {
		return adminauth.User{}, webapp.Forbidden("administrator session nonce is required", map[string]any{"code": "admin_session_nonce_required"})
	}
	return *response.User, nil
}

func (h *adminHandler) navigationReady(w http.ResponseWriter, r *http.Request, mutate bool) bool {
	if _, ok := h.navigationIdentity(w, r, mutate); !ok {
		return false
	}
	if h.options.Navigation == nil {
		webapp.WriteServiceError(w, errors.New("administrator navigation is not configured"))
		return false
	}
	return true
}

func (h *adminHandler) navigationForUser(w http.ResponseWriter, r *http.Request) {
	user, ok := h.navigationIdentity(w, r, false)
	if !ok {
		return
	}
	if h.options.Navigation == nil {
		_ = webapp.WriteJSON(w, http.StatusOK, []*adminnav.MenuItem{})
		return
	}
	value, err := h.options.Navigation.ForUser(r.Context(), user)
	writeNavigationResult(w, http.StatusOK, value, err)
}

func (h *adminHandler) navigationMenus(w http.ResponseWriter, r *http.Request) {
	if !h.navigationReady(w, r, false) {
		return
	}
	params, err := navigationCollectionParams(r)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	value, err := h.options.Navigation.ListMenus(r.Context(), params)
	writeNavigationResult(w, http.StatusOK, value, err)
}

func (h *adminHandler) navigationMenu(w http.ResponseWriter, r *http.Request) {
	if !h.navigationReady(w, r, false) {
		return
	}
	id, err := navigationPathID(r)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	value, err := h.options.Navigation.Menu(r.Context(), id)
	writeNavigationResult(w, http.StatusOK, value, err)
}

var menuNavigationFields = map[string]bool{
	"role_id": true, "user_id": true, "parent_id": true, "caption": true,
	"route_id": true, "icon": true, "sort_order": true, "is_active": true,
}

func (h *adminHandler) navigationCreateMenu(w http.ResponseWriter, r *http.Request) {
	if !h.navigationReady(w, r, true) {
		return
	}
	fields, err := navigationJSON(w, r, 16<<10, menuNavigationFields)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	for _, field := range []string{"caption", "sort_order", "is_active"} {
		if bytes.Equal(bytes.TrimSpace(fields[field]), []byte("null")) {
			webapp.WriteServiceError(w, webapp.BadRequest("menu field cannot be null", map[string]any{"field": field}))
			return
		}
	}
	bound, err := webapp.InsertInputBinder[*adminnav.MainMenu]()(r)
	if err != nil {
		webapp.WriteServiceError(w, webapp.BadRequest("invalid menu item", nil))
		return
	}
	input := bound.(modelbind.ModelInput[*adminnav.MainMenu])
	value, err := h.options.Navigation.CreateMenu(r.Context(), *input.Model)
	writeNavigationResult(w, http.StatusCreated, value, err)
}

func (h *adminHandler) navigationUpdateMenu(w http.ResponseWriter, r *http.Request) {
	if !h.navigationReady(w, r, true) {
		return
	}
	id, err := navigationPathID(r)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	patch, err := navigationJSON(w, r, 16<<10, menuNavigationFields)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	if _, err := webapp.UpdateByPathKeysBinder[*adminnav.MainMenuKey, *adminnav.MainMenu]("id")(r); err != nil {
		webapp.WriteServiceError(w, webapp.BadRequest("invalid menu update", nil))
		return
	}
	value, err := h.options.Navigation.UpdateMenu(r.Context(), id, patch)
	writeNavigationAlter(w, value, err)
}

func (h *adminHandler) navigationDeleteMenu(w http.ResponseWriter, r *http.Request) {
	if !h.navigationReady(w, r, true) {
		return
	}
	id, err := navigationPathID(r)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	value, err := h.options.Navigation.DeleteMenu(r.Context(), id)
	writeNavigationAlter(w, value, err)
}

func (h *adminHandler) navigationRoutes(w http.ResponseWriter, r *http.Request) {
	if !h.navigationReady(w, r, false) {
		return
	}
	params, err := navigationCollectionParams(r)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	value, err := h.options.Navigation.ListRoutes(r.Context(), params)
	writeNavigationResult(w, http.StatusOK, value, err)
}

func (h *adminHandler) navigationRoute(w http.ResponseWriter, r *http.Request) {
	if !h.navigationReady(w, r, false) {
		return
	}
	id, err := navigationPathID(r)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	value, err := h.options.Navigation.Route(r.Context(), id)
	writeNavigationResult(w, http.StatusOK, value, err)
}

func (h *adminHandler) navigationUpdateRoute(w http.ResponseWriter, r *http.Request) {
	if !h.navigationReady(w, r, true) {
		return
	}
	id, err := navigationPathID(r)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	patch, err := navigationJSON(w, r, 16<<10, map[string]bool{"descr": true, "section": true, "icon": true})
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	if _, err := webapp.UpdateByPathKeysBinder[*adminnav.ApplicationRouteKey, *adminnav.ApplicationRoute]("id")(r); err != nil {
		webapp.WriteServiceError(w, webapp.BadRequest("invalid application route update", nil))
		return
	}
	value, err := h.options.Navigation.UpdateRoute(r.Context(), id, patch)
	writeNavigationAlter(w, value, err)
}

func (h *adminHandler) navigationSyncRoutes(w http.ResponseWriter, r *http.Request) {
	if !h.navigationReady(w, r, true) {
		return
	}
	if _, err := navigationJSON(w, r, 1<<20, map[string]bool{"items": true}); err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	var input adminnav.ApplicationRouteSyncInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		webapp.WriteServiceError(w, webapp.BadRequest("invalid application route manifest", nil))
		return
	}
	value, err := h.options.Navigation.SyncRoutes(r.Context(), input)
	writeNavigationResult(w, http.StatusOK, value, err)
}

func (h *adminHandler) navigationRouteAutocomplete(w http.ResponseWriter, r *http.Request) {
	if !h.navigationReady(w, r, false) {
		return
	}
	query := r.URL.Query().Get("q")
	if query == "" {
		query = r.URL.Query().Get("query")
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			webapp.WriteServiceError(w, webapp.BadRequest("limit must be between 1 and 100", nil))
			return
		}
		limit = value
	}
	if len(query) > 1000 {
		webapp.WriteServiceError(w, webapp.BadRequest("autocomplete query is too long", nil))
		return
	}
	value, err := h.options.Navigation.RouteAutocomplete(r.Context(), strings.TrimSpace(query), limit)
	writeNavigationResult(w, http.StatusOK, value, err)
}

func (h *adminHandler) navigationUsers(w http.ResponseWriter, r *http.Request) {
	if !h.navigationReady(w, r, false) {
		return
	}
	params, err := navigationCollectionParams(r)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	value, err := h.options.Navigation.ListUsers(r.Context(), params)
	writeNavigationResult(w, http.StatusOK, value, err)
}

func (h *adminHandler) navigationUser(w http.ResponseWriter, r *http.Request) {
	if !h.navigationReady(w, r, false) {
		return
	}
	id, err := navigationPathID(r)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	value, err := h.options.Navigation.User(r.Context(), id)
	writeNavigationResult(w, http.StatusOK, value, err)
}

func navigationPathID(r *http.Request) (int, error) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		return 0, webapp.BadRequest("id must be a positive integer", nil)
	}
	return id, nil
}

func navigationCollectionParams(r *http.Request) (modelbind.CollectionParams, error) {
	if len(r.URL.RawQuery) > 32<<10 {
		return modelbind.CollectionParams{}, webapp.BadRequest("collection query is too long", nil)
	}
	input, err := webapp.CollectionParamsBinder()(r)
	if err != nil {
		return modelbind.CollectionParams{}, webapp.BadRequest("invalid collection parameters", nil)
	}
	return input.(modelbind.CollectionParams), nil
}

// Preserve raw null/absent fields for PATCH while restricting the body before
// passing it to the shared modelbind adapters. Query values never enter a write.
func navigationJSON(w http.ResponseWriter, r *http.Request, limit int64, fields map[string]bool) (adminnav.Patch, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || r.Body == nil {
		return nil, webapp.BadRequest("administrator requests require application/json", nil)
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return nil, webapp.BadRequest("invalid administrator request body", nil)
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return nil, webapp.BadRequest("administrator request must be a JSON object", nil)
	}
	var input adminnav.Patch
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&input); err != nil {
		return nil, webapp.BadRequest("invalid administrator request body", nil)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, webapp.BadRequest("administrator request must contain one JSON object", nil)
	}
	if fields != nil {
		for field := range input {
			if !fields[field] {
				return nil, webapp.BadRequest("unsupported administrator request field", map[string]any{"field": field})
			}
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return input, nil
}

func writeNavigationResult[T any](w http.ResponseWriter, status int, value T, err error) {
	if err != nil {
		switch {
		case errors.Is(err, adminnav.ErrNotFound):
			err = webapp.NotFound("administrator navigation item not found", nil)
		case errors.Is(err, adminnav.ErrInvalid):
			err = webapp.BadRequest("invalid administrator navigation data", map[string]any{"code": "admin_navigation_invalid"})
		}
		webapp.WriteServiceError(w, err)
		return
	}
	_ = webapp.WriteJSON(w, status, value)
}

func writeNavigationAlter(w http.ResponseWriter, value wmodels.RowsAffectedResponse, err error) {
	writeNavigationResult(w, http.StatusOK, map[string]int64{
		"affectedRows":  value.RowsAffected,
		"rows_affected": value.RowsAffected,
	}, err)
}
