package services

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dronm/modelbind"
	"github.com/dronm/paytochkabank"
	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/webapp"
)

func TestPaymentProfilePATCHPreservesOmittedBankSettings(t *testing.T) {
	customer, account, oldReturn := "customer", "40702810900000000001/044525104", "https://example.test/checkout"
	current := models.PaymentProfile{
		CompanyID: 1, Environment: models.PaymentProfileEnvironmentProduction, APIBaseURL: paytochkabank.ProductionURL,
		Name: "Current", Enabled: true, CustomerCode: &customer, AccountID: &account, CardRedirectURL: &oldReturn,
		QrTtlMinutes: 15, HTTPTimeoutSeconds: 20, CardTtlMinutes: 15,
	}
	req := httptest.NewRequest("PATCH", "/", strings.NewReader(`{"name":"  New  ","card_redirect_url":null,"enabled":false}`))
	input, err := modelbind.DecodeRequestInput[*models.PaymentProfile](req)
	if err != nil {
		t.Fatal(err)
	}
	next := mergePaymentProfile(current, input)
	if next.Name != "New" || next.CardRedirectURL != nil || next.Enabled {
		t.Fatalf("patch was not applied: %+v", next)
	}
	if next.CompanyID != current.CompanyID || next.Environment != current.Environment ||
		next.APIBaseURL != current.APIBaseURL || next.CustomerCode != current.CustomerCode ||
		next.AccountID != current.AccountID || next.HTTPTimeoutSeconds != current.HTTPTimeoutSeconds {
		t.Fatalf("omitted values were changed: %+v", next)
	}
}

func TestPaymentProfileAccountAcceptsAccountNumberAndPreservesEquivalentLegacyValue(t *testing.T) {
	const number = "40702810900000000001"
	legacy := number + "/044525104"
	req := httptest.NewRequest("PATCH", "/", strings.NewReader(`{"account_id":"`+number+`"}`))
	input, err := modelbind.DecodeRequestInput[*models.PaymentProfile](req)
	if err != nil {
		t.Fatal(err)
	}
	current := models.PaymentProfile{
		CompanyID: 1, Environment: models.PaymentProfileEnvironmentProduction, APIBaseURL: paytochkabank.ProductionURL,
		Name: "Current", QrTtlMinutes: 15, HTTPTimeoutSeconds: 20, CardTtlMinutes: 15,
	}
	next := mergePaymentProfile(current, input)
	if next.AccountID == nil || *next.AccountID != number {
		t.Fatalf("bare account was not kept: %+v", next.AccountID)
	}
	if err := validatePaymentProfile(nil, next, false); err != nil {
		t.Fatalf("bare account rejected: %v", err)
	}
	invalid := "4070281090000000000"
	next.AccountID = &invalid
	if err := validatePaymentProfile(nil, next, false); err == nil {
		t.Fatal("short account number accepted")
	}
	current.AccountID = &legacy
	next = mergePaymentProfile(current, input)
	if next.AccountID != current.AccountID {
		t.Fatalf("equivalent edit rewrote the stored account: %+v", next.AccountID)
	}
}

func TestPaymentProfileValidationSupportsDraftAndChecksActivation(t *testing.T) {
	item := models.PaymentProfile{
		CompanyID: 1, Environment: models.PaymentProfileEnvironmentProduction, APIBaseURL: paytochkabank.ProductionURL,
		Name: "Draft", QrTtlMinutes: 15, HTTPTimeoutSeconds: 20, CardTtlMinutes: 15,
	}
	if err := validatePaymentProfile(nil, item, false); err != nil {
		t.Fatalf("disabled draft rejected: %v", err)
	}
	previous := item
	item.Enabled = true
	err := validatePaymentProfile(&previous, item, false)
	var appErr *webapp.AppError
	if value, ok := err.(*webapp.AppError); ok {
		appErr = value
	}
	if appErr == nil || appErr.StatusCode() != 400 {
		t.Fatalf("incomplete enabled profile accepted: %v", err)
	}
	details, err := json.Marshal(appErr.Details())
	if err != nil || !strings.Contains(string(details), "customer_code") || !strings.Contains(string(details), "enabled") {
		t.Fatalf("missing field errors: %s, %v", details, err)
	}
	customer, merchant := "customer", "123456789012345"
	item.CustomerCode, item.AcquiringMerchantID, item.CardsEnabled = &customer, &merchant, true
	if err := validatePaymentProfile(&previous, item, true); err == nil {
		t.Fatal("enabled live card profile accepted without return URLs")
	}
	success, failure := "https://frontend.example.test/return", "https://frontend.example.test/fail"
	item.CardRedirectURL, item.CardFailRedirectURL = &success, &failure
	if err := validatePaymentProfile(&previous, item, true); err != nil {
		t.Fatalf("valid card-only profile rejected: %v", err)
	}
}

