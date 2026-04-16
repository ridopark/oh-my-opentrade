package simbroker

import (
	"fmt"
	"time"
)

// Bar carries the fill-relevant OHLC fields. Kept local so FillModel stays
// decoupled from the broader strategy.Bar type.
type Bar struct {
	Time  time.Time
	Open  float64
	High  float64
	Low   float64
	Close float64
}

// FillContext carries all inputs a FillModel needs to price a single order.
// NextBar is the first bar whose timestamp is at or after SubmitTime+Latency;
// nil when the runner has not plumbed a lookahead (legacy path) — models must
// degrade gracefully and synthesize a fill off CurrentBar in that case.
type FillContext struct {
	Symbol      string
	Side        string  // "BUY" | "SELL"
	Qty         float64
	OrderType   string  // "MKT" | "LMT"
	LimitPrice  float64
	CurrentBar  Bar
	NextBar     *Bar
	IsOption    bool
	SlippageBPS int64
	SubmitTime  time.Time
	LatencyMs   int
}

// FillResult is what a FillModel returns. When Filled is false the order did
// not trigger this bar — callers treat it as a working order and may resubmit.
type FillResult struct {
	Filled bool
	Price  float64
	At     time.Time
	Reason string // populated when !Filled for diagnostics
}

// FillModel is the strategy-agnostic pricing plug. Implementations must be
// stateless — all inputs arrive via FillContext — so multiple goroutines can
// share a single instance safely.
type FillModel interface {
	Name() string
	FillPrice(ctx FillContext) (FillResult, error)
}

// --- Optimistic -----------------------------------------------------------

// OptimisticFillModel preserves the pre-Sprint-7 behavior: every order fills
// immediately at CurrentBar.Close ± slippage. Kept as the default so existing
// backtests whose numbers were calibrated against instant fills do not drift.
type OptimisticFillModel struct{}

// Name returns the canonical identifier used in config.
func (OptimisticFillModel) Name() string { return "optimistic" }

// FillPrice fills market orders at current close ± slippage and limit orders
// at their limit price (exec assumed instant at the posted limit).
func (m OptimisticFillModel) FillPrice(c FillContext) (FillResult, error) {
	px := c.CurrentBar.Close
	if px <= 0 {
		return FillResult{}, fmt.Errorf("optimistic: no current close for %s", c.Symbol)
	}
	slip := px * float64(c.SlippageBPS) / 10000.0
	switch c.OrderType {
	case "LMT":
		if c.LimitPrice <= 0 {
			return FillResult{}, fmt.Errorf("optimistic: limit order missing limit price for %s", c.Symbol)
		}
		px = c.LimitPrice
	case "MKT", "":
		if isBuy(c.Side) {
			px += slip
		} else {
			px -= slip
		}
	default:
		return FillResult{}, fmt.Errorf("optimistic: unsupported order type %q", c.OrderType)
	}
	return FillResult{Filled: true, Price: px, At: c.CurrentBar.Time}, nil
}

// --- Realistic ------------------------------------------------------------

// RealisticFillModel simulates exchange round-trip: market orders wait for the
// next bar's open (plus one-way slippage); limits fill at the limit only if
// the next bar trades through it, otherwise they remain working. When NextBar
// is unavailable (legacy caller) the model falls back to CurrentBar.Close and
// still applies next-bar slippage so callers see some adverse selection.
type RealisticFillModel struct{}

// Name returns the canonical identifier used in config.
func (RealisticFillModel) Name() string { return "realistic" }

