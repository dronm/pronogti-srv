# Public salon and kiosk sessions

Pronogti uses the existing `webapp/session.NewRedisManager`, `webapp.SessionMiddleware`, and `github.com/dronm/session` interface. The public website and physical kiosks share the customer flow. A customer interaction can exist without a registered kiosk.

## First deployment

1. Start PostgreSQL and Redis.
2. Fill `config.json` from `config.example.json`, including YClients access. Set `PRONOGTI_DATABASE_URL` or an explicit `database.primary`.
3. Apply migrations through `000005_payment_checkout`. For a new database, apply all migrations in order.
4. Start the backend and frontend with their API URL and CORS settings configured.
5. Check the public salon name/slug. Provision physical kiosks separately when needed.

```sh
export DB_CONN="$PRONOGTI_DATABASE_URL"
migrate -database "$DB_CONN" -path ./migrations up
```

Migration `000004_salons` publishes only company `781898`, initially named `Salon 781898` at slug `company-781898`. Other YClients companies are not published automatically. The public frontend link is `/c/company-781898`.

## Public customer entry

An unregistered browser opens `/` to select from the active public salon directory. Selecting a salon opens `/c/:slug`, which also works as a direct link from a customer's phone/computer. The phone page resolves the slug through the backend before starting the customer interaction.

`GET /salons` returns the curated directory. `POST /salons/{slug}/session` establishes that salon's public session context; the slug is resolved in PostgreSQL, rather than trusting a supplied company ID. The response includes a random `scope_id`. Later catalog/customer calls send it as `X-Salon-Session` and obtain the company from the server session. Public customer access does not require `/kiosk/setup` or a kiosk credential.

Salon identity and version are rechecked on scoped requests. Disabling a salon or changing its identity invalidates its old public context. Selecting another salon clears the previous customer and cart. Nonces and scope correlation prevent late requests from a previous salon/customer from taking over the new interaction.

## Public salon administration

The salon company ID is the existing YClients company ID. `salons.company_id` and `kiosks.company_id` use the same external IDs; they do not identify locally created YClients companies.

```sh
go run ./cmd/salon -config ./config.json upsert -company 781898 -slug pronogti -name 'ПроНогти'
go run ./cmd/salon -config ./config.json disable -company 781898
go run ./cmd/salon -config ./config.json enable -company 781898
```

The upsert command changes the public name/slug or publishes another company. It preserves the disabled status of an existing entry; use `enable` explicitly to republish it. After renaming the example slug, the public link becomes `/c/pronogti`. These are operator CLI commands; no public write API publishes salons.

The directory orders entries by `sort_order`, name, and company ID. `sort_order` can be adjusted through database administration.

## Physical kiosk setup

Create a kiosk using its YClients company ID:

```sh
go run ./cmd/kiosk -config ./config.json create -name 'Kiosk 1' -company 781898
```

The CLI generates a token from 32 cryptographically random bytes. PostgreSQL stores only its SHA-256 hash. Open `/kiosk/setup` on the physical browser and enter the printed token once. The backend sets the framework session cookie and a separate HttpOnly kiosk credential cookie. The application does not save the key in localStorage or compile it into JavaScript.

The kiosk company must have an active salon entry to enter the shared customer flow. A company may have several kiosks. The registered kiosk opens its assigned salon's phone page, while ordinary visitors use the public selector/direct salon links.

## Persistent kiosk registration

The database kiosk credential has no automatic expiry. It remains valid until rotation, disablement, or removal. Redis retains the current session state through the existing provider, but losing a Redis session does not by itself require operator setup: the durable kiosk cookie authenticates the device and recreates only its kiosk/company context. It never restores the expired customer's identity or cart.

`session.max_life_time` and `session.max_idle_time` bound the framework Redis session, independently of durable device registration. The kiosk cookie uses `session.kiosk_cookie_name` and a rolling `session.kiosk_cookie_max_age`, defaulting to 400 days. Every valid request renews it. The browser can still remove credentials, impose retention limits, or expire a cookie after prolonged inactivity. Clearing browser data requires setup again; browser storage cannot be guaranteed permanent.

Existing registered browsers from before the durable cookie change can be provisioned once more at `/kiosk/setup` to receive that cookie.

```sh
go run ./cmd/kiosk -config ./config.json rotate -id 1
go run ./cmd/kiosk -config ./config.json disable -id 1
go run ./cmd/kiosk -config ./config.json enable -id 1
```

Rotation invalidates the old token. A disabled device loses its cookie when it next connects; enablement then requires setup again. Device binding/version is checked on every request. Reassigning a still-valid device credential to another company clears its old customer and restores the new company's context. The destination company must be published in `salons`.

```sql
UPDATE public.kiosks
SET company_id = 123456
WHERE id = 1;
```

## Customer lifetime

In both entry modes, entering a phone number starts a temporary customer interaction; the existing guest option remains available. Phone identification deliberately has no SMS/password challenge and does not verify ownership. YClients lookup uses the selected company and an exact normalized phone match; multiple distinct clients sharing a phone produce a conflict requiring staff resolution.

Shared customer/catalog requests send the current `X-Salon-Session` scope nonce. Customer-specific requests additionally send `X-Customer-Session`. Each interaction is bound to one server-selected salon scope, so late phone/customer requests cannot execute in a different salon after selection changes. Customer and cart data stay in memory on the frontend, with Redis authoritative after reload. Actual user activity renews the idle deadline; polling does not.

Defaults are 30 minutes total and 3 minutes idle. Cancellation, a new interaction, or expiry clears the customer/cart/payment state and returns to the salon's phone page. Ending a customer does not revoke kiosk registration. Public visitors can return to the salon selector to choose another company.

Session responses use `Cache-Control: no-store`. The resolver uses cryptographically random session IDs and a server-side creation marker. Browser mutations must originate from a configured frontend origin, including when using a same-origin reverse proxy.

## Payment scope

The same scope/customer nonces also protect authoritative checkout preparation and retrieval. Prepared orders store interaction hashes and do not become accessible to a new customer merely because the kiosk/company is unchanged. Checkout expiry is separate from customer expiry and does not extend it. See [checkout](checkout.md).

Checkout preparation supplies a durable local pricing snapshot and the migration creates later payment infrastructure. SBP bank confirmation, YClients purchase synchronization and sale receipts run as durable background jobs independent of the browser. Refunds and membership billing remain separate operations. Receipt processing is described in [receipt configuration](fiscalization.md); it runs from confirmed payment records outside customer sessions. QR creation requires an owned checkout ID and idempotency key; the legacy draft-save endpoint has been removed. See [API](api.md) and [QR payments](qr-payments.md).
