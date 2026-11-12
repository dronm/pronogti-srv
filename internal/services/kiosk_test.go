package services

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dronm/modelbind"
	"github.com/dronm/pronogti/internal/models"
)

func TestMergeKioskConfigurationPreservesSparseUpdates(t *testing.T) {
	paymentID, fiscalID, yclientsID := int64(10), int64(20), int64(30)
	current := models.Kiosk{
		ID: 7, Name: "Reception", CompanyID: 1, IsActive: true, SessionVersion: 4,
		PaymentProfileID: &paymentID, FiscalProfileID: &fiscalID, YclientsProfileID: &yclientsID,
	}
	decode := func(body string) modelbind.ModelInput[*models.Kiosk] {
		t.Helper()
		request := httptest.NewRequest(http.MethodPatch, "/admin/kiosks/7", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		input, err := modelbind.DecodeRequestInput[*models.Kiosk](request)
		if err != nil {
			t.Fatal(err)
		}
		return input
	}

	updated := mergeKioskConfiguration(current, decode(`{"name":"  Main desk  ","is_active":false,"fiscal_profile_id":null}`))
	if updated.Name != "Main desk" || updated.IsActive || updated.CompanyID != 1 || updated.FiscalProfileID != nil ||
		updated.PaymentProfileID != &paymentID || updated.YclientsProfileID != &yclientsID {
		t.Fatalf("unexpected partial update: %+v", updated)
	}

	updated = mergeKioskConfiguration(current, decode(`{"company_id":2,"payment_profile_id":40}`))
	if updated.CompanyID != 2 || !updated.IsActive || updated.PaymentProfileID == nil || *updated.PaymentProfileID != 40 ||
		updated.FiscalProfileID != nil || updated.YclientsProfileID != nil || updated.ID != 7 || updated.SessionVersion != 4 {
		t.Fatalf("unexpected reassigned kiosk: %+v", updated)
	}
}
