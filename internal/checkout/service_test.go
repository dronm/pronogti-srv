package checkout

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/dronm/webapp"
)

type sourceFixture struct {
	mu                       sync.Mutex
	visit                    Visit
	catalog                  []CatalogItem
	visitCalls, catalogCalls int
	companyID, clientID      int64
	afterRead                func()
}

func (p *sourceFixture) LoadVisit(_ context.Context, companyID, clientID int64) (Visit, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.visitCalls++
	p.companyID, p.clientID = companyID, clientID
	return p.visit, nil
}

func (p *sourceFixture) LoadCatalog(_ context.Context, companyID int64, _ []Selection) ([]CatalogItem, error) {
	p.mu.Lock()
	p.catalogCalls++
	p.companyID = companyID
	result := append([]CatalogItem(nil), p.catalog...)
	afterRead := p.afterRead
	p.mu.Unlock()
	if afterRead != nil {
		afterRead()
	}
	return result, nil
}

type repositoryFixture struct {
	mu     sync.Mutex
	orders map[string]Order
	keys   map[string]string
}

func repositoryKey(owner Owner, key string) string {
	return fmt.Sprintf("%d:%s:%s", owner.CompanyID, owner.CustomerSessionHash, key)
}

func (r *repositoryFixture) FindByKey(_ context.Context, owner Owner, key string) (Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, exists := r.keys[repositoryKey(owner, key)]
	if !exists {
		return Order{}, ErrNotFound
	}
	return r.orders[id], nil
}

func (r *repositoryFixture) Get(_ context.Context, id string) (Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	order, exists := r.orders[id]
	if !exists {
		return Order{}, ErrNotFound
	}
	return order, nil
}

func (r *repositoryFixture) Create(_ context.Context, order Order) (Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := repositoryKey(order.Owner, order.IdempotencyKey)
	if id, exists := r.keys[key]; exists {
		return r.orders[id], nil
	}
	r.keys[key] = order.ID
	r.orders[order.ID] = order
	return order, nil
}

func checkoutFixture() (*Service, *sourceFixture, *repositoryFixture, Actor, PrepareRequest, *time.Time) {
	actor := Actor{CompanyID: 781898, Mode: "public", ScopeID: "scope-a", CustomerSessionID: "customer-a", ClientID: 42}
	selected := Selection{Source: SourceCatalog, Kind: KindGood, ID: 5, StaffID: 9, Quantity: "2.5"}
	provider := &sourceFixture{catalog: []CatalogItem{{Selection: selected, Title: "Fresh catalog good", StaffName: "Master", UnitPriceKopecks: 199}}}
	repo := &repositoryFixture{orders: map[string]Order{}, keys: map[string]string{}}
	service := NewService(provider, repo, Options{MaxLifeTime: 900})
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	return service, provider, repo, actor, PrepareRequest{Items: []Selection{selected}}, &now
}

func requireStatus(t *testing.T, err error, expected int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected HTTP %d error", expected)
	}
	w := httptest.NewRecorder()
	webapp.WriteServiceError(w, err)
	if w.Code != expected {
		t.Fatalf("error=%v status=%d, want %d", err, w.Code, expected)
	}
}

func requireDetailCode(t *testing.T, err error, expected string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error detail code %q", expected)
	}
	w := httptest.NewRecorder()
	webapp.WriteServiceError(w, err)
	var body struct {
		Error struct {
			Details struct {
				Code string `json:"code"`
			} `json:"details"`
		} `json:"error"`
	}
	if decodeErr := json.Unmarshal(w.Body.Bytes(), &body); decodeErr != nil || body.Error.Details.Code != expected {
		t.Fatalf("error response=%s; expected detail code %q, decode error=%v", w.Body.String(), expected, decodeErr)
	}
}

func TestPreparedCheckoutUsesFreshPricesAndExactOutstandingVisitAmount(t *testing.T) {
	s, source, _, actor, req, _ := checkoutFixture()
	source.visit = Visit{VisitID: 11, RecordID: 12, Items: []VisitLine{{Kind: KindGood, ID: 7, LineID: 33, StaffID: 9, Title: "Partially paid visit good", Quantity: "3", UnitPriceKopecks: 33, AmountKopecks: 101}}}
	req.VisitID, req.RecordID = 11, 12
	req.Items = append(req.Items, Selection{Source: SourceVisit, Kind: KindGood, ID: 7, LineID: 33, StaffID: 9, Quantity: "3"})
	order, err := s.Prepare(context.Background(), actor, "request-1", req)
	if err != nil {
		t.Fatal(err)
	}
	if order.SubtotalKopecks != 599 || order.PayableKopecks != 599 || order.BonusKopecks != 0 || order.Currency != "RUB" || order.State != StatePrepared {
		t.Fatalf("unexpected order %+v", order)
	}
	if source.companyID != actor.CompanyID || source.clientID != actor.ClientID || source.visitCalls != 1 || source.catalogCalls != 1 {
		t.Fatal("source lookup did not use the session company/customer")
	}
	if order.Items[1].AmountKopecks != 101 || order.Items[1].UnitPriceKopecks != 33 {
		t.Fatal("partially paid visit amount was reconstructed from rounded price")
	}
	if order.Owner.CustomerSessionHash == actor.CustomerSessionID || len(order.Owner.CustomerSessionHash) != 64 || order.Owner.ScopeHash == actor.ScopeID {
		t.Fatal("raw session interaction was persisted")
	}
}

