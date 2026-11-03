package cache

import (
	"testing"
	"time"
)

func TestCacheCopiesValuesAndInvalidation(t *testing.T) {
	cache := New(time.Hour)
	source := []byte("value")
	cache.Set("key", source)
	source[0] = 'X'
	value, exists, valid := cache.Get("key")
	if !exists || !valid || string(value) != "value" {
		t.Fatal("cache retained the caller's mutable buffer")
	}
	value[0] = 'Y'
	again, _, _ := cache.Get("key")
	if string(again) != "value" {
		t.Fatal("cache exposed its internal buffer")
	}
	cache.Invalidate("key")
	if value, exists, valid := cache.Get("key"); !exists || valid || value != nil {
		t.Fatal("invalidated cache entry was returned as valid")
	}
}

func TestCacheExpiry(t *testing.T) {
	cache := New(time.Hour)
	cache.SetString("key", "value")
	entry := cache.data["key"]
	entry.date = time.Now().Add(-2 * time.Hour)
	cache.data["key"] = entry
	if _, exists, valid := cache.Get("key"); !exists || valid {
		t.Fatal("expired cache entry was returned as valid")
	}
}
