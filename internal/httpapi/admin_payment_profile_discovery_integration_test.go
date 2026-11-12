package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dronm/pronogti/internal/payment"
)

func TestAdminPaymentProfileDiscoversTochkaIdentifiers(t *testing.T) {
	const token = "private.discovery.fixture.jwt"
	tokens, err := payment.NewConfiguredTokens("", strings.Repeat("9a", 32))
	if err != nil {
		t.Fatal(err)
	}
	previous := paymentProfileCredentialTokens.Load()
	t.Cleanup(func() { paymentProfileCredentialTokens.Store(previous) })
	ConfigurePaymentProfileCredentials(tokens)
	fixture := newGeneratedAdministratorIntegration(t)

	var bankCalls atomic.Int32
	var sbpForbidden atomic.Bool
	bank := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bankCalls.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("lookup must use a read-only request and the saved credential")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-banking/v1.0/customers":
			_, _ = io.WriteString(w, `{"Data":{"Customer":[
				{"customerCode":"300000091","customerType":"Business","fullName":"Other company","taxCode":"1234567890"},
				{"customerCode":"300000092","customerType":"Business","fullName":"Test company","taxCode":"1234567891"},
				{"customerCode":"300000093","customerType":"Personal","fullName":"Private customer"}]}}`)
		case "/sbp/v1.0/customer/300000092/044525104":
			if sbpForbidden.Load() {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"message":"private bank response"}`)
				return
			}
			_, _ = io.WriteString(w, `{"Data":{"legalId":"LF0000000001","status":"Active","customerCode":"300000092","bankCode":"044525104"}}`)
		case "/sbp/v1.0/merchant/legal-entity/LF0000000001":
			_, _ = io.WriteString(w, `{"Data":{"MerchantList":[
				{"merchantId":"MF0000000001","legalId":"LF0000000001","brandName":"QR outlet","status":"Active","capabilities":"110"},
				{"merchantId":"MF0000000002","legalId":"LF0000000001","brandName":"Static only","status":"Active","capabilities":"001"}]}}`)
		case "/acquiring/v1.0/retailers":
			if r.URL.Query().Get("customerCode") != "300000092" {
				t.Error("retailer lookup used a different company")
			}
			_, _ = io.WriteString(w, `{"Data":{"Retailer":[
				{"merchantId":"200000000001056","name":"Card outlet","status":"REG","isActive":true,"paymentModes":["card"]},
				{"merchantId":"200000000001057","name":"Inactive outlet","status":"REG","isActive":false,"paymentModes":["card"]}]}}`)
		default:
			t.Errorf("unexpected bank path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(bank.Close)

	const collection = "/admin/payment-profiles"
	create := fmt.Sprintf(`{"company_id":781898,"name":"Discovery integration","environment":"sandbox",
		"api_base_url":%q,"qr_ttl_minutes":15,"http_timeout_seconds":2,"card_ttl_minutes":15,
		"enabled":false,"cards_enabled":false}`, bank.URL)
	response := adminRequest(fixture.handler, http.MethodPost, collection, create, fixture.cookie, fixture.nonce)
	var created struct {
		ID int64 `json:"id"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &created) != nil || created.ID <= 0 {
		t.Fatalf("profile creation failed: %d %s", response.Code, response.Body)
	}
	profile := fmt.Sprintf("%s/%d", collection, created.ID)
	discover := profile + "/discover"
	if got := adminRequest(fixture.handler, http.MethodPost, discover, `{}`, nil, ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous discovery returned %d", got.Code)
	}
	if got := adminRequest(fixture.handler, http.MethodPost, discover, `{}`, fixture.cookie, ""); got.Code != http.StatusForbidden {
		t.Fatalf("discovery without session header returned %d", got.Code)
	}
	if got := adminRequest(fixture.handler, http.MethodPost, discover, `{}`, fixture.cookie, fixture.nonce); got.Code != http.StatusConflict {
		t.Fatalf("discovery without saved JWT returned %d %s", got.Code, got.Body)
	}
	if bankCalls.Load() != 0 {
		t.Fatal("discovery called the bank before authentication and credential checks")
	}
	if got := adminRequest(fixture.handler, http.MethodPost, profile+"/credential", `{"jwt":"`+token+`"}`, fixture.cookie, fixture.nonce); got.Code != http.StatusOK {
		t.Fatalf("credential save returned %d", got.Code)
	}

	type candidate struct {
		ID string `json:"id"`
	}
	type lookup struct {
		Companies          []candidate       `json:"companies"`
		CustomerCode       string            `json:"customer_code"`
		SBPMerchants       []candidate       `json:"sbp_merchants"`
		AcquiringMerchants []candidate       `json:"acquiring_merchants"`
		Issues             map[string]string `json:"issues"`
	}
	lookupProfile := func(body string) lookup {
		t.Helper()
		got := adminRequest(fixture.handler, http.MethodPost, discover, body, fixture.cookie, fixture.nonce)
		if got.Code != http.StatusOK || got.Header().Get("Cache-Control") != "no-store" ||
			strings.Contains(got.Body.String(), token) || strings.Contains(got.Body.String(), "private bank response") {
			t.Fatalf("unexpected lookup response status %d", got.Code)
		}
		var result lookup
		if err := json.Unmarshal(got.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	choose := lookupProfile(`{}`)
	if choose.CustomerCode != "" || len(choose.Companies) != 2 || len(choose.SBPMerchants) != 0 ||
		len(choose.AcquiringMerchants) != 0 || choose.Issues["customer_code"] != "selection_required" || bankCalls.Load() != 1 {
		t.Fatalf("expected explicit company selection, got %+v with %d bank calls", choose, bankCalls.Load())
	}
	const selected = `{"customer_code":"300000092"}`
	full := lookupProfile(selected)
	if full.CustomerCode != "300000092" || len(full.SBPMerchants) != 1 || full.SBPMerchants[0].ID != "MF0000000001" ||
		len(full.AcquiringMerchants) != 1 || full.AcquiringMerchants[0].ID != "200000000001056" || len(full.Issues) != 0 {
		t.Fatalf("unexpected eligible lookup candidates: %+v", full)
	}
	sbpForbidden.Store(true)
	partial := lookupProfile(selected)
	if partial.Issues["sbp_merchant_id"] != "permission_denied" || len(partial.SBPMerchants) != 0 || len(partial.AcquiringMerchants) != 1 {
		t.Fatalf("SBP permission failure must preserve the card candidates: %+v", partial)
	}
	var fieldsStillEmpty bool
	err = fixture.conn.QueryRow(context.Background(), `SELECT customer_code IS NULL AND sbp_merchant_id IS NULL
		AND acquiring_merchant_id IS NULL AND NOT enabled FROM public.payment_profiles WHERE id=$1`, created.ID).Scan(&fieldsStillEmpty)
	if err != nil || !fieldsStillEmpty {
		t.Fatalf("discovery mutated the payment profile: %v", err)
	}
}