func TestCheckoutIdempotencyFreezesSnapshotAndRejectsChangedRequest(t *testing.T) {
	s, source, repo, actor, req, now := checkoutFixture()
	first, err := s.Prepare(context.Background(), actor, "same-key", req)
	if err != nil {
		t.Fatal(err)
	}
	source.catalog[0].UnitPriceKopecks = 400
	req.Items[0].Quantity = "02.5000"
	replay, err := s.Prepare(context.Background(), actor, "same-key", req)
	if err != nil || replay.ID != first.ID || replay.PayableKopecks != 498 || source.catalogCalls != 1 || len(repo.orders) != 1 {
		t.Fatalf("idempotent replay %+v: %v", replay, err)
	}
	req.Items[0].Quantity = "3"
	_, err = s.Prepare(context.Background(), actor, "same-key", req)
	requireStatus(t, err, 409)
	*now = now.Add(901 * time.Second)
	req.Items[0].Quantity = "2.5"
	_, err = s.Prepare(context.Background(), actor, "same-key", req)
	requireStatus(t, err, 409)
	requireDetailCode(t, err, "checkout_expired")
	_, err = s.Get(context.Background(), actor, first.ID)
	requireStatus(t, err, 409)
	requireDetailCode(t, err, "checkout_expired")
}

func TestCheckoutNoLongerPreparedHasDistinctConflictCode(t *testing.T) {
	s, _, repo, actor, req, _ := checkoutFixture()
	order, err := s.Prepare(context.Background(), actor, "key", req)
	if err != nil {
		t.Fatal(err)
	}
	order.State = "payment_pending"
	repo.orders[order.ID] = order
	_, err = s.Get(context.Background(), actor, order.ID)
	requireStatus(t, err, 409)
	requireDetailCode(t, err, "checkout_not_prepared")
}

func TestCheckoutGetRejectsOtherCustomerSalonAndScope(t *testing.T) {
	s, _, _, actor, req, _ := checkoutFixture()
	order, err := s.Prepare(context.Background(), actor, "key", req)
	if err != nil {
		t.Fatal(err)
	}
	if loaded, err := s.Get(context.Background(), actor, order.ID); err != nil || loaded.ID != order.ID {
		t.Fatalf("own checkout=%+v,%v", loaded, err)
	}
	for _, other := range []Actor{
		{CompanyID: 123, Mode: actor.Mode, ScopeID: actor.ScopeID, CustomerSessionID: actor.CustomerSessionID, ClientID: actor.ClientID},
		{CompanyID: actor.CompanyID, Mode: actor.Mode, ScopeID: "new-scope", CustomerSessionID: actor.CustomerSessionID, ClientID: actor.ClientID},
		{CompanyID: actor.CompanyID, Mode: actor.Mode, ScopeID: actor.ScopeID, CustomerSessionID: "next-customer", ClientID: actor.ClientID},
		{CompanyID: actor.CompanyID, Mode: actor.Mode, ScopeID: actor.ScopeID, CustomerSessionID: actor.CustomerSessionID, ClientID: 99},
	} {
		_, err := s.Get(context.Background(), other, order.ID)
		requireStatus(t, err, 404)
	}
}

