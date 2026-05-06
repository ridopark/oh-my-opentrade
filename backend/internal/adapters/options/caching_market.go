// Package options provides shared adapter decorators for OptionsMarketDataPort.
package options

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// cachingMarket wraps an OptionsMarketDataPort and caches results by
// (underlying, right, minDTE, maxDTE). The Alpaca adapter uses
// (minDTE, maxDTE) to compute expiryFrom/expiryTo before calling the
// REST API, so two calls with the same symbol+right but different DTE
// windows return different chains; the cache key must reflect that.
type cachingMarket struct {
	inner ports.OptionsMarketDataPort
	mu    sync.Mutex
	cache map[string][]domain.OptionContractSnapshot
}

// NewCachingMarket returns a thread-safe decorator that memoizes
// GetOptionChain by (symbol, right, minDTE, maxDTE). Intended for
// backtest runs to avoid re-hitting the upstream API on every signal.
func NewCachingMarket(inner ports.OptionsMarketDataPort) ports.OptionsMarketDataPort {
	return &cachingMarket{
		inner: inner,
		cache: make(map[string][]domain.OptionContractSnapshot),
	}
}

func (c *cachingMarket) GetOptionChain(
	ctx context.Context,
	underlying domain.Symbol,
	expiry time.Time,
	right domain.OptionRight,
	minDTE, maxDTE int,
) ([]domain.OptionContractSnapshot, error) {
	key := fmt.Sprintf("%s:%s:%d:%d", underlying, right, minDTE, maxDTE)

	c.mu.Lock()
	if cached, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()

	chain, err := c.inner.GetOptionChain(ctx, underlying, expiry, right, minDTE, maxDTE)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.cache[key] = chain
	c.mu.Unlock()

	return chain, nil
}
