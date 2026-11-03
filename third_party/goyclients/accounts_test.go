package goyclients

import (
	"context"
	"net/http"
	"testing"
)

func TestAccountsUsesCompanyScopeAndContext(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "account-request")
	client := NewYClients("partner-token", &mockHTTPClient{doFunc: func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/api/v1/accounts/781898" || req.URL.RawQuery != "" {
			t.Fatalf("incorrect accounts request: %s %s", req.Method, req.URL)
		}
		if req.Context().Value(contextKey{}) != "account-request" {
			t.Fatal("accounts request lost context")
		}
		if req.Body != nil && req.ContentLength != 0 {
			t.Fatal("accounts GET must not send a JSON body")
		}
		return newMockResponse(http.StatusOK, `{"success":true,"data":[{"id":12,"title":"Cashless","type":1,"comment":"SBP"},{"id":13,"title":"Cash","type":0,"comment":""}],"meta":[]}`), nil
	}})
	accounts, err := client.AccountsContext(ctx, 781898)
	if err != nil || len(accounts) != 2 || accounts[0].ID != 12 || accounts[0].Type != 1 || accounts[1].Type != 0 {
		t.Fatalf("accounts=%+v error=%v", accounts, err)
	}
}

func TestAccountsRejectsInvalidCompanyBeforeRequest(t *testing.T) {
	client := NewYClients("partner-token", &mockHTTPClient{doFunc: func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid company made an HTTP request")
		return nil, nil
	}})
	for _, id := range []int64{0, -1} {
		if _, err := client.Accounts(id); err == nil {
			t.Fatalf("company %d accepted", id)
		}
	}
}

func TestAccountsPropagatesAPIError(t *testing.T) {
	client := NewYClients("partner-token", &mockHTTPClient{doFunc: func(*http.Request) (*http.Response, error) {
		return newMockResponse(http.StatusForbidden, `{"success":false,"data":null,"meta":{"message":"Forbidden"}}`), nil
	}})
	if _, err := client.Accounts(781898); err == nil {
		t.Fatal("accounts API failure was ignored")
	}
}
