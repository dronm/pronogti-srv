package goyclients

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type contextDoer func(*http.Request) (*http.Response, error)

func (f contextDoer) Do(r *http.Request) (*http.Response, error) { return f(r) }

type contextRoundTripper func(*http.Request) (*http.Response, error)

func (f contextRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func contextResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestContextRequestSuccessUsesContextAndExistingAuth(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "request-owner")
	calls := 0
	client := NewYClients("partner-secret", contextDoer(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Context().Value(contextKey{}) != "request-owner" || r.Method != http.MethodPost || r.URL.Path != "/api/v1/payments/42" {
			t.Fatal("context, method or endpoint changed")
		}
		if r.URL.Query().Get("existing") != "one" || strings.Join(r.URL.Query()["filter"], ",") != "a,b" {
			t.Fatalf("query lost values: %s", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer partner-secret, User user-secret" || r.Header.Get("Accept") != apiVersion || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("authentication or media headers differ from the existing API")
		}
		if r.Header.Get("Idempotency-Key") != "" {
			t.Fatal("undocumented idempotency header added")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{"amount":"12.34"}` {
			t.Fatalf("unexpected exact body: %s (%v)", body, err)
		}
		return contextResponse(201, `{"success":true,"data":{"id":123},"meta":{"count":1}}`), nil
	}))
	client.User.UserToken = "user-secret"
	var data struct {
		ID int64 `json:"id"`
	}
	var meta struct {
		Count int `json:"count"`
	}
	err := client.SendRequestContext(ctx, "/api/v1/payments/42?existing=one", http.MethodPost, url.Values{"filter": {"a", "b"}}, map[string]string{"amount": "12.34"}, &data, &meta)
	if err != nil || data.ID != 123 || meta.Count != 1 || calls != 1 {
		t.Fatalf("data=%+v meta=%+v calls=%d error=%v", data, meta, calls, err)
	}
}

func TestContextRequestAuthEndpointOmitsUserToken(t *testing.T) {
	client := NewYClients("partner", contextDoer(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer partner" {
			t.Fatal("auth request included an old user token")
		}
		return contextResponse(200, `{"success":true,"data":{},"meta":{}}`), nil
	}))
	client.User.UserToken = "old-user"
	if err := client.SendRequestContext(context.Background(), authEndpoint, http.MethodPost, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestContextRequestCancellationBeforeDispatchIsDefinite(t *testing.T) {
	calls := 0
	client := NewYClients("partner", contextDoer(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("should not send") }))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := client.SendRequestContext(ctx, "/api/v1/payments/42", http.MethodPost, nil, nil, nil, nil)
	if !errors.Is(err, context.Canceled) || IsOutcomeUnknown(err) || calls != 0 {
		t.Fatalf("premature dispatch: %v, calls=%d", err, calls)
	}
	ctx, cancel = context.WithCancel(context.Background())
	err = client.SendRequestContext(ctx, "/api/v1/payments/42", http.MethodPost, nil, cancellingJSON{cancel}, nil, nil)
	if !errors.Is(err, context.Canceled) || IsOutcomeUnknown(err) || calls != 0 {
		t.Fatalf("request canceled during encoding was sent: %v", err)
	}
}

type cancellingJSON struct{ cancel context.CancelFunc }

func (v cancellingJSON) MarshalJSON() ([]byte, error) { v.cancel(); return []byte(`{}`), nil }

func TestContextRequestTransportLossAndCancellationNeverRetry(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodGet} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := NewYClients("partner", contextDoer(func(*http.Request) (*http.Response, error) {
				calls++
				cancel()
				return nil, &url.Error{Op: "Post", URL: "https://secret.example/?token=user-secret", Err: context.Canceled}
			}))
			err := client.SendRequestContext(ctx, "/api/v1/payments/42", method, nil, nil, nil, nil)
			if err == nil || !errors.Is(err, context.Canceled) || IsOutcomeUnknown(err) != (method != http.MethodGet) || calls != 1 {
				t.Fatalf("classification/call count: %v (%d)", err, calls)
			}
			if strings.Contains(err.Error(), "secret.example") || strings.Contains(err.Error(), "user-secret") {
				t.Fatal("transport error exposed a URL or credential")
			}
		})
	}
}

func TestContextRequestResponseClassification(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		unknown bool
		api     bool
	}{
		{"unauthorized JSON", 401, `{"success":false,"meta":{"message":"unauthorized"}}`, false, true},
		{"unauthorized proxy", 401, `<html>Unauthorized</html>`, false, true},
		{"forbidden proxy", 403, `not JSON`, false, true},
		{"validation", 422, `{"success":false,"meta":{"message":"invalid account"}}`, false, true},
		{"rate limit", 429, `{"success":false,"meta":{"message":"rate limited"}}`, false, true},
		{"request timeout", 408, `{"success":false,"meta":{"message":"timeout"}}`, true, false},
		{"server failure", 500, `{"success":false,"meta":{"message":"failed"}}`, true, false},
		{"unavailable", 503, `unavailable`, true, false},
		{"redirect", 302, `{"success":true,"data":{}}`, true, false},
		{"malformed", 200, `{"success":true`, true, false},
		{"empty", 204, ``, true, false},
		{"missing success", 200, `{"data":{"id":1},"meta":{}}`, true, false},
		{"null success", 200, `{"success":null,"data":{},"meta":{}}`, true, false},
		{"wrong success type", 200, `{"success":"true","data":{},"meta":{}}`, true, false},
		{"array envelope", 200, `[]`, true, false},
		{"structured API rejection", 200, `{"success":false,"meta":{"message":"not permitted"}}`, false, true},
		{"missing rejection details", 200, `{"success":false}`, true, false},
		{"null rejection details", 200, `{"success":false,"meta":null}`, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := NewYClients("partner", contextDoer(func(*http.Request) (*http.Response, error) { calls++; return contextResponse(tc.status, tc.body), nil }))
			err := client.SendRequestContext(context.Background(), "/api/v1/payments/42", http.MethodPost, nil, nil, nil, nil)
			if err == nil || IsOutcomeUnknown(err) != tc.unknown || calls != 1 {
				t.Fatalf("classification=%v unknown=%v calls=%d", err, IsOutcomeUnknown(err), calls)
			}
			var apiErr *APIError
			if tc.api && (!errors.As(err, &apiErr) || apiErr.StatusCode != tc.status) {
				t.Fatalf("missing typed API error: %v", err)
			}
			if errors.Is(err, ErrUnauthorized) != (tc.status == 401) || errors.Is(err, ErrForbidden) != (tc.status == 403) {
				t.Fatalf("legacy authorization sentinel mismatch: %v", err)
			}
		})
	}
}

func TestContextRequestSuccessfulWriteDecodeFailuresRemainUnknown(t *testing.T) {
	for _, body := range []string{
		`{"success":true,"meta":{}}`,
		`{"success":true,"data":null,"meta":{}}`,
		`{"success":true,"data":{"id":"not an integer"},"meta":{}}`,
		`{"success":true,"data":{"id":1}}`,
		`{"success":true,"data":{"id":1},"meta":null}`,
		`{"success":true,"data":{"id":1},"meta":{"count":"not an integer"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			client := NewYClients("partner", contextDoer(func(*http.Request) (*http.Response, error) { return contextResponse(200, body), nil }))
			var data struct {
				ID int64 `json:"id"`
			}
			var meta struct {
				Count int `json:"count"`
			}
			err := client.SendRequestContext(context.Background(), "/api/v1/payments/42", http.MethodPost, nil, nil, &data, &meta)
			if !IsOutcomeUnknown(err) {
				t.Fatalf("successful mutation decoding was treated as definite rejection: %v", err)
			}
		})
	}
}

type contextBrokenBody struct{ closed bool }

func (*contextBrokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (b *contextBrokenBody) Close() error           { b.closed = true; return nil }

func TestContextRequestReadFailureAndSizeLimit(t *testing.T) {
	broken := &contextBrokenBody{}
	client := NewYClients("partner", contextDoer(func(*http.Request) (*http.Response, error) { return &http.Response{StatusCode: 200, Body: broken}, nil }))
	err := client.SendRequestContext(context.Background(), "/api/v1/payments/42", http.MethodPost, nil, nil, nil, nil)
	if !IsOutcomeUnknown(err) || !errors.Is(err, io.ErrUnexpectedEOF) || !broken.closed {
		t.Fatalf("read failure classification/cleanup: %v", err)
	}
	client.HTTPClient = contextDoer(func(*http.Request) (*http.Response, error) {
		return contextResponse(200, strings.Repeat("x", int(maxContextResponseBytes)+1)), nil
	})
	err = client.SendRequestContext(context.Background(), "/api/v1/payments/42", http.MethodPost, nil, nil, nil, nil)
	if !IsOutcomeUnknown(err) || !strings.Contains(errors.Unwrap(err).Error(), "supported size") {
		t.Fatalf("oversized mutation response accepted: %v", err)
	}
}

func TestContextRequestAPIErrorRedactsCredentialsAndURLs(t *testing.T) {
	client := NewYClients("partner-secret", contextDoer(func(*http.Request) (*http.Response, error) {
		return contextResponse(400, `{"success":false,"meta":{"message":"partner-secret user-secret https://secret.example/token failed"}}`), nil
	}))
	client.User.UserToken = "user-secret"
	err := client.SendRequestContext(context.Background(), "/api/v1/payments/42", http.MethodPost, nil, nil, nil, nil)
	if err == nil || strings.Contains(err.Error(), "partner-secret") || strings.Contains(err.Error(), "user-secret") || strings.Contains(err.Error(), "https://") {
		t.Fatalf("sensitive API error: %v", err)
	}
}

func TestContextRequestInvalidLocalInputsDoNotDispatch(t *testing.T) {
	calls := 0
	client := NewYClients("partner", contextDoer(func(*http.Request) (*http.Response, error) { calls++; return nil, nil }))
	for _, tc := range []struct {
		name string
		ctx  context.Context
		body any
	}{
		{"nil context", nil, nil},
		{"unserializable body", context.Background(), make(chan int)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := client.SendRequestContext(tc.ctx, "/api/v1/payments/42", http.MethodPost, nil, tc.body, nil, nil)
			if err == nil || IsOutcomeUnknown(err) {
				t.Fatalf("local error classification: %v", err)
			}
		})
	}
	client.PartnerToken = "invalid\nsecret"
	if err := client.SendRequestContext(context.Background(), "/api/v1/payments/42", http.MethodPost, nil, nil, nil, nil); err == nil || IsOutcomeUnknown(err) {
		t.Fatal("invalid header was dispatched")
	}
	if calls != 0 {
		t.Fatal("invalid local request reached transport")
	}
	var missing *YClients
	if err := missing.SendRequestContext(context.Background(), "/api/v1/payments/42", http.MethodPost, nil, nil, nil, nil); err == nil {
		t.Fatal("nil client accepted")
	}
}

func TestContextRequestDisablesRedirectWithoutMutatingSharedClient(t *testing.T) {
	calls, redirectPolicyCalls := 0, 0
	standard := &http.Client{
		Transport: contextRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://other.example/capture"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
		}),
		CheckRedirect: func(*http.Request, []*http.Request) error { redirectPolicyCalls++; return nil },
	}
	client := NewYClients("partner", standard)
	err := client.SendRequestContext(context.Background(), "/api/v1/payments/42", http.MethodPost, nil, json.RawMessage(`{}`), nil, nil)
	if !IsOutcomeUnknown(err) || calls != 1 || redirectPolicyCalls != 0 {
		t.Fatalf("redirect followed: error=%v requests=%d redirects=%d", err, calls, redirectPolicyCalls)
	}
	if standard.CheckRedirect == nil {
		t.Fatal("caller redirect policy was overwritten")
	}
	_ = standard.CheckRedirect(nil, nil)
	if redirectPolicyCalls != 1 {
		t.Fatal("caller policy changed")
	}
}
