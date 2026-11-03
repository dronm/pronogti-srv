# Configuration

The application reads `config.json` by default. Pass `-config /path/config.json` to select another file. `config.example.json` contains the current settings and placeholder YClients credentials. Legacy flat HTTP/YClients configuration remains supported.

HTTP durations use Go duration strings. Session lifetimes below use seconds.

| Setting | Meaning / default |
| --- | --- |
| `http.addr` | Listen address; `127.0.0.1:59000` |
| `http.read_timeout` | Read/header timeout; `15s` |
| `http.write_timeout` | Write timeout; request deadline plus `5s` |
| `http.idle_timeout` | Keep-alive idle timeout; `60s` |
| `http.request_timeout` | Request-context deadline; `30s` default; example config uses `60s` |
| `database.primary` | Explicit PostgreSQL connection string |
| `database.primary_env` | Environment variable containing the primary DSN; default `PRONOGTI_DATABASE_URL` |
| `database.secondaries` | Optional named secondary connection strings |
| `checkout.max_life_time` | Prepared checkout lifetime in seconds; `900`; valid range 1..3600 |
| `payments.jwt_key_env` | Environment variable holding the encryption key for ciphertext JWT profiles; default `PRONOGTI_PAYMENT_JWT_KEY`; resolved only when needed |
| `payments.mock_external` | Use an external loopback mock endpoint for mock profiles; `false` selects the in-process, nonpayable demo |
| `payments.creating_timeout_seconds` | Minimum stale-creation threshold; `120`; actual threshold also covers the configured bank timeout |
| `payments.worker_enabled` | Run browser-independent bank reconciliation; `true` |
| `payments.poll_interval` | Poll the durable confirmation queue; `2s`; valid range `1s`..`1m` |
| `payments.mock_auto_pay_after` | Server-only built-in mock completion delay; `2s`; `0s` disables auto-completion |
| `payments.webhook_enabled` | Mount the signed bank callback outside customer sessions; `false` until its public key is configured |
| `payments.webhook_public_key_env` | Environment variable holding the bank's PEM/JWK verification key; default `PRONOGTI_TOCHKA_WEBHOOK_PUBLIC_KEY` |
| `settlement.worker_enabled` | Run durable YClients synchronization; `true` |
| `settlement.allow_live` | Permit live YClients writes for a live profile and production payment; `false` |
| `settlement.poll_interval` | Synchronization queue polling; `2s`; valid range `1s`..`1m` |
| `fiscal.worker_enabled` | Run the durable receipt worker; `true`; disabling pauses pending jobs |
| `fiscal.allow_live` | Permit live receipts only for live profiles and production payments; `false` |
| `fiscal.poll_interval` | Receipt worker polling interval; `2s`; valid range `1s`..`1m` |
| `session.enabled` | `true`; the customer/kiosk APIs require sessions and startup rejects a disabled configuration |
| `session.cookie_name` | `_pronogti_s`, separate from Meatshop cookies |
| `session.kiosk_cookie_name` | `_pronogti_kiosk`; durable device credential cookie, distinct from `cookie_name` |
| `session.kiosk_cookie_max_age` | Rolling device-cookie age in seconds; `34560000` (400 days); valid range 1..34560000 |
| `session.cookie_domain` | Empty, producing a host-only cookie |
| `session.secure` | `false` for local HTTP; use `true` with HTTPS |
| `session.same_site` | `lax`; `strict` and `none` supported; `none` requires `secure: true` |
| `session.max_life_time` | Framework Redis session maximum lifetime; `2592000` (30 days); device registration is recovered separately |
| `session.max_idle_time` | Framework Redis session maximum idle time; `604800` (7 days) |
| `session.destroy_all_time` | Optional daily session clearing time, `HH:MM` or `HH:MM:SS` |
| `session.customer_max_life_time` | Maximum customer interaction lifetime; `1800` (30 minutes) |
| `session.customer_max_idle_time` | Customer idle timeout; `180` (3 minutes) |
| `session.redis.connect` | Redis URL; default `redis://127.0.0.1:6379/0`; `rediss://` supported |
| `session.redis.connect_env` | Optional environment variable for the Redis URL; takes precedence over `connect` |
| `session.redis.namespace` | `pronogti`; use a distinct value for each deployment/environment |
| `cors.allowed_origins` | Explicit frontend origins; default `["http://localhost:3000"]` |
| `cors.allowed_methods` | Session API needs GET, POST, DELETE, OPTIONS |
| `cors.allowed_headers` | Include `Content-Type`, `X-Salon-Session`, `X-Customer-Session`, and `Idempotency-Key` |
| `cors.exposed_headers` | Browser-visible response headers; Query-Id |
| `cors.allow_credentials` | `true` for session cookies |
| `cors.max_age_seconds` | Preflight cache lifetime; `86400` |
| `debug.log_config` | Log selected non-secret startup fields; `false` |
| `debug.log_level` | `debug`, `info`, `warn`, `warning` or `error`; default `info` |
| `debug.log_format` | `text` or `json`; default `text` |
| `yClients.partnerToken` | Required YClients partner token |
| `yClients.login` / `yClients.password` | Required YClients credentials |
| `yClients.UserToken` / `yClients.authedAt` | Optional cached authorization token and timestamp |
| `cache.ttl` | Existing in-memory YClients cache lifetime; `3h` |
| `client_timeout` | Timeout for each outgoing YClients HTTP request; `10s` |
| `shutdown_timeout` | Graceful shutdown timeout; `10s` |

