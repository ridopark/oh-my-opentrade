package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// cachingOptionsMarket wraps an OptionsMarketDataPort and caches results by
// (underlying, right). Expiry is ignored for the cache key because the backtest
// uses approximate current chain data anyway — fetching once per symbol+right
// prevents spamming the Alpaca API on every signal during replay.
type cachingOptionsMarket struct {
	inner ports.OptionsMarketDataPort
	mu    sync.Mutex
	cache map[string][]domain.OptionContractSnapshot
}

func newCachingOptionsMarket(inner ports.OptionsMarketDataPort) *cachingOptionsMarket {
	return &cachingOptionsMarket{
		inner: inner,
		cache: make(map[string][]domain.OptionContractSnapshot),
	}
}

func (c *cachingOptionsMarket) GetOptionChain(
	ctx context.Context,
	underlying domain.Symbol,
	expiry time.Time,
	right domain.OptionRight,
) ([]domain.OptionContractSnapshot, error) {
	key := fmt.Sprintf("%s:%s", underlying, right)

	c.mu.Lock()
	if cached, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()

	chain, err := c.inner.GetOptionChain(ctx, underlying, expiry, right)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.cache[key] = chain
	c.mu.Unlock()

	return chain, nil
}
