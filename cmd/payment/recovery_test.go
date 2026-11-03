package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/settlement"
)

type evidenceRepository struct {
	job         settlement.Job
	loads       int
	resolutions int
	orderID     string
	operationID string
	outcome     settlement.Outcome
}

func (r *evidenceRepository) LoadJob(context.Context, string) (settlement.Job, error) {
	r.loads++
	return r.job, nil
}

func (r *evidenceRepository) ResolveOperation(_ context.Context, orderID, operationID string, outcome settlement.Outcome) error {
	r.resolutions++
	r.orderID, r.operationID, r.outcome = orderID, operationID, outcome
	return nil
}

type evidenceReader struct {
	calls   int
	err     error
	outcome settlement.Outcome
	ids     []int64
	reason  string
}

func (v *evidenceReader) VerifyPaymentEvidence(_ context.Context, _ settlement.Job, _ settlement.Operation, ids []int64, reason string) (settlement.Outcome, error) {
	v.calls++
	v.ids, v.reason = ids, reason
	return v.outcome, v.err
}

func evidenceFixture() (paymentStatus, *evidenceRepository, *evidenceReader, settlementResolution) {
	status := paymentStatus{PaymentID: "payment", OrderID: "order", State: "paid", OrderState: "paid", Environment: "production"}
	repo := &evidenceRepository{job: settlement.Job{PaymentID: status.PaymentID, Order: checkout.Order{ID: status.OrderID}, Operations: []settlement.Operation{{ID: "visit-payment", Kind: settlement.KindVisitPayment, State: "unknown"}}}}
	reader := &evidenceReader{outcome: settlement.Outcome{State: "succeeded", Result: json.RawMessage(`{"operator_evidence":{"reason":"reviewed"},"transaction_ids":[42,43]}`)}}
	resolution := settlementResolution{operationID: "visit-payment", transactionIDs: []int64{42, 43}, reason: "reviewed by salon staff"}
	return status, repo, reader, resolution
}

func TestSettlementEvidenceRequiresTheSelectedProductionPaymentAndOperation(t *testing.T) {
	for _, change := range []func(*paymentStatus, *evidenceRepository, *settlementResolution){
		func(s *paymentStatus, _ *evidenceRepository, _ *settlementResolution) { s.Environment = "mock" },
		func(s *paymentStatus, _ *evidenceRepository, _ *settlementResolution) { s.State = "pending" },
		func(s *paymentStatus, _ *evidenceRepository, _ *settlementResolution) { s.OrderState = "prepared" },
		func(_ *paymentStatus, r *evidenceRepository, _ *settlementResolution) {
			r.job.PaymentID = "other-payment"
		},
		func(_ *paymentStatus, r *evidenceRepository, _ *settlementResolution) { r.job.Order.ID = "other-order" },
		func(_ *paymentStatus, _ *evidenceRepository, r *settlementResolution) {
			r.operationID = "other-operation"
		},
		func(_ *paymentStatus, r *evidenceRepository, _ *settlementResolution) {
			r.job.Operations[0].State = "pending"
		},
		func(_ *paymentStatus, r *evidenceRepository, _ *settlementResolution) {
			r.job.Operations[0].Kind = settlement.KindServiceIncome
		},
	} {
		status, repo, reader, resolution := evidenceFixture()
		change(&status, repo, &resolution)
		if err := resolveSettlement(context.Background(), repo, reader, status, resolution); err == nil {
			t.Fatal("unrelated or unconfirmed payment evidence was accepted")
		}
		if reader.calls != 0 || repo.resolutions != 0 {
			t.Fatal("ineligible evidence reached external verification or changed local state")
		}
	}
}

func TestSettlementEvidenceFailureNeverMarksTheOperationSuccessful(t *testing.T) {
	status, repo, reader, resolution := evidenceFixture()
	reader.err = errors.New("selected transactions do not total the original payment")
	if err := resolveSettlement(context.Background(), repo, reader, status, resolution); err == nil {
		t.Fatal("invalid evidence was accepted")
	}
	if reader.calls != 1 || repo.resolutions != 0 {
		t.Fatal("failed external evidence changed local settlement state")
	}
}

func TestSettlementEvidencePersistsOnlyTheVerifiedOutcomeAndExplicitAttribution(t *testing.T) {
	status, repo, reader, resolution := evidenceFixture()
	if err := resolveSettlement(context.Background(), repo, reader, status, resolution); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 || !reflect.DeepEqual(reader.ids, resolution.transactionIDs) || reader.reason != resolution.reason {
		t.Fatal("operator attribution was altered before verification")
	}
	if repo.resolutions != 1 || repo.orderID != status.OrderID || repo.operationID != resolution.operationID || !reflect.DeepEqual(repo.outcome, reader.outcome) {
		t.Fatal("local settlement result did not preserve the verified evidence")
	}
}

func TestSettlementEvidenceArgumentsAreValidatedBeforeDatabaseOrAuthentication(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	for _, args := range [][]string{
		{"resolve-settlement", id, "visit-payment", "", "review"},
		{"resolve-settlement", id, "visit-payment", "1,1", "review"},
		{"resolve-settlement", id, "visit-payment", "1,-2", "review"},
		{"resolve-settlement", id, "visit-payment", "9223372036854775808", "review"},
		{"resolve-settlement", id, "visit-payment", "1", " "},
		{"resolve-settlement", id, "visit-payment", "1", strings.Repeat("a", 4001)},
		{"resolve-settlement", id, "other-operation", "1", "review"},
		{"resolve-settlement", id, "visit-payment", "1", "review", "extra"},
	} {
		if _, err := parsePaymentCommand(args); err == nil {
			t.Fatalf("invalid evidence arguments accepted: %q", args)
		}
	}
	command, err := parsePaymentCommand([]string{"resolve-settlement", id, "goods-payment", "42, 43", "  attributed after review  "})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(command.resolution.transactionIDs, []int64{42, 43}) || command.resolution.reason != "attributed after review" {
		t.Fatal("explicit evidence was not parsed correctly")
	}
}
