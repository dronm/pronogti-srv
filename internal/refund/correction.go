package refund

import (
	"encoding/json"
	"strings"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/fiscal"
)

// correctedRefundItems resolves the effective fiscal lines without changing
// the immutable checkout or the bank amount. A correction can change financial
// meaning, so this cashless bank-refund workflow accepts only the combinations
// it can represent exactly. Corrections after a reserved refund are prohibited
// by the correction service under the same checkout/payment locks.
func correctedRefundItems(payload json.RawMessage, original []checkout.Item, paid int64) ([]checkout.Item, string) {
	invalid := "corrected_receipt_invalid"
	unsupported := "corrected_receipt_refund_unsupported"
	if len(payload) == 0 || len(payload) > fiscal.MaxReceiptPayloadBytes || paid <= 0 || paid > fiscal.MaxReceiptKopecks {
		return nil, invalid
	}
	var receipt saleReceiptAmounts
	if err := json.Unmarshal(payload, &receipt); err != nil || receipt.CalculationSign != 1 || len(receipt.Items) < 1 || len(receipt.Items) > 200 {
		return nil, invalid
	}
	if receipt.TotalKopecks != paid || receipt.Payment.CashlessKopecks != paid ||
		receipt.Payment.CashKopecks != 0 || receipt.Payment.PrepaymentKopecks != 0 ||
		receipt.Payment.CreditKopecks != 0 || receipt.Payment.CounterProvisionKopecks != 0 {
		return nil, unsupported
	}
	items := make([]checkout.Item, 0, len(receipt.Items))
	var total int64
	for index, raw := range receipt.Items {
		var line saleReceiptLine
		if err := json.Unmarshal(raw, &line); err != nil || strings.TrimSpace(line.Name) == "" ||
			line.PriceKopecks <= 0 || line.PriceKopecks > fiscal.MaxReceiptKopecks {
			return nil, invalid
		}
		quantity, milli, err := checkout.ParseQuantity(line.Quantity)
		if err != nil {
			return nil, invalid
		}
		kind := checkout.KindGood
		switch line.PaymentObject {
		case 1:
		case 4:
			kind = checkout.KindService
			if milli%quantityScale != 0 {
				return nil, unsupported
			}
		default:
			return nil, unsupported
		}
		amount := roundedLineAmount(line.PriceKopecks, milli)
		if amount <= 0 || amount > receipt.TotalKopecks-total {
			return nil, invalid
		}
		item := checkout.Item{
			Selection: checkout.Selection{Kind: kind, Quantity: quantity},
			Title:     line.Name, UnitPriceKopecks: line.PriceKopecks, AmountKopecks: amount,
		}
		// A fiscal edit does not prove a catalogue association. Keep an ID only
		// when that position still identifies the same original catalogue item.
		if index < len(original) {
			old := original[index]
			if old.Kind == kind && old.Title == item.Title && old.UnitPriceKopecks == item.UnitPriceKopecks {
				item.ID, item.StaffName = old.ID, old.StaffName
			}
		}
		items = append(items, item)
		total += amount
	}
	if total != receipt.TotalKopecks {
		return nil, invalid
	}
	return items, ""
}
