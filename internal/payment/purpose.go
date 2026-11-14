package payment

import "strings"

const paymentPurposePrefix = "Оплата "
const legacyPaymentPurposePrefix = "Checkout "

func paymentPurpose(orderID string) string {
	return paymentPurposePrefix + orderID
}

// Already issued QR codes keep their original purpose. Both exact formats may
// correlate an early signed callback before the bank QR identity is saved.
func matchesPaymentPurpose(purpose, orderID string) bool {
	return purpose == paymentPurpose(orderID) || purpose == legacyPaymentPurposePrefix+orderID
}

func paymentPurposeOrderID(purpose string) string {
	for _, prefix := range []string{paymentPurposePrefix, legacyPaymentPurposePrefix} {
		if !strings.HasPrefix(purpose, prefix) {
			continue
		}
		orderID := strings.TrimPrefix(purpose, prefix)
		if confirmationUUID(orderID) {
			return orderID
		}
	}
	return ""
}
