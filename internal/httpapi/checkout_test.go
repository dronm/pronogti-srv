package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dronm/goyclients"
	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/pronogti/internal/models"
)

type checkoutTestProvider struct {
	calls     int
	afterRead func()
}

func (p *checkoutTestProvider) LoadVisit(context.Context, int64, int64) (checkout.Visit, error) {
	return checkout.Visit{VisitID: 15, RecordID: 16, Items: []checkout.VisitLine{{Kind: checkout.KindService, ID: 1, StaffID: 5, StaffName: "Master", Title: "Visit service", Quantity: "1", UnitPriceKopecks: 12550, AmountKopecks: 12550}}}, nil
}

func (p *checkoutTestProvider) LoadCatalog(_ context.Context, _ int64, selections []checkout.Selection) ([]checkout.CatalogItem, error) {
	p.calls++
	items := make([]checkout.CatalogItem, 0, len(selections))
	for _, selection := range selections {
		items = append(items, checkout.CatalogItem{Selection: selection, Title: "Current catalog item", StaffName: "Master", UnitPriceKopecks: 12550})
	}
	if p.afterRead != nil {
		p.afterRead()
	}
	return items, nil
}

type checkoutTestRepository struct {
	mu      sync.Mutex
	orders  map[string]checkout.Order
	creates int
}

func (r *checkoutTestRepository) FindByKey(_ context.Context, owner checkout.Owner, key string) (checkout.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.find(owner, key)
}

func (r *checkoutTestRepository) find(owner checkout.Owner, key string) (checkout.Order, error) {
	for _, order := range r.orders {
		if order.Owner.CompanyID == owner.CompanyID && order.Owner.CustomerSessionHash == owner.CustomerSessionHash && order.IdempotencyKey == key {
			return order, nil
		}
	}
	return checkout.Order{}, checkout.ErrNotFound
}

func (r *checkoutTestRepository) Get(_ context.Context, id string) (checkout.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	order, found := r.orders[id]
	if !found {
		return checkout.Order{}, checkout.ErrNotFound
	}
	return order, nil
}

func (r *checkoutTestRepository) Create(_ context.Context, order checkout.Order) (checkout.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, err := r.find(order.Owner, order.IdempotencyKey); err == nil {
		return existing, nil
	}
	r.creates++
	r.orders[order.ID] = order
	return order, nil
}

func checkoutDependencies() (CheckoutDependencies, *checkoutTestProvider, *checkoutTestRepository) {
	provider := &checkoutTestProvider{}
	repository := &checkoutTestRepository{orders: make(map[string]checkout.Order)}
	return CheckoutDependencies{Provider: provider, Repository: repository, MaxLifeTime: 900}, provider, repository
}

const checkoutCatalogBody = `{"items":[{"source":"catalog","kind":"good","id":2,"staff_id":5,"quantity":"2"}]}`

func (r *testRuntime) checkoutRequest(method, path, body, nonce, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(kiosk.CustomerHeader, nonce)
	req.Header.Set(kiosk.ScopeHeader, r.scopeID)
	req.Header.Set("Idempotency-Key", key)
	res := httptest.NewRecorder()
	r.handler.ServeHTTP(res, req)
	return res
}

func decodeCheckout(t *testing.T, response *httptest.ResponseRecorder) checkout.Order {
	t.Helper()
	if response.Code != 200 {
		t.Fatalf("checkout response=%d %s", response.Code, response.Body)
	}
	var order checkout.Order
	if err := json.Unmarshal(response.Body.Bytes(), &order); err != nil {
		t.Fatal(err)
	}
	return order
}

