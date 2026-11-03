# SBP QR payment, confirmation and recovery

Apply all pending migrations, including QR attempt and payment-completion migrations.
The backend creates a dynamic SBP QR through the bundled `paytochkabank` SDK,
using the immutable checkout amount. The legacy draft-saving endpoint has been
removed; shopping-cart editing does not create a YClients appointment.

## Request and response

`POST /customer/init-qr-pay` accepts only `{ "checkout_id": "UUID" }` and requires
cookies plus `X-Salon-Session`, `X-Customer-Session` and `Idempotency-Key`.
Reuse the same key for retries of the same checkout action. A lost response is
not permission to submit a new bank operation.

The response contains `payment_id`, `checkout_id`, `state`, `amount_kopecks`,
`currency`, `environment`, `expires_at`, and, when available, `payload` and
`qr_image_url`. Completion fields include `order_state`, `yclients_sync_status`, `fiscal_status`, `paid_at`, and `bank_reference`. The image is a PNG data URL. Provider customer, merchant and
account identifiers and all credentials remain private. The browser displays
the authoritative amount and can open the HTTPS bank payload on a phone.

`GET /customer/payments/{id}` requires the same current customer and salon scope
and returns the persisted attempt. It does not query the bank. Kiosk aliases are
`/kiosk/init-qr-pay`, `/kiosk/payments/{id}`, `/kiosk/me/init-qr-pay` and
`/kiosk/me/payments/{id}`; they additionally require kiosk mode.

## Attempt states

| State | Meaning |
| --- | --- |
| `creating` | One worker has reserved the bank request; wait and retrieve it |
| `pending` | QR creation was confirmed; payment itself is not yet confirmed |
| `unknown` | Creation may have reached the bank; no automatic replacement |
| `failed` | The request was definitively rejected before a usable QR was obtained |
| `expired` | The QR display deadline elapsed; the bank outcome still needs reconciliation |
| `paid` | The server verified bank credit and committed the paid order |

The database serializes reservation for an order. Repeated initialization reuses
an existing active attempt, including when the browser supplies another key.
Keys are scoped to a checkout; unknown financial or identity fields are rejected. Known paid
attempts are never replaced. Bank calls run after the reservation commits, so no
database transaction waits on the network. A lost bank response or interrupted registration stays
uncertain; stale `creating` attempts do not cause another bank call.

Checkout ownership includes the company, customer interaction, salon scope and
kiosk/public entry mode. Refreshes can recover the attempt while that interaction
is active. A new customer session cannot recover the previous customer's QR. Local QR expiry retains the order's payment hold; it does not authorize another registration. Bank-status reconciliation must establish the outcome before that hold is released.
The frontend URL may retain checkout/payment IDs, but these IDs do not authorize
access by themselves.

## Profiles and credentials

A kiosk uses `kiosks.payment_profile_id` when assigned. Otherwise it uses
`salons.default_payment_profile_id`; public customers use the salon default.
Profiles must belong to that company and be enabled. Their bank identity and
settings are frozen on each attempt, so changing a binding does not redirect an
existing QR to another merchant.

`payment_profiles.jwt_env` names an environment variable containing the JWT.
The secret itself is not stored in the profile snapshot or sent to the frontend.
Existing `jwt_ciphertext` values are supported through the configured
`payments.jwt_key_env`. The key is 32 bytes encoded as hex or standard base64;
the bytea format is version byte `1`, a 12-byte nonce, then AES-256-GCM ciphertext
and authentication tag. Additional authenticated data is
`pronogti:tochka:COMPANY_ID:PROFILE_ID`. Prefer `jwt_env` unless you already have
credential-encryption tooling; plaintext JWT bytes are not ciphertext.

The direct SBP `account_id` value has the form `ACCOUNT_NUMBER/BIC`. It identifies
the Tochka bank account and is unrelated to a YClients financial cashdesk ID.
`qr_ttl_minutes`, `http_timeout_seconds`, merchant/customer identifiers and the
API base URL are configured per profile. The usable lifetime is also bounded by
the checkout's expiry.

## Local demo

With the default `payments.mock_external: false`, a `mock` profile returns a
locally generated, deliberately nonpayable QR. No HTTP request is sent to the
bank, and the response is visibly marked `environment: "mock"`. With the default
`payments.mock_auto_pay_after: "2s"`, a background worker confirms this deliberately
simulated payment. YClients synchronization and fiscalization then finish in demo
mode without external writes. Closing the browser does not stop these jobs.

Example profile for company `781898`:

```sql
INSERT INTO public.payment_profiles (
	company_id, name, environment, api_base_url, jwt_env,
	client_id, customer_code, sbp_merchant_id, account_id,
	qr_ttl_minutes, http_timeout_seconds, enabled
) VALUES (
	781898, 'Local QR demo', 'mock', 'http://127.0.0.1:8099',
	'PRONOGTI_TOCHKA_DEMO_JWT', 'demo-client', 'demo-customer',
	'MF0000000001', '40702810900000000001/044525104', 15, 20, true
)
RETURNING id;
```

Use the returned profile ID in the desired kiosk binding, or make it the salon's
default. For example, replace the two IDs below with your actual values:

```sql
UPDATE public.kiosks SET payment_profile_id = 1
WHERE id = 1 AND company_id = 781898;

-- Optional fallback for public customers and kiosks without an override.
UPDATE public.salons SET default_payment_profile_id = 1
WHERE company_id = 781898;
```

