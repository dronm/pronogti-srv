// Package cache provides a simple in-memory concurrent cache for a web application.
// Values can be set, retrieved, and explicitly invalidated.
// Invalidated keys are retained in the cache and marked as empty rather than deleted.
package cache

import (
	"sync"
	"time"
)

type entry struct {
	value []byte
	valid bool
	date  time.Time
}

type Cache struct {
	mu           sync.RWMutex
	data         map[string]entry
	maxCacheTime time.Duration
}

func New(cacheTime time.Duration) *Cache {
	return &Cache{
		data:         make(map[string]entry),
		maxCacheTime: cacheTime,
	}
}

// Set stores or overwrites the value for a key and marks it valid.
func (c *Cache) Set(key string, value []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// copy to avoid external mutation after Set
	buf := append([]byte(nil), value...)

	c.data[key] = entry{
		value: buf,
		valid: true,
		date:  time.Now(),
	}
}

// SetString is a convenience helper.
func (c *Cache) SetString(key string, value string) {
	c.Set(key, []byte(value))
}

// Invalidate keeps the key but empties its value and marks it invalid.
func (c *Cache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.data[key]; !exists {
		return
	}

	c.data[key] = entry{
		value: nil,
		valid: false,
		date:  time.Now(),
	}
}

func (c *Cache) isEntryValid(e entry, now time.Time) bool {
	if !e.valid {
		return false
	}

	if c.maxCacheTime <= 0 {
		return true
	}

	return now.Sub(e.date) <= c.maxCacheTime
}

// Get returns:
// - value
// - exists: whether the key has ever been created
// - valid: whether the key currently has a valid value
func (c *Cache) Get(key string) ([]byte, bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	e, exists := c.data[key]
	if !exists {
		return nil, false, false
	}

	if !c.isEntryValid(e, time.Now()) {
		return nil, true, false
	}

	buf := append([]byte(nil), e.value...)
	return buf, true, true
}

func (c *Cache) GetString(key string) (string, bool, bool) {
	value, exists, valid := c.Get(key)
	if !exists || !valid {
		return "", exists, valid
	}

	return string(value), true, true
}

// Has reports whether the key exists at all, even if invalidated.
func (c *Cache) Has(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	_, exists := c.data[key]
	return exists
}

// IsValid reports whether the key exists and currently has a valid value.
func (c *Cache) IsValid(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	e, exists := c.data[key]
	if !exists {
		return false
	}

	return c.isEntryValid(e, time.Now())
}

// Keys returns a snapshot of all keys currently known to the cache.
func (c *Cache) Keys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	keys := make([]string, 0, len(c.data))
	for k := range c.data {
		keys = append(keys, k)
	}

	return keys
}
