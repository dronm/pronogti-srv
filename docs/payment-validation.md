# Payment validation

From the backend directory, run:

```sh
go test -race ./...
go vet ./...
```

The bank, YClients and Terminal FA unit tests use local HTTP servers or in-memory
adapters. They do not charge a bank account, write to a real salon or issue a
physical receipt. The bundled dependencies use relative source paths.

## PostgreSQL integration tests

Point the following variable at a dedicated local PostgreSQL instance whose
account can create databases:

```sh
export PRONOGTI_TEST_DATABASE_URL='postgres://USER:PASSWORD@127.0.0.1:5432/postgres?sslmode=disable'
go test -race ./internal/payment ./internal/fiscal ./internal/settlement
```

Without that variable the integration tests skip. They create isolated databases,
apply all migrations and drop those databases afterward. Never use a production
database server for these tests.

The integration suite checks:

- Authoritative checkout, QR/card creation and confirmation without a browser.
- Concurrent and duplicate confirmations, with exactly one receipt and CRM job.
- Card approval versus authorization, cross-method holds, lost card-link responses
	and exact-amount cashless card completion through simulated live downstream services.
- Early callbacks, late payments and paid-state monotonicity.
- Wrong amount, merchant, profile and customer isolation.
- Rollback of bank confirmation when durable downstream enqueueing fails.
- Concurrent worker claims, stale-worker fencing and receipt crash recovery.
- Explicit operator evidence resolution without repeating a YClients payment.
- Migration rollback and reapplication with paid orders retained.

From the frontend directory, run `npm test` and `npm run build`. The QR tests
cover persisted paid state, downstream progress, customer/cart isolation and
server-controlled expiry holds.

## Deployment acceptance

The local demo exercises the complete application path using simulated bank
confirmation and demo downstream results. Before accepting real payments,
configure the company profiles described in [QR payments](qr-payments.md),
[card payments](card-payments.md),
[YClients settlement](yclients-settlement.md) and [fiscalization](fiscalization.md),
then perform a controlled real payment and verify the actual bank credit,
YClients transaction and KKT receipt.

The bank's documented payment-history API confirms successful credit; absence
of a payment is not terminal proof of nonpayment. Local QR expiry keeps the
payment hold pending reconciliation. Lost YClients account-payment responses
require explicit transaction evidence; uncertain fiscal results retain their
original driver request ID. These cases must not be cleared by deleting journals
or submitting replacement financial writes.