func TestPaymentProfileEndpointsTrustTochkaAndLocalTestServers(t *testing.T) {
	for _, test := range []struct {
		environment models.PaymentProfileEnvironment
		url         string
		allowed     bool
	}{
		{models.PaymentProfileEnvironmentMock, "http://127.0.0.1:8099", true},
		{models.PaymentProfileEnvironmentMock, "http://127.0.0.1:8080/test-api", true},
		{models.PaymentProfileEnvironmentMock, "https://elsewhere.example.test", false},
		{models.PaymentProfileEnvironmentSandbox, paytochkabank.SandboxURL, true},
		{models.PaymentProfileEnvironmentSandbox, "http://[::1]:8090/sandbox", true},
		{models.PaymentProfileEnvironmentSandbox, paytochkabank.ProductionURL, false},
		{models.PaymentProfileEnvironmentProduction, paytochkabank.ProductionURL, true},
		{models.PaymentProfileEnvironmentProduction, "http://127.0.0.1:8099", false},
		{models.PaymentProfileEnvironmentProduction, paytochkabank.ProductionURL + "?redirect=https://elsewhere.example.test", false},
		{models.PaymentProfileEnvironmentProduction, "https://enter.tochka.com.elsewhere.example.test/uapi", false},
		{models.PaymentProfileEnvironmentProduction, "https://enter.tochka.com@elsewhere.example.test/uapi", false},
	} {
		if got := validAdminPaymentEndpoint(test.environment, test.url); got != test.allowed {
			t.Errorf("validAdminPaymentEndpoint(%q,%q) = %v, want %v", test.environment, test.url, got, test.allowed)
		}
	}
}

func TestTerminalOnlyPaymentProfileNeedsNoOnlineCredentials(t *testing.T) {
	item := models.PaymentProfile{
		CompanyID: 1, Name: "Physical terminal", Environment: models.PaymentProfileEnvironmentProduction,
		Enabled: true, TerminalsEnabled: true, QrTtlMinutes: 15, HTTPTimeoutSeconds: 20, CardTtlMinutes: 15,
	}
	if err := validatePaymentProfile(nil, item, false); err != nil {
		t.Fatalf("terminal-only activation unexpectedly requires online credentials: %v", err)
	}
	previous := item
	item.TerminalsEnabled = false
	if err := validatePaymentProfile(nil, item, false); err == nil {
		t.Fatal("online profile accepted without API URL and credentials")
	}
	item.Enabled = false
	if err := validatePaymentProfile(&previous, item, false); err == nil {
		t.Fatal("removing terminal support must not retain an unconfigured online profile")
	}
	item.Enabled = true
	item.TerminalsEnabled = true
	item.APIBaseURL = paytochkabank.ProductionURL
	if err := validatePaymentProfile(nil, item, false); err == nil {
		t.Fatal("enabling terminals must not bypass credentials for a configured online profile")
	}
	item.APIBaseURL = ""
	item.CardsEnabled = true
	if err := validatePaymentProfile(nil, item, false); err == nil {
		t.Fatal("terminal flag must not bypass hosted-card requirements")
	}
}

func TestPaymentProfileTerminalPATCHPreservesOmittedFlag(t *testing.T) {
	current := models.PaymentProfile{TerminalsEnabled: true}
	for _, body := range []string{`{"name":"New"}`, `{"terminals_enabled":false}`} {
		request := httptest.NewRequest("PATCH", "/", strings.NewReader(body))
		input, err := modelbind.DecodeRequestInput[*models.PaymentProfile](request)
		if err != nil {
			t.Fatal(err)
		}
		next := mergePaymentProfile(current, input)
		if next.TerminalsEnabled != (body == `{"name":"New"}`) {
			t.Fatal("terminal capability PATCH does not distinguish omission from false")
		}
	}
}
