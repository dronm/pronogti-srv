# goyclients

Go client for the [YCLIENTS API](https://developers.yclients.com/ru/).

## Payments

Use `NewSaleAPI(companyID).PayContext` to record an externally confirmed payment
against an existing sale document. `NewVisitAPI(companyID).PayContext` resolves
the document for an existing visit and appointment before recording payment.

```go
payment := goyclients.AccountPayment{
	AccountID:     yclientsCashlessAccountID,
	AmountKopecks: 250000, // 2500.00 RUB, already confirmed by the bank.
}
sale, err := client.NewSaleAPI(companyID).PayContext(ctx, documentID, payment)
if goyclients.IsOutcomeUnknown(err) {
	// Keep the original checkout pending reconciliation. Do not repeat Pay.
	return err
}
if err != nil {
	return err
}
_ = sale.State.PaymentTransactions
```

Payment amounts are integer kopecks. Full payment supplies the verified remaining
amount; partial payment supplies the intended increment. The client does not
automatically settle a changing balance or retry a write.

See [payment workflow and appointment decisions](docs/payments.md).

## Verification

```sh
go test -race ./...
go vet ./...
```

Default tests use mocked HTTP. Existing live integration tests require the
`integration` build tag and configured credentials. Run those only deliberately:

```sh
go test -tags integration -run '^TestIntegration_' ./...
```
