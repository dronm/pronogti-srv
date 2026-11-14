package payment

import "testing"

func TestPaymentPurposePreservesLegacyCorrelationAndRejectsLooseMatches(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	for _, purpose := range []string{"Оплата " + id, "Checkout " + id} {
		if !matchesPaymentPurpose(purpose, id) || paymentPurposeOrderID(purpose) != id {
			t.Fatalf("cannot correlate payment purpose %q", purpose)
		}
	}
	for _, purpose := range []string{"Оплата ", "Checkout ", id, "оплата " + id, "Оплата  " + id, "Оплата " + id + " extra", "Checkout " + id + "\n", "Оплата 11111111-1111-4111-8111-11111111111z"} {
		if paymentPurposeOrderID(purpose) != "" || matchesPaymentPurpose(purpose, id) {
			t.Fatalf("accepted ambiguous payment purpose %q", purpose)
		}
	}
}
