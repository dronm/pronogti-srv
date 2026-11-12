package refund

import (
	"context"
	"errors"
	"testing"

	"github.com/dronm/pronogti/internal/payment"
)

type statusBank struct {
	Bank
	checks      int
	observation BankObservation
	err         error
}

func (b *statusBank) Check(context.Context, BankPayment, int64, []string, string) (BankObservation, error) {
	b.checks++
	return b.observation, b.err
}

func TestCardHistoryRequiresRecordedAcknowledgement(t *testing.T) {
	bank := &statusBank{observation: BankObservation{State: "confirmed", ExternalID: "bank-refund"}}
	service := NewService(nil, bank, nil, true)
	job := Job{
		Step:   "bank_check",
		Sale:   Sale{Bank: BankPayment{Method: payment.MethodCard, Profile: payment.Profile{Environment: "production"}}},
		Refund: Refund{BankState: "submitting"},
	}
	result := service.process(context.Background(), job)
	if bank.checks != 0 || result.State != "unknown" || result.RetryAfter != 0 {
		t.Fatalf("ambiguous card refund was eligible for reconciliation: %+v", result)
	}

	job.Refund.BankState = "pending"
	bank.err = errors.New("status request interrupted")
	result = service.process(context.Background(), job)
	if bank.checks != 1 || result.State != "pending" || result.RetryAfter <= 0 {
		t.Fatalf("temporary status failure lost the recorded acknowledgement: %+v", result)
	}

	bank.err = nil
	result = service.process(context.Background(), job)
	if bank.checks != 2 || result.State != "confirmed" || result.Reference != "bank-refund" {
		t.Fatalf("acknowledged refund did not reconcile: %+v", result)
	}
}

func TestSBPRecoveryRequiresBankRequestID(t *testing.T) {
	unknown := BankObservation{State: "unknown"}
	if outcome := bankOutcome(payment.MethodSBP, unknown, nil); outcome.RetryAfter != 0 {
		t.Fatalf("uncorrelated SBP refund was scheduled for polling: %+v", outcome)
	}
	unknown.ExternalID = "sbp-request-id"
	if outcome := bankOutcome(payment.MethodSBP, unknown, nil); outcome.RetryAfter <= 0 {
		t.Fatalf("identified SBP refund was not scheduled for polling: %+v", outcome)
	}
}
