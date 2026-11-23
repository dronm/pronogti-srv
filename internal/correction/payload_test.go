package correction

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dronm/pronogti/internal/fiscal"
)

func payloadSource() Source {
	return Source{
		Version: "sale", State: "issued", CalculationDate: "2026-11-16",
		Identity: FiscalIdentity{DocumentNumber: 52, FiscalSign: 123456789, DateTime: "2026-11-16T12:30", ReceiptNumber: 2, ShiftNumber: 5},
		Profile:  fiscal.Profile{TaxSystem: 4, ServiceVATRate: 12, GoodVATRate: 12},
		Payload: Receipt{
			CashierName: "Кассир", CalculationSign: 1, TotalKopecks: 10000,
			Items:          []Item{{Name: "Услуга", PriceKopecks: 10000, Quantity: "1", VATRate: 6, PaymentMethod: 4, PaymentObject: 4}},
			AdditionalData: AdditionalData{CashierName: "Кассир", CustomerContact: "79990000000", SettlementPlace: "Салон"},
			Payment:        Payment{TaxSystem: 1, CashlessKopecks: 10000},
		},
	}
}

func payloadRequest(source Source) CreateRequest {
	receipt := cloneReceipt(source.Payload)
	receipt.Items[0].VATRate = 1
	receipt.AdditionalData.CorrectedCalculationDate = 0
	receipt.AdditionalData.AdditionalReceiptProperty = ""
	receipt.AdditionalData.PrescriptionNumber = ""
	return CreateRequest{SourceVersion: source.Version, Reason: "Исправление ставки НДС", Basis: "voluntary", CalculationDate: source.CalculationDate, Receipt: receipt}
}

var payloadNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func TestPayloadBuildPreservesOriginalFiscalDataAndDoesNotMutateInputs(t *testing.T) {
	source := payloadSource()
	request := payloadRequest(source)
	request.Receipt.Payment.TaxSystem = 2
	originalJSON, _ := json.Marshal(source.Payload)
	requestJSON, _ := json.Marshal(request)
	preview, err := Build(source, request, payloadNow, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Reversal.CalculationSign != 2 || preview.Replacement.CalculationSign != 1 || preview.Reversal.Items[0].VATRate != 6 || preview.Reversal.Payment.TaxSystem != 1 || preview.Replacement.Items[0].VATRate != 1 || preview.Replacement.Payment.TaxSystem != 2 {
		t.Fatalf("incorrect reversed/replacement fiscal data: %+v", preview)
	}
	for _, receipt := range []Receipt{preview.Reversal, preview.Replacement} {
		if receipt.AdditionalData.AdditionalReceiptProperty != "123456789" || receipt.AdditionalData.CorrectedCalculationDate != time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC).Unix() || receipt.Payment.CashlessKopecks != 10000 {
			t.Fatalf("missing source link, date or explicit payment: %+v", receipt)
		}
	}
	gotSourceJSON, _ := json.Marshal(source.Payload)
	gotRequestJSON, _ := json.Marshal(request)
	if string(gotSourceJSON) != string(originalJSON) || string(gotRequestJSON) != string(requestJSON) {
		t.Fatal("Build mutated its source or request")
	}
}

