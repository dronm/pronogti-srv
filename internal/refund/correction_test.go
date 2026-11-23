package refund

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/fiscal"
	"github.com/dronm/pronogti/internal/payment"
)

func correctedSaleFixture(t *testing.T) (json.RawMessage, []checkout.Item, fiscal.Profile) {
	t.Helper()
	profile := fiscal.DefaultProfile(1)
	items := []checkout.Item{
		{Selection: checkout.Selection{ID: 15, Kind: checkout.KindGood, Quantity: "2"}, Title: "Good", UnitPriceKopecks: 100, AmountKopecks: 200},
		{Selection: checkout.Selection{ID: 16, Kind: checkout.KindService, Quantity: "1"}, Title: "Service", UnitPriceKopecks: 150, AmountKopecks: 150},
	}
	order := checkout.Order{Owner: checkout.Owner{CompanyID: 1}, Currency: "RUB", SubtotalKopecks: 350, PayableKopecks: 350, Items: items}
	payload, err := fiscal.BuildPayload(order, profile)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	additional := document["additionalData"].(map[string]any)
	additional["correctedCalculationDate"] = 1704067200
	additional["prescriptionNumber"] = "FNS-1"
	additional["additionalReceiptProperty"] = "123456789"
	lines := document["items"].([]any)
	good := lines[0].(map[string]any)
	good["name"], good["priceKopecks"], good["quantity"], good["vatRate"] = "Corrected good", 200, "1", 5
	payload, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return payload, items, profile
}

func TestRefundUsesEffectiveCorrectedItemsAndFiscalValues(t *testing.T) {
	payload, original, profile := correctedSaleFixture(t)
	items, reason := correctedRefundItems(payload, original, 350)
	if reason != "" || len(items) != 2 || items[0].Title != "Corrected good" ||
		items[0].Quantity != "1" || items[0].UnitPriceKopecks != 200 || items[0].AmountKopecks != 200 ||
		items[0].ID != 0 || items[1].ID != 16 {
		t.Fatalf("refund did not resolve corrected fiscal items: %+v, %s", items, reason)
	}
	lines, total, err := buildLines(items, 350, []Selection{{LineIndex: 0, Quantity: "1"}}, nil)
	if err != nil || total != 200 {
		t.Fatalf("corrected item could not be selected for refund: %+v, %d, %v", lines, total, err)
	}
	returned, err := PrepareReturnReceipt(profile, "mock", "demo", payload, lines, total, 350, false)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		CalculationSign int                        `json:"calculationSign"`
		TotalKopecks    int64                      `json:"totalKopecks"`
		AdditionalData  map[string]json.RawMessage `json:"additionalData"`
		Items           []struct {
			Name         string `json:"name"`
			VATRate      int    `json:"vatRate"`
			PriceKopecks int64  `json:"priceKopecks"`
		} `json:"items"`
	}
	if err := json.Unmarshal(returned, &document); err != nil {
		t.Fatal(err)
	}
	if document.CalculationSign != 2 || document.TotalKopecks != 200 || len(document.Items) != 1 ||
		document.Items[0].Name != "Corrected good" || document.Items[0].VATRate != 5 || document.Items[0].PriceKopecks != 200 {
		t.Fatalf("return discarded the effective correction data: %s", returned)
	}
	for _, key := range []string{"correctedCalculationDate", "prescriptionNumber", "additionalReceiptProperty"} {
		if _, copied := document.AdditionalData[key]; copied {
			t.Fatalf("normal return receipt copied correction-only metadata %s: %s", key, returned)
		}
	}
	var originalDocument struct {
		AdditionalData map[string]json.RawMessage `json:"additionalData"`
	}
	if err := json.Unmarshal(payload, &originalDocument); err != nil || len(originalDocument.AdditionalData["correctedCalculationDate"]) == 0 {
		t.Fatal("preparing a refund mutated the correction snapshot")
	}
}

func TestRefundRejectsCorrectionsOutsideBankRefundMeaning(t *testing.T) {
	payload, original, _ := correctedSaleFixture(t)
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		reason string
	}{
		{"different fiscal total", func(document map[string]any) { document["totalKopecks"] = 351 }, "corrected_receipt_refund_unsupported"},
		{"cash correction", func(document map[string]any) {
			payment := document["payment"].(map[string]any)
			payment["cashKopecks"], payment["cashlessKopecks"] = 100, 250
		}, "corrected_receipt_refund_unsupported"},
		{"credit correction", func(document map[string]any) {
			payment := document["payment"].(map[string]any)
			payment["creditKopecks"], payment["cashlessKopecks"] = 100, 250
		}, "corrected_receipt_refund_unsupported"},
		{"unsupported object", func(document map[string]any) {
			document["items"].([]any)[0].(map[string]any)["paymentObject"] = 10
		}, "corrected_receipt_refund_unsupported"},
		{"fractional service", func(document map[string]any) {
			document["items"].([]any)[1].(map[string]any)["quantity"] = "0.5"
		}, "corrected_receipt_refund_unsupported"},
		{"invalid quantity", func(document map[string]any) {
			document["items"].([]any)[0].(map[string]any)["quantity"] = "invalid"
		}, "corrected_receipt_invalid"},
		{"items total mismatch", func(document map[string]any) {
			document["items"].([]any)[0].(map[string]any)["priceKopecks"] = 201
		}, "corrected_receipt_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal(payload, &document); err != nil {
				t.Fatal(err)
			}
			test.mutate(document)
			changed, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, reason := correctedRefundItems(changed, original, 350); reason != test.reason {
				t.Fatalf("unsafe automatic refund accepted: reason=%s, want=%s", reason, test.reason)
			}
		})
	}
}

func TestIncompleteCorrectionBlocksRefundBeforeBankOrItemValidation(t *testing.T) {
	sale := Sale{
		Bank:         BankPayment{Method: payment.MethodSBP, PaymentID: "payment", AmountKopecks: 350},
		PaymentState: "paid", OrderState: "paid", CorrectionBlockReason: "receipt_correction_incomplete",
		Items: []checkout.Item{{Selection: checkout.Selection{Quantity: "invalid"}}},
	}
	service := NewService(terminalQuoteRepository{sale: sale}, nil, nil, true)
	quote, err := service.Quote(context.Background(), "payment")
	if err != nil || quote.Eligible || quote.Reason != "receipt_correction_incomplete" {
		t.Fatalf("incomplete correction did not block refund quote: %+v, %v", quote, err)
	}
	if reason := service.ineligible(sale, nil); reason != "receipt_correction_incomplete" {
		t.Fatalf("new refund can pass unfinished correction: %s", reason)
	}
}
