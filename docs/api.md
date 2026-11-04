# API

Public discovery and customer sessions are independent of operator kiosk setup. Company/client IDs are resolved from the server-selected salon context and customer session. The earlier unscoped `/companies` and `/{companyID}/...` APIs remain removed.

Successful calls return HTTP 200 with direct JSON objects/arrays. Requests send cookies. All shared `/customer/...` calls also send `X-Salon-Session` with the current server-generated `scope_id` from the session response. Customer-specific calls additionally send `X-Customer-Session` with the interaction nonce from `customer.session_id`. Both nonces are separate from either cookie value.

## Discovery and entry

| Method | Path | Behavior |
| --- | --- | --- |
| GET | `/session` | Restore current context; anonymous browsers get HTTP 200 with null identities |
| GET | `/salons` | Active public salon identities, ordered by the directory |
| POST | `/salons/{slug}/session` | JSON `{}`; resolve the public slug and select its company context; clear any previous customer |
| POST | `/kiosk/session` | Operator JSON `{ "token": "KIOSK_TOKEN" }`; bind an active kiosk and set its durable cookie |
| GET | `/kiosk/session` | Strict kiosk-mode restore; requires a valid device binding |

A salon identity contains `company_id`, `slug`, and `name`. The public selector is backed by `salons`, rather than exposing every company available to the YClients credentials. A kiosk credential is not required for public salon selection.

Bootstrap, directory listing, salon selection and device setup do not require the salon nonce. It is returned after selecting/restoring a salon and changes when the context changes. A request from a previous context is rejected instead of looking up its phone/customer in a newly selected company.

## Shared customer API

These routes support both public and kiosk contexts:

| Method | Path | Session / behavior |
| --- | --- | --- |
| POST | `/customer/session` | Salon context required; JSON `{ "phone": "79991234567" }` or `{ "guest": true }` |
| DELETE | `/customer/session` | Customer nonce required; end the interaction; returns `null` |
| POST | `/customer/session/touch` | Customer nonce required; record actual customer activity |
| GET | `/customer/company` | Selected salon context required; assigned company object |
| GET | `/customer/staff` | Selected salon context required; active, visible staff |
| GET | `/customer/service-cats` | Selected salon context required; optional `staff` filter |
| GET | `/customer/services` | Selected salon context required; optional `staff`, `cat`, `term` filters |
| GET | `/customer/goods` | Selected salon context required; optional `term` filter |
| GET | `/customer/me/pay-draft` | Customer nonce required; current customer's latest unpaid visit; guest gets an empty draft |
| POST | `/customer/client/register` | Customer nonce required; register the current phone customer |
| POST | `/customer/checkouts` | Both nonces and `Idempotency-Key` required; prepare a server-priced checkout |
| GET | `/customer/checkouts/{id}` | Both nonces required; retrieve the current owner's unexpired prepared checkout |
| POST | `/customer/init-qr-pay` | Both nonces and `Idempotency-Key` required; create or reuse a QR attempt for `{ "checkout_id": "..." }` |
| POST | `/customer/init-card-pay` | Both nonces and `Idempotency-Key` required; create or reuse a hosted card attempt for `{ "checkout_id": "..." }` |
| GET | `/customer/payments/{id}` | Both nonces required; retrieve the current owner’s persisted attempt |

The former `/kiosk/customer-session`, `/kiosk/customer-session/touch`, `/kiosk/company`, `/kiosk/staff`, and other `/kiosk/...` customer-operation aliases remain available for existing kiosk callers. Every alias requires both a valid kiosk binding and kiosk-mode context. A public salon session alone cannot use those aliases, even if a cached kiosk identity is present. New frontend calls use the shared `/customer/...` API.

Telephone input is normalized to eleven digits beginning with `7`; formatted Russian numbers and a leading `8` are accepted. Entering a telephone number does not verify ownership. A phone customer not found in YClients receives `registered: false`; use registration before paying. Guests can select new goods/services without existing customer information.

## Session response

An active public customer response is:

```json
{
	"kiosk": null,
	"scope_id": "opaque-salon-context-nonce",
	"salon": {
		"company_id": 781898,
		"slug": "company-781898",
		"name": "Salon 781898"
	},
	"mode": "public",
	"customer": {
		"session_id": "opaque-interaction-nonce",
		"client_id": 123,
		"phone": "79991234567",
		"name": "Customer",
		"email": "",
		"guest": false,
		"registered": true,
		"expires_at": "2026-11-02T12:30:00Z"
	},
	"customer_idle_timeout_seconds": 180
}
```

Kiosk mode uses `mode: "kiosk"` and a `kiosk` identity containing `id`, `name`, and `company_id`. The selected salon and `scope_id` are included in both modes. `customer` is null before phone/guest entry, after logout, or after expiry. A fresh anonymous response has `kiosk`, `salon`, and `customer` all null and empty `mode`/`scope_id` strings. Explicit public selection is independent of a cached device registration.

Registration uses `phone`, optional `name`, `email`, and `birth_date`. Its phone must match the current session. Birth dates accept `YYYY-MM-DD` or RFC3339 timestamps. Successful registration returns the updated session response and reuses an existing matching client when found.

Customer operations reject missing, stale, or another interaction/salon's nonce. Checkout requests contain only visit/item/staff identifiers, source kinds and decimal-string quantities; the backend supplies company/client identity and prices. The response stores totals in integer kopecks. See [authoritative checkout](checkout.md) for the request example and pricing rules. QR creation accepts a checkout ID and derives its amount from the owned prepared order; browser `amount`/`client_id` fields are not accepted. See [QR payments](qr-payments.md) for states, recovery and bank configuration.

## Errors

Errors use the webapp format. Session failures return HTTP 401 with `details.code` of `salon_required`, `customer_session_required`, or `kiosk_required`. Invalid or disabled salon selection returns not found. Internal errors are logged and sanitized.

```json
{
	"error": {
		"code": "unauthorized",
		"message": "customer session required",
		"details": { "code": "customer_session_required" }
	}
}
```

Malformed filters/input return HTTP 400. Required registration, stale checkout inputs, unsupported bonus redemption, an expired checkout, or an idempotency-key content mismatch can return HTTP 409. An attempt to operate on another customer's object is rejected without returning its contents. Session-start, salon-selection and checkout requests require JSON and bounded request bodies.

## Integration boundary

The legacy `/customer/me/save-pay-draft` and kiosk aliases are removed and return 404. Checkout preparation stores a local `payment_orders` snapshot; it does not mutate a YClients appointment. QR creation/reuse now writes durable `payments` attempts and calls the configured bank SDK for sandbox/production or external mock profiles. Default in-process mock mode sends no bank requests.

Payment status reads return persisted state updated by independent bank reconciliation and verified callbacks. They include the paid order, YClients synchronization and receipt statuses. Card checkout uses the same status API with `method: "card"` and a `checkout_url`, and confirms payment through independent bank reads. See [card payments](card-payments.md). Refunds remain separate operations. The [receipt worker](fiscalization.md) consumes confirmed paid orders; displaying a QR does not issue a receipt.

## Administrator API

The same executable serves a separately authenticated `/admin/...` namespace.
Login, session restoration, logout and starter shell endpoints use their own
HttpOnly cookie and Redis namespace. Customer or kiosk credentials do not grant
administrator access. See [administrator API and setup](admin.md).
