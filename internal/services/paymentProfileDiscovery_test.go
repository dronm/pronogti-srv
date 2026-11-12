package services

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/dronm/paytochkabank"
	"github.com/dronm/pronogti/internal/payment"
	"github.com/dronm/webapp"
)

func TestPaymentProfileDiscoveryRequiresAuthorizedBusinessSelection(t *testing.T) {
	client := discoveryTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open-banking/v1.0/customers" {
			t.Errorf("looked up outlets without an eligible selected company: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"Data":{"Customer":[
			{"customerCode":"company01","customerType":"Business","taxCode":"1234567890","fullName":"First"},
			{"customerCode":"company02","customerType":"Business","shortName":"Second"},
			{"customerCode":"personal1","customerType":"Personal","shortName":"Individual"}
		]}}`)
	})
	result, err := discoverPaymentProfile(context.Background(), client, payment.Profile{}, "")
	if err != nil || result.CustomerCode != "" || result.Issues["customer_code"] != "selection_required" ||
		!reflect.DeepEqual(result.Companies, []PaymentProfileDiscoveryCandidate{
			{ID: "company01", Name: "First", Detail: "ИНН 1234567890"},
			{ID: "company02", Name: "Second"},
		}) {
		t.Fatalf("unexpected company candidates: %+v, %v", result, err)
	}
	_, err = discoverPaymentProfile(context.Background(), client, payment.Profile{}, "personal1")
	var appErr webapp.HTTPError
	if !errors.As(err, &appErr) || appErr.StatusCode() != http.StatusBadRequest {
		t.Fatalf("expected a rejected company selection, got %v", err)
	}
}

func TestPaymentProfileDiscoveryFiltersOutletsAndPreservesPartialResult(t *testing.T) {
	for _, failCard := range []bool{false, true} {
		name := "both outlet types"
		if failCard {
			name = "card access denied"
		}
		t.Run(name, func(t *testing.T) {
			client := discoveryTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/open-banking/v1.0/customers":
					_, _ = io.WriteString(w, `{"Data":{"Customer":[
						{"customerCode":"company01","customerType":"Business"},
						{"customerCode":"company02","customerType":"Business"}
					]}}`)
				case "/sbp/v1.0/customer/company02/040000000":
					_, _ = io.WriteString(w, `{"Data":{"legalId":"LF0000000001","customerCode":"company02","bankCode":"040000000","status":"Active"}}`)
				case "/sbp/v1.0/merchant/legal-entity/LF0000000001":
					_, _ = io.WriteString(w, `{"Data":{"MerchantList":[
						{"merchantId":"MF0000000001","legalId":"LF0000000001","brandName":"QR outlet","city":"Moscow","status":"Active","capabilities":"010"},
						{"merchantId":"MF0000000002","legalId":"LF0000000001","status":"Suspended","capabilities":"111"},
						{"merchantId":"MF0000000003","legalId":"LF0000000001","status":"Active","capabilities":"001"},
						{"merchantId":"MF0000000004","legalId":"LF0000000002","status":"Active","capabilities":"111"}
					]}}`)
				case "/acquiring/v1.0/retailers":
					if r.URL.Query().Get("customerCode") != "company02" {
						t.Errorf("unexpected acquiring company: %s", r.URL)
					}
					if failCard {
						http.Error(w, "private diagnostic", http.StatusForbidden)
						return
					}
					_, _ = io.WriteString(w, `{"Data":{"Retailer":[
						{"merchantId":"200000000000001","name":"Card outlet","status":"REG","isActive":true,"paymentModes":["card"]},
						{"merchantId":"200000000000002","status":"REG","isActive":false,"paymentModes":["card"]},
						{"merchantId":"200000000000003","status":"REG","isActive":true,"paymentModes":["sbp"]},
						{"merchantId":"200000000000004","status":"REG","isActive":true,"paymentModes":["sbp","card"]}
					]}}`)
				default:
					t.Errorf("unexpected bank request: %s", r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			result, err := discoverPaymentProfile(context.Background(), client, payment.Profile{
				Environment: "production", CustomerCode: "company01", AccountID: "40702810900000000001/040000000",
			}, "company02")
			if err != nil || result.CustomerCode != "company02" || !reflect.DeepEqual(result.SBPMerchants, []PaymentProfileDiscoveryCandidate{
				{ID: "MF0000000001", Name: "QR outlet", Detail: "Moscow"},
			}) {
				t.Fatalf("unexpected discovery result: %+v, %v", result, err)
			}
			if failCard {
				if result.AcquiringMerchants == nil || len(result.AcquiringMerchants) != 0 ||
					result.Issues["acquiring_merchant_id"] != "permission_denied" {
					t.Fatalf("expected a safe acquiring issue and preserved SBP candidate: %+v", result)
				}
			} else if !reflect.DeepEqual(result.AcquiringMerchants, []PaymentProfileDiscoveryCandidate{
				{ID: "200000000000001", Name: "Card outlet"}, {ID: "200000000000004", Name: "200000000000004"},
			}) || result.Issues["acquiring_merchant_id"] != "selection_required" {
				t.Fatalf("unexpected acquiring candidates: %+v", result)
			}
		})
	}
}

func TestPaymentProfileDiscoverySanitizesUpstreamFailures(t *testing.T) {
	client := discoveryTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "private diagnostic", http.StatusForbidden)
	})
	_, err := discoverPaymentProfile(context.Background(), client, payment.Profile{}, "")
	var appErr webapp.HTTPError
	if !errors.As(err, &appErr) || appErr.Code() != "payment_discovery_customers_permission_denied" ||
		strings.Contains(err.Error(), "private diagnostic") {
		t.Fatalf("expected a safe upstream error: %v", err)
	}
}

func discoveryTestClient(t *testing.T, bank http.HandlerFunc) *paytochkabank.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer private.test.jwt" {
			t.Errorf("unexpected outbound discovery request: %s %s", r.Method, r.URL.Path)
		}
		bank(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := paytochkabank.NewClient("private.test.jwt", paytochkabank.WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	return client
}
