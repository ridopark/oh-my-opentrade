package onchain

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlowCache_SetGet(t *testing.T) {
	c := newFlowCache(time.Minute)

	c.Set("btc:24", "result-1")
	val, ok := c.Get("btc:24")
	require.True(t, ok)
	assert.Equal(t, "result-1", val)
}

func TestFlowCache_Miss(t *testing.T) {
	c := newFlowCache(time.Minute)

	_, ok := c.Get("nonexistent")
	assert.False(t, ok)
}

func TestFlowCache_TTLExpiry(t *testing.T) {
	c := newFlowCache(time.Minute)

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c.nowFn = func() time.Time { return now }

	c.Set("btc:24", "result-1")

	// Still valid at now + 30s.
	c.nowFn = func() time.Time { return now.Add(30 * time.Second) }
	val, ok := c.Get("btc:24")
	require.True(t, ok)
	assert.Equal(t, "result-1", val)

	// Expired at now + 61s.
	c.nowFn = func() time.Time { return now.Add(61 * time.Second) }
	_, ok = c.Get("btc:24")
	assert.False(t, ok)
}

func TestFlowCache_Evict(t *testing.T) {
	c := newFlowCache(time.Minute)

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c.nowFn = func() time.Time { return now }

	c.Set("a", 1)
	c.Set("b", 2)
	assert.Equal(t, 2, c.Len())

	// Advance past TTL and evict.
	c.nowFn = func() time.Time { return now.Add(2 * time.Minute) }
	c.Evict()
	assert.Equal(t, 0, c.Len())
}

func TestFlowCache_DefaultTTL(t *testing.T) {
	c := newFlowCache(0) // should default to 5 minutes
	assert.Equal(t, defaultCacheTTL, c.ttl)
}

func TestFlowCache_Overwrite(t *testing.T) {
	c := newFlowCache(time.Minute)

	c.Set("key", "v1")
	c.Set("key", "v2")
	val, ok := c.Get("key")
	require.True(t, ok)
	assert.Equal(t, "v2", val)
}

func TestFlowCache_ThreadSafety(t *testing.T) {
	c := newFlowCache(time.Minute)
	const goroutines = 50
	const ops = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				key := fmt.Sprintf("key-%d-%d", id, i)
				c.Set(key, i)
				c.Get(key)
			}
		}(g)
	}

	wg.Wait()
	// If we reach here without data race, the test passes.
	assert.Greater(t, c.Len(), 0)
}
