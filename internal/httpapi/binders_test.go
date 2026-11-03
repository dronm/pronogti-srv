package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistrationBinderRequiresPhone(t *testing.T) {
	for _, body := range []string{`{}`, `{"phone":""}`, `{"phone":null}`} {
		r := httptest.NewRequest(http.MethodPost, "/12/client/register", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.SetPathValue("companyID", "12")
		if _, err := registerClientBinder(r); err == nil {
			t.Errorf("accepted registration without a phone: %s", body)
		}
	}
}

func TestRegistrationBinderAcceptsFormsAndExistingJSON(t *testing.T) {
	tests := []struct {
		contentType string
		body        string
	}{
		{contentType: "application/json", body: `{"phone":"79991234567","birth_date":"1990-04-05T00:00:00Z","unknown":"ignored"}`},
		{contentType: "application/x-www-form-urlencoded", body: "phone=79991234567&birth_date=1990-04-05"},
	}
	for _, test := range tests {
		r := httptest.NewRequest(http.MethodPost, "/12/client/register", strings.NewReader(test.body))
		r.Header.Set("Content-Type", test.contentType)
		r.SetPathValue("companyID", "12")
		value, err := registerClientBinder(r)
		if err != nil {
			t.Fatalf("bind %s: %v", test.contentType, err)
		}
		request := value.(registerClientRequest)
		if request.CompanyID != 12 || request.Client.Phone != "79991234567" || request.Client.BirthDate == nil || request.Client.BirthDate.Format("2006-01-02") != "1990-04-05" {
			t.Errorf("decoded request = %#v", request)
		}
	}
}

func TestQRBinderRejectsEmptyJSON(t *testing.T) {
	for _, body := range []string{"", " \n\t"} {
		r := httptest.NewRequest(http.MethodPost, "/customer/init-qr-pay", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json; charset=utf-8")
		if _, err := kioskQRBinder(r); err == nil {
			t.Error("QR accepted empty JSON")
		}
	}
}
