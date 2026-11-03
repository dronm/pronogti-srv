# Recording external payments in YCLIENTS

Reviewed against the [official API specification](https://developers.yclients.com/ru/)
on 2026-11-03. The acquiring bank confirms receipt of money; this library records
that payment in YCLIENTS. Calling `Pay` does not charge a card, create an SBP QR
or print a receipt.

## API methods

| Method | Purpose |
| --- | --- |
| `client.AccountsContext(ctx, companyID)` | List company cashdesks; `Type == 1` denotes cashless |
| `client.NewSaleAPI(companyID).GetContext(ctx, documentID)` | Read current sale items, transactions and payment methods |
| `client.NewSaleAPI(companyID).PayContext(ctx, documentID, payment)` | Record an account payment against the sale document |
| `client.NewVisitAPI(companyID).PaymentDocumentIDContext(ctx, visitID, recordID)` | Resolve the visit document from `records[].documents` |
| `client.NewVisitAPI(companyID).PayContext(ctx, visitID, recordID, payment)` | Convenience lookup followed by the same sale payment |

The underlying payment operation is
`POST /api/v1/company/{company_id}/sale/{document_id}/payment`, with method slug
`account`, an explicit YCLIENTS `account_id` and a decimal ruble amount. The Go
input is `AccountPayment{AccountID, AmountKopecks}`; conversion uses integer
arithmetic. Response monetary fields use `json.Number`.

Document, visit and appointment/record IDs are different. The visit resolver
selects the requested company's record and its type-7 document. Missing,
inconsistent or ambiguous documents produce an error before payment. For a
durable checkout, resolve and store `documentID` before charging, then use the
sale API with that frozen reference.

Use a separate company-scoped YCLIENTS cashless-account setting. The bank's
account identifier in Pronogti `payment_profiles.account_id` is a different value.
The API requires partner and user authorization with appropriate finance rights.

## Choosing the sale target

These are application workflow recommendations; the payment method deliberately
does not create or alter appointments.

| Customer selection | Recommended handling |
| --- | --- |
| Existing unpaid visit | Reuse its existing document; do not create another appointment |
| Extra services performed during that visit | Save them to the appropriate existing appointment, refresh the sale, then prepare the charge |
| New services with a real employee and scheduled time | Create an appointment and retain the returned visit/record/document references |
| Goods only | Create a standalone goods-sale document and stock transactions, then pay that document |
| Services for a future, unspecified appointment | Choose an explicit prepayment, deposit, certificate or subscription workflow instead of fabricating attendance |

YCLIENTS documents standalone goods sales through its storage-operation API.
This update does not implement goods-sale creation, inventory mutation or a
general cart-to-appointment synchronizer. Existing `VisitAPI.Update` sends an
item-only payload; the current specification describes a different update
contract. That method is not used by `Pay` and needs a separate compatibility
update before it is used to persist checkout selections.

Appointment creation now sends a full ISO timestamp, including timezone, while
retaining the existing `CreateRecord.DateTime` Go field type for compatibility.
Set it from a complete timestamp rather than a date parsed without a time:

```go
record := goyclients.CreateRecord{
	StaffID:      staffID,
	DateTime:     goyclients.APIDate(appointmentTime),
	SeanceLength: 1800,
	Services:     []goyclients.CreateRecordService{{ID: serviceID}},
	Client:       &goyclients.CreateRecordClient{Phone: customerPhone},
	APIID:        checkoutReference,
}
records, err := client.NewRecordAPI(companyID).Create(record)
```

This is an appointment write, not an idempotent payment operation. Set the
employee, time and attendance according to the actual booking. An `api_id`
reference is not a documented server-side idempotency guarantee.

## Failure handling and application integration

Before requesting money, verify the sale matches the authoritative checkout and
the cashless account is appropriate. Serialize payment/settlement for that sale
in the application. After bank confirmation, durably enqueue YCLIENTS settlement
and fiscalization independently; retain the confirmed bank payment if either
downstream operation fails.

There is no automatic retry or invented idempotency header. Transport loss,
server failures, HTTP 408, cancellation after dispatch or an unreadable write
response return `OutcomeUnknownError`. Read the original sale and compare
transaction identities with the application's saved pre-payment state before
deciding whether another write is needed. Amount equality alone cannot identify
your transaction when other staff or integrations can write to the same sale.

`APIError` exposes the status of an explicit rejection and supports
`errors.Is(err, goyclients.ErrUnauthorized)` and `ErrForbidden`. Existing
`SendRequest` behavior is retained; the additive `SendRequestContext` supplies
cancellation, bounded responses and uncertainty handling for the new APIs.

Deleting a YCLIENTS payment entry is not a bank refund. Loyalty, refunds and
receipt registration are separate operations. No live payment or receipt is
issued by the test suite.
