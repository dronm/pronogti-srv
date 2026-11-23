package correction

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func eligibleSource() Source {
	source := payloadSource()
	source.OrderID = "order"
	source.PaymentID = "payment"
	source.CompanyID = 1
	source.Environment = "production"
	source.Profile.Mode = "live"
	source.RawPayload, _ = json.Marshal(source.Payload)
	return source
}

func TestServicePreviewAndCreateValidateEligibilityAndSourceVersion(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Source, *CreateRequest)
	}{
		{"stale source", func(_ *Source, r *CreateRequest) { r.SourceVersion = "old" }},
		{"refund exists", func(s *Source, _ *CreateRequest) { s.HasRefunds = true }},
		{"correction unfinished", func(s *Source, _ *CreateRequest) { s.HasOpenCorrection = true }},
		{"unknown receipt", func(s *Source, _ *CreateRequest) { s.State = "unknown" }},
		{"missing source", func(s *Source, _ *CreateRequest) { s.RawPayload = nil }},
		{"invalid payment", func(_ *Source, r *CreateRequest) { r.Receipt.Payment.CashlessKopecks++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := eligibleSource()
			request := payloadRequest(source)
			test.change(&source, &request)
			repo := &correctionFixture{source: source}
			svc := NewService(repo, nil, Options{AllowLive: true, Location: time.UTC})
			svc.now = func() time.Time { return payloadNow }
			if _, err := svc.Preview(context.Background(), source.OrderID, request); err == nil {
				t.Fatal("invalid preview accepted")
			}
			if _, err := svc.Create(context.Background(), source.OrderID, 1, "request-key", request); err == nil || repo.reserved {
				t.Fatal("invalid correction reserved")
			}
		})
	}
}

func TestServiceSuccessfulPreviewAndCreateUseSamePlan(t *testing.T) {
	source := eligibleSource()
	request := payloadRequest(source)
	repo := &correctionFixture{source: source}
	svc := NewService(repo, nil, Options{AllowLive: true, Location: time.UTC})
	svc.now = func() time.Time { return payloadNow }
	preview, err := svc.Preview(context.Background(), source.OrderID, request)
	if err != nil || preview.Reversal.CalculationSign != 2 || preview.Replacement.CalculationSign != 1 {
		t.Fatalf("unexpected preview=%+v err=%v", preview, err)
	}
	created, err := svc.Create(context.Background(), source.OrderID, 1, "request-key", request)
	if err != nil || !repo.reserved || created.ID != "reserved" {
		t.Fatalf("create=%+v err=%v", created, err)
	}
}

func TestServiceQuoteLiveDisabledHasReasonAndNoDispatch(t *testing.T) {
	repo := &correctionFixture{source: eligibleSource()}
	quote, err := NewService(repo, nil, Options{}).Quote(context.Background(), "order")
	if err != nil || quote.Eligible || quote.Reason == "" {
		t.Fatalf("quote=%+v err=%v", quote, err)
	}
}
