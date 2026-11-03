package kiosk

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/session"
)

type testSession struct {
	session.Session
	mu     sync.Mutex
	values map[string][]byte
}

func (s *testSession) Set(key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b bytes.Buffer
	if err := gob.NewEncoder(&b).Encode(value); err != nil {
		return err
	}
	s.values[key] = b.Bytes()
	return nil
}
func (s *testSession) Get(key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.values[key]
	if !ok {
		return errors.New("missing")
	}
	return gob.NewDecoder(bytes.NewReader(b)).Decode(value)
}
func (s *testSession) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}
func (s *testSession) Flush() error { return nil }

type testRegistry struct {
	device Kiosk
	err    error
}

func (r *testRegistry) FindByToken(context.Context, string) (Kiosk, error) { return r.device, r.err }
func (r *testRegistry) FindByID(context.Context, int64) (Kiosk, error)     { return r.device, r.err }

func newFixture(t *testing.T) (*Manager, *testSession, *testRegistry, *time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	registry := &testRegistry{device: Kiosk{ID: 1, Name: "Kiosk", CompanyID: 12, IsActive: true, SessionVersion: 1}}
	manager := NewManager(registry, Options{CustomerMaxLifeTime: 600, CustomerMaxIdleTime: 120})
	manager.now = func() time.Time { return now }
	sess := &testSession{values: map[string][]byte{}}
	if _, err := manager.Bind(context.Background(), sess, "token"); err != nil {
		t.Fatal(err)
	}
	return manager, sess, registry, &now
}

func TestCustomerIdleExpiryPreservesKiosk(t *testing.T) {
	m, s, _, now := newFixture(t)
	_, customer, err := m.StartCustomer(context.Background(), s, "79991234567", false)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(121 * time.Second)
	current, err := m.Current(context.Background(), s)
	if err != nil || current.Kiosk.ID != 1 || current.Customer != nil {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	if _, _, err := m.RequireCustomer(context.Background(), s, customer.SessionID); err == nil {
		t.Fatal("expired customer accepted")
	}
}

func TestCustomerTouchDoesNotExtendAbsoluteLifetime(t *testing.T) {
	m, s, _, now := newFixture(t)
	_, customer, err := m.StartCustomer(context.Background(), s, "", true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		*now = now.Add(100 * time.Second)
		if _, err := m.Touch(context.Background(), s, customer.SessionID); err != nil {
			t.Fatalf("touch %d: %v", i, err)
		}
	}
	*now = now.Add(100 * time.Second)
	if _, err := m.Touch(context.Background(), s, customer.SessionID); err == nil {
		t.Fatal("absolute lifetime extended by touch")
	}
}

func TestDelayedCustomerUpdateCannotRestorePreviousIdentity(t *testing.T) {
	m, s, _, _ := newFixture(t)
	_, old, err := m.StartCustomer(context.Background(), s, "79991234567", false)
	if err != nil {
		t.Fatal(err)
	}
	_, next, err := m.StartCustomer(context.Background(), s, "", true)
	if err != nil {
		t.Fatal(err)
	}
	old.ClientID = 99
	old.Registered = true
	if err := m.UpdateCustomer(s, old); err == nil {
		t.Fatal("old identity update accepted")
	}
	current, err := m.Current(context.Background(), s)
	if err != nil || current.Customer == nil || current.Customer.SessionID != next.SessionID || !current.Customer.Guest {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	if err := m.End(context.Background(), s, old.SessionID); err == nil {
		t.Fatal("old logout accepted")
	}
}

func TestKioskBindingMustRemainCurrent(t *testing.T) {
	for _, change := range []func(*Kiosk){func(k *Kiosk) { k.IsActive = false }, func(k *Kiosk) { k.CompanyID = 99 }, func(k *Kiosk) { k.SessionVersion++ }} {
		m, s, r, _ := newFixture(t)
		change(&r.device)
		if _, err := m.Current(context.Background(), s); err == nil {
			t.Fatal("revoked kiosk accepted")
		}
		var state models.CustomerSession
		if err := s.Get("customer", &state); err == nil {
			t.Fatal("unexpected shared customer record")
		}
	}
}

func TestPhoneNormalizationAndGuestValidation(t *testing.T) {
	for _, input := range []string{"9991234567", "89991234567", "+7 (999) 123-45-67"} {
		phone, err := NormalizePhone(input)
		if err != nil || phone != "79991234567" {
			t.Errorf("phone %q => %q, %v", input, phone, err)
		}
	}
	for _, input := range []string{"", "123", "+1 9991234567", "7999abc4567", "799912345670"} {
		if _, err := NormalizePhone(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	m, s, _, _ := newFixture(t)
	if _, _, err := m.StartCustomer(context.Background(), s, "79991234567", true); err == nil {
		t.Fatal("phone and guest accepted together")
	}
}