func TestPayloadBuildValidatesBeforeCreatingEitherDocument(t *testing.T) {
	tests := []struct {
		name   string
		change func(*CreateRequest)
	}{
		{"missing reason", func(request *CreateRequest) { request.Reason = "  " }},
		{"stale source", func(request *CreateRequest) { request.SourceVersion = "other" }},
		{"future date", func(request *CreateRequest) { request.CalculationDate = "2026-11-18" }},
		{"invalid date", func(request *CreateRequest) { request.CalculationDate = "2026-02-30" }},
		{"basis missing", func(request *CreateRequest) { request.Basis = "" }},
		{"prescription required", func(request *CreateRequest) { request.Basis = "prescription" }},
		{"prescription on voluntary", func(request *CreateRequest) { request.PrescriptionNumber = "123" }},
		{"incorrect payment total", func(request *CreateRequest) { request.Receipt.Payment.CashlessKopecks++ }},
		{"negative payment bucket", func(request *CreateRequest) { request.Receipt.Payment.CashKopecks = -1 }},
		{"incorrect item total", func(request *CreateRequest) { request.Receipt.Items[0].PriceKopecks++ }},
		{"overflowing item amount", func(request *CreateRequest) {
			request.Receipt.Items[0].PriceKopecks = fiscal.MaxReceiptKopecks
			request.Receipt.Items[0].Quantity = "1000000"
		}},
		{"zero quantity", func(request *CreateRequest) { request.Receipt.Items[0].Quantity = "0" }},
		{"exponent quantity", func(request *CreateRequest) { request.Receipt.Items[0].Quantity = "1e0" }},
		{"excess quantity scale", func(request *CreateRequest) { request.Receipt.Items[0].Quantity = "1.0001" }},
		{"quantity overflow", func(request *CreateRequest) {
			request.Receipt.Items[0].Quantity = "9999999999999999999999999999999999999999999999"
		}},
		{"unsupported goods", func(request *CreateRequest) { request.Receipt.Items[0].PaymentObject = 33 }},
		{"invalid VAT", func(request *CreateRequest) { request.Receipt.Items[0].VATRate = 0 }},
		{"invalid settlement method", func(request *CreateRequest) { request.Receipt.Items[0].PaymentMethod = 8 }},
		{"unsupported measure", func(request *CreateRequest) { measure := 1; request.Receipt.Items[0].Measure = &measure }},
		{"invalid tax system", func(request *CreateRequest) { request.Receipt.Payment.TaxSystem = 8 }},
		{"unsupported character", func(request *CreateRequest) { request.Receipt.Items[0].Name = "Услуга 🌸" }},
		{"excess encoded name", func(request *CreateRequest) { request.Receipt.Items[0].Name = strings.Repeat("У", 129) }},
		{"control character", func(request *CreateRequest) { request.Receipt.Items[0].Name = "Услуга\n" }},
		{"missing internet contact", func(request *CreateRequest) {
			request.Receipt.AdditionalData.InternetPayment = 1
			request.Receipt.AdditionalData.CustomerContact = ""
		}},
		{"forged source link", func(request *CreateRequest) { request.Receipt.AdditionalData.AdditionalReceiptProperty = "999" }},
		{"forged correction date", func(request *CreateRequest) { request.Receipt.AdditionalData.CorrectedCalculationDate = 1 }},
		{"forged prescription", func(request *CreateRequest) { request.Receipt.AdditionalData.PrescriptionNumber = "forged" }},
		{"wrong calculation sign", func(request *CreateRequest) { request.Receipt.CalculationSign = 2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := payloadSource()
			request := payloadRequest(source)
			test.change(&request)
			preview, err := Build(source, request, payloadNow, time.UTC)
			if err == nil || preview.Reversal.Items != nil || preview.Replacement.Items != nil {
				t.Fatalf("invalid request returned executable documents: %+v, %v", preview, err)
			}
		})
	}
}