For an external local mock endpoint, set `payments.mock_external: true` and
populate the named variable with a phony JWT, for example
`export PRONOGTI_TOCHKA_DEMO_JWT=phony.jwt.token`. External mock URLs must be
loopback URLs. The SDK sends the real request shape to that fake server.

## Sandbox and production

Tochka sandbox uses `environment: "sandbox"`, base URL
`https://enter.tochka.com/sandbox/v2`, and JWT `sandbox.jwt.token` in the variable
named by `jwt_env`. Sandbox responses may contain fixed example identifiers;
they are not a stateful payment simulator. The backend also supports a loopback
mock endpoint for sandbox testing. Production profiles require HTTPS and actual
bank credentials/merchant/account settings.

Use an HTTP request deadline longer than the profile's bank timeout; the example
configuration uses `http.request_timeout: "60s"` and `http.write_timeout: "65s"`.
A timeout may occur after the bank registered a QR, so the backend retains the
attempt as uncertain instead of retrying creation.

## Independent confirmation

The background confirmation worker reads the bank's payment history for the
frozen customer and QR reference. Only the bank's final `Accepted` state is
considered successful credit; intermediate statuses do not mark an order paid.
Polling requires a stable bank reference. Signed notifications retain the
verified transaction or operation ID; a digital-ruble notification can lack a
refund reference, and the backend does not invent one. Local display expiry
and a failed payment attempt are not proof that a QR can no longer receive money.

Optionally enable signed callbacks with `payments.webhook_enabled: true`. Put
the bank's PEM or RSA JWK public key in the variable named by
`payments.webhook_public_key_env`, then register the externally reachable HTTPS
callback URL with Tochka:

```text
https://YOUR_API_HOST/payments/tochka/webhook
```

The reverse proxy must pass that path to the backend. The handler accepts the
bank's raw `text/plain` JWT, verifies its RSA signature and checks the configured
profile, merchant/customer identity, exact amount and QR correlation before
confirmation. It is mounted outside customer sessions, browser origin checks and
Redis cookie middleware. No customer interaction or frontend request is needed.
Polling remains active as recovery when callback delivery is delayed or absent.

The shared URL supports payment profiles that use the same Tochka API client.
Routing follows verified bank identity and the stored QR/purpose reference. The
optional `/payments/tochka/webhook/PAYMENT_PROFILE_ID` alias additionally restricts
the match to one profile; its path alone is never proof of a payment.

Verified notifications are journaled, including duplicates and notifications
that arrive before QR registration is fully persisted. A replay cannot create a
second receipt or synchronization job. Conflicting identities or amounts never
turn an unrelated order paid. The payment/order transition and durable downstream
job creation happen in the database transaction.

## Settlement and fiscalization

Bank payment, YClients synchronization and fiscalization have separate states.
A failure in YClients or Terminal FA does not turn received money back into an
unpaid order. The QR page continues to show successful payment while reporting
any pending or uncertain downstream work.

Configure [YClients profiles](yclients-settlement.md) and [Terminal FA
profiles](fiscalization.md) before production. New service purchases use the
explicit `financial_income` policy or require an existing visit; the backend does
not fabricate an appointment simply to store a shopping cart. Newly purchased
goods produce a sale document and stock transactions. Existing visit payments
reuse their existing document.

For live downstream processing, set `settlement.allow_live: true` and
`fiscal.allow_live: true`, select live profiles with actual company/device settings,
and use production bank payments. Mock and sandbox bank payments always use demo
synchronization and receipts. Receipt data includes the customer contact frozen
from the server session, so browser logout cannot remove receipt information.

## Operator inspection and recovery

Read a payment and its separate completion states without creating a customer
session:

```sh
go run ./cmd/payment -config ./config.json status PAYMENT_UUID
```

For a manual demo, set `payments.mock_auto_pay_after: "0s"`, create a QR through
the normal checkout page, then run:

```sh
go run ./cmd/payment -config ./config.json confirm-demo PAYMENT_UUID
```

The command accepts only a `mock` payment and uses the normal transactional
confirmation path. It cannot mark a sandbox or production payment paid. There
is no browser endpoint that overrides bank confirmation.

After fixing an account/configuration problem, retry a failed YClients job only
when no upstream write was dispatched:

```sh
go run ./cmd/payment -config ./config.json retry-settlement PAYMENT_UUID
```

The database enforces that restriction. The command refuses jobs with a recorded
dispatch, including uncertain operations; those continue through reconciliation
with the original references. A retry does not create a new bank payment.

For a failed receipt whose driver command was never dispatched, the corresponding
command is `retry-receipt PAYMENT_UUID`. It preserves the request ID and receipt
payload and refuses any dispatched or uncertain receipt.

For an uncertain YClients visit/goods account payment, the privileged
`resolve-settlement PAYMENT_UUID OPERATION_ID TRANSACTION_ID[,ID...] 'review reason'`
command verifies explicitly attributed transactions by reading the original
YClients sale, then records local audit evidence. See [settlement recovery](yclients-settlement.md#attributing-an-uncertain-account-payment)
for the review workflow and restrictions. It never posts another payment.

An interrupted bank registration with no known QR identifier is retained as
`unknown` and is never submitted again automatically. A verified notification
or an independent bank read must establish its identity and result. An uncertain
YClients write or receipt is likewise reconciled against its original journal
identity; do not delete rows or journals and restart the operation under a new ID.

The current flow handles sale payments and sale receipts. [Card checkout](card-payments.md) uses the same durable completion jobs. Refunds, specialized/marked goods and subscriptions remain separate operations.

See [payment validation](payment-validation.md) for unit, PostgreSQL recovery and
deployment acceptance checks.
