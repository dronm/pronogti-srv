package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dronm/pronogti/internal/services"
	"github.com/dronm/webapp"
)

func TestBindPaymentProfileDiscovery(t *testing.T) {
	const marker = "private.test.jwt"
	for _, body := range []string{
		`{"jwt":"` + marker + `"}`,
		`{"api_base_url":"https://unexpected.example"}`,
		`{"customer_code":12}`,
		`{}{}`,
		`[]`,
		`{}` + strings.Repeat(" ", 1024),
	} {
		request := httptest.NewRequest(http.MethodPost, "/admin/payment-profiles/5/discover", strings.NewReader(body))
		request.SetPathValue("id", "5")
		request.Header.Set("Content-Type", "application/json")
		_, err := bindPaymentProfileDiscovery(request)
		if err == nil {
			t.Fatalf("discovery binder accepted unexpected input")
		}
		response := httptest.NewRecorder()
		webapp.WriteBindingError(response, err)
		if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), marker) {
			t.Fatalf("discovery binder did not reject input safely")
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/admin/payment-profiles/5/discover?id=9&customer_code=wrong",
		strings.NewReader(`{"customer_code":"300000092"}`))
	request.SetPathValue("id", "5")
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	input, err := bindPaymentProfileDiscovery(request)
	if err != nil || input != (services.PaymentProfileDiscoveryInput{ID: 5, CustomerCode: "300000092"}) {
		t.Fatalf("discovery binder did not use the profile path and JSON body: %+v, %v", input, err)
	}
}
