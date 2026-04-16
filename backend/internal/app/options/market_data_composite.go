package options

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// CompositeOptionMarketData fans out a single OptionMarketDataPort call
// across an ordered chain of underlying providers. The first source that
// returns a non-error result wins; ports.ErrOptionDataNotConfigured and
// any other error skip to the next source. When every source errors,
// the aggregate error is returned so callers can choose to fall back to
// a synthetic source (BSM/ATR-derived IV).
//
// Typical wiring once Theta Data is provisioned:
//
//	composite := NewComposite(theta, alpacaSnap, doltHist)
//
// Any nil source is silently skipped, so bootstrap can pass a nil Theta
// client when the API key is unset.
type CompositeOptionMarketData struct {
	sources []namedSource
	log     zerolog.Logger
}

type namedSource struct {
	name string
	port ports.OptionMarketDataPort
}

// NewComposite returns a composite that walks primary → fallbacks in
// order. Pass nil for any slot to skip it.
func NewComposite(primary ports.OptionMarketDataPort, fallbacks ...ports.OptionMarketDataPort) *CompositeOptionMarketData {
	return NewCompositeWithLogger(zerolog.Nop(), primary, fallbacks...)
}

// NewCompositeWithLogger is like NewComposite but binds a logger so each
// satisfied call records which source answered. Useful in production.
func NewCompositeWithLogger(log zerolog.Logger, primary ports.OptionMarketDataPort, fallbacks ...ports.OptionMarketDataPort) *CompositeOptionMarketData {
	c := &CompositeOptionMarketData{log: log.With().Str("component", "option_market_data_composite").Logger()}
	c.append("primary", primary)
	for i, f := range fallbacks {
		c.append(fmt.Sprintf("fallback%d", i+1), f)
	}
	return c
}

func (c *CompositeOptionMarketData) append(name string, p ports.OptionMarketDataPort) {
	if p == nil {
		return
	}
	c.sources = append(c.sources, namedSource{name: name, port: p})
}

// Quote walks the source chain.
func (c *CompositeOptionMarketData) Quote(ctx context.Context, underlying string, expiry time.Time, strike float64, right string) (ports.OptionQuote, error) {
	if len(c.sources) == 0 {
		return ports.OptionQuote{}, ports.ErrOptionDataNotConfigured
	}
	var errs []error
	for _, s := range c.sources {
		q, err := s.port.Quote(ctx, underlying, expiry, strike, right)
		if err == nil {
			c.log.Debug().Str("source", s.name).Str("op", "quote").Msg("served by")
			return q, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", s.name, err))
	}
	return ports.OptionQuote{}, errors.Join(errs...)
}

// Greeks walks the source chain.
func (c *CompositeOptionMarketData) Greeks(ctx context.Context, underlying string, expiry time.Time, strike float64, right string) (ports.OptionGreeks, error) {
	if len(c.sources) == 0 {
		return ports.OptionGreeks{}, ports.ErrOptionDataNotConfigured
	}
	var errs []error
	for _, s := range c.sources {
		g, err := s.port.Greeks(ctx, underlying, expiry, strike, right)
		if err == nil {
			c.log.Debug().Str("source", s.name).Str("op", "greeks").Msg("served by")
			return g, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", s.name, err))
	}
	return ports.OptionGreeks{}, errors.Join(errs...)
}

// Compile-time interface assertion.
var _ ports.OptionMarketDataPort = (*CompositeOptionMarketData)(nil)
