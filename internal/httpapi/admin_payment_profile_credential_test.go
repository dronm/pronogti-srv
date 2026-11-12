package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/dronm/pronogti/internal/payment"
	"github.com/dronm/pronogti/internal/services"
	"github.com/dronm/webapp"
)

func TestBindPaymentProfileCredential(t *testing.T) {
	const marker = "private.jwt.marker"
	for _, body := range []string{
		`{"jwt":"` + marker + `","jwt":"another"}`,
		`{"jwt":"` + marker + `","unknown":true}`,
		`{"` + marker + `":"unknown"}`,
		`{"jwt":"` + marker + `"}{}`,
		`{"jwt":null}`,
		`{"jwt":""}`,
		`{"jwt":false}`,
		`{"jwt":"` + strings.Repeat("a", maxPaymentProfileCredentialJWT+1) + `"}`,
		`{"jwt":"` + marker + `"}` + strings.Repeat(" ", maxPaymentProfileCredentialBody),
	} {
		request := httptest.NewRequest(http.MethodPost, "/admin/payment-profiles/5/credential", strings.NewReader(body))
		request.SetPathValue("id", "5")
		request.Header.Set("Content-Type", "application/json")
		_, err := bindPaymentProfileCredential(request)
		if err == nil {
			t.Fatal("credential binder accepted invalid input")
		}
		response := httptest.NewRecorder()
		webapp.WriteBindingError(response, err)
		if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), marker) {
			t.Fatal("credential binder did not reject input without exposing its contents")
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/payment-profiles/5/credential?id=9&jwt=untrusted", strings.NewReader(`{"jwt":"`+marker+`"}`))
	request.SetPathValue("id", "5")
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	input, err := bindPaymentProfileCredential(request)
	if err != nil || input != (services.SetPaymentProfileCredentialInput{ID: 5, JWT: marker}) {
		t.Fatal("credential binder failed to use the path ID and JSON body")
	}
}

func TestAdminPaymentProfileCredentialManagement(t *testing.T) {
	const keyEnv = "PRONOGTI_TEST_PAYMENT_PROFILE_KEY"
	t.Setenv(keyEnv, strings.Repeat("3a", 32))
	previous := paymentProfileCredentialTokens.Load()
	t.Cleanup(func() { paymentProfileCredentialTokens.Store(previous) })
	ConfigurePaymentProfileCredentials(payment.NewEnvironmentTokens(keyEnv))
	fixture := newGeneratedAdministratorIntegration(t)
	// An already built handler must keep its own configured store.
	ConfigurePaymentProfileCredentials(nil)
	const companyID int64 = 781898
	ctx := context.Background()
	const collection = "/admin/payment-profiles"
	const create = `{"company_id":781898,"name":"Credential profile","environment":"production",
		"api_base_url":"https://enter.tochka.com/uapi","customer_code":"300000092",
		"sbp_merchant_id":"MF0000000001","account_id":"40702810900000000001/044525104",
		"qr_ttl_minutes":15,"http_timeout_seconds":20,"card_ttl_minutes":15,"enabled":false,"cards_enabled":false}`
	response := adminRequest(fixture.handler, http.MethodPost, collection, create, fixture.cookie, fixture.nonce)
	var created struct {
		ID int64 `json:"id"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &created) != nil || created.ID <= 0 {
		t.Fatalf("payment profile create: %d %s", response.Code, response.Body)
	}
	profile := collection + "/" + strconv.FormatInt(created.ID, 10)
	credential := profile + "/credential"
	const first = "private.test.jwt.first"
	const second = "private.test.jwt.second"
	for _, path := range []string{credential, profile, collection} {
		response := adminRequest(fixture.handler, http.MethodGet, path, "", nil, "")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous payment profile request returned %d", response.Code)
		}
	}
	response = adminRequest(fixture.handler, http.MethodPost, credential, `{"jwt":"`+first+`"}`, fixture.cookie, "wrong")
	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), first) {
		t.Fatalf("credential update without current nonce returned %d", response.Code)
	}
	response = adminRequest(fixture.handler, http.MethodGet, credential, "", fixture.cookie, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"configured":false`) {
		t.Fatalf("new payment profile credential status returned %d", response.Code)
	}
	for _, token := range []string{first, second} {
		response := adminRequest(fixture.handler, http.MethodPost, credential, `{"jwt":"`+token+`"}`, fixture.cookie, fixture.nonce)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
			!strings.Contains(response.Body.String(), `"configured":true`) || strings.Contains(response.Body.String(), token) {
			t.Fatalf("credential update did not return a safe status: %d", response.Code)
		}
		stored := payment.Profile{ID: created.ID, CompanyID: companyID}
		if err := fixture.conn.QueryRow(ctx, `SELECT jwt_ciphertext, coalesce(jwt_env, '')
			FROM public.payment_profiles WHERE id=$1`, created.ID).Scan(&stored.JWTCiphertext, &stored.JWTEnv); err != nil {
			t.Fatal(err)
		}
		decoded, err := payment.NewEnvironmentTokens(keyEnv).Token(ctx, stored)
		if err != nil || decoded != token || stored.JWTEnv != "" || strings.Contains(string(stored.JWTCiphertext), token) {
			t.Fatal("stored credential is not readable by the configured production resolver")
		}
	}
	for _, path := range []string{credential, profile, collection} {
		response := adminRequest(fixture.handler, http.MethodGet, path, "", fixture.cookie, "")
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
			strings.Contains(response.Body.String(), first) || strings.Contains(response.Body.String(), second) ||
			strings.Contains(response.Body.String(), `"jwt_ciphertext"`) {
			t.Fatalf("payment profile read returned an unsafe response: %d", response.Code)
		}
	}
	for _, body := range []string{`{"enabled":true}`, `{"enabled":false}`} {
		response := adminRequest(fixture.handler, http.MethodPatch, profile, body, fixture.cookie, fixture.nonce)
		if response.Code != http.StatusOK {
			t.Fatalf("payment profile activation update: %d %s", response.Code, response.Body)
		}
	}
	response = adminRequest(fixture.handler, http.MethodDelete, profile, "", fixture.cookie, fixture.nonce)
	if response.Code != http.StatusOK {
		t.Fatalf("delete unused disabled profile: %d %s", response.Code, response.Body)
	}
}
