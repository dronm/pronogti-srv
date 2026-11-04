# Administrator application and login

The administrator frontend is a separate Vue application at
`dronm-workspace/apps/pronogti-admin`. It follows the Meatshop administrator
application structure and uses the workspace packages `@katren/vue-business-app`
and `@katren/vue-collection-lib`. The same Pronogti executable serves its API,
public customer routes, bank callbacks and background workers.

## Prepare the backend

Apply every pending migration through `000011_admin_navigation` before starting
this version. Migration 000010 adds bcrypt password storage and session revocation
metadata to the existing users table, and extends login audit records for IPv6.
Legacy rows remain; the old `pwd` field does not authenticate administrators.
No default password or public administrator registration is installed.

Create the first administrator from the backend directory. This example reads
its password without echoing it or placing its value in process arguments:

```sh
read -rsp 'Administrator password: ' PRONOGTI_ADMIN_PASSWORD
printf '\n'
export PRONOGTI_ADMIN_PASSWORD
go run ./cmd/admin -config ./config.json create -name admin -password-env PRONOGTI_ADMIN_PASSWORD
unset PRONOGTI_ADMIN_PASSWORD
```

Passwords must contain 12–72 UTF-8 bytes. Alternatively, omit `-password-env`
and supply the password on standard input. For an existing administrator, use
`reset-password` in place of `create`. A reset revokes existing administrator
sessions and preserves a blocked account's blocked state. A client account is
never promoted through password reset. The command prints only public account
information.

Start the backend normally with `make run`. The administrator routes use the
existing PostgreSQL and Redis connection settings.

## Configuration

Existing configuration files receive the administrator defaults when no `admin`
section is present. The development defaults enable the API, use a separate
`pronogti_admin_session` cookie and `pronogti_admin` Redis namespace, and permit
frontend origins `http://localhost:5174` and `http://127.0.0.1:5174`.

The maximum session lifetime is eight hours and the idle limit is thirty minutes.
`secure` inherits the existing session setting when omitted. Production settings
should name the actual administrator frontend origin and use HTTPS cookies:

```json
{
	"admin": {
		"enabled": true,
		"cookie_name": "pronogti_admin_session",
		"cookie_domain": "",
		"cookie_path": "/",
		"secure": true,
		"same_site": "lax",
		"max_life_time": 28800,
		"max_idle_time": 1800,
		"redis_namespace": "pronogti_admin",
		"allowed_origins": ["https://YOUR_ADMIN_HOST"]
	}
}
```

`cookie_path` refers to the URL visible to the browser. The default `/` supports
both direct `/admin/...` requests and the development proxy's `/api/admin/...`
prefix. If deployment fixes an external prefix, a narrower cookie path can be
configured to match it. Customer and kiosk cookie names and the customer Redis
namespace must remain distinct. Set `admin.enabled: false` to disable the whole
administrator namespace.

The bundled `third_party/session` source fixes the pinned Redis provider's shared
state between managers. Each manager now retains its own namespace, connection
and expiration settings. It preserves the existing session library interfaces;
administrator sessions still use that library through `webapp/session`.

## API contract

| Method | Path | Behavior |
| --- | --- | --- |
| POST | `/admin/login` | JSON `{ "name": "admin", "pwd": "..." }`; rotate the session ID and authenticate an active administrator |
| GET | `/admin/session` | Restore the authenticated identity, or return `{ "auth": null, "user": null }` |
| POST | `/admin/logout` | JSON `{}` plus `X-Admin-Session`; revoke the audit record, destroy Redis state and clear the cookie |
| GET | `/admin/main-menu` | Current administrator's saved menu tree; alias of `/admin/main-menus/for-user` |
| GET, POST | `/admin/main-menus` | List or create menu entries |
| GET, PATCH, DELETE | `/admin/main-menus/{id}` | Read, edit or delete a menu entry and its descendants |
| GET | `/admin/main-menus/for-user` | Active user-specific menu, falling back to the administrator role menu |
| GET | `/admin/main-menus/routes/autocomplete` | Select active, menu-available registered routes |
| GET | `/admin/application-routes` | List registered administrator frontend forms |
| GET, PATCH | `/admin/application-routes/{id}` | Read route information or edit its description, section and icon |
| POST | `/admin/application-routes/sync` | Synchronize the compiled frontend route manifest |
| GET | `/admin/users`, `/admin/users/{id}` | Read-only administrator references for menu ownership |
| GET, POST | `/admin/salons` | List or create salons through the generated collection service |
| GET, PATCH | `/admin/salons/{company_id}` | Read or update a salon through the generated collection service |
| GET | `/admin/permissions` | Authenticated administrator capability information |
| GET | `/admin/prog-about` | Authenticated application/build information for the shared dialog |

