package ports

import (
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// PriceSnapshot holds a cached price with its observation time.
type PriceSnapshot struct {
	Price      float64
	ObservedAt time.Time
}

// PriceCachePort provides the latest known price for a symbol.
// Implementations subscribe to market data and maintain an in-memory cache.
type PriceCachePort interface {
	// LatestPrice returns the most recent price and observation time for a symbol.
	// Returns false if no price is available.
	LatestPrice(symbol domain.Symbol) (PriceSnapshot, bool)
	// UpdatePrice manually sets a price for a symbol (used for polling-based feeds
	// like options contracts that have no bar stream).
	UpdatePrice(symbol domain.Symbol, price float64, at time.Time)
}
