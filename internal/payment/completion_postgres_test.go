package payment

import (
	"encoding/json"
	"testing"
)

func TestReceiptViewUsesOnlyCustomerFacingData(t *testing.T) {
	items := []byte(`[{"title":"Service","quantity":"1.5","unit_price_kopecks":10000,"amount_kopecks":15000,"staff_name":"private staff"}]`)
	details := []byte(`{"dateTime":"2026-11-06T10:15","fiscalSign":4294967295,"documentNumber":11,"receiptNumber":3,"shiftNumber":2,"driverToken":"private token"}`)
	view := receiptView("issued", items, 15000, "RUB", details)
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"kind":"fiscal","items":[{"name":"Service","quantity":"1.5","unit_price_kopecks":10000,"amount_kopecks":15000}],"total_kopecks":15000,"currency":"RUB","date_time":"2026-11-06T10:15","fiscal_sign":4294967295,"document_number":11,"receipt_number":3,"shift_number":2}`
	if string(encoded) != want {
		t.Fatalf("receipt JSON = %s", encoded)
	}
	for _, incomplete := range [][]byte{nil, []byte(`{"dateTime":"invalid","fiscalSign":"invalid"}`)} {
		view := receiptView("issued", items, 15000, "RUB", incomplete)
		if view == nil || view.Kind != "fiscal" || view.FiscalSign != nil || view.DateTime != "" {
			t.Fatalf("issued receipt with incomplete device data = %+v", view)
		}
	}
	if demo := receiptView("demo", items, 15000, "RUB", details); demo == nil || demo.Kind != "demo" || demo.FiscalSign != nil {
		t.Fatalf("demo receipt = %+v", demo)
	}
	if receiptView("pending", items, 15000, "RUB", details) != nil || receiptView("issued", []byte(`[null]`), 15000, "RUB", details) != nil {
		t.Fatal("showed a receipt for an incomplete fiscal job or invalid checkout items")
	}
}
