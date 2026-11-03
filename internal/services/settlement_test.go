package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSettlementTransportDoesNotFollowRedirectsOrAlterSharedClient(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client := &http.Client{Timeout: time.Second}
	ctx := context.Background()
	wrapped := settlementNoRedirects(contextHTTPClient{context: ctx, client: client})
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, origin.URL, nil)
	response, err := wrapped.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || destinationCalls.Load() != 0 || client.CheckRedirect != nil {
		t.Fatal("settlement followed a write redirect or mutated the shared HTTP client")
	}
}

func TestSettlementRateLimitWaitHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider := &SettlementProvider{nextRequest: time.Now().Add(time.Minute)}
	transport := settlementHTTPClient{provider: provider, inner: testHTTPClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("cancelled settlement request reached YCLIENTS")
		return nil, nil
	})}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid/", nil)
	if _, err := transport.Do(request); !errors.Is(err, context.Canceled) {
		t.Fatalf("rate-limit wait ignored cancellation: %v", err)
	}
}
