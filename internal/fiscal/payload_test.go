package fiscal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dronm/pronogti/internal/checkout"
)

func receiptOrder() checkout.Order {
	return checkout.Order{
		Owner: checkout.Owner{CompanyID: 781898}, Currency: "RUB",
		SubtotalKopecks: 12500, PayableKopecks: 12500,
		Items: []checkout.Item{{Selection: checkout.Selection{Kind: checkout.KindService, Quantity: "1"}, Title: "Маникюр", UnitPriceKopecks: 12500, AmountKopecks: 12500}},
	}
}

func TestBuildPayloadCashlessReceipt(t *testing.T) {
	profile := DefaultProfile(781898)
	profile.CashierName, profile.CashierINN = "Иван", "123456789012"
	profile.ServiceVATRate, profile.GoodVATRate = 2, 6
	profile.SuppressPrint = true
	profile.InternetPayment, profile.SettlementPlace = true, "https://salon.example"
	raw, err := BuildPayload(receiptOrder(), profile)
	if err != nil {
		t.Fatal(err)
	}
	var p receiptPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.CalculationSign != 1 || p.TotalKopecks != 12500 || p.Payment.CashlessKopecks != 12500 || p.Payment.CashKopecks != 0 || p.Payment.PrepaymentKopecks != 0 || p.Payment.CreditKopecks != 0 || p.Payment.CounterProvisionKopecks != 0 {
		t.Fatalf("unexpected receipt settlement: %s", raw)
	}
	if p.Items[0].PaymentObject != 4 || p.Items[0].VATRate != 2 || p.Items[0].PaymentMethod != 4 || p.AdditionalData.InternetPayment != 1 || p.AdditionalData.CashierINN != "123456789012" || p.CashierINN != "123456789012" {
		t.Fatalf("incorrect item/additional fields: %s", raw)
	}
	if p.PrintSuppressionCode == nil || *p.PrintSuppressionCode != 41029 || p.SuppressShiftPrint == nil || *p.SuppressShiftPrint != 1 {
		t.Fatalf("incorrect printing flags: %s", raw)
	}
	if !strings.Contains(string(raw), `"cashierInn"`) || !strings.Contains(string(raw), `"cashierINN"`) {
		t.Fatalf("missing cashier INN fields: %s", raw)
	}
}

