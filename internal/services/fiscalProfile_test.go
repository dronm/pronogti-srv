package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dronm/modelbind"
	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/webapp"
)

func TestValidateFiscalProfileCodes(t *testing.T) {
	decode := func(body string) modelbind.ModelInput[*models.FiscalProfile] {
		t.Helper()
		request := httptest.NewRequest(http.MethodPatch, "/admin/fiscal-profiles/1", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		input, err := modelbind.DecodeRequestInput[*models.FiscalProfile](request)
		if err != nil {
			t.Fatal(err)
		}
		return input
	}

	for _, body := range []string{
		`{"name":"New name"}`,
		`{"service_vat_rate":11,"good_vat_rate":12}`,
		`{"tax_system":32,"payment_method":7,"timeout_seconds":100}`,
		`{"time_zone":4,"automatic_device_number":"0004","send_settlement_details":true}`,
		`{"time_zone":null,"automatic_device_number":null,"send_settlement_details":false}`,
	} {
		if err := validateFiscalProfileCodes(decode(body)); err != nil {
			t.Fatalf("valid input %s rejected: %v", body, err)
		}
	}

	err := validateFiscalProfileCodes(decode(
		`{"tax_system":8,"service_vat_rate":0,"good_vat_rate":13,"payment_method":8,"timeout_seconds":301,"time_zone":12,"automatic_device_number":"💅"}`,
	))
	recorder := httptest.NewRecorder()
	webapp.WriteServiceError(recorder, err)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid codes returned %d: %s", recorder.Code, recorder.Body)
	}
	var response struct {
		Error struct {
			Details struct {
				FieldErrors map[string]string `json:"field_errors"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"tax_system", "service_vat_rate", "good_vat_rate", "payment_method", "timeout_seconds", "time_zone", "automatic_device_number"} {
		if response.Error.Details.FieldErrors[field] == "" {
			t.Errorf("missing validation error for %s", field)
		}
	}
}
