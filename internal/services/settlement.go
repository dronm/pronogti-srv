package services

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/dronm/goyclients"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/settlement"
)

type SettlementProvider struct {
	service     *YClientService
	mu          sync.Mutex
	nextRequest time.Time
}

func NewSettlementProvider(service *YClientService) *SettlementProvider {
	return &SettlementProvider{service: service}
}

func (p *SettlementProvider) withClient(ctx context.Context, action func(*settlement.YClientsProvider) error) error {
	if err := p.service.yClient.auth(ctx); err != nil {
		return err
	}
	release, err := p.service.yClient.lockClient(ctx)
	if err != nil {
		return err
	}
	defer release()
	client := p.service.yClient.Client
	original := client.HTTPClient
	client.HTTPClient = settlementHTTPClient{provider: p, inner: settlementNoRedirects(original)}
	defer func() { client.HTTPClient = original }()
	return action(settlement.NewYClientsProvider(p.service.yClient.Client))
}

type settlementHTTPClient struct {
	provider *SettlementProvider
	inner    goyclients.HTTPClient
}

func settlementNoRedirects(client goyclients.HTTPClient) goyclients.HTTPClient {
	switch value := client.(type) {
	case *http.Client:
		copy := *value
		copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return &copy
	case contextHTTPClient:
		value.client = settlementNoRedirects(value.client)
		return value
	default:
		return client
	}
}

func (c settlementHTTPClient) Do(request *http.Request) (*http.Response, error) {
	c.provider.mu.Lock()
	now := time.Now()
	start := c.provider.nextRequest
	if start.Before(now) {
		start = now
	}
	c.provider.nextRequest = start.Add(250 * time.Millisecond)
	c.provider.mu.Unlock()
	if wait := time.Until(start); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-timer.C:
		}
	}
	return c.inner.Do(request)
}

func (p *SettlementProvider) Preflight(ctx context.Context, order checkout.Order, profile settlement.Profile) error {
	if profile.Mode != "live" {
		return settlement.ValidateCheckout(order, profile)
	}
	return p.withClient(ctx, func(client *settlement.YClientsProvider) error { return client.Preflight(ctx, order, profile) })
}

func (p *SettlementProvider) Plan(ctx context.Context, job settlement.Job) (result []settlement.Operation, err error) {
	err = p.withClient(ctx, func(client *settlement.YClientsProvider) error { result, err = client.Plan(ctx, job); return err })
	return
}

func (p *SettlementProvider) PrepareDispatch(ctx context.Context, job settlement.Job, operation settlement.Operation) error {
	return p.withClient(ctx, func(client *settlement.YClientsProvider) error { return client.PrepareDispatch(ctx, job, operation) })
}
func (p *SettlementProvider) Execute(ctx context.Context, job settlement.Job, operation settlement.Operation) (result settlement.Outcome, err error) {
	err = p.withClient(ctx, func(client *settlement.YClientsProvider) error {
		result, err = client.Execute(ctx, job, operation)
		return err
	})
	return
}
func (p *SettlementProvider) Reconcile(ctx context.Context, job settlement.Job, operation settlement.Operation) (result settlement.Outcome, err error) {
	err = p.withClient(ctx, func(client *settlement.YClientsProvider) error {
		result, err = client.Reconcile(ctx, job, operation)
		return err
	})
	return
}

// VerifyPaymentEvidence is an operator-only, authenticated read of the original
// sale. Callers must authorize the operator and persist the returned audit result.
func (p *SettlementProvider) VerifyPaymentEvidence(ctx context.Context, job settlement.Job, operation settlement.Operation, transactionIDs []int64, reason string) (result settlement.Outcome, err error) {
	err = p.withClient(ctx, func(client *settlement.YClientsProvider) error {
		result, err = client.VerifyPaymentEvidence(ctx, job, operation, transactionIDs, reason)
		return err
	})
	return
}

var _ settlement.Provider = (*SettlementProvider)(nil)

// SetPaidVisitLookup is configured once during startup. Verified payments must
// hide their old external debt while asynchronous YCLIENTS settlement recovers.
func (s *YClientService) SetPaidVisitLookup(lookup func(context.Context, int64, int64) ([]settlement.VisitReference, error)) {
	s.paidVisitLookup = lookup
}
