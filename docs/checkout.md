# Authoritative checkout

Checkout preparation freezes a backend-validated payable amount before a customer enters the payment screens. It creates a durable `payment_orders` row. It does not create a bank QR, charge money, change a YClients visit, or write off loyalty bonuses.

Apply all pending migrations through `000009_card_checkout` before using the current API. Existing `config.json` files receive the default `checkout.max_life_time` of 900 seconds; this is independent of the customer session lifetime. A bank profile does not need to be configured to prepare a checkout.

## Preparation request

`POST /customer/checkouts` requires cookies and all three headers:

```text
X-Salon-Session: current scope_id
X-Customer-Session: current customer.session_id
Idempotency-Key: a unique key for this unchanged checkout request
```

Send `Content-Type: application/json` and identifiers/quantities only:

```json
{
	"visit_id": 123,
	"record_id": 456,
	"items": [
		{
			"source": "visit",
			"kind": "service",
			"id": 789,
			"staff_id": 12,
			"quantity": "1"
		},
		{
			"source": "catalog",
			"kind": "good",
			"id": 987,
			"staff_id": 12,
			"quantity": "2"
		}
	],
	"bonus_to_apply_kopecks": 0
}
```

For an entirely new catalog purchase, use zero visit/record IDs and only `catalog` lines. `line_id` identifies an existing visit goods transaction when applicable. The backend obtains company, client, entry mode, and kiosk from the current session. Caller-supplied prices, amounts, company/client IDs, and unknown fields are rejected.

## Pricing rules

- Existing visit selections are matched against a newly fetched unpaid visit. Visit/record identity, item kind, item ID, transaction line ID, staff and quantity must match. Quote preparation can price a subset, but QR registration requires the entire outstanding visit: YClients account payments cannot allocate payment to selected visit lines. An existing line's outstanding quantity cannot be changed. A stale or fully paid visit is rejected rather than converted to a catalog purchase.
- Each selected visit line uses its full current outstanding amount. In particular, an unpaid goods balance is not reconstructed by multiplying its rounded display unit price by quantity.
- Catalog selections use fresh company/staff-specific service prices, goods prices, and staff validation. The regular browse cache is not the checkout price authority. Variable-price services are rejected when the adapter cannot establish a fixed price.
- Service quantities are positive integers. Goods may use a positive decimal quantity with up to three nonzero decimal places. Quantities are decimal strings in the API and cannot exceed 1,000,000. Each request accepts 1 to 200 selections.
- Monetary amounts in the prepared order are integer kopecks. Catalog line amounts are rounded once, half up, to a kopeck with checked integer arithmetic, then summed. Unsupported precision, invalid values and overflow are rejected.
- Duplicate selections are rejected. The frontend can combine identical catalog selections before submission.
- Nonzero bonus redemption is rejected until actual server-side write-off is implemented. Displayed loyalty balances do not authorize reducing a bank charge.
- Guests can prepare catalog purchases, but cannot select existing visit lines. A phone customer must finish registration before checkout.

Scope/customer validity is checked again after external reads before an order is saved. The stored owner contains hashes of the salon/customer interaction nonces. Another customer, another company, or a replacement interaction cannot read the prepared order by knowing its ID.

## Response and recovery

The response is the prepared order itself. It includes `id`, `visit_id`, `record_id`, `items`, `subtotal_kopecks`, `bonus_kopecks`, `payable_kopecks`, `currency`, `state`, `expires_at`, and timestamps. Each item contains the validated selection plus `title`, `staff_name`, `unit_price_kopecks`, and `amount_kopecks`. The currency is `RUB`, state starts as `prepared`, and YClients synchronization starts as `not_started`.

`GET /customer/checkouts/{id}` retrieves an unexpired prepared checkout for the same current owner. It uses both session headers. Persistence does not extend a customer session or restore a prior customer's identity after logout. The frontend may retain a checkout ID in a payment route for reload recovery, but authorization still comes from the current customer session.

The frontend prepares the checkout before entering payment selection and uses the returned amount. If server pricing differs from the displayed cart, it applies the verified values and asks the customer to review them before continuing. Editing the cart invalidates its prepared checkout. Expired or missing checkouts must be prepared again.

The same owner and `Idempotency-Key` with the same canonical request returns the existing unexpired order, including under concurrent creation. Reusing the key for different content returns a conflict. Replaying an expired order also returns a conflict; prepare a new checkout with a new key. Request idempotency does not establish bank-side exactly-once processing.

Expired prepared quotes return HTTP 409 with `error.details.code: "checkout_expired"`; other checkout states use `checkout_not_prepared`. The frontend clears a stale preparation key only for expiry, so the next customer-initiated retry obtains a fresh quote. Transport errors retain the key to recover a quote whose first response was lost.

## Payment schema

Migration `000005_payment_checkout` adds:

| Table/change | Responsibility |
| --- | --- |
| `payment_profiles` | Company-owned mock/sandbox/production bank settings, encrypted JWT bytes, identifiers, QR lifetime and timeout |
| `kiosks.payment_profile_id` | Optional physical kiosk profile binding |
| `salons.default_payment_profile_id` | Optional public-customer default profile binding |
| `payment_orders` | Immutable server-priced checkout and ownership/idempotency metadata |
| `payments` | Bank QR attempts, provider identifiers, statuses and reconciliation fields |
| `payment_events` | Verified event inbox, deduplication and processing fields |

Kiosk/salon profile bindings enforce company ownership. Historical checkout ownership remains unchanged when a kiosk is reassigned; its current profile binding is cleared when it no longer belongs to the new company. Used bank identity configuration must not be changed in a way that reroutes an existing bank attempt. Separate profile rows support test/production switches while old attempts retain their original profile.

Checkout preparation populates `payment_orders`. Migration `000006_fiscal_receipts` adds frozen fiscal settings and receipt jobs. Migration `000007_qr_attempt` enables durable QR creation/reuse with per-kiosk/salon bank profiles and JWT environment references. Migration `000008_payment_completion` adds independent confirmation, YClients profiles and operation journals, buyer contact snapshots and receipt recovery fencing. Migration `000009_card_checkout` adds method-aware card attempts and acquiring profiles. See [QR payments](qr-payments.md) and [card payments](card-payments.md).

## QR integration

`POST /customer/init-qr-pay` accepts `{ "checkout_id": "..." }` and `Idempotency-Key`. It validates current ownership and the authoritative quote before reserving a bank attempt. Active attempts are reused, including after a lost browser response. `GET /customer/payments/{id}` retrieves persisted attempt state for the current owner.

QR creation changes the checkout to `payment_pending`; it does not mean the customer paid. The QR screen recovers through the payment APIs, because the ordinary checkout getter intentionally returns only usable prepared quotes. Creating/displaying a QR does not create a YClients appointment or issue a fiscal receipt. Verified bank credit marks the payment and order paid and queues independent YClients synchronization and receipt processing. Their states are exposed separately by the payment API.
