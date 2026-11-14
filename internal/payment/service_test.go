package payment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/webapp"
)

type qrRepository struct {
	mu               sync.Mutex
	order            checkout.Order
	profile          Profile
	attempt          Attempt
	reserveCalls     int
	completeContexts []error
	completeBudgets  []time.Duration
	failContexts     []error
	failErr          error
	completeErr      error
}

func (r *qrRepository) GetOrder(context.Context, string) (checkout.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.order, nil
}
func (r *qrRepository) Reserve(_ context.Context, owner checkout.Owner, orderID, key, hash string, now time.Time) (Reservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reserveCalls++
	if !owns(owner, r.order.Owner) || orderID != r.order.ID {
		return Reservation{}, webapp.NotFound("checkout not found", nil)
	}
	if r.attempt.ID != "" {
		return Reservation{Attempt: r.attempt, Profile: r.profile}, nil
	}
	if r.order.State != "prepared" || !r.order.ExpiresAt.After(now) {
		return Reservation{}, webapp.Conflict("checkout is not payable", nil)
	}
	expires := r.order.ExpiresAt
	profile, _ := json.Marshal(r.profile)
	r.attempt = Attempt{ID: "22222222-2222-4222-8222-222222222222", OrderID: r.order.ID, CompanyID: owner.CompanyID,
		PaymentProfileID: r.profile.ID, Environment: r.profile.Environment, State: "creating", IdempotencyKey: key, RequestHash: hash,
		AmountKopecks: r.order.PayableKopecks, Currency: "RUB", ExpiresAt: &expires, CreatedAt: now, UpdatedAt: now, ProfileSnapshot: profile}
	r.order.State = "payment_pending"
	return Reservation{Attempt: r.attempt, Profile: r.profile, Created: true}, nil
}
func (r *qrRepository) Get(_ context.Context, owner checkout.Owner, id string) (Attempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !owns(owner, r.order.Owner) || id != r.attempt.ID {
		return Attempt{}, webapp.NotFound("payment not found", nil)
	}
	return r.attempt, nil
}
func (r *qrRepository) Complete(ctx context.Context, id string, result QRResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completeContexts = append(r.completeContexts, ctx.Err())
	if deadline, ok := ctx.Deadline(); ok {
		r.completeBudgets = append(r.completeBudgets, time.Until(deadline))
	} else {
		r.completeBudgets = append(r.completeBudgets, -1)
	}
	if r.completeErr != nil {
		return r.completeErr
	}
	r.attempt.State, r.attempt.QRCID, r.attempt.Payload = "pending", result.QRCID, result.Payload
	r.attempt.Image, r.attempt.ImageMediaType = append([]byte(nil), result.Image...), result.ImageMediaType
	r.attempt.ExpiresAt = &result.ExpiresAt
	return nil
}
func (r *qrRepository) Fail(ctx context.Context, id string, unknown bool, code string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failContexts = append(r.failContexts, ctx.Err())
	if r.failErr != nil {
		return r.failErr
	}
	r.attempt.ErrorCode = code
	if unknown {
		r.attempt.State = "unknown"
	} else {
		r.attempt.State = "failed"
		r.order.State = "prepared"
	}
	return nil
}
func (r *qrRepository) UpdateExpired(_ context.Context, id string, now time.Time) (Attempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempt.State = "expired"
	return r.attempt, nil
}
func (r *qrRepository) AbandonStale(_ context.Context, id string, now time.Time) (Attempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempt.State = "unknown"
	r.attempt.ErrorCode = "creation_outcome_unknown"
	return r.attempt, nil
}

type qrBank struct {
	calls  atomic.Int32
	create func(context.Context, BankRequest) (QRResult, error)
}

func (b *qrBank) Create(ctx context.Context, request BankRequest) (QRResult, error) {
	b.calls.Add(1)
	if b.create != nil {
		return b.create(ctx, request)
	}
	return qrResult(request), nil
}
func qrResult(request BankRequest) QRResult {
	request.Profile.Environment = "mock"
	result, err := (SDKBank{}).Create(context.Background(), request)
	if err != nil {
		panic(err)
	}
	return result
}

type qrTokens struct {
	calls int
	err   error
}

func (t *qrTokens) Token(context.Context, Profile) (string, error) {
	t.calls++
	return "phony.jwt.token", t.err
}