func TestBuildPayloadSettlementDetails(t *testing.T) {
	for _, tc := range []struct {
		name      string
		snapshot  string
		wantExtra string
	}{
		{"enabled", `{"send_settlement_details":true,"automatic_device_number":"0007","time_zone":4}`,
			`{"automaticDeviceNumber":"0007","cashierName":"Cashier","internetPayment":0,"settlementAddress":"Тюмень","settlementPlace":"https://salon.example","timeZone":4}`},
		{"disabled", `{"send_settlement_details":false,"automatic_device_number":"0007","time_zone":4}`,
			`{"cashierName":"Cashier","internetPayment":0}`},
		{"internet payment", `{"send_settlement_details":false,"internet_payment":true}`,
			`{"cashierName":"Cashier","internetPayment":1,"settlementPlace":"https://salon.example"}`},
		{"legacy snapshot", `{}`,
			`{"cashierName":"Cashier","internetPayment":0,"settlementAddress":"Тюмень","settlementPlace":"https://salon.example"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := DefaultProfile(781898)
			profile.SettlementAddress = "Тюмень"
			profile.SettlementPlace = "https://salon.example"
			if err := json.Unmarshal([]byte(tc.snapshot), &profile); err != nil {
				t.Fatal(err)
			}
			payload, err := BuildPayload(receiptOrder(), profile)
			if err != nil {
				t.Fatal(err)
			}
			var receipt struct {
				AdditionalData map[string]json.RawMessage `json:"additionalData"`
			}
			if err := json.Unmarshal(payload, &receipt); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(receipt.AdditionalData)
			if err != nil || string(got) != tc.wantExtra {
				t.Fatalf("additionalData = %s, want %s: %v", got, tc.wantExtra, err)
			}
		})
	}
}

func TestBuildPayloadRoundedGoodsAmount(t *testing.T) {
	order := receiptOrder()
	order.Items[0].Kind = checkout.KindGood
	order.Items[0].Quantity = "0.5000"
	order.Items[0].UnitPriceKopecks = 101
	order.Items[0].AmountKopecks = 51
	order.SubtotalKopecks, order.PayableKopecks = 51, 51
	raw, err := BuildPayload(order, DefaultProfile(781898))
	if err != nil {
		t.Fatal(err)
	}
	var p receiptPayload
	_ = json.Unmarshal(raw, &p)
	if p.Items[0].Quantity != "0.5" || p.Items[0].PaymentObject != 1 || p.TotalKopecks != 51 {
		t.Fatalf("unexpected rounded goods receipt: %s", raw)
	}
}

func TestBuildPayloadRejectsUnrepresentableHistoricAmount(t *testing.T) {
	order := receiptOrder()
	order.Items[0].AmountKopecks = 4000
	order.PayableKopecks, order.SubtotalKopecks = 4000, 4000
	_, err := BuildPayload(order, DefaultProfile(781898))
	if err == nil || !strings.Contains(err.Error(), "outstanding amount") {
		t.Fatalf("historic partial payment must not silently change unit price: %v", err)
	}
}

func TestBuildPayloadRejectsInvalidSnapshot(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*checkout.Order)
	}{
		{"different company", func(o *checkout.Order) { o.Owner.CompanyID++ }},
		{"non RUB", func(o *checkout.Order) { o.Currency = "USD" }},
		{"bonus", func(o *checkout.Order) { o.BonusKopecks = 100 }},
		{"zero total", func(o *checkout.Order) { o.PayableKopecks, o.SubtotalKopecks = 0, 0 }},
		{"UINT40 overflow", func(o *checkout.Order) {
			o.PayableKopecks, o.SubtotalKopecks = MaxReceiptKopecks+1, MaxReceiptKopecks+1
		}},
		{"empty items", func(o *checkout.Order) { o.Items = nil }},
		{"too many items", func(o *checkout.Order) { o.Items = make([]checkout.Item, 201) }},
		{"unsupported text", func(o *checkout.Order) { o.Items[0].Title = "Маникюр 💅" }},
		{"long text", func(o *checkout.Order) { o.Items[0].Title = strings.Repeat("Я", 129) }},
		{"empty title", func(o *checkout.Order) { o.Items[0].Title = " " }},
		{"quantity precision", func(o *checkout.Order) { o.Items[0].Quantity = "0.0001" }},
		{"negative quantity", func(o *checkout.Order) { o.Items[0].Quantity = "-1" }},
		{"exponent quantity", func(o *checkout.Order) { o.Items[0].Quantity = "1e0" }},
		{"zero quantity", func(o *checkout.Order) { o.Items[0].Quantity = "0" }},
		{"large quantity", func(o *checkout.Order) { o.Items[0].Quantity = "1000001" }},
		{"unknown kind", func(o *checkout.Order) { o.Items[0].Kind = "marked" }},
		{"sum mismatch", func(o *checkout.Order) { o.PayableKopecks++; o.SubtotalKopecks++ }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			order := receiptOrder()
			tc.mutate(&order)
			if _, err := BuildPayload(order, DefaultProfile(781898)); err == nil {
				t.Fatal("invalid fiscal snapshot accepted")
			}
		})
	}
}

func TestReceiptTextLimitsUseCP866Bytes(t *testing.T) {
	order := receiptOrder()
	order.Items[0].Title = strings.Repeat("Я", 128)
	if _, err := BuildPayload(order, DefaultProfile(781898)); err != nil {
		t.Fatalf("128 CP866 bytes must be accepted despite longer UTF-8 representation: %v", err)
	}
}

func TestProfileValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Profile)
	}{
		{"disabled", func(p *Profile) { p.Enabled = false }},
		{"invalid mode", func(p *Profile) { p.Mode = "test" }},
		{"missing driver", func(p *Profile) { p.Mode = "live" }},
		{"invalid URL", func(p *Profile) { p.Mode = "live"; p.DriverBaseURL = "ftp://host"; p.DeviceID = "cashdesk-1" }},
		{"encoded path", func(p *Profile) {
			p.Mode = "live"
			p.DriverBaseURL = "http://localhost/a%2Fb"
			p.DeviceID = "cashdesk-1"
		}},
		{"URL credentials", func(p *Profile) {
			p.Mode = "live"
			p.DriverBaseURL = "http://user:secret@host"
			p.DeviceID = "cashdesk-1"
		}},
		{"invalid device", func(p *Profile) { p.Mode = "live"; p.DriverBaseURL = "http://localhost:8080"; p.DeviceID = "device/1" }},
		{"invalid env", func(p *Profile) { p.BearerTokenEnv = "TOKEN;echo" }},
		{"short timeout", func(p *Profile) { p.TimeoutSeconds = 0 }},
		{"long timeout", func(p *Profile) { p.TimeoutSeconds = 301 }},
		{"bad tax", func(p *Profile) { p.TaxSystem = 8 }},
		{"bad VAT", func(p *Profile) { p.GoodVATRate = 0 }},
		{"bad method", func(p *Profile) { p.PaymentMethod = 8 }},
		{"empty cashier", func(p *Profile) { p.CashierName = "" }},
		{"cashier too long", func(p *Profile) { p.CashierName = strings.Repeat("Я", 65) }},
		{"short INN", func(p *Profile) { p.CashierINN = "1234567890" }},
		{"nonnumeric INN", func(p *Profile) { p.CashierINN = "12345678901A" }},
		{"website required", func(p *Profile) { p.InternetPayment = true }},
		{"address too long", func(p *Profile) { p.SettlementAddress = strings.Repeat("Я", 257) }},
		{"invalid time zone", func(p *Profile) { zone := 12; p.TimeZone = &zone }},
		{"automatic device number too long", func(p *Profile) { p.AutomaticDeviceNumber = strings.Repeat("Я", 21) }},
		{"unsupported automatic device number", func(p *Profile) { p.AutomaticDeviceNumber = "KKT\x00-1" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profile := DefaultProfile(781898)
			tc.mutate(&profile)
			if err := profile.Validate(); err == nil {
				t.Fatal("invalid fiscal profile accepted")
			}
		})
	}
	profile := DefaultProfile(781898)
	profile.Mode, profile.DriverBaseURL, profile.DeviceID = "live", "http://192.0.2.1:8080", "cashdesk-ffd12"
	if err := profile.Validate(); err != nil {
		t.Fatalf("explicit private driver configuration should be allowed: %v", err)
	}
}

func TestReceiptPayloadFitsDefaultDriverBodyLimit(t *testing.T) {
	order := receiptOrder()
	item := order.Items[0]
	item.Title, item.Quantity = strings.Repeat("Я", 128), "1"
	item.UnitPriceKopecks, item.AmountKopecks = 100, 100
	order.Items = make([]checkout.Item, 200)
	for i := range order.Items {
		order.Items[i] = item
	}
	order.SubtotalKopecks, order.PayableKopecks = 20000, 20000
	if _, err := BuildPayload(order, DefaultProfile(781898)); err == nil || !strings.Contains(err.Error(), "request size") {
		t.Fatalf("oversized receipt accepted before payment: %v", err)
	}
}
