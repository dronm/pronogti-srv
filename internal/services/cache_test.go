package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/dronm/goyclients"
	"github.com/dronm/pronogti/internal/cache"
)

func cacheHitOnlyService(t *testing.T, store *cache.Cache) *YClientService {
	t.Helper()
	client := newTestYClient(t, nil, testHTTPClient(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unexpected upstream request: expected a cache hit")
	}))
	return NewYClientService(client, store)
}

func TestStaffCacheIsScopedByCompany(t *testing.T) {
	store := cache.New(time.Hour)
	for company, staffID := range map[int64]int64{1: 101, 2: 202} {
		encoded, err := json.Marshal([]goyclients.Staff{{ID: staffID}})
		if err != nil {
			t.Fatal(err)
		}
		store.Set(makeStaffListCacheKey(company), encoded)
	}
	service := cacheHitOnlyService(t, store)
	for company, staffID := range map[int64]int64{1: 101, 2: 202} {
		staff, err := service.FetchStaffList(context.Background(), company)
		if err != nil || len(staff) != 1 || staff[0].ID != staffID {
			t.Fatalf("company %d: staff=%v, error=%v", company, staff, err)
		}
	}
}

func TestClientPhoneCacheIsScopedByCompany(t *testing.T) {
	store := cache.New(time.Hour)
	const phone = "79991234567"
	for company, clientID := range map[int64]int64{1: 101, 2: 202} {
		// Date types use pointer-receiver JSON marshalers, matching the pointer
		// cached by FindClientByPhone after an upstream response.
		encoded, err := json.Marshal(&goyclients.Client{ID: clientID, Phone: phone})
		if err != nil {
			t.Fatal(err)
		}
		store.Set(makeClientLookupCacheKey(company, phone), encoded)
	}
	service := cacheHitOnlyService(t, store)
	for company, clientID := range map[int64]int64{1: 101, 2: 202} {
		client, err := service.FindClientByPhone(context.Background(), company, phone)
		if err != nil || client.ID != clientID {
			t.Fatalf("company %d: client=%v, error=%v", company, client, err)
		}
	}
}

func TestClientInvalidationPreservesOtherCompaniesAndCaches(t *testing.T) {
	store := cache.New(time.Hour)
	invalidated := []string{
		makeClientLookupCacheKey(1, "79991111111"),
		makeClientLookupCacheKey(1, "79992222222"),
	}
	preserved := []string{
		makeClientLookupCacheKey(10, "79991111111"),
		makeClientLookupCacheKey(2, "79991111111"),
		makeStaffListCacheKey(1),
	}
	for _, key := range append(invalidated, preserved...) {
		store.SetString(key, "cached value")
	}
	NewYClientService(nil, store).invalidateClientLookups(1)
	for _, key := range invalidated {
		if store.IsValid(key) {
			t.Errorf("client cache still valid: %s", key)
		}
	}
	for _, key := range preserved {
		if !store.IsValid(key) {
			t.Errorf("unrelated cache invalidated: %s", key)
		}
	}
}