// FillPrice fills market orders at the next-bar open and limit orders only
// when the next bar prints through the limit; returns Filled=false otherwise.
func (m RealisticFillModel) FillPrice(c FillContext) (FillResult, error) {
	basis, at := nextBarOrFallback(c)
	if basis <= 0 {
		return FillResult{}, fmt.Errorf("realistic: no fill basis for %s", c.Symbol)
	}
	slip := basis * float64(c.SlippageBPS) / 10000.0
	switch c.OrderType {
	case "LMT":
		if c.LimitPrice <= 0 {
			return FillResult{}, fmt.Errorf("realistic: limit order missing limit price for %s", c.Symbol)
		}
		if c.NextBar != nil {
			if limitTriggered(c.NextBar, c.Side, c.LimitPrice) {
				return FillResult{Filled: true, Price: c.LimitPrice, At: c.NextBar.Time}, nil
			}
			// Favorable open: fill at open only if better than limit for our side.
			if openFavorable(c.NextBar.Open, c.Side, c.LimitPrice) {
				return FillResult{Filled: true, Price: c.NextBar.Open, At: c.NextBar.Time}, nil
			}
			return FillResult{Filled: false, At: c.NextBar.Time, Reason: "limit not triggered"}, nil
		}
		// Legacy path without lookahead: accept the limit if the current close
		// is at or better than the posted limit for the order's side.
		if openFavorable(c.CurrentBar.Close, c.Side, c.LimitPrice) {
			return FillResult{Filled: true, Price: c.LimitPrice, At: at}, nil
		}
		return FillResult{Filled: false, At: at, Reason: "limit not triggered (legacy)"}, nil
	case "MKT", "":
		if isBuy(c.Side) {
			return FillResult{Filled: true, Price: basis + slip, At: at}, nil
		}
		return FillResult{Filled: true, Price: basis - slip, At: at}, nil
	default:
		return FillResult{}, fmt.Errorf("realistic: unsupported order type %q", c.OrderType)
	}
}

// --- Pessimistic ----------------------------------------------------------

// PessimisticFillModel is RealisticFillModel with an additional adverse slip
// multiplier applied to market orders — used for sensitivity analysis / worst-
// case P&L bounds. Limit orders behave the same as Realistic.
type PessimisticFillModel struct {
	SlippageMultiplier float64 // applied on top of SlippageBPS; default 2.0
}

// Name returns the canonical identifier used in config.
func (PessimisticFillModel) Name() string { return "pessimistic" }

// FillPrice adds a configurable extra slippage buffer to every market order
// on top of the realistic next-bar-open fill price.
func (m PessimisticFillModel) FillPrice(c FillContext) (FillResult, error) {
	mult := m.SlippageMultiplier
	if mult <= 0 {
		mult = 2.0
	}
	basis, at := nextBarOrFallback(c)
	if basis <= 0 {
		return FillResult{}, fmt.Errorf("pessimistic: no fill basis for %s", c.Symbol)
	}
	slip := basis * float64(c.SlippageBPS) / 10000.0 * mult
	switch c.OrderType {
	case "LMT":
		// Limits still fill at the posted limit when triggered — no extra skid,
		// because a resting limit either clears at its price or does not.
		return RealisticFillModel{}.FillPrice(c)
	case "MKT", "":
		if isBuy(c.Side) {
			return FillResult{Filled: true, Price: basis + slip, At: at}, nil
		}
		return FillResult{Filled: true, Price: basis - slip, At: at}, nil
	default:
		return FillResult{}, fmt.Errorf("pessimistic: unsupported order type %q", c.OrderType)
	}
}

// --- helpers --------------------------------------------------------------

func isBuy(side string) bool {
	return side == "BUY" || side == "buy"
}

// nextBarOrFallback returns the price to base the fill on (next bar open when
// available, otherwise current close) along with the best-known fill timestamp.
func nextBarOrFallback(c FillContext) (float64, time.Time) {
	if c.NextBar != nil && c.NextBar.Open > 0 {
		return c.NextBar.Open, c.NextBar.Time
	}
	return c.CurrentBar.Close, c.CurrentBar.Time
}

// limitTriggered reports whether the given bar traded through the limit in a
// direction that would fill the order.
func limitTriggered(b *Bar, side string, limit float64) bool {
	if b == nil {
		return false
	}
	if isBuy(side) {
		return b.Low <= limit
	}
	return b.High >= limit
}

// openFavorable reports whether the open price is at least as good as the
// limit for the order's side — used when we can't see a full OHLC bar.
func openFavorable(open float64, side string, limit float64) bool {
	if isBuy(side) {
		return open <= limit
	}
	return open >= limit
}

// FillModelByName maps a config string to a FillModel. Returns an error for
// unknown names so operators see typos instead of silent fallback.
func FillModelByName(name string, pessimisticMult float64) (FillModel, error) {
	switch name {
	case "", "optimistic":
		return OptimisticFillModel{}, nil
	case "realistic":
		return RealisticFillModel{}, nil
	case "pessimistic":
		return PessimisticFillModel{SlippageMultiplier: pessimisticMult}, nil
	default:
		return nil, fmt.Errorf("simbroker: unknown fill_model %q", name)
	}
}
