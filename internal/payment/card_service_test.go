package payment

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/webapp"
)

type cardRepositoryFixture struct {
	*qrRepository
	completion Completion
}

func (r *cardRepositoryFixture) ReserveCard(ctx context.Context, owner checkout.Owner, order, key, hash string, now time.Time) (Reservation, error) {
	reservation, err := r.Reserve(ctx, owner, order, key, hash, now)
	if err == nil && reservation.Created {
		r.mu.Lock()
		r.attempt.Method = MethodCard
		reservation.Attempt.Method = MethodCard
		r.mu.Unlock()
	}
	return reservation, err
}

func (r *cardRepositoryFixture) CompleteCard(ctx context.Context, id string, result CardResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completeContexts = append(r.completeContexts, ctx.Err())
	r.attempt.State, r.attempt.OperationID, r.attempt.Payload = "pending", result.OperationID, result.CheckoutURL
	r.attempt.ExpiresAt = &result.ExpiresAt
	return nil
}

func (r *cardRepositoryFixture) GetCompletion(context.Context, checkout.Owner, string) (Completion, error) {
	return r.completion, nil
}

type cardBankFixture struct {
	calls atomic.Int32
	create func(context.Context, BankRequest) (CardResult, error)
}

func (b *cardBankFixture) CreateCard(ctx context.Context, request BankRequest) (CardResult, error) {
	b.calls.Add(1)
	if b.create != nil {
		return b.create(ctx, request)
	}
	return (SDKCardBank{}).CreateCard(ctx, request)
}

func cardServiceFixture() (*Service, *cardRepositoryFixture, *cardBankFixture, checkout.Actor, time.Time) {
	s, qr, _, actor, now := qrFixture()
	qr.profile.CardsEnabled, qr.profile.CardTTLMinutes = true, 10
	repo := &cardRepositoryFixture{qrRepository: qr, completion: Completion{OrderState: "payment_pending", YClientsSyncStatus: "not_started", FiscalStatus: "not_started"}}
	bank := &cardBankFixture{}
	s.dependencies.Repository, s.dependencies.CardBank = repo, bank
	return s, repo, bank, actor, now
}

func TestCardServicePersistsAndReusesHostedCheckout(t *testing.T) {
	s, repo, bank, actor, _ := cardServiceFixture()
	bank.create = func(ctx context.Context, request BankRequest) (CardResult, error) {
		if request.AmountKopecks != 12345 || request.OrderID != repo.order.ID || request.TTLMinutes != 10 || request.AttemptID == "" {
			t.Errorf("wrong authoritative card request: %+v", request)
		}
		result, err := (SDKCardBank{}).CreateCard(ctx, request)
		// A registration response alone cannot mark an order paid.
		result.Status = "APPROVED"
		return result, err
	}
	view, err := s.InitCard(context.Background(), actor, "card-key", repo.order.ID)
	if err != nil || view.State != "pending" || view.Method != MethodCard || view.CheckoutURL == "" || view.Payload != "" || view.QRImageURL != "" {
		t.Fatalf("card view=%+v error=%v", view, err)
	}
	for _, key := range []string{"card-key", "replacement-key"} {
		replayed, err := s.InitCard(context.Background(), actor, key, repo.order.ID)
		if err != nil || replayed.PaymentID != view.PaymentID || replayed.CheckoutURL != view.CheckoutURL || bank.calls.Load() != 1 {
			t.Fatalf("replay=%+v error=%v calls=%d", replayed, err, bank.calls.Load())
		}
	}
}

func TestCardServiceNeverRepeatsUnknownRegistration(t *testing.T) {
	s, repo, bank, actor, _ := cardServiceFixture()
	bank.create = func(context.Context, BankRequest) (CardResult, error) { return CardResult{}, errors.New("unknown transport outcome") }
	view, err := s.InitCard(context.Background(), actor, "card-key", repo.order.ID)
	if err != nil || view.State != "unknown" || view.CheckoutURL != "" {
		t.Fatalf("view=%+v error=%v", view, err)
	}
	_, err = s.InitCard(context.Background(), actor, "another-key", repo.order.ID)
	if err != nil || bank.calls.Load() != 1 {
		t.Fatalf("unknown registration repeated: calls=%d error=%v", bank.calls.Load(), err)
	}
}

func TestCardServiceBlocksChangedCheckoutOrForeignSessionBeforeBank(t *testing.T) {
	s, repo, bank, actor, _ := cardServiceFixture()
	other := actor
	other.CustomerSessionID = "someone-else"
	_, err := s.InitCard(context.Background(), other, "card-key", repo.order.ID)
	qrRequireStatus(t, err, 404)
	s.dependencies.ValidatePayable = func(context.Context, checkout.Order) error { return webapp.Conflict("visit changed", nil) }
	_, err = s.InitCard(context.Background(), actor, "card-key", repo.order.ID)
	qrRequireStatus(t, err, 409)
	if bank.calls.Load() != 0 || repo.attempt.State != "failed" {
		t.Fatal("changed or foreign checkout reached bank")
	}
}

func TestCardGetPaidCompletionNeverExposesBankLinkAgain(t *testing.T) {
	s, repo, _, actor, now := cardServiceFixture()
	view, err := s.InitCard(context.Background(), actor, "card-key", repo.order.ID)
	if err != nil {
		t.Fatal(err)
	}
	repo.attempt.State = "paid"
	repo.completion = Completion{OrderState: "paid", YClientsSyncStatus: "succeeded", FiscalStatus: "issued", PaidAt: &now, BankReference: "approved-operation"}
	view, err = s.Get(context.Background(), actor, view.PaymentID)
	if err != nil || view.Method != MethodCard || view.CheckoutURL != "" || view.QRImageURL != "" || view.YClientsSyncStatus != "succeeded" || view.FiscalStatus != "issued" || view.PaidAt == nil {
		t.Fatalf("completion=%+v error=%v", view, err)
	}
}

func TestCardServiceRejectsAnotherActivePaymentMethod(t *testing.T) {
	s, repo, bank, actor, _ := cardServiceFixture()
	_, err := s.Init(context.Background(), actor, "qr-key", repo.order.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.InitCard(context.Background(), actor, "card-key", repo.order.ID)
	qrRequireStatus(t, err, 409)
	if bank.calls.Load() != 0 {
		t.Fatal("another active payment method dispatched card request")
	}
}
