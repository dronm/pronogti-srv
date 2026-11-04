// Package adminnav stores administrator navigation. Route registration is UI
// metadata; it never grants access to a backend operation.
package adminnav

import (
	"encoding/json"
	"time"

	wmodels "github.com/dronm/webapp/models"
)

// Patch retains the difference between an omitted field and an explicit null.
type Patch map[string]json.RawMessage

type MainMenu struct {
	ID        int       `json:"id" primaryKey:"true" srvCalc:"true"`
	RoleID    *string   `json:"role_id"`
	UserID    *int      `json:"user_id"`
	ParentID  *int      `json:"parent_id"`
	Caption   string    `json:"caption" required:"true" maxLen:"250"`
	RouteID   *int      `json:"route_id"`
	Icon      *string   `json:"icon" maxLen:"250"`
	SortOrder int       `json:"sort_order"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at" srvCalc:"true"`
	UpdatedAt time.Time `json:"updated_at" srvCalc:"true"`
}

func (MainMenu) Relation() string   { return "public.main_menus" }
func (MainMenu) CollectionAgg() any { return &wmodels.TotCount{} }

type MainMenuKey struct {
	ID int `json:"id" primaryKey:"true" required:"true"`
}

func (MainMenuKey) Relation() string { return "public.main_menus" }

type Reference struct {
	Keys struct {
		ID int `json:"id"`
	} `json:"keys"`
	Descr string `json:"descr"`
}

type MainMenuList struct {
	ID               int        `json:"id" primaryKey:"true" srvCalc:"true"`
	RoleID           *string    `json:"role_id"`
	User             *Reference `json:"users_ref"`
	Parent           *Reference `json:"main_menus_ref"`
	ApplicationRoute *Reference `json:"application_routes_ref"`
	RouteName        *string    `json:"route_name"`
	Caption          string     `json:"caption"`
	Icon             *string    `json:"icon"`
	SortOrder        int        `json:"sort_order"`
	IsActive         bool       `json:"is_active"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

func (MainMenuList) Relation() string   { return "public.main_menus_list" }
func (MainMenuList) CollectionAgg() any { return &wmodels.TotCount{} }

type MenuItem struct {
	ID       int         `json:"id"`
	Caption  string      `json:"caption"`
	Route    string      `json:"route"`
	Icon     *string     `json:"icon"`
	Children []*MenuItem `json:"children"`
}

type ApplicationRoute struct {
	ID            int       `json:"id" primaryKey:"true" srvCalc:"true"`
	Name          string    `json:"name" srvCalc:"true"`
	Path          string    `json:"path" srvCalc:"true"`
	Descr         string    `json:"descr" required:"true" maxLen:"250"`
	Section       string    `json:"section" required:"true" maxLen:"250"`
	Icon          *string   `json:"icon" maxLen:"250"`
	MenuAvailable bool      `json:"menu_available" srvCalc:"true"`
	IsActive      bool      `json:"is_active" srvCalc:"true"`
	CreatedAt     time.Time `json:"created_at" srvCalc:"true"`
	UpdatedAt     time.Time `json:"updated_at" srvCalc:"true"`
}

func (ApplicationRoute) Relation() string   { return "public.application_routes" }
func (ApplicationRoute) CollectionAgg() any { return &wmodels.TotCount{} }

type ApplicationRouteKey struct {
	ID int `json:"id" primaryKey:"true" required:"true"`
}

func (ApplicationRouteKey) Relation() string { return "public.application_routes" }

type ApplicationRouteManifestItem struct {
	Name          string  `json:"name"`
	Path          string  `json:"path"`
	Descr         string  `json:"descr"`
	Section       string  `json:"section"`
	Icon          *string `json:"icon"`
	MenuAvailable bool    `json:"menu_available"`
}

type ApplicationRouteSyncInput struct {
	Items []ApplicationRouteManifestItem `json:"items"`
}
type ApplicationRouteSyncResult struct {
	Registered  int `json:"registered"`
	Deactivated int `json:"deactivated"`
}

type ApplicationRouteOption struct {
	ID      int     `json:"id"`
	Name    string  `json:"name"`
	Path    string  `json:"path"`
	Descr   string  `json:"descr"`
	Section string  `json:"section"`
	Icon    *string `json:"icon"`
}

// UserReference deliberately excludes password/session fields. Its database
// view also restricts autocomplete to accounts that can own an admin menu.
type UserReference struct {
	ID     int    `json:"id" primaryKey:"true"`
	Name   string `json:"name"`
	RoleID string `json:"role_id"`
}

func (UserReference) Relation() string   { return "public.admin_menu_users" }
func (UserReference) CollectionAgg() any { return &wmodels.TotCount{} }
