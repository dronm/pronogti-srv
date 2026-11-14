# Receipts with Terminal FA

Apply all pending migrations through `000009_card_checkout`; `000006_fiscal_receipts` introduces the original receipt profiles and queue. The backend uses the uploaded `github.com/dronm/goterminalfa` source snapshot in `third_party/goterminalfa`. Its complete receipt workflow is an HTTP service: the public Go package exposes lower-level protocol calls, while `createReceipt`, validation, device locking and SQLite journaling belong to the driver service. The application calls that service rather than duplicating the device protocol.

The backend now creates and reuses QR attempts; see [QR payments](qr-payments.md). The server confirms bank credit independently of the customer browser, then queues receipt processing. Creating a checkout, opening a payment page or displaying a QR code never issues a receipt.

## Demo mode

The default application configuration is:

```json
{
	"fiscal": {
		"worker_enabled": true,
		"allow_live": false,
		"poll_interval": "2s"
	}
}
```

Migration `000006` creates demo profiles for existing salons. A missing profile binding also produces a demo snapshot. A kiosk can override its salon's default profile; customers on their own phones use the salon default. The selected settings are frozen on the checkout, so changing a binding or profile applies to new checkouts. Keep `allow_live: false` for a deployment-wide demo switch; changing a profile alone does not alter already prepared live checkouts.

Only a **live profile, a production bank payment and `fiscal.allow_live: true` together** permit a real receipt. New pending jobs in other combinations produce a stored `demo` result without any HTTP request to the driver. An existing uncertain live outcome remains `unknown` while live access is disabled; it is not relabeled as a demo. Sandbox and mock bank payments therefore cannot issue live receipts. Disabling `worker_enabled` pauses processing; pending jobs remain in PostgreSQL.

## Profiles and kiosk assignment

`fiscal_profiles` stores company-owned settings. `kiosks.fiscal_profile_id` selects an override; `salons.default_fiscal_profile_id` selects the fallback. A kiosk reassigned to another company loses its previous profile binding. Composite foreign keys prevent binding a profile from a different company.

This example creates and assigns a demo profile. Replace the kiosk ID with the ID printed by your kiosk provisioning command:

```sql
WITH profile AS (
	INSERT INTO public.fiscal_profiles (
		company_id, name, mode, cashier_name, tax_system,
		service_vat_rate, good_vat_rate, payment_method
	) VALUES (
		781898, 'Kiosk demo', 'demo', 'Cashier', 1, 6, 6, 4
	)
	RETURNING id, company_id
)
UPDATE public.kiosks AS kiosk
SET fiscal_profile_id = profile.id
FROM profile
WHERE kiosk.id = 1 AND kiosk.company_id = profile.company_id;
```

The numeric tax, VAT, payment method and payment object codes are the driver's protocol fields. Choose the actual values for your business before enabling live mode; the demo defaults are examples. Services map to payment object `4`, ordinary goods to `1`. Marked goods and other specialized fiscal item types are outside this integration.

For a live profile, configure `driver_base_url`, `device_id`, `bearer_token_env`, `timeout_seconds`, cashier identity, tax settings, and settlement fields. `bearer_token_env` is the **name** of an environment variable available to the backend; the token itself is not stored in the profile or checkout snapshot.

```sql
UPDATE public.fiscal_profiles
SET mode = 'live',
	driver_base_url = 'http://127.0.0.1:8080',
	device_id = 'cashdesk-ffd12',
	bearer_token_env = 'PRONOGTI_TERMINALFA_TOKEN',
	timeout_seconds = 100,
	cashier_name = 'YOUR CASHIER NAME',
	cashier_inn = 'YOUR CASHIER INN',
	settlement_address = 'YOUR SETTLEMENT ADDRESS',
	settlement_place = 'YOUR SETTLEMENT PLACE'
WHERE id = 1 AND company_id = 781898;
```

Use a dedicated profile for a real device rather than changing an existing demo profile if old checkouts should remain easy to distinguish. Enable `fiscal.allow_live` only in the production backend configuration. `internet_payment` and `suppress_print` are explicit per-profile flags; no value is inferred from the browser's payment page.

## Run the driver

Build or run the exact bundled dependency from the backend directory:

```sh
go tool goterminalfa -config /absolute/path/to/terminalfa-config.json
```

Start from `third_party/goterminalfa/config.ffd12.example.json`. Configure the actual device transport, a stable logical `deviceId`, `protocolProfile: "ffd1.2"`, the driver's bearer token and a durable SQLite database path. The driver needs serial/USB access or network access to the KKT; a centrally hosted backend typically reaches a driver service running beside the kiosk. The backend URL refers to that HTTP service, not to the KKT's raw TCP port. Protect access to the driver and keep its operation database across restarts.

The driver's `createReceipt` workflow checks shift state, opens a shift when necessary, registers the items and cashless payment, then finalizes. It leaves the shift open. Its HTTP timeout must allow the complete workflow and cleanup; the backend's default per-profile timeout is 100 seconds. These jobs run outside customer HTTP requests.

## Payment and failure handling

A transition of a bank payment to `paid` creates one durable `fiscal_receipts` job for the order. Processing also requires that the order is `paid`. The worker uses the checkout's immutable items and amount. It never accepts browser-supplied fiscal totals. Card and SBP payments both use `cashlessKopecks`; the remaining payment buckets are zero.

Before accepting a checkout, the receipt adapter checks item text, quantities, totals and supported fiscal fields. The driver only supports a unit price and quantity, without a separate line amount override. An outstanding visit line whose amount cannot be represented by those fields is rejected; the adapter does not silently change its price or fiscalize a different amount. Nonzero bonus redemption remains unsupported. Payloads are capped at 60 KiB to fit the driver’s default 64 KiB HTTP limit, including the command envelope. A validation failure returns HTTP 409 with `fiscal_receipt_unsupported`; a quote with an invalid frozen configuration requires a new checkout/idempotency key after the configuration is corrected.

Each sale uses the stable request ID `pronogti-sale-<order UUID>`. The backend stores the request and response with states `pending`, `processing`, `demo`, `issued`, `failed` or `unknown`. The driver also journals the same ID. Duplicate payment notifications cannot create a second sale receipt for the same order.

A lost response or interrupted request is uncertain. The worker checks the driver's request lookup using the original ID and retains uncertainty when the journal cannot establish the result. It does not issue a replacement request with a new ID. An acknowledged cancellation is not proof that an earlier uncertain finalization did not issue a receipt. Investigate `failed` jobs and reconcile `unknown` jobs using the driver's journal before any manual action.

A failed receipt whose command was never dispatched can be requeued after fixing
the cause with `go run ./cmd/payment -config ./config.json retry-receipt PAYMENT_UUID`.
The database requires a paid payment/order, refuses any recorded dispatch or
uncertain outcome, and preserves the original request ID, payload and profiles.
Frozen invalid receipt data requires explicit investigation; this command does
not edit the checkout or rebuild its fiscal values.

The integration creates sale receipts and sends the server-snapshotted customer contact to the driver when available. Refund receipts, specialized goods, independent delivery-service integration and shift-closing schedules are separate work.

## Closing shifts

The administrator can close a saved fiscal profile's shift; a bound kiosk has an
operator-protected maintenance page at `/kiosk/close-shift`. The standalone
`cmd/fiscal-shift` command and persistent cron wrapper support unattended closing
of open shifts. See [shift closing](shift-closing.md) for deployment, permissions,
timeouts, safe retry behavior, and scheduling examples.
