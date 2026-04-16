package ports

import (
	"context"
	"errors"
	"time"
)

// ErrCorporateActionsNotImplemented is returned by adapters that have not
// yet wired a real feed (e.g. the IBKR stub). Callers should treat this as
// "no data" rather than a hard failure so the composite chain can fall back
// to other sources.
var ErrCorporateActionsNotImplemented = errors.New("ports: corporate actions not implemented")

// CorporateAction describes a single corporate event affecting a symbol.
//
// For forward splits (ActionType == "split"), RatioNumerator is the number
// of post-action shares per 1 pre-action share (e.g. 4 for a 4-for-1 split)
// and RatioDenominator is 1. For reverse splits, RatioNumerator is 1 and
// RatioDenominator is the consolidation factor (e.g. Num=1, Denom=5 for a
// 1-for-5 reverse split). The effective adjustment factor is Num/Denom.
//
// For dividends, CashComponent holds the per-share cash amount.
type CorporateAction struct {
	Symbol           string
	ActionType       string // 'split' | 'reverse_split' | 'dividend' | 'delisting' | 'merger'
	EffectiveDate    time.Time
	RatioNumerator   float64
	RatioDenominator float64
	CashComponent    float64
	Source           string // 'ibkr' | 'alpaca' | 'manual'
}

// AdjustmentFactor returns RatioNumerator / RatioDenominator with guards.
// Returns 1.0 when the ratio is malformed so that callers multiplying a
// pre-action value by the factor degrade to a no-op rather than NaN.
func (c CorporateAction) AdjustmentFactor() float64 {
	if c.RatioDenominator == 0 {
		return 1.0
	}
	f := c.RatioNumerator / c.RatioDenominator
	if f <= 0 {
		return 1.0
	}
	return f
}

// CorporateActionsPort is the domain-facing interface for corporate-action
// lookups. Implementations are expected to be side-effect free beyond the
// Upsert method.
type CorporateActionsPort interface {
	// Between returns all corporate actions for symbol whose effective_date
	// falls in [from, to] (inclusive on both ends). Results are ordered by
	// effective_date ascending so callers can apply them in chronological
	// order.
	Between(ctx context.Context, symbol string, from, to time.Time) ([]CorporateAction, error)

	// Delisted reports whether the symbol has a delisting action with an
	// effective_date <= asOf.
	Delisted(ctx context.Context, symbol string, asOf time.Time) (bool, error)

	// Upsert stores or replaces a corporate action keyed by
	// (symbol, effective_date, action_type).
	Upsert(ctx context.Context, ca CorporateAction) error
}
