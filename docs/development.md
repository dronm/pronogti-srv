# Development

## Dependencies

Use Go 1.25 or newer. webapp and modelbind are pinned to published revisions in `go.mod`.

The application uses bundled source snapshots under `third_party` for the customized goyclients integration, Tochka SDK, Terminal FA driver and isolated Redis session providers. Their relative Go module replacements make the complete application build independent of a developer-specific checkout path.

To intentionally work against a separate goyclients checkout, replace its module locally:

```sh
go mod edit -replace=github.com/dronm/goyclients=/path/to/goyclients
go mod tidy
```

Restore the bundled replacement before distributing the application. Preserve the Terminal FA driver's external SQLite operation journal across application and driver upgrades.

## Build and run

```sh
go build -o pronogti ./cmd/pronogti
./pronogti -config ./config.json
```

`config.json` is the default configuration file. The existing positional configuration argument is also accepted:

```sh
./pronogti ./config.json
```

The Makefile resolves paths and its optional `.env` file from the project root, including when invoked with `make -f /path/to/Makefile` from another directory. The executable itself does not load `.env`; use exported variables for a direct run or configure `database.primary` in JSON. A Makefile-loaded environment setting must be exported, for example `export PRONOGTI_DATABASE_URL=postgres://...`.

| Command | Action |
| --- | --- |
| `make build` | Development build of `./cmd/pronogti`, saved as `./pronogti`; default target |
| `make run` | Run the backend using the root `config.json` |
| `make run CONFIG_FILE=/path/config.json` | Run with another configuration file |
| `make test` / `make test-race` | Run unit tests, optionally with the race detector |
| `make test-integration` | Run `internal/apitest` against `API_BASE_URL`, when that package is present |
| `make prod` | Optimized backend build with version metadata and CGO disabled |
| `make clean` | Remove the backend executable and clear the Go test cache |
| `make codegen [gen\|check\|valid]` | Generate, check or validate schemas when codegen is configured |
| `make migup` / `make migdown` | Apply or revert one migration using `DB_CONN` |
| `make migadd <name>` | Create a migration pair |
| `make migfix <version>` / `make migver` | Force or inspect the migration version |
| `make runsql <name>` | Run a SQL script using `internal/scripts/run_sql.php` |
| `make deploy` / `make deploy-full` / `make deploy-backend` | Run `internal/scripts/deploy.sh --backend-only` |
| `make deploy-no-migrations` | Deploy with `--backend-only --skip-migrations` |

Override build settings on the command line, for example `make prod VERSION=1.0.0` or `make build GO=/path/to/go`.

The project includes `codegen.yaml` and its first object definition, `schema/salon.yaml`; see [code generation schemas](../schema/README.md). Codegen is not declared as a Go tool dependency yet. The project does not currently contain an API integration-test package or deployment/SQL helpers. Targets with missing prerequisites report them. Add deployment helpers under `internal/scripts/` to use their commands; set `DB_CONN` for database commands. No bot or frontend deployment targets are included.

## Tests

Tests use fake dependencies and HTTP responses rather than the live YClients API.

```sh
go test ./...
go test -race ./...
```

The suite covers configuration/token persistence, cache lifetime and company isolation, YClients authentication concurrency, public salon selection, company-bound customer expiry, persistent kiosk recovery/revocation, administration, scoped routing, request validation, exact checkout money/quantity arithmetic, fresh visit/catalog pricing, immutable order ownership, checkout expiry and idempotency, removed legacy routes, confirmation and signed-callback handling, durable synchronization phases, receipt recovery, and cross-company/session isolation.

An optional integration test exercises the real library Redis provider against an isolated Redis instance:

```sh
PRONOGTI_TEST_REDIS_URL=redis://127.0.0.1:6379/15 \
	go test -tags=integration ./internal/kiosk -run TestRedisCustomerLifecycle -count=1
```

The test creates a unique namespace and cleans its own session data. It does not use YClients credentials or perform payments.

## Runtime design

Startup and dependency initialization live in `cmd/pronogti`. Middleware and routing use webapp directly; CORS settings are passed through `cfg.CORS.WebappConfig()`.

The YClients connection and in-memory cache are injected into the service. Authentication refreshes are coalesced. Separate synchronization protects authentication state and serialized API calls. Request contexts reach authentication and all subsequent outbound calls; canceling one waiter does not cancel another request's authentication.

Staff and client-phone cache keys include the company ID. Successful client changes invalidate that company's cached phone lookups.

Public entry uses `/session`, `/salons`, and `/salons/{slug}/session`. Shared operations use `/customer/...`; `/kiosk/session` handles operator setup and strict device restoration. Existing `/kiosk/...` customer aliases remain strict kiosk-mode operations. Session-aware services receive `ServiceContext.Session`; a public slug is resolved once to establish company context, then company/customer IDs come from the session. Former arbitrary company/client-selector URLs remain removed. Client values are encoded through pointers so goyclients date marshalers run for cache entries and HTTP responses.

Startup opens PostgreSQL for the salon/device registries and initializes the framework Redis session manager. Salon session versions invalidate obsolete public contexts. Persistent kiosk credentials are checked against PostgreSQL on requests; rotation/disablement revokes the key, while company reassignment clears the previous customer. A valid HttpOnly device cookie recreates only device scope after Redis cleanup. The application starts/stops the library garbage collector with its runtime and continues using that Redis provider. See [sessions](sessions.md) for the migration, salon administration and kiosk setup workflow.

Authoritative checkout preparation lives in `internal/checkout` with a YClients boundary adapter in `internal/services`. The provider loads fresh visit/catalog/staff data and supplies integer-kopek prices to the checkout service. PostgreSQL persists an immutable prepared order and hashes of interaction ownership; requests are revalidated after external reads. The frontend uses its returned amount before payment selection. Migration `000005_payment_checkout` creates payment profile/order/attempt/event infrastructure; `000007_qr_attempt` adds durable QR creation and recovery through the bank SDK. Background bank reconciliation and verified bank callbacks confirm payment without browser participation. Paid orders enqueue independently journaled YClients synchronization and Terminal FA fiscalization. Their failures never change a confirmed payment back to unpaid. See [checkout](checkout.md) and [QR payments](qr-payments.md).