Login and session restoration return `user` with `id`, `name`, `role_id` and
`locale_id`, together with `auth.token` and `auth.expires_at`. `auth.token` is a
CSRF nonce; the actual session credential remains in the HttpOnly cookie.
Unsafe authenticated requests send the nonce in `X-Admin-Session`. Requests use
cookies with `credentials: include`. Authenticated GET endpoints validate the
cookie against fresh database role, blocked-state, session-version and open
login evidence.

The admin HTTP stack is separate from public salon/customer and persistent kiosk
session middleware. A customer phone number or kiosk credential cannot grant
administrator access. Login uses bounded JSON inputs and a bounded per-IP attempt
limit. Login audit rows contain a SHA-256 session binding rather than the browser
session credential; raw request headers are not retained.

## Frontend development and deployment

Extract the administrator source archive into the existing `dronm-workspace`;
it contains `apps/pronogti-admin`, and does not replace other apps or libraries.
The app's local package references expect `packages/vue-business-app` version
0.1.8 or newer and `packages/vue-collection-lib` version 0.1.15 or newer.

Use the existing workspace install workflow, then run the app's `dev` script.
With pnpm, a typical invocation is:

```sh
cd /path/to/dronm-workspace
pnpm install
pnpm --filter pronogti-admin dev
```

Vite listens on port 5174 and proxies `/api/...` to `http://127.0.0.1:59000/...`.
The `.env.example` documents the API prefix, proxy target and frontend base path.
For a production build, configure the external API prefix and serve the resulting
SPA with history fallback. A same-origin reverse proxy can strip `/api` before
forwarding to Pronogti, as the development proxy does.

The shared administrator menu provides Profile, Menu constructor, All forms,
About and Logout. Profile displays the current account; About displays backend
build information. The constructor edits role-wide or user-specific menu trees,
including groups, ordering, icons and active state. Saved changes refresh the
navigation. A user-specific active menu takes precedence over the role menu.

Migration 000011 creates `application_routes` and `main_menus`, their collection
views and a Home entry for the administrator role. The five standard account
actions come from the shared Vue component and do not need duplicate database
entries. Parent/child entries must have the same owner; cycles are rejected.
Deleting a group also deletes its descendants.

All forms lists the compiled administrator routes. Use its synchronization action
after adding generated pages to register their names and paths. Synchronization
retains route IDs and edited labels, sections and icons, and deactivates routes
removed from the manifest. Only active routes marked as menu-available can be
added to menus. Route registration controls navigation metadata; it does not
grant access to a backend endpoint.

The list endpoints use the existing modelbind collection query format, matching
the shared Vue library. Creates and patches accept flat JSON objects. Menu writes,
route edits and manifest synchronization require `X-Admin-Session` and fresh
administrator authentication. User references contain only ID, name and role.

Your existing `codegen.yaml` targets this app. `schema/salon.yaml` generates the
salon collection and standalone edit page. Unrelated Meatshop business pages and
the public kiosk UI are not included in the administrator app.

## Generated administrator collections

Startup connects both codegen registries: `services.RegisterGeneratedServices()`
registers the generated service factories, and `registerGeneratedRoutes()` builds
their administrator routes. The generated router is mounted inside the existing
administrator cookie, origin and timeout middleware. Generated collection writes
require the current session nonce, and every collection request revalidates the
administrator against the database. The public customer/kiosk router remains a
separate entry point.

Generated route definitions must use the `/admin/` namespace and declare a
permission. Active administrators can use the exact permission codes declared by
these compiled routes; unknown codes are denied. `/admin/permissions` reports
these codes alongside `admin.access`. This application currently has one
administrator role; this does not introduce per-user permission grants in the
database or derive API permissions from the editable frontend route registry.

After adding a schema, keep `registries.enabled: true`, rerun codegen and rebuild
the backend. The generated registry files pick up new collections without manual
changes to the startup code. Do not add administrator routes to the public
`BuildRoutes()` function, or edit generated files to install authentication.
Rebuild the administrator frontend and synchronize All forms to register its new
pages for menu construction. Salon CRUD uses the existing `salons` table and
requires no additional migration for this routing change.

With the current modelbind path-key binder, `keys[].pathName` must match the
key's JSON field name. The salon schema uses `company_id` for both, producing
`/admin/salons/{company_id}`. The requested URL still contains the company ID
value, for example `/admin/salons/781898`. Generated update inputs reject primary
key changes and writes to server-calculated fields such as `session_version`
and timestamps; nullable profile IDs still support explicit JSON `null`.

## Validation

Run `go test -race ./...` and `go vet ./...` in the backend. Navigation integration
tests run when `PRONOGTI_TEST_DATABASE_URL` points to a dedicated loopback
PostgreSQL service. Redis session integration tests additionally require
`PRONOGTI_TEST_REDIS_URL`. They use fresh isolated databases and random Redis
namespaces. Coverage includes persisted CRUD, owner isolation, concurrent cycle
prevention, manifest synchronization, nullable patches, shared Vue collection
requests, authentication and CSRF. In the frontend, run its `test` and `build`
scripts.
