// Package backtest — synthetic_options_chain.go fills gaps in the DoltHub-
// sourced historical chain with a synthetic chain generated from Black-Scholes.
//
// DoltHub's options_chain table is heavy on monthlies and sparse on weeklies,
// so weekly-biased strategies (e.g. macd_only_v1 with DTE 5-14) select zero
// contracts and emit zero trades during HTTP backtests. This package generates
// a plausible chain on demand — weekly expiries, a dense strike grid around
// spot, and BSM-priced bid/ask/Greeks — so the selector has something to pick.
//
// Live trading is unaffected: the Alpaca live chain already has weeklies.
// This path runs only when HistoricalOptionsAdapter finds no cached/DB rows
// for the requested (symbol, date, right).
package backtest

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/options"
	"github.com/oh-my-opentrade/backend/internal/domain"
)
// SyntheticChainConfig tunes the synthetic chain generator. All fields have
// safe defaults applied by config.applyBacktestDefaults so bootstrap wiring
// never has to hand-populate this struct.
type SyntheticChainConfig struct {
	Enabled         bool    // master kill switch; false reproduces today's behavior
	StrikeGridPct   float64 // spot +/- this fraction defines the strike window
	StrikeStepPct   float64 // strike step as fraction of spot (pre-rounding)
	IVDefault       float64 // fallback IV when no iv_snapshots row is available
	RiskFreeRate    float64 // flat across term structure; good enough for v1
	BidAskSpreadPct float64 // total spread as fraction of mid (split evenly bid/ask)
}
// SpotProvider returns the underlying's spot price on the given date. The
// generator returns empty when this is zero — it cannot produce a chain
// without a spot anchor.
type SpotProvider func(ctx context.Context, symbol domain.Symbol, asOf time.Time) (float64, error)
// IVProvider returns the ATM implied volatility for the symbol on the given
// date. Returning 0 tells the generator to fall back to cfg.IVDefault.
type IVProvider func(ctx context.Context, symbol domain.Symbol, asOf time.Time) (float64, error)
// SyntheticChainGenerator produces plausible option chain snapshots via BSM
// pricing. The value is immutable after construction and safe for concurrent
// use — all state lives on the stack of each GenerateChain call.
type SyntheticChainGenerator struct {
	cfg  SyntheticChainConfig
	spot SpotProvider
	iv   IVProvider
}
// NewSyntheticChainGenerator wires a generator with its data providers.
// A nil ivProvider is allowed and always falls through to cfg.IVDefault.
// A nil spotProvider is treated as "no spot available" — GenerateChain will
// always return empty, which the caller interprets as "synthetic unavailable".
func NewSyntheticChainGenerator(cfg SyntheticChainConfig, spot SpotProvider, iv IVProvider) *SyntheticChainGenerator {
	return &SyntheticChainGenerator{cfg: cfg, spot: spot, iv: iv}
}
// GenerateChain produces synthetic option snapshots for the given symbol,
// as-of date, right, and DTE window. Returns nil on configuration-disabled,
// missing spot, or an empty DTE window — none of these are errors from the
// caller's perspective, they just mean "no synthetic chain available".
func (g *SyntheticChainGenerator) GenerateChain(
	ctx context.Context,
	symbol domain.Symbol,
	asOf time.Time,
	right domain.OptionRight,
	minDTE, maxDTE int,
) ([]domain.OptionContractSnapshot, error) {
	if !g.cfg.Enabled {
		return nil, nil
	}
	if minDTE < 0 {
		minDTE = 0
	}
	if maxDTE < minDTE {
		return nil, nil
	}
	spot := 0.0
	if g.spot != nil {
		s, err := g.spot(ctx, symbol, asOf)
		if err != nil {
			return nil, fmt.Errorf("synthetic_chain: spot lookup: %w", err)
		}
		spot = s
	}
	if spot <= 0 {
		return nil, nil
	}
	iv := 0.0
	if g.iv != nil {
		v, err := g.iv(ctx, symbol, asOf)
		if err == nil {
			iv = v
		}
	}
	if iv <= 0 {
		iv = g.cfg.IVDefault
	}
	if iv <= 0 {
		return nil, nil
	}
	expiries := weeklyExpiries(asOf, minDTE, maxDTE)
	if len(expiries) == 0 {
		return nil, nil
	}
	strikes := strikeGrid(spot, g.cfg.StrikeGridPct, g.cfg.StrikeStepPct)
	if len(strikes) == 0 {
		return nil, nil
	}
	isCall := right == domain.OptionRightCall
	halfSpread := g.cfg.BidAskSpreadPct / 2.0
	out := make([]domain.OptionContractSnapshot, 0, len(expiries)*len(strikes))
	for _, exp := range expiries {
		dteYears := float64(daysBetween(asOf, exp)) / 365.0
		if dteYears <= 0 {
			continue
		}
		for _, k := range strikes {
			price, delta, gamma, thetaDay := options.BSMPrice(spot, k, dteYears, g.cfg.RiskFreeRate, iv, isCall)
			if price <= 0 {
				continue
			}
			vega := bsmVega(spot, k, dteYears, g.cfg.RiskFreeRate, iv)
			bid := price * (1 - halfSpread)
			ask := price * (1 + halfSpread)
			if bid < 0.01 {
				bid = 0.01
			}
			if ask < bid+0.01 {
				ask = bid + 0.01
			}
			contract, err := domain.NewOptionContract(string(symbol), exp, k, right, domain.OptionStyleAmerican)
			if err != nil {
				continue
			}
			out = append(out, domain.OptionContractSnapshot{
				OptionContract: contract,
				OptionQuote: domain.OptionQuote{
					Bid:       bid,
					Ask:       ask,
					Last:      price,
					Timestamp: asOf,
				},
				Greeks: domain.Greeks{
					Delta: delta,
					Gamma: gamma,
					Theta: thetaDay,
					Vega:  vega,
					IV:    iv,
				},
				// Flat OI so the selector's OI filter (defaults to 100) never
				// rejects synthetic contracts. Real liquidity is irrelevant
				// for a synthetic chain; the selector's job is to pick a
				// strike, not to check a market that doesn't exist.
				OpenInterest: 1000,
			})
		}
	}
	return out, nil
}
// weeklyExpiries returns every Friday whose DTE falls in [minDTE, maxDTE]
// inclusive, relative to asOf. Good-Friday style holiday shifting is
// explicitly out of scope for v1 — the BSM pricing difference from a
// Thursday-vs-Friday expiry on a 7-DTE option is well under 1%, so we ship
// plain Fridays and revisit if telemetry shows a material divergence.
func weeklyExpiries(asOf time.Time, minDTE, maxDTE int) []time.Time {
	day := truncateToDate(asOf)
	start := day.AddDate(0, 0, minDTE)
	end := day.AddDate(0, 0, maxDTE)
	// Advance start to the next Friday on-or-after start.
	offset := (int(time.Friday) - int(start.Weekday()) + 7) % 7
	firstFriday := start.AddDate(0, 0, offset)
	var out []time.Time
	for d := firstFriday; !d.After(end); d = d.AddDate(0, 0, 7) {
		out = append(out, d)
	}
	return out
}
// strikeGrid builds a list of strikes spanning spot*(1-gridPct) to
// spot*(1+gridPct) in steps of spot*stepPct, rounded to a sensible tick
// (lower price = finer tick) and deduplicated.
func strikeGrid(spot, gridPct, stepPct float64) []float64 {
	if spot <= 0 || gridPct <= 0 || stepPct <= 0 {
		return nil
	}
	low := spot * (1 - gridPct)
	high := spot * (1 + gridPct)
	step := spot * stepPct
	if step <= 0 {
		return nil
	}
	seen := make(map[float64]struct{}, 64)
	var out []float64
	for k := low; k <= high+1e-9; k += step {
		r := roundStrike(k)
		if r <= 0 {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}
// roundStrike snaps a raw strike to a listable tick. Cheap heuristic: the
// cheaper the stock, the finer the grid.
func roundStrike(k float64) float64 {
	switch {
	case k >= 50:
		return math.Round(k)
	case k >= 10:
		return math.Round(k*2) / 2.0 // $0.50
	default:
		return math.Round(k*4) / 4.0 // $0.25
	}
}
// bsmVega computes vega (per 1.0 change in sigma, NOT per 1%). Needed here
// because options.BSMPrice only returns price/delta/gamma/theta.
func bsmVega(s, k, t, r, sigma float64) float64 {
	if t <= 0 || sigma <= 0 || s <= 0 || k <= 0 {
		return 0
	}
	sqrtT := math.Sqrt(t)
	d1 := (math.Log(s/k) + (r+0.5*sigma*sigma)*t) / (sigma * sqrtT)
	npd1 := math.Exp(-0.5*d1*d1) / math.Sqrt(2*math.Pi)
	return s * sqrtT * npd1
}
// truncateToDate zeros the clock of t but preserves its location, so weekday
// arithmetic matches the caller's timezone (asOf is typically UTC midnight
// from the backtest clock).
func truncateToDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
// daysBetween returns calendar days between two timestamps, measured in the
// caller's timezone. Approximation via hours/24 is safe here because both
// inputs are in the same zone (the backtest clock).
func daysBetween(a, b time.Time) int {
	da := truncateToDate(a)
	db := truncateToDate(b)
	return int(db.Sub(da).Hours() / 24)
}
