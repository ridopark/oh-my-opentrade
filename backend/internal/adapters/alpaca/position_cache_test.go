package alpaca

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestPositionCache_HitWithinTTL(t *testing.T) {
	c := newPositionCache(50 * time.Millisecond)
	pos := []domain.Trade{{TenantID: "t1", EnvMode: domain.EnvModePaper, Side: "long", Status: "open"}}
	c.Set("t1", domain.EnvModePaper, pos)

	out, ok := c.Get("t1", domain.EnvModePaper)
	assert.True(t, ok)
	assert.Len(t, out, 1)
}

func TestPositionCache_MissAfterTTL(t *testing.T) {
	c := newPositionCache(10 * time.Millisecond)
	c.Set("t1", domain.EnvModePaper, []domain.Trade{{TenantID: "t1", EnvMode: domain.EnvModePaper}})
	time.Sleep(20 * time.Millisecond)

	_, ok := c.Get("t1", domain.EnvModePaper)
	assert.False(t, ok)
}

func TestPositionCache_MissDifferentTenantOrEnv(t *testing.T) {
	c := newPositionCache(50 * time.Millisecond)
	c.Set("t1", domain.EnvModePaper, []domain.Trade{{TenantID: "t1", EnvMode: domain.EnvModePaper}})

	_, ok := c.Get("t2", domain.EnvModePaper)
	assert.False(t, ok)

	_, ok = c.Get("t1", domain.EnvModeLive)
	assert.False(t, ok)
}

func TestPositionCache_InvalidateClearsCache(t *testing.T) {
	c := newPositionCache(50 * time.Millisecond)
	c.Set("t1", domain.EnvModePaper, []domain.Trade{{TenantID: "t1", EnvMode: domain.EnvModePaper}})
	c.Invalidate()

	_, ok := c.Get("t1", domain.EnvModePaper)
	assert.False(t, ok)
}
