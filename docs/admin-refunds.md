# Administrator refunds

Apply migration `000019_payment_refunds`, then rebuild the backend. Set
`refunds.enabled` to register the routes and start the refund worker. Both flags
default to false.

```json
{
	"refunds": {
		"enabled": true,
		"allow_live": false
	}
}
```

Production refunds require `refunds.allow_live`, `fiscal.allow_live`, and an
issued original cashless sale receipt. The worker uses the original payment
registration and the saved fiscal receipt settings. Built-in mock refunds
simulate the bank and the receipt. Sandbox refunds use the configured bank
sandbox and simulate the receipt.

## API

All routes use the administrator session. The POST also requires the
`X-Admin-Session` header and a client-generated `Idempotency-Key`.

| Method | Route | Purpose |
|---|---|---|
| GET | `/admin/payments/{id}/refunds` | Original lines, remaining quantities and amounts, eligibility, and refund history |
| POST | `/admin/payments/{id}/refunds` | Reserve and queue a refund. Returns `202` with the refund ID and a status URL |
| GET | `/admin/payments/{id}/refunds/{refund_id}` | Current bank and receipt statuses |

```json
{
	"items": [
		{ "line_index": 0, "quantity": "1" },
		{ "line_index": 2, "quantity": "0.5" }
	]
}
```

Use the zero-based line indexes from the GET response. Quantities are decimal
strings. Services use whole units. The backend calculates amounts in kopecks
at the original receipt prices and checks both the selected quantities and
the remaining balance. For a full refund, select every remaining line and
its remaining quantity. Keep the same idempotency key when retrying the
same selection.

## Status

The worker confirms the bank result before sending a TerminalFA return
receipt. Poll the refund status to observe `bank_state` and `fiscal_state`
separately. A production refund is complete when they are `confirmed` and
`issued`. Test receipts finish as `demo`.

Tochka card refunds require the original bank operation to be in
`APPROVED` status. Once a card refund is confirmed, the app blocks further
card refunds for that payment. Live SBP refunds require the original
dynamic QR ID, the NSPK transaction ID from a signed bank notification,
and the original account in account/BIC form. Poll-confirmed SBP payments
become eligible once the signed notification supplies the transaction ID.
Use this workflow as the sole refund writer for a payment. Some card
acknowledgements identify the original sale only, so the worker correlates
the acknowledged amount with the new bank refund history.

An ambiguous bank submission reserves the amount and requires review.
An uncertain receipt is checked in the TerminalFA journal by refund ID.
Review bank and receipt outcomes before any external recovery work.