func TestCheckoutPreparationUsesSessionIdentityAndPersistsFreshPrice(t *testing.T) {
	dependencies, provider, repository := checkoutDependencies()
	r := newRuntime(&fakeBackend{clientResult: goyclients.Client{ID: 45}}, dependencies)
	if res := r.request(http.MethodPost, "/salons/salon-one/session", `{}`, ""); res.Code != 200 {
		t.Fatal(res.Body)
	}
	res := r.request(http.MethodPost, "/customer/session", `{"phone":"79991234567"}`, "")
	var session models.SessionResponse
	if err := json.Unmarshal(res.Body.Bytes(), &session); err != nil || session.Customer == nil {
		t.Fatalf("customer start=%d %s", res.Code, res.Body)
	}
	firstResponse := r.checkoutRequest(http.MethodPost, "/customer/checkouts", checkoutCatalogBody, session.Customer.SessionID, "same-key")
	first := decodeCheckout(t, firstResponse)
	if first.PayableKopecks != 25100 || first.Currency != "RUB" || first.State != checkout.StatePrepared || len(first.Items) != 1 {
		t.Fatalf("prepared quote=%+v", first)
	}
	second := decodeCheckout(t, r.checkoutRequest(http.MethodPost, "/customer/checkouts", checkoutCatalogBody, session.Customer.SessionID, "same-key"))
	if first.ID != second.ID || provider.calls != 1 || repository.creates != 1 {
		t.Fatalf("duplicate preparation was not reused: %s %s calls=%d writes=%d", first.ID, second.ID, provider.calls, repository.creates)
	}
	stored := repository.orders[first.ID]
	if stored.Owner.CompanyID != 12 || stored.Owner.ClientID != 45 || stored.Owner.KioskID != nil || stored.Owner.Mode != "public" {
		t.Fatalf("wrong checkout owner=%+v", stored.Owner)
	}
	if strings.Contains(firstResponse.Body.String(), "customer_session_hash") || strings.Contains(firstResponse.Body.String(), "request_hash") {
		t.Fatal("internal ownership information leaked")
	}
	if res := r.checkoutRequest(http.MethodPost, "/customer/checkouts", strings.Replace(checkoutCatalogBody, `"2"`, `"3"`, 1), session.Customer.SessionID, "same-key"); res.Code != 409 {
		t.Fatalf("idempotency key accepted changed selection: %d %s", res.Code, res.Body)
	}
	if res := r.checkoutRequest(http.MethodGet, "/customer/checkouts/"+first.ID, "", session.Customer.SessionID, ""); res.Code != 200 {
		t.Fatalf("same-owner restore=%d %s", res.Code, res.Body)
	}
	if res := r.checkoutRequest(http.MethodPost, "/customer/init-qr-pay", `{"checkout_id":"`+first.ID+`"}`, session.Customer.SessionID, ""); res.Code != 400 {
		t.Fatalf("QR accepted missing idempotency key: %d %s", res.Code, res.Body)
	}
	if res := r.request(http.MethodPost, "/customer/session", `{"guest":true}`, ""); res.Code != 200 {
		t.Fatal(res.Body)
	} else {
		var next models.SessionResponse
		_ = json.Unmarshal(res.Body.Bytes(), &next)
		if res := r.checkoutRequest(http.MethodGet, "/customer/checkouts/"+first.ID, "", next.Customer.SessionID, ""); res.Code != 404 {
			t.Fatalf("next customer could read previous checkout: %d %s", res.Code, res.Body)
		}
	}
}

func TestCheckoutCannotPersistAfterSalonChangesDuringPriceRead(t *testing.T) {
	dependencies, provider, repository := checkoutDependencies()
	r := newRuntime(&fakeBackend{clientResult: goyclients.Client{ID: 45}}, dependencies)
	r.bind(t)
	start := r.start(t, `{"phone":"79991234567"}`)
	provider.afterRead = func() {
		manager := kiosk.NewManager(r.registry, kiosk.Options{Salons: r.salons})
		if _, err := manager.SelectSalon(context.Background(), r.sess, "salon-two"); err != nil {
			t.Fatal(err)
		}
	}
	res := r.checkoutRequest(http.MethodPost, "/customer/checkouts", checkoutCatalogBody, start.Customer.SessionID, "changing-salon")
	if res.Code != 401 || repository.creates != 0 {
		t.Fatalf("late checkout survived salon change: status=%d writes=%d body=%s", res.Code, repository.creates, res.Body)
	}
}

func TestCheckoutRequiresCurrentCustomerAndIdempotency(t *testing.T) {
	dependencies, _, repository := checkoutDependencies()
	r := newRuntime(&fakeBackend{clientResult: goyclients.Client{ID: 45}}, dependencies)
	r.bind(t)
	start := r.start(t, `{"phone":"79991234567"}`)
	if res := r.checkoutRequest(http.MethodPost, "/customer/checkouts", checkoutCatalogBody, "wrong-nonce", "key"); res.Code != 401 {
		t.Fatalf("unauthorized prepare=%d", res.Code)
	}
	if res := r.checkoutRequest(http.MethodPost, "/customer/checkouts", checkoutCatalogBody, start.Customer.SessionID, ""); res.Code != 400 {
		t.Fatalf("missing idempotency=%d %s", res.Code, res.Body)
	}
	if res := r.checkoutRequest(http.MethodPost, "/customer/checkouts", strings.Replace(checkoutCatalogBody, `"items":`, `"bonus_to_apply_kopecks":100,"items":`, 1), start.Customer.SessionID, "bonus"); res.Code != 409 {
		t.Fatalf("unbacked bonus deduction=%d %s", res.Code, res.Body)
	}
	if repository.creates != 0 {
		t.Fatal("invalid checkout persisted")
	}
}
