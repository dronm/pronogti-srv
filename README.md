# Pronogti backend

Go HTTP API for the Pronogti customer website and pay kiosks. Public salon slugs and kiosk bindings map to YClients companies in PostgreSQL. Temporary customer identities use [webapp](https://github.com/dronm/webapp)'s Redis session support and [session](https://github.com/dronm/session). A separate durable device cookie keeps kiosk registration valid across Redis session cleanup.

Customers identify themselves by telephone number, without SMS or another challenge. Public visitors select a salon at `/` or open `/c/:slug` directly. Registered kiosks use their company binding. The backend resolves the company and client from the selected session context.

## Start

Requirements: Go 1.25 or newer, PostgreSQL, Redis, YClients credentials, `golang-migrate`, and access to the configured YClients company. The customized [goyclients](https://github.com/dronm/goyclients) source is bundled with the other integration dependencies.

```sh
cp config.example.json config.json
export PRONOGTI_DATABASE_URL='postgres://USER:PASSWORD@127.0.0.1:5432/pronogti?sslmode=disable'
export DB_CONN="$PRONOGTI_DATABASE_URL"
# Set YClients credentials and Redis settings in config.json.
migrate -database "$DB_CONN" -path ./migrations up
make build
./pronogti -config ./config.json
```

Migration `000004_salons` publishes company `781898` at `/c/company-781898`. Change its public name/slug, or publish additional companies, through the salon CLI:

```sh
go run ./cmd/salon -config ./config.json upsert -company 781898 -slug pronogti -name 'ПроНогти'
```

After renaming the slug, the direct customer link is `/c/pronogti`. Personal browsers do not need kiosk setup. To provision a physical device, use its YClients company ID:

```sh
go run ./cmd/kiosk -config ./config.json create -name 'Kiosk 1' -company 781898
```

The command prints the kiosk ID and a newly generated token. Open the frontend's `/kiosk/setup` page on that device, enter the token, and continue to the assigned salon's phone page. Only the token hash is stored in PostgreSQL. Kiosk registration has no server-side expiry; the durable HttpOnly cookie recreates its Redis context as needed. Customer logout/expiry retains device registration. Clearing browser credentials still requires setup again.

The bundled goyclients snapshot contains APIs absent from its older public revision. See [development](docs/development.md#dependencies) if you intentionally use another checkout. For an update over an existing source directory, follow [UPGRADE.md](UPGRADE.md) to remove retired files.

Apply migration `000005_payment_checkout` for server-priced checkout preparation. It creates the payment configuration/order/attempt/event tables and optional company-owned profile bindings. The frontend submits visit/catalog identifiers and quantities, then uses the stored backend amount. Checkout preparation does not require configured bank credentials. See [authoritative checkout](docs/checkout.md) for request, pricing, ownership and idempotency rules.

Apply `000006_fiscal_receipts` for per-kiosk receipt profiles and durable receipt jobs. Demo mode is the default and does not contact receipt hardware. See [receipt configuration](docs/fiscalization.md) for the bundled Terminal FA driver, profile assignment and live activation.

Apply `000007_qr_attempt` for durable SBP QR attempts and JWT environment references. Select an enabled payment profile on a kiosk or salon. Default mock mode creates a nonpayable demo QR without bank HTTP requests; sandbox/production profiles use the bundled SDK. See [QR payments](docs/qr-payments.md).

Apply all pending migrations through `000009_card_checkout` before starting this version. Bank confirmation runs independently of the browser, and a confirmed payment queues separate YClients synchronization and Terminal FA receipt jobs. The default built-in mock completes automatically on the server and performs no upstream financial or device writes. Production setup and recovery are covered in [QR payments](docs/qr-payments.md) and [card payments](docs/card-payments.md). Card checkout uses the bank-hosted card-only page and the same YClients/Terminal FA completion jobs. Cards remain disabled on existing profiles until explicitly configured.

## Documentation

- [Session workflow and device administration](docs/sessions.md)
- [Authoritative checkout and payment schema](docs/checkout.md)
- [SBP QR confirmation, recovery and bank profiles](docs/qr-payments.md)
- [Bank-hosted card checkout](docs/card-payments.md)
- [Payment validation](docs/payment-validation.md)
- [YClients settlement profiles and purchase accounting](docs/yclients-settlement.md)
- [Updating an existing installation](UPGRADE.md)
- [Receipts, demo mode and Terminal FA configuration](docs/fiscalization.md)
- [Configuration](docs/configuration.md)
- [API](docs/api.md)
- [Development, Make commands and tests](docs/development.md)

## Layout

| Directory | Contents |
| --- | --- |
| `cmd/pronogti` | API executable and dependency initialization |
| `cmd/kiosk` | Device provisioning, token rotation and enable/disable commands |
| `cmd/salon` | Public salon name/slug and enable/disable commands |
| `cmd/payment` | Inspect payments, confirm mock payments, retry never-dispatched failures and verify operator-attributed YClients transaction evidence |
| `docs` | Technical documentation |
| `documents` | Business and integration reference materials |
| `internal/kiosk` | Session identities, kiosk registry and PostgreSQL implementation |
| `internal/checkout` | Server-priced checkout preparation, exact amounts and PostgreSQL order persistence |
| `internal/payment` | Durable QR/card attempts, signed notifications and independent bank reconciliation |
| `internal/settlement` | Durable, recoverable YClients purchase synchronization |
| `internal/fiscal` | Receipt validation, driver HTTP adapter and durable receipt worker |
| `internal/salon` | Public salon registry and company/slug lookup |
| `internal/httpapi` | Public discovery, scoped customer routes, device setup and modelbind binders |
| `internal/services` | Existing YClients calls and cache integration |
| `migrations` | PostgreSQL schema, including kiosk/salon registration and `000005_payment_checkout` |
| `schema` | Reserved for code generation definitions |
| `third_party/paytochkabank` | Uploaded Tochka bank SDK source dependency |
| `third_party/goterminalfa` | Uploaded receipt driver source dependency |
| `third_party/goyclients` | Customized YClients integration source dependency |

The backend prepares authoritative checkouts with integer-kopek amounts and durable ownership/idempotency records. Nonzero bonus redemption remains disabled until actual write-off support exists. The legacy draft-saving API is removed. QR and card creation reuse durable attempts; bank confirmation atomically marks the payment and order paid, then queues YClients and fiscal jobs independently. The browser observes their persisted states and cannot mark a production payment paid. Refunds and subscriptions remain separate operations.
