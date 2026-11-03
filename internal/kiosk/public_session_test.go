package kiosk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/pronogti/internal/salon"
	"github.com/dronm/webapp"
)

type publicSalonRegistry struct {
	entries map[string]salon.Entry
	err     error
}

func (r *publicSalonRegistry) List(context.Context) ([]salon.Entry, error) {
	if r.err != nil {
		return nil, r.err
	}
	var result []salon.Entry
	for _, entry := range r.entries {
		if entry.IsActive {
			result = append(result, entry)
		}
	}
	return result, nil
}
func (r *publicSalonRegistry) FindBySlug(_ context.Context, slug string) (salon.Entry, error) {
	if r.err != nil {
		return salon.Entry{}, r.err
	}
	entry, ok := r.entries[slug]
	if !ok {
		return salon.Entry{}, salon.ErrSalonNotFound
	}
	return entry, nil
}
func (r *publicSalonRegistry) FindByCompanyID(_ context.Context, companyID int64) (salon.Entry, error) {
	if r.err != nil {
		return salon.Entry{}, r.err
	}
	for _, entry := range r.entries {
		if entry.CompanyID == companyID {
			return entry, nil
		}
	}
	return salon.Entry{}, salon.ErrSalonNotFound
}

func newPublicFixture() (*Manager, *testSession, *publicSalonRegistry, *time.Time) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	registry := &publicSalonRegistry{entries: map[string]salon.Entry{
		"first":  {SalonIdentity: models.SalonIdentity{CompanyID: 12, Slug: "first", Name: "First salon"}, IsActive: true, SessionVersion: 1},
		"second": {SalonIdentity: models.SalonIdentity{CompanyID: 34, Slug: "second", Name: "Second salon"}, IsActive: true, SessionVersion: 1},
	}}
	manager := NewManager(nil, Options{Salons: registry, CustomerMaxLifeTime: 600, CustomerMaxIdleTime: 120})
	manager.now = func() time.Time { return now }
	return manager, &testSession{values: map[string][]byte{}}, registry, &now
}

func TestPublicBootstrapAndCustomerNeedSalonNotKiosk(t *testing.T) {
	manager, sess, _, _ := newPublicFixture()
	current, err := manager.Current(context.Background(), sess)
	if err != nil || current.Mode != "" || current.Salon != nil || current.Kiosk != nil || current.Customer != nil {
		t.Fatalf("anonymous=%+v err=%v", current, err)
	}
	_, _, err = manager.StartCustomer(context.Background(), sess, "79991234567", false)
	var httpError webapp.HTTPError
	if !errors.As(err, &httpError) || httpError.StatusCode() != http.StatusUnauthorized || httpError.Details().(map[string]any)["code"] != "salon_required" {
		t.Fatalf("missing salon: %v", err)
	}
	selected, err := manager.SelectSalon(context.Background(), sess, "first")
	if err != nil || selected.Mode != "public" || selected.Kiosk != nil || selected.Salon == nil || selected.Salon.CompanyID != 12 {
		t.Fatalf("selection=%+v err=%v", selected, err)
	}
	scope, customer, err := manager.StartCustomer(context.Background(), sess, "+7 (999) 123-45-67", false)
	if err != nil || scope.CompanyID != 12 || scope.Mode != "public" || scope.Kiosk != nil || customer.Phone != "79991234567" {
		t.Fatalf("scope=%+v customer=%+v err=%v", scope, customer, err)
	}
	customer.ClientID, customer.Registered = 45, true
	if err := manager.UpdateCustomer(sess, customer); err != nil {
		t.Fatal(err)
	}
	_, restored, err := manager.RequireCustomer(context.Background(), sess, customer.SessionID)
	if err != nil || restored.ClientID != 45 {
		t.Fatalf("customer=%+v err=%v", restored, err)
	}
	encoded, err := json.Marshal(restored)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), customer.ScopeID) {
		t.Fatal("internal scope correlation leaked into customer JSON")
	}
}

func TestPublicExpiryAndLogoutKeepSelectedSalon(t *testing.T) {
	for _, idleExpiry := range []bool{false, true} {
		manager, sess, _, now := newPublicFixture()
		if _, err := manager.SelectSalon(context.Background(), sess, "first"); err != nil {
			t.Fatal(err)
		}
		_, customer, err := manager.StartCustomer(context.Background(), sess, "", true)
		if err != nil {
			t.Fatal(err)
		}
		if idleExpiry {
			*now = now.Add(121 * time.Second)
		} else if err := manager.End(context.Background(), sess, customer.SessionID); err != nil {
			t.Fatal(err)
		}
		current, err := manager.Current(context.Background(), sess)
		if err != nil || current.Customer != nil || current.Salon == nil || current.Salon.CompanyID != 12 || current.Mode != "public" {
			t.Fatalf("current=%+v err=%v", current, err)
		}
		if _, _, err := manager.RequireCustomer(context.Background(), sess, customer.SessionID); err == nil {
			t.Fatal("ended public customer accepted")
		}
	}
}

