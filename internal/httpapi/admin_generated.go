package httpapi

import (
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/modelbind/metadata"
	"github.com/dronm/pronogti/internal/services"
	"github.com/dronm/webapp"
)

var generatedAdminServiceRegistration = sync.OnceValue(func() (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("register generated administrator services: %v", value)
		}
	}()
	services.RegisterGeneratedServices()
	return nil
})

// NewGeneratedAdminHandler connects codegen's registries to the administrator
// API. Mount it through AdminHTTPOptions so it uses the administrator's existing
// Redis, CORS and origin middleware rather than the public kiosk/customer stack.
// The returned permissions describe only the compiled routes, not the editable
// frontend route registry. All currently supported administrative CRUD belongs
// to the administrator role.
func NewGeneratedAdminHandler(service AdminService, db ds.Provider, requestTimeout time.Duration) (handler http.Handler, permissions []string, err error) {
	defer func() {
		if value := recover(); value != nil {
			handler, permissions = nil, nil
			err = fmt.Errorf("build generated administrator routes: %v", value)
		}
	}()
	if err := generatedAdminServiceRegistration(); err != nil {
		return nil, nil, err
	}
	routes, err := buildGeneratedAdminRoutes()
	if err != nil {
		return nil, nil, err
	}
	allowed := make(map[string]struct{}, len(routes))
	for index := range routes {
		route := &routes[index]
		allowed[route.Permission] = struct{}{}
		if route.ServiceFunc == "Create" || route.ServiceFunc == "Update" {
			route.Binder = generatedAdminWriteBinder(route.Binder, route.ServiceFunc == "Update")
		}
		// Authentication and permission checks run before this middleware and
		// the generated binder. Requiring a JSON body prevents query parameters
		// from being decoded as a create/update request.
		route.Middleware = append([]webapp.Middleware{generatedAdminRequestMiddleware()}, route.Middleware...)
	}
	permissions = make([]string, 0, len(allowed))
	for permission := range allowed {
		permissions = append(permissions, permission)
	}
	sort.Strings(permissions)
	app := &webapp.AppContext{
		DB: db, DefaultTimeout: requestTimeout,
		PermissionChecker: generatedAdminPermissionChecker(service, allowed),
	}
	return webapp.MustBuildRouter(app, routes), permissions, nil
}

// Older modelbind update helpers can assign submitted primary-key and srvCalc
// fields. Enforce codegen's immutable-field metadata before any service call.
func generatedAdminWriteBinder(binder webapp.Binder, update bool) webapp.Binder {
	return func(r *http.Request) (any, error) {
		if binder == nil {
			return nil, webapp.Internal("generated write binder is not configured", nil)
		}
		bound, err := binder(r)
		if err != nil {
			return nil, err
		}
		input := reflect.ValueOf(bound)
		if update && input.Kind() == reflect.Struct {
			input = input.FieldByName("Input")
		}
		if !input.IsValid() || input.Kind() != reflect.Struct || !input.CanInterface() {
			return nil, webapp.Internal("generated write input is not a model input", nil)
		}
		presence, ok := input.Interface().(interface{ IsPresent(string) bool })
		model := input.FieldByName("Model")
		if !ok || !model.IsValid() || !model.CanInterface() {
			return nil, webapp.Internal("generated write model metadata is not available", nil)
		}
		md, err := metadata.NewModelMetadata(model.Interface())
		if err != nil {
			return nil, err
		}
		for name, field := range md.Fields {
			if presence.IsPresent(name) && (field.SrvCalc() || (update && field.PrimaryKey())) {
				return nil, webapp.BadRequest("administrator field is read-only", map[string]any{"field": name})
			}
		}
		return bound, nil
	}
}

func buildGeneratedAdminRoutes() ([]webapp.Route, error) {
	var routes []webapp.Route
	group := webapp.NewGroup("", &routes)
	registerGeneratedRoutes(&group)
	if err := webapp.ValidateRoutes(routes); err != nil {
		return nil, fmt.Errorf("validate generated administrator routes: %w", err)
	}
	for _, route := range routes {
		if !strings.HasPrefix(route.Pattern, "/admin/") || route.Permission == "" {
			return nil, fmt.Errorf("generated administrator route %q must use /admin/ and declare a permission", route.Name)
		}
		switch route.Method {
		case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			return nil, fmt.Errorf("generated administrator route %q has unsupported HTTP method %q", route.Name, route.Method)
		}
	}
	return routes, nil
}

func generatedAdminPermissionChecker(service AdminService, allowed map[string]struct{}) webapp.PermissionChecker {
	return func(r *http.Request, permission string) error {
		if _, ok := allowed[permission]; !ok {
			return webapp.Forbidden("administrator permission is not registered", map[string]any{"permission": permission})
		}
		mutate := r.Method != http.MethodGet && r.Method != http.MethodHead
		_, err := administratorIdentity(service, r, mutate)
		return err
	}
}

func generatedAdminRequestMiddleware() webapp.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(r.URL.RawQuery) > 32<<10 {
				webapp.WriteServiceError(w, webapp.BadRequest("administrator query is too long", nil))
				return
			}
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch:
				// The generated model binder validates field types, required
				// presence and server-calculated fields after this envelope check.
				if _, err := navigationJSON(w, r, 1<<20, nil); err != nil {
					webapp.WriteServiceError(w, err)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
