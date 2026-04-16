package domain

import (
	"context"
	"time"
)

// HistoricalOptionChainRow represents a single option contract snapshot from
// historical data (e.g., DoltHub). Each row corresponds to one strike/expiry/right
// combination for a given underlying on a given date.
type HistoricalOptionChainRow struct {
	Date       time.Time
	Symbol     Symbol      // underlying symbol (e.g., "HIMS")
	Expiration time.Time
	Strike     float64
	Right      OptionRight // CALL or PUT
	Bid        float64
	Ask        float64
	IV         float64 // implied volatility as decimal (e.g., 0.35 = 35%)
	Delta      float64
	Gamma      float64
	Theta      float64
	Vega       float64
	Rho        float64

	// Multiplier is the OCC contract multiplier (default 100). Kept as a
	// field so future non-standard adjustments (e.g. special dividends
	// that shrink multiplier to 99 temporarily) can be modeled without
	// touching the strike. Zero is treated as 100 by consumers.
	Multiplier float64

	// PostDelisting is true for rows whose Date falls strictly after the
	// underlying's delisting effective date. Set by ApplyCorporateActions.
	// Backtest code should skip rows where this is true.
	PostDelisting bool
}

// Mid returns the bid-ask midpoint price.
func (r HistoricalOptionChainRow) Mid() float64 {
	if r.Bid > 0 && r.Ask > 0 {
		return (r.Bid + r.Ask) / 2
	}
	if r.Ask > 0 {
		return r.Ask
	}
	return r.Bid
}

// IsPostDelisting reports whether the row is past the underlying's
// delisting date. A helper on the row value keeps the check close to the
// data so higher layers do not need to re-derive it.
func (r HistoricalOptionChainRow) IsPostDelisting() bool {
	return r.PostDelisting
}

// CorporateActionView is the read surface the chain needs from a
// corporate-actions source. Declared in domain rather than imported from
// ports so the domain layer stays dependency-free; the concrete
// ports.CorporateAction struct satisfies this via an adapter shim in the
// caller (see app layer).
type CorporateActionView struct {
	ActionType       string
	EffectiveDate    time.Time
	RatioNumerator   float64
	RatioDenominator float64
}

// AdjustmentFactor returns Num/Denom with a 1.0 fallback when the ratio is
// malformed.
func (v CorporateActionView) AdjustmentFactor() float64 {
	if v.RatioDenominator == 0 {
		return 1.0
	}
	f := v.RatioNumerator / v.RatioDenominator
	if f <= 0 {
		return 1.0
	}
	return f
}

// CorporateActionsLookup is the minimal behavior the chain needs. The app
// layer wraps ports.CorporateActionsPort to produce one of these.
type CorporateActionsLookup interface {
	Between(ctx context.Context, symbol string, from, to time.Time) ([]CorporateActionView, error)
	Delisted(ctx context.Context, symbol string, asOf time.Time) (bool, error)
}

// HistoricalOptionChain is a slice of chain rows ordered by Date ascending.
// Attaching the slice type lets us hang methods like ApplyCorporateActions
// off it without introducing a package-level helper.
type HistoricalOptionChain []HistoricalOptionChainRow

// ApplyCorporateActions retroactively adjusts strikes on pre-split rows so
// backtests run in post-split coordinates. This follows OCC Rule 809 for
// standard forward/reverse splits (see
// https://infomemo.theocc.com/infomemos/search — "Contract Adjustments
// Pursuant to Rule 809"): the strike is divided by the adjustment factor
// f = RatioNumerator / RatioDenominator (e.g. f=4 for a 4-for-1 split), the
// quantity is multiplied by f, and the contract multiplier is unchanged at
// 100 because the notional (strike*multiplier*qty) is preserved.
//
// Example: pre-split AAPL 500C becomes AAPL 125C (strike/4) after a 4-for-1
// split effective on effDate. Rows with Date < effDate get their strike
// divided by 4; rows with Date >= effDate are already expressed in
// post-split coordinates and are left alone. Multiple actions in the same
// window are applied in chronological order so cascaded splits compound
// correctly.
//
// The chain's Multiplier field is stamped to 100 where currently zero so
// callers can rely on a concrete value after this call. Dividend and
// merger adjustments are intentionally not implemented here — those require
// strike-reduction rules that depend on ex-div price and are deferred to
// Track D of the equity-options gap plan (G12).
//
// Delisting handling: if the port reports the underlying delisted as of
// the latest Date in the chain, every row whose Date is strictly after the
// delisting effective date is flagged PostDelisting=true so backtests can
// filter them out.
func (c HistoricalOptionChain) ApplyCorporateActions(ctx context.Context, port CorporateActionsLookup) error {
	if len(c) == 0 || port == nil {
		return nil
	}

	// Span covered by this chain. Rows are expected to be ordered by Date
	// but we don't rely on it — derive the [from, to] window explicitly.
	from, to := c[0].Date, c[0].Date
	for i := range c {
		d := c[i].Date
		if d.Before(from) {
			from = d
		}
		if d.After(to) {
			to = d
		}
		if c[i].Multiplier == 0 {
			c[i].Multiplier = 100
		}
	}

	symbol := string(c[0].Symbol)
	actions, err := port.Between(ctx, symbol, from, to)
	if err != nil {
		return err
	}

	// Apply each split in chronological order. For each action with
	// effective date E and factor f, every row with Date < E is pre-split
	// and its strike is divided by f. Reverse splits (f<1) are handled by
	// the same transform — a 1-for-5 reverse split has f=0.2, so the
	// pre-split strike*5 becomes strike (i.e., strike/0.2).
	for _, a := range actions {
		if a.ActionType != "split" && a.ActionType != "reverse_split" {
			continue
		}
		f := a.AdjustmentFactor()
		if f == 1.0 || f <= 0 {
			continue
		}
		for i := range c {
			if c[i].Date.Before(a.EffectiveDate) {
				c[i].Strike /= f
			}
		}
	}

	// Delisting flag. Look up once using the latest row date so backtests
	// that run past the delisting can discard those bars.
	delisted, err := port.Delisted(ctx, symbol, to)
	if err != nil {
		return err
	}
	if delisted {
		// Find the earliest delisting action inside the window; rows after
		// it are post-delisting.
		var delistDate time.Time
		for _, a := range actions {
			if a.ActionType != "delisting" {
				continue
			}
			if delistDate.IsZero() || a.EffectiveDate.Before(delistDate) {
				delistDate = a.EffectiveDate
			}
		}
		if !delistDate.IsZero() {
			for i := range c {
				if c[i].Date.After(delistDate) {
					c[i].PostDelisting = true
				}
			}
		}
	}

	return nil
}