func TestSalonSwitchRejectsOldIdentityIncludingLateStart(t *testing.T) {
	manager, sess, _, _ := newPublicFixture()
	if _, err := manager.SelectSalon(context.Background(), sess, "first"); err != nil {
		t.Fatal(err)
	}
	_, old, err := manager.StartCustomer(context.Background(), sess, "79991234567", false)
	if err != nil {
		t.Fatal(err)
	}
	old.ClientID, old.Registered = 45, true
	for _, slug := range []string{"second", "first"} {
		if _, err := manager.SelectSalon(context.Background(), sess, slug); err != nil {
			t.Fatal(err)
		}
		if err := manager.UpdateCustomer(sess, old); err == nil {
			t.Fatal("old company identity updated after salon switch")
		}
	}
	// Simulate an earlier phone lookup publishing its old nonce after the new
	// salon was selected. A->B->A must still invalidate the old interaction.
	if err := sess.Set(customerKey(old.SessionID), old); err != nil {
		t.Fatal(err)
	}
	if err := sess.Set(activeCustomerKey, old.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.RequireCustomer(context.Background(), sess, old.SessionID); err == nil {
		t.Fatal("late customer start crossed scope transition")
	}
	current, err := manager.Current(context.Background(), sess)
	if err != nil || current.Customer != nil || current.Salon == nil || current.Salon.Slug != "first" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func TestPublicSalonChangesInvalidateScope(t *testing.T) {
	for _, change := range []func(*salon.Entry){func(e *salon.Entry) { e.IsActive = false }, func(e *salon.Entry) { e.CompanyID++ }, func(e *salon.Entry) { e.SessionVersion++ }} {
		manager, sess, registry, _ := newPublicFixture()
		if _, err := manager.SelectSalon(context.Background(), sess, "first"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := manager.StartCustomer(context.Background(), sess, "", true); err != nil {
			t.Fatal(err)
		}
		entry := registry.entries["first"]
		change(&entry)
		registry.entries["first"] = entry
		if _, err := manager.Current(context.Background(), sess); err == nil {
			t.Fatal("changed salon retained scope")
		}
		current, err := manager.Current(context.Background(), sess)
		if err != nil || current.Customer != nil || current.Salon != nil {
			t.Fatalf("cleared=%+v err=%v", current, err)
		}
	}
}

func TestUnavailableSalonCannotReplaceValidSelection(t *testing.T) {
	manager, sess, registry, _ := newPublicFixture()
	if _, err := manager.SelectSalon(context.Background(), sess, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SelectSalon(context.Background(), sess, "missing"); err == nil {
		t.Fatal("unknown salon accepted")
	}
	sentinel := errors.New("database unavailable")
	registry.err = sentinel
	if _, err := manager.Current(context.Background(), sess); !errors.Is(err, sentinel) {
		t.Fatalf("registry error=%v", err)
	}
	registry.err = nil
	current, err := manager.Current(context.Background(), sess)
	if err != nil || current.Salon == nil || current.Salon.Slug != "first" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func TestClearingDevicePreservesExplicitPublicCustomer(t *testing.T) {
	manager, sess, salons, _ := newPublicFixture()
	manager.registry = &testRegistry{device: Kiosk{ID: 1, CompanyID: 12, IsActive: true, SessionVersion: 1}}
	if _, err := manager.Bind(context.Background(), sess, "token"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SelectSalon(context.Background(), sess, "second"); err != nil {
		t.Fatal(err)
	}
	_, customer, err := manager.StartCustomer(context.Background(), sess, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ClearKiosk(sess); err != nil {
		t.Fatal(err)
	}
	current, err := manager.Current(context.Background(), sess)
	if err != nil || current.Kiosk != nil || current.Mode != "public" || current.Customer == nil || current.Customer.SessionID != customer.SessionID || current.Salon.CompanyID != salons.entries["second"].CompanyID {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func TestExpectedScopePreventsLateSalonSelectionUsingWrongCompany(t *testing.T) {
	manager, sess, _, _ := newPublicFixture()
	ctx := context.Background()
	first, err := manager.SelectSalon(ctx, sess, "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.SelectSalon(ctx, sess, "second")
	if err != nil {
		t.Fatal(err)
	}
	if first.ScopeID == "" || second.ScopeID == "" || first.ScopeID == second.ScopeID {
		t.Fatal("salon selection did not change correlation ID")
	}
	// An older selection request may commit after a newer browser response.
	// The phone request carries the newer displayed scope and must fail before
	// it can establish customer identity in the older request's company.
	if _, err := manager.SelectSalon(ctx, sess, "first"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.StartCustomer(ContextWithScope(ctx, second.ScopeID), sess, "79991234567", false); err == nil {
		t.Fatal("stale displayed salon started a customer in another company")
	}
	var nonce string
	if err := sess.Get(activeCustomerKey, &nonce); err == nil && nonce != "" {
		t.Fatal("rejected start published a customer")
	}
	current, err := manager.Current(ctx, sess)
	if err != nil {
		t.Fatal(err)
	}
	if _, customer, err := manager.StartCustomer(ContextWithScope(ctx, current.ScopeID), sess, "", true); err != nil || customer.ScopeID != current.ScopeID {
		t.Fatalf("current selection customer=%+v err=%v", customer, err)
	}
}
