package services

import (
	"testing"

	"github.com/dronm/pronogti/internal/checkout"
)

func TestPayableVisitRechecksOutstandingAmountAndIdentity(t *testing.T) {
	item := checkout.Item{Selection: checkout.Selection{Source: checkout.SourceVisit, Kind: checkout.KindGood, ID: 7, LineID: 8, StaffID: 9, Quantity: "2.5"}, AmountKopecks: 123}
	order := checkout.Order{VisitID: 11, RecordID: 12, Items: []checkout.Item{item}}
	line := checkout.VisitLine{Kind: checkout.KindGood, ID: 7, LineID: 8, StaffID: 9, Quantity: "2.500", AmountKopecks: 123}
	visit := checkout.Visit{VisitID: 11, RecordID: 12, Items: []checkout.VisitLine{line}}
	if err := validatePayableVisit(order, visit); err != nil {
		t.Fatalf("unchanged visit rejected: %v", err)
	}
	for _, change := range []func(*checkout.Visit){
		func(v *checkout.Visit) { v.VisitID++ },
		func(v *checkout.Visit) { v.RecordID++ },
		func(v *checkout.Visit) { v.Items = nil },
		func(v *checkout.Visit) { v.Items[0].AmountKopecks-- },
		func(v *checkout.Visit) { v.Items[0].Quantity = "2.4" },
		func(v *checkout.Visit) { v.Items[0].StaffID++ },
		func(v *checkout.Visit) { v.Items = append(v.Items, v.Items[0]) },
	} {
		changed := visit
		changed.Items = append([]checkout.VisitLine(nil), visit.Items...)
		change(&changed)
		if err := validatePayableVisit(order, changed); err == nil {
			t.Fatal("changed visit accepted for new QR registration")
		}
	}
}

func TestPayableVisitPreservesFrozenCatalogAdditions(t *testing.T) {
	order := checkout.Order{VisitID: 11, RecordID: 12, Items: []checkout.Item{
		{Selection: checkout.Selection{Source: checkout.SourceVisit, Kind: checkout.KindService, ID: 5, StaffID: 9, Quantity: "1"}, AmountKopecks: 500},
		{Selection: checkout.Selection{Source: checkout.SourceCatalog, Kind: checkout.KindService, ID: 7, StaffID: 9, Quantity: "1"}, AmountKopecks: 123},
	}}
	if err := validatePayableVisit(order, checkout.Visit{VisitID: 11, RecordID: 12, Items: []checkout.VisitLine{{Kind: checkout.KindService, ID: 5, StaffID: 9, Quantity: "1", AmountKopecks: 500}}}); err != nil {
		t.Fatalf("catalog addition was mistaken for an existing visit line: %v", err)
	}
}

func TestPayableVisitRejectsPartialSelection(t *testing.T) {
	order := checkout.Order{VisitID: 11, RecordID: 12, Items: []checkout.Item{{Selection: checkout.Selection{Source: checkout.SourceVisit, Kind: checkout.KindService, ID: 5, StaffID: 9, Quantity: "1"}, AmountKopecks: 500}}}
	visit := checkout.Visit{VisitID: 11, RecordID: 12, Items: []checkout.VisitLine{{Kind: checkout.KindService, ID: 5, StaffID: 9, Quantity: "1", AmountKopecks: 500}, {Kind: checkout.KindService, ID: 6, StaffID: 9, Quantity: "1", AmountKopecks: 500}}}
	if err := validatePayableVisit(order, visit); err == nil {
		t.Fatal("partial document selection could be allocated to the wrong lines")
	}
	order.Items = nil
	if err := validatePayableVisit(order, checkout.Visit{VisitID: 11, RecordID: 12}); err == nil {
		t.Fatal("removed visit items retained a visit association")
	}
}
