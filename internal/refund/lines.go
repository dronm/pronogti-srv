package refund

import (
	"errors"
	"fmt"
	"math/big"
	"sort"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/fiscal"
)

const quantityScale int64 = 1000

// Selection identifies a line by its zero-based position in the frozen order.
type Selection struct {
	LineIndex int    `json:"line_index"`
	Quantity  string `json:"quantity"`
}

// Line is the authoritative refund of part or all of a frozen order line.
type Line struct {
	LineIndex        int    `json:"line_index"`
	ItemID           int64  `json:"item_id"`
	Kind             string `json:"kind"`
	Title            string `json:"title"`
	Quantity         string `json:"quantity"`
	QuantityMilli    int64  `json:"quantity_milli"`
	UnitPriceKopecks int64  `json:"unit_price_kopecks"`
	AmountKopecks    int64  `json:"amount_kopecks"`
}

type ConsumedLine struct {
	QuantityMilli int64
	AmountKopecks int64
}

func normalizeSelections(selections []Selection) ([]Selection, error) {
	if len(selections) == 0 || len(selections) > 200 {
		return nil, errors.New("select between 1 and 200 refund lines")
	}
	normalized := append([]Selection(nil), selections...)
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].LineIndex < normalized[j].LineIndex
	})
	for i := range normalized {
		line := &normalized[i]
		if line.LineIndex < 0 || line.LineIndex >= 200 {
			return nil, errors.New("refund line_index must be between 0 and 199")
		}
		if i > 0 && normalized[i-1].LineIndex == line.LineIndex {
			return nil, fmt.Errorf("refund line_index %d is duplicated", line.LineIndex)
		}
		quantity, _, err := checkout.ParseQuantity(line.Quantity)
		if err != nil {
			return nil, fmt.Errorf("refund line_index %d: %w", line.LineIndex, err)
		}
		line.Quantity = quantity
	}
	return normalized, nil
}

func buildLines(items []checkout.Item, paid int64, selections []Selection, consumed map[int]ConsumedLine) ([]Line, int64, error) {
	if len(items) == 0 || len(items) > 200 || paid <= 0 || paid > fiscal.MaxReceiptKopecks {
		return nil, 0, errors.New("the original payment is outside the supported receipt limits")
	}
	normalized, err := normalizeSelections(selections)
	if err != nil {
		return nil, 0, err
	}
	remaining := make([]ConsumedLine, len(items))
	var originalTotal int64
	for index, item := range items {
		_, quantity, err := checkout.ParseQuantity(item.Quantity)
		if err != nil || item.Kind != checkout.KindGood && item.Kind != checkout.KindService ||
			item.Kind == checkout.KindService && quantity%quantityScale != 0 ||
			item.UnitPriceKopecks <= 0 || item.UnitPriceKopecks > fiscal.MaxReceiptKopecks ||
			item.AmountKopecks <= 0 || item.AmountKopecks > paid-originalTotal ||
			roundedLineAmount(item.UnitPriceKopecks, quantity) != item.AmountKopecks {
			return nil, 0, fmt.Errorf("original line_index %d does not have a supported fiscal quantity and amount", index)
		}
		originalTotal += item.AmountKopecks
		used := consumed[index]
		if used.QuantityMilli < 0 || used.QuantityMilli > quantity || used.AmountKopecks < 0 ||
			used.AmountKopecks > item.AmountKopecks || (used.QuantityMilli == 0) != (used.AmountKopecks == 0) ||
			item.Kind == checkout.KindService && used.QuantityMilli%quantityScale != 0 {
			return nil, 0, fmt.Errorf("prior refunds for line_index %d have invalid totals", index)
		}
		available := ConsumedLine{QuantityMilli: quantity - used.QuantityMilli, AmountKopecks: item.AmountKopecks - used.AmountKopecks}
		if (available.QuantityMilli == 0) != (available.AmountKopecks == 0) ||
			roundedLineAmount(item.UnitPriceKopecks, available.QuantityMilli) != available.AmountKopecks {
			return nil, 0, fmt.Errorf("prior refunds for line_index %d leave an unsupported fiscal balance", index)
		}
		remaining[index] = available
	}
	if originalTotal != paid {
		return nil, 0, errors.New("the original line amounts do not match the paid amount")
	}
	for index := range consumed {
		if index < 0 || index >= len(items) {
			return nil, 0, errors.New("prior refunds reference an unknown order line")
		}
	}

	lines := make([]Line, 0, len(normalized))
	var total int64
	for _, selection := range normalized {
		index := selection.LineIndex
		if index >= len(items) {
			return nil, 0, fmt.Errorf("refund line_index %d does not exist on the original order", index)
		}
		item, available := items[index], remaining[index]
		// normalizeSelections validated this canonical decimal before allocation.
		_, quantity, _ := checkout.ParseQuantity(selection.Quantity)
		if item.Kind == checkout.KindService && quantity%quantityScale != 0 {
			return nil, 0, fmt.Errorf("refund line_index %d is a service and requires whole units", index)
		}
		if quantity > available.QuantityMilli {
			return nil, 0, fmt.Errorf("refund quantity for line_index %d exceeds the remaining quantity", index)
		}
		amount := roundedLineAmount(item.UnitPriceKopecks, quantity)
		leftQuantity, leftAmount := available.QuantityMilli-quantity, available.AmountKopecks-amount
		// Both pieces must be printable at the original price. This also
		// prevents leaving an unrefundable fraction after a partial return.
		if amount <= 0 || leftAmount < 0 || (leftQuantity == 0) != (leftAmount == 0) ||
			roundedLineAmount(item.UnitPriceKopecks, leftQuantity) != leftAmount {
			return nil, 0, fmt.Errorf("refund quantity for line_index %d cannot be split at the original unit price", index)
		}
		total += amount
		lines = append(lines, Line{
			LineIndex: index, ItemID: item.ID, Kind: item.Kind, Title: item.Title,
			Quantity: selection.Quantity, QuantityMilli: quantity,
			UnitPriceKopecks: item.UnitPriceKopecks, AmountKopecks: amount,
		})
	}
	return lines, total, nil
}

// roundedLineAmount matches sale and TerminalFA half-up rounding. Its callers
// bound the price by MaxReceiptKopecks and the quantity by checkout's limit.
func roundedLineAmount(price, quantityMilli int64) int64 {
	amount := new(big.Int).Mul(big.NewInt(price), big.NewInt(quantityMilli))
	amount.Add(amount, big.NewInt(quantityScale/2))
	amount.Quo(amount, big.NewInt(quantityScale))
	return amount.Int64()
}
