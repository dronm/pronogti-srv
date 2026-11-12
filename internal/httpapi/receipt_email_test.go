package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dronm/pronogti/internal/kiosk"
)

func TestReceiptEmailBinderAcceptsOnlyTheRecipient(t *testing.T) {
	const id = "55fa1799-b883-4a23-9831-451360dd5a6d"
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"recipient", `{"email":"customer@example.test"}`, true},
		{"client-supplied URL", `{"email":"customer@example.test","url":"https://example.test"}`, false},
		{"oversize", `{"email":"` + strings.Repeat("a", 1024) + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/customer/payments/"+id+"/receipt-email", strings.NewReader(tc.body))
			request.SetPathValue("id", id)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(kiosk.CustomerHeader, "customer-session")
			result, err := receiptEmailBinder(request)
			if !tc.valid {
				if err == nil {
					t.Fatal("unexpectedly accepted receipt email input")
				}
				return
			}
			want := receiptEmailInput{Nonce: "customer-session", ID: id, Email: "customer@example.test"}
			if err != nil || result != want {
				t.Fatalf("receipt email input = %+v, %v", result, err)
			}
		})
	}
}
