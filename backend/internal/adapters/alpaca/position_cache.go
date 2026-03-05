package alpaca

import (
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

type positionCache struct {
	mu        sync.RWMutex
	positions []domain.Trade
	tenantID  string
	envMode   domain.EnvMode
	fetchedAt time.Time
	ttl       time.Duration
}

func newPositionCache(ttl time.Duration) *positionCache {
	if ttl <= 0 {
		ttl = 2 * time.Second
	}
	return &positionCache{ttl: ttl}
}

func (c *positionCache) Get(tenantID string, envMode domain.EnvMode) ([]domain.Trade, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.tenantID == "" {
		return nil, false
	}
	if c.tenantID != tenantID || c.envMode != envMode {
		return nil, false
	}
	if time.Since(c.fetchedAt) > c.ttl {
		return nil, false
	}
	if c.positions == nil {
		return nil, false
	}
	out := make([]domain.Trade, len(c.positions))
	copy(out, c.positions)
	return out, true
}

func (c *positionCache) Set(tenantID string, envMode domain.EnvMode, positions []domain.Trade) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.tenantID = tenantID
	c.envMode = envMode
	c.fetchedAt = time.Now()

	if positions == nil {
		c.positions = nil
		return
	}
	c.positions = make([]domain.Trade, len(positions))
	copy(c.positions, positions)
}

func (c *positionCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.positions = nil
	c.tenantID = ""
	c.envMode = ""
	c.fetchedAt = time.Time{}
}
