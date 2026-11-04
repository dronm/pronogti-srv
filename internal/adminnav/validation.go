package adminnav

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalid  = errors.New("invalid administrator navigation input")
	ErrNotFound = errors.New("administrator navigation entry not found")
	routeName   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,99}$`)
)

func invalid(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }

func validText(value string, required bool) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= 250 && (!required || strings.TrimSpace(value) != "") && !strings.ContainsRune(value, 0)
}

func validateMenu(menu *MainMenu) error {
	menu.Caption = strings.TrimSpace(menu.Caption)
	if (menu.RoleID == nil) == (menu.UserID == nil) {
		return invalid("choose exactly one menu owner: role or user")
	}
	if menu.RoleID != nil && *menu.RoleID != "admin" {
		return invalid("only administrator menus are supported")
	}
	for _, id := range []*int{menu.UserID, menu.ParentID, menu.RouteID} {
		if id != nil && *id <= 0 {
			return invalid("reference IDs must be positive")
		}
	}
	if !validText(menu.Caption, true) {
		return invalid("caption is required and must have at most 250 characters")
	}
	if menu.Icon != nil && !validText(*menu.Icon, false) {
		return invalid("icon must have at most 250 characters")
	}
	return nil
}

func validRoutePath(path string, menuAvailable bool) bool {
	if path == "" || len(path) > 2000 || !utf8.ValidString(path) || path[0] != '/' || strings.HasPrefix(path, "//") || strings.Contains(path, `\`) || strings.ContainsAny(path, "?#") || strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return false
	}
	parsed, err := url.Parse(path)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Opaque != "" {
		return false
	}
	decoded, err := url.PathUnescape(path)
	if err != nil || strings.HasPrefix(decoded, "//") || strings.Contains(decoded, `\`) || strings.IndexFunc(decoded, unicode.IsControl) >= 0 {
		return false
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	// A menu item must be navigable without an application's record-specific parameters.
	return !menuAvailable || !strings.ContainsAny(decoded, ":*(){}")
}

func validateManifest(input ApplicationRouteSyncInput) (ApplicationRouteSyncInput, error) {
	if len(input.Items) == 0 || len(input.Items) > 2000 {
		return input, invalid("route manifest must contain between 1 and 2000 items")
	}
	seen := make(map[string]bool, len(input.Items))
	for i := range input.Items {
		item := &input.Items[i]
		item.Name, item.Path, item.Descr, item.Section = strings.TrimSpace(item.Name), strings.TrimSpace(item.Path), strings.TrimSpace(item.Descr), strings.TrimSpace(item.Section)
		if !routeName.MatchString(item.Name) || seen[item.Name] {
			return input, invalid("route names must be unique stable identifiers")
		}
		seen[item.Name] = true
		if !validRoutePath(item.Path, item.MenuAvailable) {
			return input, invalid("route paths must be local SPA paths; menu routes cannot require parameters")
		}
		if !validText(item.Descr, true) || !validText(item.Section, true) || (item.Icon != nil && !validText(*item.Icon, false)) {
			return input, invalid("route labels and section are required and limited to 250 characters")
		}
	}
	return input, nil
}

func decodePatchValue(raw json.RawMessage, dest any, nullable bool) error {
	if !nullable && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return invalid("this field cannot be null")
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return invalid("incorrect field type")
	}
	return nil
}

func applyMenuPatch(menu *MainMenu, patch Patch) error {
	if len(patch) == 0 {
		return invalid("at least one editable field is required")
	}
	for field, raw := range patch {
		var err error
		switch field {
		case "role_id":
			err = decodePatchValue(raw, &menu.RoleID, true)
		case "user_id":
			err = decodePatchValue(raw, &menu.UserID, true)
		case "parent_id":
			err = decodePatchValue(raw, &menu.ParentID, true)
		case "route_id":
			err = decodePatchValue(raw, &menu.RouteID, true)
		case "caption":
			err = decodePatchValue(raw, &menu.Caption, false)
		case "icon":
			err = decodePatchValue(raw, &menu.Icon, true)
		case "sort_order":
			err = decodePatchValue(raw, &menu.SortOrder, false)
		case "is_active":
			err = decodePatchValue(raw, &menu.IsActive, false)
		default:
			return invalid("unknown or read-only menu field: " + field)
		}
		if err != nil {
			return err
		}
	}
	return validateMenu(menu)
}

func applyRoutePatch(route *ApplicationRoute, patch Patch) error {
	if len(patch) == 0 {
		return invalid("at least one editable field is required")
	}
	for field, raw := range patch {
		var err error
		switch field {
		case "descr":
			err = decodePatchValue(raw, &route.Descr, false)
		case "section":
			err = decodePatchValue(raw, &route.Section, false)
		case "icon":
			err = decodePatchValue(raw, &route.Icon, true)
		default:
			return invalid("unknown or read-only route field: " + field)
		}
		if err != nil {
			return err
		}
	}
	route.Descr, route.Section = strings.TrimSpace(route.Descr), strings.TrimSpace(route.Section)
	if !validText(route.Descr, true) || !validText(route.Section, true) || (route.Icon != nil && !validText(*route.Icon, false)) {
		return invalid("route labels and section are required and limited to 250 characters")
	}
	return nil
}