func TestPayloadBuildPrescribedCorrectionAndDecimalRounding(t *testing.T) {
	source := payloadSource()
	request := payloadRequest(source)
	request.Basis, request.PrescriptionNumber = "prescription", "АБ-123"
	request.Receipt.Items[0].PriceKopecks = 101
	request.Receipt.Items[0].Quantity = "1.5000"
	request.Receipt.TotalKopecks, request.Receipt.Payment.CashlessKopecks = 152, 152
	preview, err := Build(source, request, payloadNow, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Replacement.Items[0].Quantity != "1.5" || preview.Replacement.TotalKopecks != 152 || preview.Reversal.AdditionalData.PrescriptionNumber != "АБ-123" || preview.Replacement.AdditionalData.PrescriptionNumber != "АБ-123" {
		t.Fatalf("decimal rounding or prescription was lost: %+v", preview)
	}
}

func TestPayloadBuildUsesConfiguredKKTDateAndAllowsDateCorrection(t *testing.T) {
	source := payloadSource()
	request := payloadRequest(source)
	request.Receipt = cloneReceipt(source.Payload)
	request.CalculationDate = "2026-11-17"
	location := time.FixedZone("KKT", 5*60*60)
	now := time.Date(2026, 11, 16, 21, 0, 0, 0, time.UTC)
	preview, err := Build(source, request, now, location)
	if err != nil {
		t.Fatal(err)
	}
	expected := time.Date(2026, 11, 16, 19, 0, 0, 0, time.UTC).Unix()
	if preview.Reversal.AdditionalData.CorrectedCalculationDate != expected {
		t.Fatalf("wrong KKT calendar conversion: got %d, want %d", preview.Reversal.AdditionalData.CorrectedCalculationDate, expected)
	}
}

func TestPayloadBuildRejectsNoOpButAllowsSubsequentCorrection(t *testing.T) {
	source := payloadSource()
	source.Version = "previous-correction"
	source.Payload.AdditionalData.CorrectedCalculationDate = 1704067200
	source.Payload.AdditionalData.AdditionalReceiptProperty = "old-sign"
	source.Payload.AdditionalData.PrescriptionNumber = "old-prescription"
	request := payloadRequest(source)
	request.Receipt.Items[0].VATRate = source.Payload.Items[0].VATRate
	request.Receipt.Items[0].Quantity = "01.0000"
	if _, err := Build(source, request, payloadNow, time.UTC); err == nil {
		t.Fatal("unchanged fiscal data was accepted as a correction")
	}
	request.Receipt.AdditionalData.CustomerContact = "79991112233"
	preview, err := Build(source, request, payloadNow, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Reversal.AdditionalData.AdditionalReceiptProperty != "123456789" || preview.Reversal.AdditionalData.PrescriptionNumber != "" {
		t.Fatal("previous correction's linking attributes were reused")
	}
}

func TestPayloadBuildDemoSourceOmitsInventedFiscalSign(t *testing.T) {
	source := payloadSource()
	source.State, source.Identity = "demo", FiscalIdentity{}
	preview, err := Build(source, payloadRequest(source), payloadNow, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Reversal.AdditionalData.AdditionalReceiptProperty != "" {
		t.Fatal("demo source was assigned a fake fiscal sign")
	}
}

func TestPayloadDecodeRejectsUnsupportedSavedFiscalAttributes(t *testing.T) {
	encoded, _ := json.Marshal(payloadSource().Payload)
	if _, err := DecodeReceipt(encoded); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`null`,
		`{} {}`,
		`{"unknownFiscalTag":42}`,
		`{"items":[{"unknownFiscalTag":42}]}`,
		`{"additionalData":{"unknownFiscalTag":42}}`,
	} {
		if _, err := DecodeReceipt(json.RawMessage(raw)); err == nil {
			t.Fatalf("unsafe source accepted: %s", raw)
		}
	}
}

func TestPayloadBuildReversesMissingContactWithoutRewritingOriginal(t *testing.T) {
	source := payloadSource()
	source.Payload.AdditionalData.InternetPayment = 1
	source.Payload.AdditionalData.CustomerContact = ""
	source.Payload.AdditionalData.SettlementPlace = "https://example.org"
	request := payloadRequest(source)
	request.Receipt.AdditionalData.CustomerContact = "79991112233"
	preview, err := Build(source, request, payloadNow, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Reversal.AdditionalData.CustomerContact != "" || preview.Replacement.AdditionalData.CustomerContact != "79991112233" {
		t.Fatal("original missing contact was not preserved for the reversal")
	}
}
