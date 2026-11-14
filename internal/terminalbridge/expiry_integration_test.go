package terminalbridge

import (
	"context"
	"testing"
	"time"
)

func TestTerminalIntegrationOfflineExpiryReleasesOnlyNeverDeliveredPayment(t *testing.T) {
	f := bridgeIntegrationSetup(t, false)
	ctx := context.Background()
	repository := NewRepository(f.provider, Options{Enabled: true, AllowSimulated: true, CommandTTL: time.Millisecond, HeartbeatMaxAge: time.Minute})
	order := bridgeIntegrationOrder(t, f.provider, f.owner, "offline-expiry")
	paymentID, err := repository.Initiate(ctx, f.owner, order.ID, "offline-expiry-key")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	changed, err := repository.ExpirePayment(ctx, paymentID)
	if err != nil || !changed {
		t.Fatalf("offline expiry changed=%v err=%v", changed, err)
	}
	changed, err = repository.ExpirePayment(ctx, paymentID)
	if err != nil || changed {
		t.Fatalf("expiry was not idempotent: %v %v", changed, err)
	}
	state, receipts, jobs := bridgeIntegrationCounts(t, f.conn, order.ID)
	if state != "prepared" || receipts != 0 || jobs != 0 {
		t.Fatalf("expiry affected financial work: %s %d %d", state, receipts, jobs)
	}
	details, err := repository.PaymentDetails(ctx, paymentID)
	if err != nil || details.State != "expired" {
		t.Fatalf("expiry details=%+v err=%v", details, err)
	}

	repository = NewRepository(f.provider, Options{Enabled: true, AllowSimulated: true, CommandTTL: time.Second, HeartbeatMaxAge: time.Minute})
	second := bridgeIntegrationOrder(t, f.provider, f.owner, "claimed-expiry")
	id, err := repository.Initiate(ctx, f.owner, second.ID, "claimed-expiry-key")
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := repository.Claim(ctx, f.agent, ClaimRequest{TerminalIDs: []string{f.terminal.TerminalID}})
	if err != nil || delivery == nil || delivery.Command == nil {
		t.Fatalf("claim failed: %+v %v", delivery, err)
	}
	time.Sleep(1100 * time.Millisecond)
	changed, err = repository.ExpirePayment(ctx, id)
	if err != nil || changed {
		t.Fatalf("expiry released a potentially delivered payment: %v %v", changed, err)
	}
	state, receipts, jobs = bridgeIntegrationCounts(t, f.conn, second.ID)
	if state != "payment_pending" || receipts != 0 || jobs != 0 {
		t.Fatalf("claimed payment lost its hold: %s %d %d", state, receipts, jobs)
	}
}