func TestCheckoutValidationAndUnavailableItems(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*PrepareRequest)
		status int
	}{
		{"empty", func(r *PrepareRequest) { r.Items = nil }, 400},
		{"bonus", func(r *PrepareRequest) { r.BonusToApplyKopecks = 1 }, 409},
		{"negative bonus", func(r *PrepareRequest) { r.BonusToApplyKopecks = -1 }, 409},
		{"duplicate", func(r *PrepareRequest) { r.Items = append(r.Items, r.Items[0]) }, 400},
		{"quantity", func(r *PrepareRequest) { r.Items[0].Quantity = "0" }, 400},
		{"fractional service", func(r *PrepareRequest) { r.Items[0].Kind = KindService }, 400},
		{"catalog transaction", func(r *PrepareRequest) { r.Items[0].LineID = 3 }, 400},
		{"missing visit", func(r *PrepareRequest) { r.Items[0].Source = SourceVisit }, 400},
		{"unknown item", func(r *PrepareRequest) { r.Items[0].ID = 999 }, 409},
		{"unknown staff", func(r *PrepareRequest) { r.Items[0].StaffID = 999 }, 409},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _, repo, actor, req, _ := checkoutFixture()
			tt.change(&req)
			_, err := s.Prepare(context.Background(), actor, "key", req)
			requireStatus(t, err, tt.status)
			if len(repo.orders) != 0 {
				t.Fatal("invalid request was persisted")
			}
		})
	}
}

func TestCheckoutNeverRepricesStaleVisitAsCatalog(t *testing.T) {
	for _, cause := range []string{"paid", "changed", "quantity", "staff"} {
		t.Run(cause, func(t *testing.T) {
			s, source, repo, actor, _, _ := checkoutFixture()
			source.visit = Visit{VisitID: 11, RecordID: 12, Items: []VisitLine{{Kind: KindService, ID: 5, StaffID: 9, Title: "Visit", Quantity: "1", UnitPriceKopecks: 200, AmountKopecks: 200}}}
			req := PrepareRequest{VisitID: 11, RecordID: 12, Items: []Selection{{Source: SourceVisit, Kind: KindService, ID: 5, StaffID: 9, Quantity: "1"}}}
			switch cause {
			case "paid":
				source.visit.Items = nil
			case "changed":
				source.visit.RecordID = 13
			case "quantity":
				req.Items[0].Quantity = "2"
			case "staff":
				req.Items[0].StaffID = 10
			}
			_, err := s.Prepare(context.Background(), actor, "key", req)
			requireStatus(t, err, 409)
			if len(repo.orders) != 0 || source.catalogCalls != 0 {
				t.Fatal("stale visit used catalog pricing")
			}
		})
	}
}

func TestGuestMayPrepareCatalogButCannotReadVisit(t *testing.T) {
	s, source, _, actor, req, _ := checkoutFixture()
	actor.Guest, actor.ClientID = true, 0
	if _, err := s.Prepare(context.Background(), actor, "guest", req); err != nil {
		t.Fatal(err)
	}
	req.VisitID, req.RecordID = 1, 2
	_, err := s.Prepare(context.Background(), actor, "guest-visit", req)
	requireStatus(t, err, 403)
	if source.visitCalls != 0 {
		t.Fatal("guest fetched private visit")
	}
}

func TestHistoricalVisitCanRetainMissingStaffButCatalogRequiresStaff(t *testing.T) {
	s, source, _, actor, _, _ := checkoutFixture()
	source.visit = Visit{VisitID: 11, RecordID: 12, Items: []VisitLine{{Kind: KindService, ID: 5, StaffID: 0, Title: "Historic visit", Quantity: "1", UnitPriceKopecks: 200, AmountKopecks: 200}}}
	req := PrepareRequest{VisitID: 11, RecordID: 12, Items: []Selection{{Source: SourceVisit, Kind: KindService, ID: 5, StaffID: 0, Quantity: "1"}}}
	if _, err := s.Prepare(context.Background(), actor, "historic", req); err != nil {
		t.Fatalf("fresh historic visit line rejected: %v", err)
	}
	req.VisitID, req.RecordID = 0, 0
	req.Items[0].Source = SourceCatalog
	_, err := s.Prepare(context.Background(), actor, "catalog", req)
	requireStatus(t, err, 400)
}

func TestSessionChangeDuringSourceReadDoesNotPersistCheckout(t *testing.T) {
	s, source, repo, actor, req, _ := checkoutFixture()
	valid := true
	s.options.ValidateActor = func(context.Context, Actor) error {
		if !valid {
			return webapp.Unauthorized("customer interaction ended", nil)
		}
		return nil
	}
	source.afterRead = func() { valid = false }
	_, err := s.Prepare(context.Background(), actor, "key", req)
	requireStatus(t, err, 401)
	if len(repo.orders) != 0 {
		t.Fatal("stale actor checkout persisted")
	}
}

func TestConcurrentCheckoutPreparationKeepsOneIdempotentWinner(t *testing.T) {
	s, _, repo, actor, req, _ := checkoutFixture()
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for index := 0; index < 8; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			order, err := s.Prepare(context.Background(), actor, "same-key", req)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- order.ID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("concurrent requests returned different orders")
		}
	}
	if len(repo.orders) != 1 {
		t.Fatalf("stored %d orders", len(repo.orders))
	}
}
