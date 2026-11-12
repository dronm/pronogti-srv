package httpapi

import "github.com/dronm/webapp"

// registerKioskManagementRoutes adds actions that have no CRUD equivalent.
// The admin router applies the same session, nonce and no-store policy as CRUD.
func registerKioskManagementRoutes(api *webapp.Group) {
	api.POST(
		"/admin/kiosks/{id}/token/rotate",
		webapp.WithName("kiosk.rotateToken"),
		webapp.WithPermission("kiosk.rotateToken"),
		webapp.WithService("AdminKiosk", "RotateToken"),
		webapp.WithBinder(webapp.PathValueBinder[int64]("id")),
	)
}
