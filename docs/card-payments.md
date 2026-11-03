# Card checkout

Apply all pending migrations through `000009_card_checkout`. Card payments use
Tochka's hosted checkout through the bundled `paytochkabank` library. The browser
never collects card numbers or determines whether a payment succeeded.

## Flow

1. Prepare an authoritative checkout using the existing checkout endpoint.
2. Select card payment. `POST /customer/init-card-pay` accepts only
	`{"checkout_id":"UUID"}`, with the current session headers and an
	`Idempotency-Key`. Retries return the same durable attempt.
3. The backend creates a one-stage bank link with `paymentMode: ["card"]` and
	`paymentLinkId` equal to the payment attempt UUID. The card screen opens
	the returned `checkout_url` on an explicit customer action.
4. A background worker reads the bank operation. Only `APPROVED`, with the
	original operation ID, merchant, customer and exact amount, confirms payment.
	`AUTHORIZED` is a hold and `WAIT_FULL_PAYMENT` is incomplete. Returning from
	the bank to the application does not confirm payment.
5. Confirmation marks the order paid and creates one YClients job and one
	Terminal FA receipt job in the same transaction. These continue without
	the browser or customer session. The card page displays their separate states.

`GET /customer/payments/{payment_id}` is shared with QR payments. Its `method`
is `card` or `sbp`; card attempts expose `checkout_url` instead of QR payload and
image. Kiosk aliases are `/kiosk/init-card-pay` and `/kiosk/me/init-card-pay`.
They require a kiosk session in addition to the current customer interaction.

An active card attempt prevents starting a QR attempt for that checkout, and
vice versa. A new browser key cannot bypass this hold. Customer/company/session
ownership checks apply to both methods.

## Bank configuration per kiosk

The existing `payment_profiles` binding selects bank settings:
`kiosks.payment_profile_id`, falling back to `salons.default_payment_profile_id`.
Public customers use the salon default. Migration 000009 adds:

| Column | Meaning |
| --- | --- |
| `cards_enabled` | Explicit card checkout enablement, initially false |
| `acquiring_merchant_id` | The 15-digit internet-acquiring merchant ID; separate from the SBP merchant ID |
| `card_ttl_minutes` | Link lifetime, capped by the checkout deadline |
| `card_redirect_url` | Frontend card screen after a successful bank interaction |
| `card_fail_redirect_url` | Frontend card screen after a failed bank interaction |

Settings are frozen on each payment attempt. Changing return URLs or link
lifetime affects new attempts. A used acquiring merchant identity cannot be
reassigned; create another profile. Credential values remain in environment
variables or the existing encrypted credential mechanism.

For a built-in local demo, enable cards on an existing mock profile:

```sql
UPDATE public.payment_profiles
SET cards_enabled = true,
	acquiring_merchant_id = '200000000001056',
	card_ttl_minutes = 15
WHERE id = YOUR_MOCK_PROFILE_ID AND company_id = 781898
	AND environment = 'mock';
```

The built-in mock produces a deliberately nonpayable checkout URL and confirms
it on the server according to `payments.mock_auto_pay_after`. The frontend
shows a demo message and does not open that URL. No bank, YClients financial
write or physical receipt is produced. Set the delay to `"0s"` to disable
automatic demo confirmation, then use `confirm-demo PAYMENT_UUID` with the
existing payment operator command.

For sandbox or production, configure both return URLs to the real frontend
card route, for example:

```sql
UPDATE public.payment_profiles
SET cards_enabled = true,
	acquiring_merchant_id = 'YOUR_15_DIGIT_ACQUIRING_MERCHANT',
	card_ttl_minutes = 15,
	card_redirect_url = 'https://YOUR_FRONTEND_HOST/pay-bank-card',
	card_fail_redirect_url = 'https://YOUR_FRONTEND_HOST/pay-bank-card'
WHERE id = YOUR_PROFILE_ID AND company_id = 781898;
```

Replace all placeholders. The backend appends the immutable `checkout_id` and
`payment_id` to the configured URLs. Production requires HTTPS; non-production
return URLs may use HTTP on loopback. Tokens need acquiring-create and
acquiring-read permissions. The production API base is
`https://enter.tochka.com/uapi`; the sandbox base is
`https://enter.tochka.com/sandbox/v2`. A phony JWT is supported only by the
configured mock server or bank sandbox, not production.

The bank request uses ordinary payment creation without bank fiscalization.
Terminal FA is the application's receipt issuer. See
[YClients settlement](yclients-settlement.md) and [fiscalization](fiscalization.md)
for their company profiles and live deployment switches. Production downstream
writes require the corresponding live profiles and `settlement.allow_live`
and `fiscal.allow_live` enabled before payment.

## Confirmation and recovery

The existing confirmation worker configuration also controls card polling.
For signed callbacks, enable the existing webhook configuration and subscribe
Tochka's `acquiringInternetPayment` event to:

```text
https://YOUR_API_HOST/payments/tochka/webhook
```

The shared handler verifies the raw JWT and durably journals the notification.
A card callback schedules an independent bank detail read; it is not accepted
as a browser success signal. The optional profile-ID URL alias further
restricts matching. Early callbacks can attach the original bank operation to
an interrupted creation when the signed `paymentLinkId` identifies the exact
attempt and the frozen company/merchant/amount match.

A lost creation response remains `unknown` and is never blindly resubmitted.
If neither the response nor a correlatable signed callback supplies the bank
operation ID, operator investigation is required. Local expiry or the absence
of a callback cannot prove nonpayment and does not release the financial hold.
A late verified payment can still settle the original order.

Use `go run ./cmd/payment -config ./config.json status PAYMENT_UUID` to inspect
progress. The existing safe retry and operator-evidence commands apply to
card-paid YClients and receipt jobs as well. Failed downstream work does not
revert bank payment or permit charging the customer again.

Migration 000009 rollback refuses while card payment records exist, rather
than discarding their method and recovery identity. Disable unused card-only
profiles before rollback if they lack the older SBP configuration.

## Validation

Run `go test -race ./...` and `go vet ./...` in the backend, and `npm test` and
`npm run build` in the frontend. PostgreSQL integration instructions are in
[payment validation](payment-validation.md). A controlled production payment
must verify actual bank credit, the YClients transaction and the Terminal FA
fiscal receipt before deployment acceptance.

Official contracts:
[Create Payment Operation](https://developers.tochka.com/docs/tochka-api/api/create-payment-operation-acquiring-v-1-0-payments-post),
[Get Payment Operation Info](https://developers.tochka.com/docs/tochka-api/api/get-payment-operation-info-acquiring-v-1-0-payments-operation-id-get),
[payment-link workflow](https://developers.tochka.com/docs/tochka-api/opisanie-metodov/platyozhnye-ssylki).
