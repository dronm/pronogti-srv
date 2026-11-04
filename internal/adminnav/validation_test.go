package adminnav

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRouteManifestAcceptsLocalRoutesAndRejectsUnsafeNavigation(t *testing.T) {
	for _, path := range []string{"/", "/salons", "/salons/new", "/profile/settings"} {
		if !validRoutePath(path, true) {
			t.Errorf("valid menu path rejected: %q", path)
		}
	}
	if !validRoutePath("/salons/:id", false) {
		t.Fatal("parameterized forms must remain registrable without a menu entry")
	}
	for _, path := range []string{"", "https://example.com", "//example.com", "/%2fexample.com", "/\\example.com", "/a/%5c/b", "/../a", "/a/%2e%2e/b", "/a?next=x", "/a#next", "/a\n", "/a%00", "/salons/:id", "/:pathMatch(.*)*"} {
		if validRoutePath(path, true) {
			t.Errorf("unsafe or parameterized menu path accepted: %q", path)
		}
	}
	_, err := validateManifest(ApplicationRouteSyncInput{Items: []ApplicationRouteManifestItem{{Name: "main", Path: "/", Descr: "Home", Section: "Forms"}, {Name: "main", Path: "/other", Descr: "Other", Section: "Forms"}}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate stable route name must be invalid: %v", err)
	}
	_, err = validateManifest(ApplicationRouteSyncInput{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty manifest must not deactivate every registered route: %v", err)
	}
}

func TestMenuPatchExplicitNullAndOwnerValidation(t *testing.T) {
	role, icon := "admin", "pi pi-home"
	parentID := 10
	menu := MainMenu{RoleID: &role, ParentID: &parentID, Caption: "Home", Icon: &icon, IsActive: true, SortOrder: 7}
	err := applyMenuPatch(&menu, Patch{"parent_id": json.RawMessage(`null`), "icon": json.RawMessage(`null`), "caption": json.RawMessage(`" Updated "`)})
	if err != nil {
		t.Fatal(err)
	}
	if menu.ParentID != nil || menu.Icon != nil || menu.Caption != "Updated" || !menu.IsActive || menu.SortOrder != 7 {
		t.Fatalf("partial update lost nullable or omitted values: %+v", menu)
	}
	for _, patch := range []Patch{
		{"id": json.RawMessage(`2`)},
		{"caption": json.RawMessage(`null`)},
		{"sort_order": json.RawMessage(`"2"`)},
		{"is_active": json.RawMessage(`null`)},
		{"role_id": json.RawMessage(`null`)},
		{"user_id": json.RawMessage(`12`)},
		{"caption": json.RawMessage(`"` + strings.Repeat("x", 251) + `"`)},
	} {
		fresh := MainMenu{RoleID: &role, Caption: "Home", IsActive: true}
		if err := applyMenuPatch(&fresh, patch); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid menu patch accepted: %v, error: %v", patch, err)
		}
	}
	personal := MainMenu{RoleID: &role, Caption: "Private"}
	if err := applyMenuPatch(&personal, Patch{"role_id": json.RawMessage(`null`), "user_id": json.RawMessage(`12`)}); err != nil {
		t.Fatal(err)
	}
	if personal.RoleID != nil || personal.UserID == nil || *personal.UserID != 12 {
		t.Fatalf("owner replacement failed: %+v", personal)
	}
}

func TestRoutePatchCannotChangeTechnicalFields(t *testing.T) {
	route := ApplicationRoute{Name: "main", Path: "/", Descr: "Home", Section: "Forms"}
	if err := applyRoutePatch(&route, Patch{"descr": json.RawMessage(`"Start"`), "icon": json.RawMessage(`null`)}); err != nil {
		t.Fatal(err)
	}
	if route.Name != "main" || route.Path != "/" || route.Section != "Forms" || route.Descr != "Start" {
		t.Fatalf("partial patch changed technical fields: %+v", route)
	}
	for _, field := range []string{"name", "path", "is_active", "menu_available", "created_at"} {
		if err := applyRoutePatch(&route, Patch{field: json.RawMessage(`"bad"`)}); !errors.Is(err, ErrInvalid) {
			t.Errorf("technical field %q must be read only", field)
		}
	}
}

func TestMenuTreeOwnerOverrideOrderAndInactiveAncestor(t *testing.T) {
	personalUser, otherUser, groupID, missingID := 12, 13, 20, 404
	rows := []menuTreeRow{
		{item: &MenuItem{ID: 1, Caption: "Role home"}},
		{item: &MenuItem{ID: 21, Caption: "First"}, parentID: &groupID, userID: &personalUser},
		{item: &MenuItem{ID: 22, Caption: "Second"}, parentID: &groupID, userID: &personalUser},
		{item: &MenuItem{ID: 20, Caption: "Personal group"}, userID: &personalUser},
		{item: &MenuItem{ID: 30, Caption: "Hidden ancestor child"}, parentID: &missingID, userID: &personalUser},
		{item: &MenuItem{ID: 40, Caption: "Another user's group"}, userID: &otherUser},
	}
	menu := menuTree(rows, 12)
	if len(menu) != 1 || menu[0].ID != 20 || len(menu[0].Children) != 2 || menu[0].Children[0].ID != 21 || menu[0].Children[1].ID != 22 {
		t.Fatalf("incorrect personal tree or ordering: %+v", menu)
	}
	// An orphan child cannot override the role menu when its parent is inactive.
	menu = menuTree([]menuTreeRow{rows[0], rows[4]}, 12)
	if len(menu) != 1 || menu[0].ID != 1 {
		t.Fatalf("missing personal root should fall back to role: %+v", menu)
	}
}
