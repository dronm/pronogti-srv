package services

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/dronm/goyclients"
	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/models"
)

func TestCheckoutStaffRequiresCurrentActiveCompanyStaff(t *testing.T) {
	staff, err := checkoutStaff([]byte(`[
		{"id":1,"name":"Active"},
		{"id":2,"name":"Fired","fired":1},
		{"id":3,"name":"Inactive","status":1},
		{"id":4,"name":"Hidden","hidden":true},
		null
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(staff) != 1 || staff[1] != "Active" {
		t.Fatalf("inactive staff entered checkout: %+v", staff)
	}
}

func TestCheckoutGoodsUseCanonicalIDAndRejectAmbiguousPrices(t *testing.T) {
	selection := checkout.Selection{Source: checkout.SourceCatalog, Kind: checkout.KindGood, ID: 100, StaffID: 1, Quantity: "1"}
	rows := []checkoutCatalogRow{{ID: 7, GoodID: 100, Title: "Good", Cost: json.Number("12.34")}}
	row, err := checkoutCatalogMatch(rows, selection)
	if err != nil {
		t.Fatal(err)
	}
	price, err := row.fixedPrice(checkout.KindGood)
	if err != nil || price != 1234 {
		t.Fatalf("canonical good price=%d error=%v", price, err)
	}
	rows = append(rows, checkoutCatalogRow{ID: 8, GoodID: 100, Title: "Good", Cost: json.Number("12.35")})
	if _, err := checkoutCatalogMatch(rows, selection); err == nil {
		t.Fatal("accepted ambiguous duplicate canonical good prices")
	}
	selection.ID = 7
	if _, err := checkoutCatalogMatch(rows[:1], selection); err == nil {
		t.Fatal("accepted stock-row id instead of canonical good_id")
	}
}

func TestCheckoutServiceRequiresExplicitStaffAndFixedExactPrice(t *testing.T) {
	selection := checkout.Selection{Source: checkout.SourceCatalog, Kind: checkout.KindService, ID: 10, StaffID: 5, Quantity: "1"}
	row := checkoutCatalogRow{ID: 10, Title: "Service", PriceMin: json.Number("10.10"), PriceMax: json.Number("10.10"), Staff: json.RawMessage(`[{"id":5}]`)}
	if _, err := checkoutCatalogMatch([]checkoutCatalogRow{row}, selection); err != nil {
		t.Fatal(err)
	}
	for _, staff := range []string{`[{"id":6}]`, `[]`, `null`} {
		row.Staff = json.RawMessage(staff)
		if _, err := checkoutCatalogMatch([]checkoutCatalogRow{row}, selection); err == nil {
			t.Errorf("accepted service without selected staff: %s", staff)
		}
	}
	row.Staff = json.RawMessage(`{"id":5}`)
	if _, err := checkoutCatalogMatch([]checkoutCatalogRow{row}, selection); err != nil {
		t.Fatalf("legacy staff object rejected: %v", err)
	}
	for _, price := range []string{"10.11", "10.101", "0"} {
		row.PriceMax = json.Number(price)
		if _, err := checkoutCatalogMatch([]checkoutCatalogRow{row}, selection); err == nil {
			t.Errorf("accepted variable/non-kopeck price %s", price)
		}
	}
}

func TestCheckoutVisitKeepsFullOutstandingAmount(t *testing.T) {
	draft := models.PayDraft{VisitID: 11, RecordID: 12, Items: []models.PayDraftItem{{
		ID: 1, LineID: 2, ItemKind: models.PayDraftItemKindGood, Title: "Partly paid goods",
		Staff: goyclients.Staff{ID: 5, Name: "Master"}, Quant: 3, Price: 3.33, Amount: 10,
	}}}
	visit, err := checkoutVisit(draft)
	if err != nil {
		t.Fatal(err)
	}
	if visit.VisitID != 11 || visit.RecordID != 12 || len(visit.Items) != 1 || visit.Items[0].AmountKopecks != 1000 || visit.Items[0].UnitPriceKopecks != 333 || visit.Items[0].Quantity != "3" {
		t.Fatalf("outstanding amount reconstructed incorrectly: %+v", visit)
	}
	draft.Items[0].Amount = math.NaN()
	if _, err := checkoutVisit(draft); err == nil {
		t.Fatal("accepted invalid upstream money")
	}
	draft.Items[0].Amount = 10
	draft.Items[0].Quant = 1.0001
	if _, err := checkoutVisit(draft); err == nil {
		t.Fatal("silently rounded an unsupported upstream quantity")
	}
}
