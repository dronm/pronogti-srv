package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBankWebhookBypassesCustomerSessionAndOriginMiddleware(t *testing.T) {
	browserCalls := 0
	bankCalls := 0
	customer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		browserCalls++
		http.SetCookie(w, &http.Cookie{Name: "browser-session", Value: "customer"})
		w.WriteHeader(http.StatusUnauthorized)
	})
	webhook := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bankCalls++
		if r.PathValue("profileID") != "17" {
			t.Fatalf("profile ID=%q", r.PathValue("profileID"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "signed-bank-token" {
			t.Fatalf("callback body was altered: %q %v", body, err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := WithTochkaWebhook(customer, webhook)
	req := httptest.NewRequest(http.MethodPost, "/payments/tochka/webhook/17", strings.NewReader("signed-bank-token"))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Origin", "https://bank.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusNoContent || browserCalls != 0 || bankCalls != 1 || len(response.Result().Cookies()) != 0 {
		t.Fatalf("bank callback was routed through the customer API: code=%d browser=%d bank=%d cookies=%v", response.Code, browserCalls, bankCalls, response.Result().Cookies())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/customer/payments/example", nil))
	if response.Code != http.StatusUnauthorized || browserCalls != 1 || bankCalls != 1 {
		t.Fatal("customer API no longer uses its own middleware")
	}
}

func TestBankWebhookDisabledUsesOriginalRouter(t *testing.T) {
	customer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	handler := WithTochkaWebhook(customer, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/payments/tochka/webhook/17", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("unexpected disabled callback status=%d", response.Code)
	}
}

func TestSharedBankWebhookHasNoCallerSuppliedProfile(t *testing.T) {
	bankCalls := 0
	customer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("shared bank callback reached the customer session stack")
	})
	webhook := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bankCalls++
		if r.PathValue("profileID") != "" {
			t.Fatalf("shared callback supplied profile %q", r.PathValue("profileID"))
		}
		w.WriteHeader(http.StatusOK)
	})
	response := httptest.NewRecorder()
	WithTochkaWebhook(customer, webhook).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/payments/tochka/webhook", strings.NewReader("signed-bank-token")))
	if bankCalls != 1 || response.Code != http.StatusOK {
		t.Fatalf("shared callback was not mounted: calls=%d code=%d", bankCalls, response.Code)
	}
}
