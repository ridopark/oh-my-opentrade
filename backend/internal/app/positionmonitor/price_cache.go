package positionmonitor

import (
	"context"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// PriceCache implements ports.PriceCachePort by subscribing to MarketBarSanitized
// events and maintaining an in-memory last-price map.
type PriceCache struct {
	mu     sync.RWMutex
	prices map[domain.Symbol]ports.PriceSnapshot
	log    zerolog.Logger
	clock  func() time.Time
}

// PriceCacheOption is a functional option for PriceCache.
type PriceCacheOption func(*PriceCache)

// WithClock injects a deterministic clock for testing.
func WithClock(fn func() time.Time) PriceCacheOption {
	return func(pc *PriceCache) { pc.clock = fn }
}

// NewPriceCache creates a PriceCache.
func NewPriceCache(log zerolog.Logger, opts ...PriceCacheOption) *PriceCache {
	pc := &PriceCache{
		prices: make(map[domain.Symbol]ports.PriceSnapshot),
		log:    log.With().Str("component", "price_cache").Logger(),
		clock:  time.Now,
	}
	for _, opt := range opts {
		opt(pc)
	}
	return pc
}

// Start subscribes to MarketBarSanitized events to maintain the price cache.
func (pc *PriceCache) Start(ctx context.Context, eventBus ports.EventBusPort) error {
	return eventBus.Subscribe(ctx, domain.EventMarketBarSanitized, pc.handleBar)
}

// handleBar processes a MarketBarSanitized event and updates the cache.
func (pc *PriceCache) handleBar(_ context.Context, event domain.Event) error {
	bar, ok := event.Payload.(domain.MarketBar)
	if !ok {
		return nil
	}

	pc.mu.Lock()
	pc.prices[bar.Symbol] = ports.PriceSnapshot{
		Price:      bar.Close,
		ObservedAt: pc.clock(), // Use arrival time, not bar open time — 1-min bars arrive ~60s after bar.Time
	}
	pc.mu.Unlock()
	return nil
}

// LatestPrice returns the most recent cached price for a symbol.
func (pc *PriceCache) LatestPrice(symbol domain.Symbol) (ports.PriceSnapshot, bool) {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	snap, ok := pc.prices[symbol]
	return snap, ok
}

// UpdatePrice manually sets a price (useful for testing or broker-API fallback).
func (pc *PriceCache) UpdatePrice(symbol domain.Symbol, price float64, at time.Time) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.prices[symbol] = ports.PriceSnapshot{
		Price:      price,
		ObservedAt: at,
	}
}
