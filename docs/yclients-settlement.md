# Synchronizing a paid purchase with YClients

Shopping-cart edits and quote preparation do not create appointments, financial
transactions or goods movements. A confirmed bank payment queues one durable
YClients synchronization job for its immutable checkout. The job runs without
the customer browser or Redis session.

## Profiles

`yclients_profiles` are owned by a company. A kiosk selects
`kiosks.yclients_profile_id`; otherwise the backend uses
`salons.default_yclients_profile_id`. The selected settings are frozen on the
checkout, so changing a kiosk binding does not redirect a paid purchase.

| Setting | Meaning |
| --- | --- |
| `mode` | `demo` or `live` |
| `cashless_account_id` | YClients financial cashdesk/account, not the Tochka account |
| `storage_id` | Warehouse for newly purchased goods |
| `service_policy` | `require_visit` or `financial_income` |
| `service_income_expense_id` | YClients income article for standalone service purchases |

Missing bindings receive a safe demo snapshot. Existing salons receive demo
profiles through migration. Default demo profiles permit the complete cart flow
without writing anything to YClients. Real synchronization requires a production
bank payment, a live profile and `settlement.allow_live: true` together.

For an actual integration, create a company profile using real account, article
and warehouse IDs, then assign its returned ID:

```sql
INSERT INTO public.yclients_profiles (
	company_id, name, mode, cashless_account_id, storage_id,
	service_policy, service_income_expense_id, enabled
) VALUES (
	781898, 'Salon purchase accounting', 'live', 12345, 67890,
	'financial_income', 5, true
)
RETURNING id;

-- Replace profile/kiosk IDs with your actual values.
UPDATE public.kiosks SET yclients_profile_id = 1
WHERE id = 1 AND company_id = 781898;

UPDATE public.salons SET default_yclients_profile_id = 1
WHERE company_id = 781898;
```

The example IDs are placeholders. Obtain a company cashless account using the
YClients account list and choose the intended income article and goods warehouse.
The pre-payment check validates the frozen configuration before asking the bank
for a QR.

## Accounting policy

| Checkout item | Synchronization |
| --- | --- |
| Existing unpaid visit items | Resolve that visit's sale document and record the verified outstanding payment |
| New goods selected from the catalogue | Create a goods-sale document and stock transactions, then pay that document |
| New services with `financial_income` | Create an income transaction for each service line, with customer, employee, amount and checkout reference |
| New services with `require_visit` | Reject the checkout before bank registration |

`financial_income` deliberately records a purchased service as financial income.
It does not invent an appointment time, mark services as performed, change
attendance, or create a subscription. Immutable local lines and journaled
operation references retain the purchase detail. Choose `require_visit` if the
salon's reporting requires all service income to belong to an existing visit.

Payments into an existing visit do not remove its other services or debt. The
adapter does not use the former item-only `VisitAPI.Update` payload or force a
visit into an attended state.

## Recovery

The job persists its operation plan and external IDs. It records dispatch before
each upstream write and records each outcome independently. Completed operations
are reused after restart. An interrupted write is reconciled using its original
document/transaction references; it is not automatically submitted again.

If YClients is unavailable before dispatch, the job remains retryable. If an
upstream write may have succeeded, its state remains `unknown` until evidence
establishes the result. Matching only a monetary amount is insufficient when
staff and other integrations can write concurrently. The checkout reference,
company, account and returned identities are used together.

A downstream failure does not undo bank confirmation or prevent a fiscal receipt
from being processed. Inspect the separate `yclients_sync_status` and
`fiscal_status` values in the payment response, or run the operator payment-status
command. Resolve an uncertain upstream outcome before any manual replacement.

For a failed or blocked job that has never dispatched an upstream write, use
`go run ./cmd/payment -config ./config.json retry-settlement PAYMENT_UUID` after
resolving the cause. The repository refuses this action once any operation was
dispatched. Its original checkout, bank proof and planned operation identities
remain unchanged.

### Attributing an uncertain account payment

An account payment whose response was lost may need staff review when its sale
document has several concurrent payments. An operator with backend configuration
and database access can resolve that ambiguity using explicit existing YClients
transaction IDs. This operation is available only in the server CLI.

Find the unresolved local operation for the selected payment:

```sql
SELECT op.id, op.kind, op.state, op.error_code, op.dispatched_at
FROM public.yclients_operations op
JOIN public.payments p ON p.order_id = op.order_id
WHERE p.id = 'PAYMENT_UUID';
```

Inspect the original YClients sale and bank payment, attribute the corresponding
transactions, then supply their IDs and a review reason:

```sh
go run ./cmd/payment -config ./config.json resolve-settlement \
  PAYMENT_UUID visit-payment '12345,12346' \
  'Checked bank reference and original sale with salon staff; these transactions settle this checkout'
```

Use `goods-payment` for the equivalent goods-sale operation. The selected
operation must be `unknown` or `dispatched`, its bank payment must be confirmed
in production, and its frozen settlement profile must be live. The command
authenticates with the existing YClients configuration and reads the original
sale. It requires unique positive transaction IDs that were absent from the
pre-dispatch baseline, belong to the original document/account, and total the
exact kopeck amount. Matching the amount alone does not establish attribution;
the operator must review which transactions belong to this bank payment.

After verification, the database stores the selected IDs, reason and verification
time, marks that operation complete and requeues the remaining job. The command
sends no payment or goods-write request to YClients or the bank. It cannot resolve
service-income or goods-document creation by asserting success. Normal workers
resume remaining operations when enabled; fiscalization keeps its independent
journal and state.
