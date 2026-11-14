package payment

import (
	"context"
	"testing"
	"time"
)

func TestTerminalReadsDoNotApplyInternetPaymentTimeoutsOrExposeBankPayload(t *testing.T) {
	for _, state := range []string{"creating", "pending", "unknown", "paid"} {
		t.Run(state, func(t *testing.T) {
			_, repo, _, actor, now := qrFixture()
			expired := now.Add(-time.Hour)
			repo.attempt = Attempt{
				ID: "22222222-2222-4222-8222-222222222222", OrderID: repo.order.ID,
				Method: MethodTerminalCard, State: state, AmountKopecks: 12345, Currency: "RUB",
				CreatedAt: now.Add(-24 * time.Hour), ExpiresAt: &expired,
				Payload: "should-not-expose-as-QR-or-checkout", Image: []byte("private"), ImageMediaType: "image/png",
			}
			service := NewService(Dependencies{Repository: repo}, Options{})
			service.now = func() time.Time { return now }
			view, err := service.Get(context.Background(), actor, repo.attempt.ID)
			if err != nil {
				t.Fatal(err)
			}
			if view.State != state || repo.attempt.State != state {
				t.Fatalf("terminal state was changed by an online timer: view=%s stored=%s", view.State, repo.attempt.State)
			}
			if view.Method != MethodTerminalCard || view.Payload != "" || view.QRImageURL != "" || view.CheckoutURL != "" {
				t.Fatalf("terminal read exposed an online payment payload: %+v", view)
			}
		})
	}
}
