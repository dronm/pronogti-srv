package checkout

import (
	"context"
	"testing"

	"github.com/dronm/webapp"
)

func TestFiscalValidationAppliesToPreparationReplayAndRead(t *testing.T) {
	s, _, _, actor, request, _ := checkoutFixture()
	calls := 0
	s.options.ValidateOrder = func(_ context.Context, order Order) error {
		calls++
		if order.ID == "" || order.PayableKopecks != 498 {
			t.Fatal("validator did not receive the stored authoritative quote")
		}
		return nil
	}
	order, err := s.Prepare(context.Background(), actor, "fiscal-check", request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Prepare(context.Background(), actor, "fiscal-check", request); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(context.Background(), actor, order.ID); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("validation calls=%d", calls)
	}
	s.options.ValidateOrder = func(context.Context, Order) error { return webapp.Conflict("unsupported receipt", nil) }
	if _, err = s.Prepare(context.Background(), actor, "fiscal-check", request); err == nil {
		t.Fatal("replay bypassed fiscal validation")
	}
	if _, err = s.Get(context.Background(), actor, order.ID); err == nil {
		t.Fatal("read bypassed fiscal validation")
	}
}

func TestFiscalValidationRunsAfterOwnershipAndRejectsStaleSession(t *testing.T) {
	s, _, _, actor, request, _ := checkoutFixture()
	order, err := s.Prepare(context.Background(), actor, "fiscal-owner", request)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.options.ValidateOrder = func(context.Context, Order) error { calls++; return nil }
	other := actor
	other.CustomerSessionID = "different-customer"
	_, err = s.Get(context.Background(), other, order.ID)
	requireStatus(t, err, 404)
	if calls != 0 {
		t.Fatal("fiscal data was read before ownership validation")
	}
	active := true
	s.options.ValidateActor = func(context.Context, Actor) error {
		if !active {
			return webapp.Unauthorized("expired customer", nil)
		}
		return nil
	}
	s.options.ValidateOrder = func(context.Context, Order) error { active = false; return nil }
	_, err = s.Get(context.Background(), actor, order.ID)
	requireStatus(t, err, 401)
}
