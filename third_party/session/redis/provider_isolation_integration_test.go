package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dronm/session"
	redisclient "github.com/redis/go-redis/v9"
)

// This test uses only fresh random namespaces on an explicitly selected local
// Redis server. Existing historical provider tests have separate prerequisites.
func TestManagersKeepRedisProvidersAndStoresIsolated(t *testing.T) {
	connection := os.Getenv("SESSION_TEST_REDIS_URL")
	if connection == "" {
		t.Skip("set SESSION_TEST_REDIS_URL to run Redis provider isolation checks")
	}
	options, err := redisclient.ParseURL(connection)
	if err != nil {
		t.Fatal("invalid test Redis URL")
	}
	host, _, err := net.SplitHostPort(options.Addr)
	if err != nil || host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
		t.Fatal("provider isolation test requires a loopback Redis server")
	}
	ctx := context.Background()
	inspector := redisclient.NewClient(options)
	t.Cleanup(func() { _ = inspector.Close() })
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "session-isolation:" + hex.EncodeToString(random[:])
	publicNamespace, adminNamespace := prefix+":public", prefix+":admin"
	t.Cleanup(func() {
		iterator := inspector.Scan(ctx, 0, prefix+":*", 0).Iterator()
		for iterator.Next(ctx) {
			_ = inspector.Del(ctx, iterator.Val()).Err()
		}
	})
	public, err := session.NewManager(PROVIDER, 60, 5, "", connection, publicNamespace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(public.CloseProvider)
	const sid = "11111111-1111-4111-8111-111111111111"
	publicStore, err := public.SessionStart(sid)
	if err != nil {
		t.Fatal(err)
	}
	if err := publicStore.Put("identity", "customer"); err != nil {
		t.Fatal(err)
	}

	// The first store must remain attached to its original provider after a
	// second manager is created, even when both managers use the same SID.
	admin, err := session.NewManager(PROVIDER, 120, 10, "", connection, adminNamespace)
	if err != nil {
		t.Fatal(err)
	}
	adminClosed := false
	t.Cleanup(func() {
		if !adminClosed {
			admin.CloseProvider()
		}
	})
	adminStore, err := admin.SessionStart(sid)
	if err != nil {
		t.Fatal(err)
	}
	if err := adminStore.Put("identity", "administrator"); err != nil {
		t.Fatal(err)
	}
	if public.GetMaxLifeTime() != 60 || public.GetMaxIdleTime() != 5 || admin.GetMaxLifeTime() != 120 || admin.GetMaxIdleTime() != 10 {
		t.Fatal("creating another manager changed an existing manager's lifetimes")
	}
	public.SetMaxLifeTime(75)
	public.SetMaxIdleTime(7)
	if admin.GetMaxLifeTime() != 120 || admin.GetMaxIdleTime() != 10 {
		t.Fatal("changing public lifetimes changed administrator lifetimes")
	}
	if err := publicStore.Put("identity", "customer"); err != nil {
		t.Fatal(err)
	}
	if publicStore.GetString("identity") != "customer" || adminStore.GetString("identity") != "administrator" {
		t.Fatal("held stores crossed Redis namespaces")
	}
	for _, check := range []struct {
		namespace string
		seconds   int64
	}{{publicNamespace, 75}, {adminNamespace, 120}} {
		ttl, err := inspector.TTL(ctx, check.namespace+":"+sid+":identity").Result()
		if err != nil || ttl < time.Duration(check.seconds-10)*time.Second || ttl > time.Duration(check.seconds)*time.Second {
			t.Fatalf("provider lifetime not applied independently: %v (%v)", ttl, err)
		}
	}
	created := time.Now().UTC().Truncate(time.Second)
	for _, store := range []session.Session{publicStore, adminStore} {
		for key, value := range map[string]any{"flag": true, "count": int64(17), "amount": 2.5, "time_created": created} {
			if err := store.Set(key, value); err != nil {
				t.Fatal(err)
			}
		}
		if store.GetBool("flag") != true || store.GetInt("count") != 17 || store.GetFloat("amount") != 2.5 || !store.TimeCreated().Equal(created) {
			t.Fatal("typed session reads did not use the owning provider")
		}
	}
	if err := publicStore.Delete("flag"); err != nil || publicStore.GetBool("flag") || !adminStore.GetBool("flag") {
		t.Fatal("deleting a public value changed the administrator store")
	}
	var wait sync.WaitGroup
	errors := make(chan error, 2)
	for _, item := range []struct {
		store session.Session
		value string
	}{{publicStore, "customer"}, {adminStore, "administrator"}} {
		wait.Add(1)
		go func(store session.Session, value string) {
			defer wait.Done()
			for index := 0; index < 20; index++ {
				if err := store.Put("concurrent", value); err != nil {
					errors <- err
					return
				}
				if store.GetString("concurrent") != value {
					errors <- fmt.Errorf("concurrent store read crossed namespaces")
					return
				}
			}
		}(item.store, item.value)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	admin.DestroyAllSessions(nil, session.LOG_LEVEL_ERROR)
	if publicStore.GetString("identity") != "customer" || adminStore.GetString("identity") != "" {
		t.Fatal("administrator namespace cleanup affected public sessions")
	}
	if err := adminStore.Put("identity", "administrator"); err != nil {
		t.Fatal(err)
	}
	publicRead, err := public.SessionStart(sid)
	if err != nil || publicRead.GetString("identity") != "customer" {
		t.Fatal("reopening a public session used another manager's provider")
	}
	admin.CloseProvider()
	adminClosed = true
	if err := adminStore.Put("closed", true); err == nil {
		t.Fatal("closing the administrator provider did not close its own connection")
	}
	if err := publicStore.Put("identity", "customer-after-admin-close"); err != nil || publicRead.GetString("identity") != "customer-after-admin-close" {
		t.Fatal("closing administrator connections broke the public manager")
	}
	if err := public.SessionDestroy(sid); err != nil {
		t.Fatal(err)
	}
	if publicStore.GetString("identity") != "" {
		t.Fatal("public session was not destroyed")
	}
	if exists, err := inspector.Exists(ctx, adminNamespace+":"+sid+":identity").Result(); err != nil || exists != 1 {
		t.Fatal("destroying the public session removed administrator data")
	}
}