func qrFixture() (*Service, *qrRepository, *qrBank, checkout.Actor, time.Time) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	actor := checkout.Actor{CompanyID: 781898, Mode: "public", ClientID: 17, ScopeID: "salon-scope", CustomerSessionID: "customer-scope"}
	repo := &qrRepository{order: checkout.Order{ID: "11111111-1111-4111-8111-111111111111", Owner: ownerFor(actor), State: "prepared", PayableKopecks: 12345, SubtotalKopecks: 12345, Currency: "RUB", ExpiresAt: now.Add(15 * time.Minute)}, profile: Profile{ID: 9, CompanyID: 781898, Environment: "mock", Enabled: true, QRTTLMinutes: 15, HTTPTimeoutSeconds: 20}}
	bank := &qrBank{}
	service := NewService(Dependencies{Repository: repo, Bank: bank}, Options{})
	service.now = func() time.Time { return now }
	return service, repo, bank, actor, now
}

func qrRequireStatus(t *testing.T, err error, status int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected HTTP %d", status)
	}
	w := httptest.NewRecorder()
	webapp.WriteServiceError(w, err)
	if w.Code != status {
		t.Fatalf("status=%d error=%v", w.Code, err)
	}
}

func TestQRServiceConcurrentInitElectsOneBankCall(t *testing.T) {
	s, repo, bank, actor, _ := qrFixture()
	started, release := make(chan struct{}), make(chan struct{})
	bank.create = func(ctx context.Context, request BankRequest) (QRResult, error) {
		close(started)
		<-release
		return qrResult(request), nil
	}
	first := make(chan error, 1)
	go func() { _, err := s.Init(context.Background(), actor, "key-a", repo.order.ID); first <- err }()
	<-started
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			key := "key-a"
			if i%2 != 0 {
				key = "key-b"
			}
			view, err := s.Init(context.Background(), actor, key, "11111111-1111-4111-8111-111111111111")
			if err != nil || view.State != "creating" || view.Payload != "" {
				t.Errorf("concurrent response=%+v err=%v", view, err)
			}
		}(i)
	}
	group.Wait()
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if bank.calls.Load() != 1 {
		t.Fatalf("bank calls=%d", bank.calls.Load())
	}
	for _, key := range []string{"key-a", "new-key"} {
		view, err := s.Init(context.Background(), actor, key, "11111111-1111-4111-8111-111111111111")
		if err != nil || view.State != "pending" || view.Payload == "" {
			t.Fatalf("replay=%+v err=%v", view, err)
		}
	}
	if bank.calls.Load() != 1 {
		t.Fatal("replay created another QR")
	}
}

func TestQRServiceUsesReservedAmountAndDoesNotExposeCredentials(t *testing.T) {
	s, repo, bank, actor, _ := qrFixture()
	bank.create = func(ctx context.Context, request BankRequest) (QRResult, error) {
		if request.AmountKopecks != 12345 || request.AttemptID == "" || request.Purpose != "Оплата "+repo.order.ID || request.TTLMinutes != 15 {
			t.Fatalf("bank request=%+v", request)
		}
		return qrResult(request), nil
	}
	view, err := s.Init(context.Background(), actor, "key-a", repo.order.ID)
	if err != nil || view.AmountKopecks != 12345 || view.Currency != "RUB" || view.State != "pending" || !strings.HasPrefix(view.QRImageURL, "data:image/png;base64,") {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "credential") || strings.Contains(string(encoded), "merchant") || strings.Contains(string(encoded), "token") {
		t.Fatalf("internal configuration exposed: %s", encoded)
	}
}