An explicit `database.primary` takes precedence over `primary_env`. Set one source intentionally. If an environment-variable reference is selected, that variable must be populated at startup. Redis environment references support keeping Redis credentials out of JSON.

The loader adds DELETE, the salon/customer session headers and `Idempotency-Key` to CORS, including for legacy config files. Origins must contain only scheme and host, such as `https://kiosk.example.org`, without a URL path. Keep frontend and API on the same origin through the reverse proxy where practical. For local development, use the same host name on both sides (`localhost`, not a mixture of `localhost` and `127.0.0.1`). Cross-site deployments require HTTPS and suitable cookie settings.

Checkout lifetime is independent of Redis/customer lifetime. A prepared order is an immutable local quote; retrieving it still requires the same active customer interaction. Enabled `payment_profiles` are selected per kiosk or salon for QR creation. Profiles can reference a JWT through `jwt_env` or use encrypted JWT bytes with the deployment key. See [QR payments](qr-payments.md) for demo/sandbox/production settings and browser-independent confirmation.

Receipt device, cashier and fiscal settings live in company-owned `fiscal_profiles`, selected by `kiosks.fiscal_profile_id` or `salons.default_fiscal_profile_id`. Profiles default to demo mode, and mock/sandbox payments always stay in demo. Live receipt service credentials are resolved through the profile's `bearer_token_env` environment-variable name. See [receipt configuration](fiscalization.md) for profile examples and the separate driver process.

YClients synchronization settings live in company-owned `yclients_profiles`, selected by `kiosks.yclients_profile_id` or `salons.default_yclients_profile_id`. The selected profile and receipt contact are frozen with the checkout. These settings are separate from the acquiring bank profile and Terminal FA profile. See [YClients settlement](yclients-settlement.md).

Bank confirmation, YClients synchronization and receipt workers continue independently of customer logout or browser closure. Enabling the bank callback requires a valid PEM or RSA JWK public key in the named environment variable at startup. The shared callback URL is `/payments/tochka/webhook`; `/payments/tochka/webhook/PROFILE_ID` is an optional profile restriction. Neither requires a Redis cookie or customer headers. Payment and receipt secrets are never substituted into responses.

Both cookies are HttpOnly and share the configured domain, Secure and SameSite settings. PostgreSQL stores the public salon directory, kiosk bindings and device-token hashes. Redis holds the current salon/customer context through the existing session library. Redis must be available at startup. Public discovery and slug selection intentionally support unregistered personal browsers; customer/catalog operations require a selected salon, and device aliases require a registered kiosk.

Kiosk credentials have no automatic server-side expiry. A valid durable cookie is renewed on requests and recreates a missing/expired Redis device context without recovering a previous customer. Cookie deletion, browser retention limits, or expiry after prolonged inactivity still requires operator setup. Rotating or disabling a kiosk revokes its old key. See [sessions](sessions.md) for the distinct device and customer lifecycles.

Refreshed YClients tokens are written atomically to the loaded configuration file with owner-only permissions. New database/session settings survive this rewrite. Environment-resolved database/Redis secrets remain separate runtime values and are not substituted into the saved JSON. The configuration directory must remain writable for YClients token persistence.

Logging uses `slog` through `webapp/logger`. Raw goyclients HTTP debug logging remains disabled. Device provisioning tokens belong in the setup exchange and HttpOnly cookie; they must not be placed in frontend Vite environment variables.
