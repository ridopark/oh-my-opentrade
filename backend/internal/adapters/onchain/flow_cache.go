package onchain

import (
	"sync"
	"time"
)

const defaultCacheTTL = 5 * time.Minute

// flowCacheEntry stores a cached value with its expiration time.
type flowCacheEntry struct {
	value     any
	expiresAt time.Time
}

// flowCache is a simple thread-safe TTL cache for Dune query results.
// Keys follow the pattern "asset:windowHrs" or "asset:windowHrs:minUSD".
type flowCache struct {
	mu      sync.RWMutex
	entries map[string]flowCacheEntry
	ttl     time.Duration
	nowFn   func() time.Time // injectable clock for testing
}

// newFlowCache creates a cache with the given TTL. If ttl is zero,
// defaultCacheTTL (5 minutes) is used.
func newFlowCache(ttl time.Duration) *flowCache {
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	return &flowCache{
		entries: make(map[string]flowCacheEntry),
		ttl:     ttl,
		nowFn:   time.Now,
	}
}

// Get retrieves a cached value. Returns (value, true) on hit, (nil, false) on miss or expiry.
func (c *flowCache) Get(key string) (any, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
		return nil, false
	}
	if c.now().After(entry.expiresAt) {
		// Expired — lazy eviction on next Set or explicit Evict call.
		return nil, false
	}
	return entry.value, true
}

// Set stores a value in the cache with the configured TTL.
func (c *flowCache) Set(key string, value any) {
	c.mu.Lock()
	c.entries[key] = flowCacheEntry{
		value:     value,
		expiresAt: c.now().Add(c.ttl),
	}
	c.mu.Unlock()
}

// Evict removes expired entries. Callers may invoke periodically to prevent
// unbounded growth, though the cache is expected to be small (< 100 keys).
func (c *flowCache) Evict() {
	now := c.now()
	c.mu.Lock()
	for k, e := range c.entries {
		if now.After(e.expiresAt) {
			delete(c.entries, k)
		}
	}
	c.mu.Unlock()
}

// Len returns the number of entries (including potentially expired ones).
func (c *flowCache) Len() int {
	c.mu.RLock()
	n := len(c.entries)
	c.mu.RUnlock()
	return n
}

func (c *flowCache) now() time.Time {
	if c.nowFn != nil {
		return c.nowFn()
	}
	return time.Now()
}
