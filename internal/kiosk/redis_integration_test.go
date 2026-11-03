//go:build integration

package kiosk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	websession "github.com/dronm/webapp/session"
)

// TestRedisCustomerLifecycle exercises the same public Redis/session provider
// used at runtime. A separate namespace is created and removed for this test.
func TestRedisCustomerLifecycle(t *testing.T) {
	connect := os.Getenv("PRONOGTI_TEST_REDIS_URL")
	if connect == "" {
		t.Skip("set PRONOGTI_TEST_REDIS_URL to an isolated Redis instance")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(random)
	sessions, err := websession.NewRedisManager(websession.Config{CookieName: "_pronogti_test", HTTPOnly: true, MaxLifeTime: 300}, websession.RedisConfig{Connect: connect, Namespace: "pronogti-test-" + id})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := sessions.Core().SessionStart(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Core().SessionDestroy(id); sessions.Core().CloseProvider() })
	if err := sess.Put("time_created", time.Now()); err != nil {
		t.Fatal(err)
	}
	registry := &testRegistry{device: Kiosk{ID: 7, Name: "Test kiosk", CompanyID: 12, IsActive: true, SessionVersion: 1}}
	manager := NewManager(registry, Options{CustomerMaxLifeTime: 1800, CustomerMaxIdleTime: 180})
	if _, err := manager.Bind(context.Background(), sess, "token"); err != nil {
		t.Fatal(err)
	}
	_, first, err := manager.StartCustomer(context.Background(), sess, "79991234567", false)
	if err != nil {
		t.Fatal(err)
	}
	first.ClientID = 45
	first.Registered = true
	if err := manager.UpdateCustomer(sess, first); err != nil {
		t.Fatal(err)
	}
	// Resolve a fresh store handle, as the next HTTP request would do.
	fresh, err := sessions.Core().SessionStart(id)
	if err != nil {
		t.Fatal(err)
	}
	current, err := manager.Current(context.Background(), fresh)
	if err != nil || current.Customer == nil || current.Customer.ClientID != 45 {
		t.Fatalf("restored=%+v error=%v", current, err)
	}
	if err := manager.End(context.Background(), fresh, first.SessionID); err != nil {
		t.Fatal(err)
	}
	current, err = manager.Current(context.Background(), fresh)
	if err != nil || current.Customer != nil || current.Kiosk.ID != 7 {
		t.Fatalf("ended=%+v error=%v", current, err)
	}
	if _, _, err := manager.RequireCustomer(context.Background(), sess, first.SessionID); err == nil {
		t.Fatal("original handle retained logged-out identity")
	}
	_, guest, err := manager.StartCustomer(context.Background(), fresh, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Touch(context.Background(), fresh, guest.SessionID); err != nil {
		t.Fatal(err)
	}
	registry.device.SessionVersion++
	if _, err := manager.Current(context.Background(), fresh); err == nil {
		t.Fatal("rotated kiosk credential retained session")
	}

	// The same real Redis provider must preserve public company scope and its
	// customer correlation after reopening the store on another HTTP request.
	_, _, salons, _ := newPublicFixture()
	manager.options.Salons = salons
	selected, err := manager.SelectSalon(context.Background(), fresh, "first")
	if err != nil {
		t.Fatal(err)
	}
	publicCtx := ContextWithScope(context.Background(), selected.ScopeID)
	_, publicCustomer, err := manager.StartCustomer(publicCtx, fresh, "79991234567", false)
	if err != nil {
		t.Fatal(err)
	}
	publicCustomer.ClientID, publicCustomer.Registered = 77, true
	if err := manager.UpdateCustomer(fresh, publicCustomer); err != nil {
		t.Fatal(err)
	}
	restored, err := sessions.Core().SessionStart(id)
	if err != nil {
		t.Fatal(err)
	}
	publicCurrent, err := manager.Current(publicCtx, restored)
	if err != nil || publicCurrent.Mode != "public" || publicCurrent.Kiosk != nil || publicCurrent.Salon == nil || publicCurrent.Salon.Slug != "first" || publicCurrent.Customer == nil || publicCurrent.Customer.ClientID != 77 {
		t.Fatalf("restored public=%+v err=%v", publicCurrent, err)
	}
	if _, err := manager.SelectSalon(context.Background(), restored, "second"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.StartCustomer(publicCtx, restored, "", true); err == nil {
		t.Fatal("old public scope header accepted after salon switch")
	}
	if _, _, err := manager.RequireCustomer(context.Background(), restored, publicCustomer.SessionID); err == nil {
		t.Fatal("old public customer accepted after salon switch")
	}
}
