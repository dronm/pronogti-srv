package refund

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dronm/pronogti/internal/checkout"
)

func TestBuildLinesPreservesOriginalAmountsAcrossPartialRefunds(t *testing.T) {
	items := []checkout.Item{
		{Selection: checkout.Selection{ID: 15, Kind: checkout.KindGood, Quantity: "1.333"}, Title: "Good", UnitPriceKopecks: 333, AmountKopecks: 444},
		{Selection: checkout.Selection{ID: 16, Kind: checkout.KindService, Quantity: "2"}, Title: "Service", UnitPriceKopecks: 234, AmountKopecks: 468},
	}
	selection := []Selection{{LineIndex: 1, Quantity: "1.000"}, {LineIndex: 0, Quantity: "0.3330"}}
	first, firstTotal, err := buildLines(items, 912, selection, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Line{
		{LineIndex: 0, ItemID: 15, Kind: checkout.KindGood, Title: "Good", Quantity: "0.333", QuantityMilli: 333, UnitPriceKopecks: 333, AmountKopecks: 111},
		{LineIndex: 1, ItemID: 16, Kind: checkout.KindService, Title: "Service", Quantity: "1", QuantityMilli: 1000, UnitPriceKopecks: 234, AmountKopecks: 234},
	}
	if !reflect.DeepEqual(first, want) || firstTotal != 345 {
		t.Fatalf("first refund = %+v, %d", first, firstTotal)
	}
	if selection[0].LineIndex != 1 || selection[1].Quantity != "0.3330" {
		t.Fatalf("original request modified: %+v", selection)
	}

	consumed := map[int]ConsumedLine{
		0: {QuantityMilli: 333, AmountKopecks: 111},
		1: {QuantityMilli: 1000, AmountKopecks: 234},
	}
	last, lastTotal, err := buildLines(items, 912, []Selection{{0, "1"}, {1, "1"}}, consumed)
	if err != nil {
		t.Fatal(err)
	}
	if last[0].AmountKopecks != 333 || last[1].AmountKopecks != 234 || firstTotal+lastTotal != 912 {
		t.Fatalf("remaining refund = %+v, %d", last, lastTotal)
	}
	if _, _, err := buildLines(items, 912, []Selection{{0, "1.001"}}, consumed); err == nil {
		t.Fatal("accepted a quantity exceeding the remaining balance")
	}
}

func TestBuildLinesRejectsUnrepresentableSplits(t *testing.T) {
	items := []checkout.Item{
		{Selection: checkout.Selection{ID: 15, Kind: checkout.KindGood, Quantity: "1"}, Title: "Good", UnitPriceKopecks: 1, AmountKopecks: 1},
	}
	if _, _, err := buildLines(items, 1, []Selection{{0, "0.5"}}, nil); err == nil || !strings.Contains(err.Error(), "split") {
		t.Fatalf("half-kopeck split error = %v", err)
	}
	if _, amount, err := buildLines(items, 1, []Selection{{0, "1"}}, nil); err != nil || amount != 1 {
		t.Fatalf("complete return of line = %d, %v", amount, err)
	}
}

func TestBuildLinesRejectsInvalidOrCorruptSelections(t *testing.T) {
	items := []checkout.Item{
		{Selection: checkout.Selection{ID: 16, Kind: checkout.KindService, Quantity: "2"}, Title: "Service", UnitPriceKopecks: 234, AmountKopecks: 468},
	}
	cases := []struct {
		name       string
		selections []Selection
		paid       int64
		consumed   map[int]ConsumedLine
	}{
		{name: "duplicate", selections: []Selection{{0, "1"}, {0, "1"}}, paid: 468},
		{name: "missing line", selections: []Selection{{1, "1"}}, paid: 468},
		{name: "fractional service", selections: []Selection{{0, "0.5"}}, paid: 468},
		{name: "missing quantity", selections: []Selection{{0, ""}}, paid: 468},
		{name: "paid total differs", selections: []Selection{{0, "1"}}, paid: 469},
		{name: "corrupt previous amount", selections: []Selection{{0, "1"}}, paid: 468, consumed: map[int]ConsumedLine{0: {QuantityMilli: 1000, AmountKopecks: 200}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := buildLines(items, tc.paid, tc.selections, tc.consumed); err == nil {
				t.Fatal("expected refund validation to fail")
			}
		})
	}
}
