package refund

import (
	"context"
	"testing"

	"github.com/dronm/pronogti/internal/payment"
)

type terminalQuoteRepository struct {
	Repository
	sale Sale
}

func (r terminalQuoteRepository) LoadSale(context.Context, string) (Sale, []Refund, error) {
	return r.sale, nil, nil
}

func TestTerminalRefundIsExplicitlyUnsupportedBeforeOnlineValidation(t *testing.T) {
	sale := Sale{
		Bank:    BankPayment{Method: payment.MethodTerminalCard, PaymentID: "terminal-payment", AmountKopecks: 100},
		OrderID: "order", PaymentState: "paid", OrderState: "paid",
		// Terminal refunds must be rejected before parsing lines or consulting
		// the online bank/fiscal adapters, which intentionally are absent here.
	}
	service := NewService(terminalQuoteRepository{sale: sale}, nil, nil, false)
	quote, err := service.Quote(context.Background(), sale.Bank.PaymentID)
	if err != nil || quote.Eligible || quote.Reason != "terminal_refunds_unsupported" {
		t.Fatalf("terminal refund quote did not identify its unsupported channel: %+v, %v", quote, err)
	}
	if reason := service.ineligible(sale, nil); reason != "terminal_refunds_unsupported" {
		t.Fatalf("terminal refund creation may reach the online adapter: %s", reason)
	}
	if err := (SDKBank{}).Validate(sale.Bank); err == nil {
		t.Fatal("online bank adapter accepted a physical terminal refund")
	}
}
