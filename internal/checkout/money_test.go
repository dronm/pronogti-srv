package checkout

import (
	"math"
	"testing"
)

func TestExactMoneyBoundary(t *testing.T) {
	for value, expected := range map[string]int64{
		"0": 0, "0.01": 1, "123.45": 12345, "001.2300": 123,
		"90071992547409.91": MaxMoney,
	} {
		actual, err := ParseMoney(value)
		if err != nil || actual != expected {
			t.Errorf("ParseMoney(%q)=%d,%v; want %d", value, actual, err, expected)
		}
	}
	for _, value := range []string{"", "-1", "+1", " 1", "1 ", "1e2", "NaN", ".1", "1.", "1.001", "90071992547409.92", "999999999999999999999999999999999999999999"} {
		if _, err := ParseMoney(value); err == nil {
			t.Errorf("accepted invalid money %q", value)
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, math.Nextafter(1.23, 2)} {
		if _, err := MoneyFromFloat(value); err == nil {
			t.Errorf("accepted unsupported legacy float %v", value)
		}
	}
	if amount, err := MoneyFromFloat(123.45); err != nil || amount != 12345 {
		t.Fatalf("legacy decimal=%d,%v", amount, err)
	}
}

func TestQuantityAndLineRounding(t *testing.T) {
	for value, expected := range map[string]string{"1": "1", "01.0000": "1", "2.500": "2.5", "0.001": "0.001", "1000000": "1000000"} {
		actual, _, err := normalizeQuantity(value)
		if err != nil || actual != expected {
			t.Errorf("quantity %q=%q,%v", value, actual, err)
		}
	}
	for _, value := range []string{"0", "-1", "0.0001", "1000000.001", "1e2", "1.2345", "1.", ".1"} {
		if _, _, err := normalizeQuantity(value); err == nil {
			t.Errorf("accepted invalid quantity %q", value)
		}
	}
	if amount, err := lineAmount(199, 2500); err != nil || amount != 498 {
		t.Fatalf("line rounding=%d,%v", amount, err)
	}
	if _, err := lineAmount(MaxMoney, 2000); err == nil {
		t.Fatal("accepted overflowing line")
	}
	if _, err := lineAmount(1, 1); err == nil {
		t.Fatal("accepted line rounded to zero")
	}
	if _, err := addMoney(MaxMoney, 1); err == nil {
		t.Fatal("accepted overflowing total")
	}
}
