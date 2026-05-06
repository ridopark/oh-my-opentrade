package main

import (
	optadapter "github.com/oh-my-opentrade/backend/internal/adapters/options"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// newCachingOptionsMarket re-exports the shared caching decorator from
// internal/adapters/options. The single in-tree call site at main.go:514
// stays unchanged. See that package for the cache key shape.
func newCachingOptionsMarket(inner ports.OptionsMarketDataPort) ports.OptionsMarketDataPort {
	return optadapter.NewCachingMarket(inner)
}