func TestQRServiceRejectsForeignOwnershipBeforeBank(t *testing.T) {
	for _, change := range []func(*checkout.Actor){func(a *checkout.Actor) { a.CompanyID++ }, func(a *checkout.Actor) { a.CustomerSessionID = "other" }, func(a *checkout.Actor) { a.ScopeID = "other" }, func(a *checkout.Actor) { a.ClientID++ }} {
		s, repo, bank, actor, _ := qrFixture()
		other := actor
		change(&other)
		_, err := s.Init(context.Background(), other, "key", repo.order.ID)
		qrRequireStatus(t, err, 404)
		if bank.calls.Load() != 0 || repo.reserveCalls != 0 {
			t.Fatal("foreign checkout reached reservation/bank")
		}
		view, err := s.Init(context.Background(), actor, "key", repo.order.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Get(context.Background(), other, view.PaymentID)
		qrRequireStatus(t, err, 404)
	}
}

func TestQRServicePreBankFailuresDoNotDispatch(t *testing.T) {
	for _, stage := range []string{"credentials", "payable", "session"} {
		t.Run(stage, func(t *testing.T) {
			s, repo, bank, actor, _ := qrFixture()
			switch stage {
			case "credentials":
				repo.profile.Environment = "sandbox"
				s.dependencies.Tokens = &qrTokens{err: errors.New("secret token configuration")}
			case "payable":
				s.dependencies.ValidatePayable = func(context.Context, checkout.Order) error { return webapp.Conflict("visit changed", nil) }
			case "session":
				active := true
				s.options.ValidateActor = func(context.Context, checkout.Actor) error {
					if !active {
						return webapp.Unauthorized("customer expired", nil)
					}
					return nil
				}
				s.dependencies.ValidatePayable = func(context.Context, checkout.Order) error { active = false; return nil }
			}
			_, err := s.Init(context.Background(), actor, "key", repo.order.ID)
			if err == nil || bank.calls.Load() != 0 || repo.attempt.State != "failed" {
				t.Fatalf("calls=%d state=%s error=%v", bank.calls.Load(), repo.attempt.State, err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("credential failure exposed secret")
			}
		})
	}
}

func TestQRServiceChangedSessionAfterBankDoesNotLeakSavedQR(t *testing.T) {
	s, repo, bank, actor, _ := qrFixture()
	active := true
	s.options.ValidateActor = func(context.Context, checkout.Actor) error {
		if !active {
			return webapp.Unauthorized("customer expired", nil)
		}
		return nil
	}
	bank.create = func(ctx context.Context, request BankRequest) (QRResult, error) {
		active = false
		return qrResult(request), nil
	}
	view, err := s.Init(context.Background(), actor, "key", repo.order.ID)
	qrRequireStatus(t, err, 401)
	if view.PaymentID != "" || repo.attempt.State != "pending" || bank.calls.Load() != 1 {
		t.Fatalf("view=%+v state=%s calls=%d", view, repo.attempt.State, bank.calls.Load())
	}
}

func TestQRServiceUnknownAndMalformedResponseBlockResubmission(t *testing.T) {
	for _, kind := range []string{"unknown", "untyped error", "invalid response", "definite rejection"} {
		t.Run(kind, func(t *testing.T) {
			s, repo, bank, actor, _ := qrFixture()
			bank.create = func(ctx context.Context, request BankRequest) (QRResult, error) {
				switch kind {
				case "unknown":
					return QRResult{}, &BankError{Code: "bank_outcome_unknown", Unknown: true}
				case "untyped error":
					return QRResult{}, errors.New("transport secret")
				case "invalid response":
					return QRResult{}, nil
				default:
					return QRResult{}, &BankError{Code: "bank_rejected"}
				}
			}
			view, err := s.Init(context.Background(), actor, "key", repo.order.ID)
			want := "unknown"
			if kind == "definite rejection" {
				want = "failed"
			}
			if err != nil || view.State != want || view.Payload != "" || view.QRImageURL != "" {
				t.Fatalf("view=%+v error=%v", view, err)
			}
			if want == "unknown" {
				view, err = s.Init(context.Background(), actor, "different-key", repo.order.ID)
				if err != nil || view.State != "unknown" || bank.calls.Load() != 1 {
					t.Fatalf("retry=%+v calls=%d err=%v", view, bank.calls.Load(), err)
				}
			}
		})
	}
}

func TestQRServicePersistsAfterBrowserCancellation(t *testing.T) {
	s, repo, bank, actor, _ := qrFixture()
	ctx, cancel := context.WithCancel(context.Background())
	bank.create = func(bankCtx context.Context, request BankRequest) (QRResult, error) {
		cancel()
		if bankCtx.Err() != nil {
			t.Fatal("browser cancellation interrupted authorized bank operation")
		}
		if _, ok := bankCtx.Deadline(); !ok {
			t.Fatal("detached bank request has no timeout")
		}
		return qrResult(request), nil
	}
	view, err := s.Init(ctx, actor, "key", repo.order.ID)
	if !errors.Is(err, context.Canceled) || view.PaymentID != "" || repo.attempt.State != "pending" || len(repo.completeContexts) != 1 || repo.completeContexts[0] != nil {
		t.Fatalf("view=%+v state=%s contexts=%v err=%v", view, repo.attempt.State, repo.completeContexts, err)
	}
	if len(repo.completeBudgets) != 1 || repo.completeBudgets[0] <= 0 || repo.completeBudgets[0] > 5*time.Second {
		t.Fatalf("persistence timeout=%v", repo.completeBudgets)
	}
	view, err = s.Init(context.Background(), actor, "key", repo.order.ID)
	if err != nil || view.State != "pending" || bank.calls.Load() != 1 {
		t.Fatalf("reload=%+v calls=%d err=%v", view, bank.calls.Load(), err)
	}
}

func TestQRServiceOneMinuteProfileUsesCheckoutCapThenRealTTL(t *testing.T) {
	s, repo, bank, actor, now := qrFixture()
	repo.profile.QRTTLMinutes = 1
	bank.create = func(ctx context.Context, request BankRequest) (QRResult, error) {
		if request.TTLMinutes != 1 {
			t.Fatalf("profile TTL ignored: %d", request.TTLMinutes)
		}
		return qrResult(request), nil
	}
	view, err := s.Init(context.Background(), actor, "key", repo.order.ID)
	if err != nil || view.State != "pending" || view.ExpiresAt == nil || !view.ExpiresAt.Equal(now.Add(time.Minute)) || bank.calls.Load() != 1 {
		t.Fatalf("one-minute QR=%+v err=%v", view, err)
	}
}

func TestQRServiceLostPersistenceAfterBankSuccessCannotResubmit(t *testing.T) {
	s, repo, bank, actor, now := qrFixture()
	repo.completeErr = errors.New("database connection lost")
	_, err := s.Init(context.Background(), actor, "key", repo.order.ID)
	if err == nil || repo.attempt.State != "creating" || bank.calls.Load() != 1 {
		t.Fatalf("state=%s calls=%d err=%v", repo.attempt.State, bank.calls.Load(), err)
	}
	repo.completeErr = nil
	view, err := s.Init(context.Background(), actor, "another-key", repo.order.ID)
	if err != nil || view.State != "creating" || bank.calls.Load() != 1 {
		t.Fatalf("replay=%+v calls=%d err=%v", view, bank.calls.Load(), err)
	}
	s.now = func() time.Time { return now.Add(11 * time.Minute) }
	view, err = s.Get(context.Background(), actor, view.PaymentID)
	if err != nil || view.State != "unknown" || bank.calls.Load() != 1 {
		t.Fatalf("recovery=%+v calls=%d err=%v", view, bank.calls.Load(), err)
	}
}

func TestQRServicePersistsUncertaintyAfterBrowserCancellation(t *testing.T) {
	s, repo, bank, actor, _ := qrFixture()
	ctx, cancel := context.WithCancel(context.Background())
	bank.create = func(bankCtx context.Context, request BankRequest) (QRResult, error) {
		cancel()
		return QRResult{}, &BankError{Code: "bank_outcome_unknown", Unknown: true}
	}
	_, err := s.Init(ctx, actor, "key", repo.order.ID)
	if !errors.Is(err, context.Canceled) || repo.attempt.State != "unknown" || len(repo.failContexts) != 1 || repo.failContexts[0] != nil {
		t.Fatalf("state=%s contexts=%v err=%v", repo.attempt.State, repo.failContexts, err)
	}
	view, err := s.Init(context.Background(), actor, "another-key", repo.order.ID)
	if err != nil || view.State != "unknown" || bank.calls.Load() != 1 {
		t.Fatalf("replay=%+v calls=%d err=%v", view, bank.calls.Load(), err)
	}
}

func TestQRServiceStaleCreationRemainsUnknownWithoutBankCall(t *testing.T) {
	s, repo, bank, actor, now := qrFixture()
	_, err := repo.Reserve(context.Background(), ownerFor(actor), repo.order.ID, "key", "hash", now.Add(-10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.Get(context.Background(), actor, repo.attempt.ID)
	if err != nil || view.State != "unknown" || view.Payload != "" || bank.calls.Load() != 0 {
		t.Fatalf("stale=%+v calls=%d err=%v", view, bank.calls.Load(), err)
	}
	_, err = s.Init(context.Background(), actor, "different-key", repo.order.ID)
	if err != nil || bank.calls.Load() != 0 {
		t.Fatal("stale creation was submitted again")
	}
}

func TestQRServiceExpiredAttemptHidesQRWithoutClaimingPayment(t *testing.T) {
	s, repo, _, actor, now := qrFixture()
	view, err := s.Init(context.Background(), actor, "key", repo.order.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now.Add(16 * time.Minute) }
	view, err = s.Get(context.Background(), actor, view.PaymentID)
	if err != nil || view.State != "expired" || view.Payload != "" || view.QRImageURL != "" || repo.order.State == "paid" {
		t.Fatalf("expired=%+v order=%s err=%v", view, repo.order.State, err)
	}
}
