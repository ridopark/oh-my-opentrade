package ports

import (
	"context"
	"errors"
	"time"
)

// OptionQuote is a snapshot of bid/ask/last for a single option contract.
//
// Time-of-quote is preserved so callers can detect stale snapshots. All
// price fields are quoted in dollars per share (multiply by the contract
// multiplier — typically 100 — for total notional).
type OptionQuote struct {
	Symbol    string    // underlying root, e.g. "AAPL"
	Expiry    time.Time // contract expiry (UTC, day precision)
	Strike    float64
	Right     string // "C" or "P"
	Bid       float64
	Ask       float64
	Last      float64
	BidSize   int
	AskSize   int
	Timestamp time.Time
}

// OptionGreeks is a snapshot of analytics (IV plus the first-order Greeks)
// for a single option contract.
type OptionGreeks struct {
	IV              float64
	Delta           float64
	Gamma           float64
	Theta           float64
	Vega            float64
	Rho             float64
	UnderlyingPrice float64
	Timestamp       time.Time
}

// OptionMarketDataPort exposes per-contract option quote and Greeks
// snapshots. It is the read surface used by the debate service for IV
// derivation and by the broker for exit-pricing realism.
//
// Implementations should return ErrOptionDataNotConfigured when the
// adapter cannot serve the request for an environmental reason
// (typically missing API credentials) so callers can gracefully fall
// back to a synthetic source.
type OptionMarketDataPort interface {
	Quote(ctx context.Context, underlying string, expiry time.Time, strike float64, right string) (OptionQuote, error)
	Greeks(ctx context.Context, underlying string, expiry time.Time, strike float64, right string) (OptionGreeks, error)
}

// ErrOptionDataNotConfigured signals that the adapter is wired but lacks
// the credentials or upstream configuration required to answer. Callers
// must treat this as a soft fallback condition, not an error.
var ErrOptionDataNotConfigured = errors.New("option_market_data: adapter not configured")
