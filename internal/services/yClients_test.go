package services

import (
	"context"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dronm/goyclients"
	"github.com/dronm/pronogti/internal/config"
)

type testHTTPClient func(*http.Request) (*http.Response, error)

func (client testHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return client(request)
}

type observedContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *observedContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func authResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"success":true,"data":{"user_token":"refreshed-token"}}`)),
	}
}

func newTestYClient(t *testing.T, persist func(string, time.Time) error, httpClients ...goyclients.HTTPClient) *YClientT {
	t.Helper()
	client, err := NewYClient(config.YClients{
		Login:        "test-login",
		Password:     "test-password",
		PartnerToken: "test-partner-token",
	}, false, persist, httpClients...)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestAuthConcurrentRequestsShareAuthentication(t *testing.T) {
	var authentications atomic.Int32
	var persisted atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	client := newTestYClient(t, func(token string, at time.Time) error {
		if token != "refreshed-token" || at.IsZero() {
			t.Errorf("unexpected persisted token or timestamp")
		}
		persisted.Add(1)
		return nil
	}, testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/auth") {
			t.Errorf("unexpected authentication request: %s %s", request.Method, request.URL.Path)
		}
		authentications.Add(1)
		close(started)
		<-release
		return authResponse(), nil
	}))

	const callers = 12
	var callersDone sync.WaitGroup
	callersDone.Add(callers)
	for range callers {
		go func() {
			defer callersDone.Done()
			if err := client.auth(context.Background()); err != nil {
				t.Errorf("authenticate: %v", err)
			}
		}()
	}
	<-started
	close(release)
	done := make(chan struct{})
	go func() {
		callersDone.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("authentication callers did not finish")
	}
	if authentications.Load() != 1 || persisted.Load() != 1 {
		t.Fatalf("authentications=%d, persisted=%d", authentications.Load(), persisted.Load())
	}
}

func TestAuthWaiterReturnsFailureAndReleasesMutex(t *testing.T) {
	want := errors.New("authentication failed")
	attempt := &authAttempt{done: make(chan struct{}), err: want}
	client := &YClientT{authAttempt: attempt}
	finished := make(chan error, 1)
	go func() {
		err := client.auth(context.Background())
		client.authMu.Lock()
		client.authMu.Unlock()
		client.mu.Lock()
		client.mu.Unlock()
		finished <- err
	}()
	close(attempt.done)
	select {
	case err := <-finished:
		if !errors.Is(err, want) {
			t.Fatalf("got %v, want %v", err, want)
		}
	case <-time.After(time.Second):
		t.Fatal("authentication waiter retained the client mutex")
	}
}

func TestAuthRefreshWaitsForAPIRequest(t *testing.T) {
	apiEntered := make(chan struct{})
	authEntered := make(chan struct{})
	releaseAPI := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseAPI) }) }
	defer release()
	var authentications atomic.Int32
	var client *YClientT
	client = newTestYClient(t, nil, testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/auth") {
			if client.mu.TryLock() {
				client.mu.Unlock()
				t.Error("authentication entered HTTP without holding the API mutex")
			}
			authentications.Add(1)
			close(authEntered)
			return authResponse(), nil
		}
		close(apiEntered)
		<-releaseAPI
		return authResponse(), nil
	}))

	apiFinished := make(chan error, 1)
	go func() {
		client.mu.Lock()
		defer client.mu.Unlock()
		apiFinished <- client.Client.SendRequest("/test-resource", http.MethodGet, nil, nil, nil, nil)
	}()
	select {
	case <-apiEntered:
	case <-time.After(time.Second):
		t.Fatal("API request did not start")
	}

	authFinished := make(chan error, 2)
	for range 2 {
		go func() { authFinished <- client.auth(context.Background()) }()
	}
	deadline := time.Now().Add(time.Second)
	for {
		client.authMu.Lock()
		pending := client.authAttempt != nil
		client.authMu.Unlock()
		if pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("authentication attempt did not start")
		}
		runtime.Gosched()
	}
	select {
	case <-authEntered:
		t.Error("authentication overlapped an API request")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case err := <-apiFinished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("API request did not finish")
	}
	for range 2 {
		select {
		case err := <-authFinished:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("authentication did not finish after the API request")
		}
	}
	if authentications.Load() != 1 {
		t.Fatalf("concurrent callers performed %d authentications", authentications.Load())
	}
}

func TestAuthFailureCanBeRetried(t *testing.T) {
	want := errors.New("temporary authentication failure")
	calls := 0
	client := newTestYClient(t, nil, testHTTPClient(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, want
		}
		return authResponse(), nil
	}))
	if err := client.auth(context.Background()); !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
	if err := client.auth(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAuthPersistenceFailureDoesNotDiscardValidAuthentication(t *testing.T) {
	calls := 0
	client := newTestYClient(t, func(string, time.Time) error {
		return errors.New("configuration is read-only")
	}, testHTTPClient(func(*http.Request) (*http.Response, error) {
		calls++
		return authResponse(), nil
	}))
	for range 2 {
		if err := client.auth(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("authentication retried despite valid token: %d calls", calls)
	}
}

func TestAuthRequestUsesCancelableContextAndRestoresTransport(t *testing.T) {
	started := make(chan struct{})
	client := newTestYClient(t, nil, testHTTPClient(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- client.auth(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("authentication HTTP request did not start")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled authentication, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("authentication HTTP request ignored cancellation")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if _, restored := client.Client.HTTPClient.(testHTTPClient); !restored {
		t.Fatal("authentication left its request context on the shared HTTP client")
	}
}

func TestAuthWaiterCancellationDoesNotCancelSharedAuthentication(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAuth := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAuth()
	client := newTestYClient(t, nil, testHTTPClient(func(*http.Request) (*http.Response, error) {
		close(started)
		<-release
		return authResponse(), nil
	}))
	leaderFinished := make(chan error, 1)
	go func() { leaderFinished <- client.auth(context.Background()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("shared authentication did not start")
	}
	waiterContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &observedContext{Context: waiterContext, waiting: make(chan struct{})}
	waiterFinished := make(chan error, 1)
	go func() { waiterFinished <- client.auth(ctx) }()
	select {
	case <-ctx.waiting:
	case <-time.After(time.Second):
		t.Fatal("authentication waiter did not start waiting")
	}
	cancel()
	select {
	case err := <-waiterFinished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled waiter, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("authentication waiter ignored cancellation")
	}
	releaseAuth()
	select {
	case err := <-leaderFinished:
		if err != nil {
			t.Fatalf("waiter canceled shared authentication: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shared authentication did not finish")
	}
}

func TestAuthWaiterRetriesAfterRefreshLeaderIsCanceled(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	client := newTestYClient(t, nil, testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-request.Context().Done()
			return nil, request.Context().Err()
		}
		return authResponse(), nil
	}))
	leaderContext, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leaderFinished := make(chan error, 1)
	go func() { leaderFinished <- client.auth(leaderContext) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh leader did not start")
	}
	waiterContext := &observedContext{Context: context.Background(), waiting: make(chan struct{})}
	waiterFinished := make(chan error, 1)
	go func() { waiterFinished <- client.auth(waiterContext) }()
	select {
	case <-waiterContext.waiting:
	case <-time.After(time.Second):
		t.Fatal("authentication waiter did not join the refresh")
	}
	cancelLeader()
	select {
	case err := <-leaderFinished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled leader, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh leader ignored cancellation")
	}
	select {
	case err := <-waiterFinished:
		if err != nil {
			t.Fatalf("leader cancellation failed unrelated waiter: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("authentication waiter did not retry")
	}
	if calls.Load() != 2 {
		t.Fatalf("expected canceled refresh and successful retry, got %d calls", calls.Load())
	}
}

func TestClientOperationBindsContextAcrossRequests(t *testing.T) {
	type contextKey struct{}
	var values []any
	client := newTestYClient(t, nil, testHTTPClient(func(request *http.Request) (*http.Response, error) {
		values = append(values, request.Context().Value(contextKey{}))
		return authResponse(), nil
	}))
	for _, operation := range []struct {
		name     string
		requests int
	}{{name: "first operation", requests: 2}, {name: "next operation", requests: 1}} {
		ctx := context.WithValue(context.Background(), contextKey{}, operation.name)
		err := func() error {
			release, err := client.lockClient(ctx)
			if err != nil {
				return err
			}
			defer release()
			for range operation.requests {
				if err := client.Client.SendRequest("/test-resource", http.MethodGet, nil, nil, nil, nil); err != nil {
					return err
				}
			}
			return nil
		}()
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []any{"first operation", "first operation", "next operation"}
	if len(values) != len(want) {
		t.Fatalf("got request contexts %v, want %v", values, want)
	}
	for index, value := range want {
		if values[index] != value {
			t.Fatalf("request %d: got context %v, want %v", index, values[index], value)
		}
	}
	if _, restored := client.Client.HTTPClient.(testHTTPClient); !restored {
		t.Fatal("operation left its context on the shared HTTP client")
	}
}
