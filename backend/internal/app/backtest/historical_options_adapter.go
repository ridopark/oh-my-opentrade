// Package backtest — historical_options_adapter.go adapts HistoricalOptionsPort
// (DoltHub-backed historical data) to OptionsMarketDataPort so that the
// RiskSizer can select option contracts during backtests.
package backtest

import (
	"context"
	"fmt"
	"time"
	"unsafe"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// optionCacheKey indexes the pre-loaded full chain (all expiries/strikes)
// by (symbol, date, right). GetOptionChain looks up the whole set and the
// consumer filters by DTE window, so no DTE component is needed here.
type optionCacheKey struct {
	Symbol string // e.g. "AAPL"
	Date   string // "2006-01-02"
	Right  string // "Call" or "Put"
}

// syntheticCacheKey indexes synthetic-generator output by
// (symbol, date, right, DTE window). Unlike the historical chain, the
// synthetic generator materializes a DIFFERENT contract set per window,
// so two requests with different windows must not share cached output.
type syntheticCacheKey struct {
	Symbol string
	Date   string
	Right  string
	MinDTE int
	MaxDTE int
}

// contractCacheKey indexes individual contract rows for SimBroker exit pricing.
type contractCacheKey struct {
	Symbol string // underlying e.g. "AAPL"
	Date   string // "2006-01-02"
	Right  string // "Call" or "Put"
}

// HistoricalOptionsAdapter wraps a HistoricalOptionsPort to satisfy
// OptionsMarketDataPort for backtesting. It uses a clock function to
// determine the "current" backtest date for historical lookups.
//
// When PreLoad is called, all subsequent GetOptionChain calls are served
// from an in-memory cache, eliminating per-signal DB round-trips.
//
// When the cache or DB has no data for the requested (symbol, date, right),
// and a SyntheticChainGenerator is attached, the adapter fills the gap by
// generating a synthetic chain via Black-Scholes — see SetSyntheticGenerator.
type HistoricalOptionsAdapter struct {
	repo    ports.HistoricalOptionsPort
	clockFn func() time.Time
	log     zerolog.Logger

	// Pre-loaded chain snapshots keyed by (symbol, date, right).
	chainCache map[optionCacheKey][]domain.OptionContractSnapshot

	// Pre-loaded raw rows keyed by (symbol, date, right) for SimBroker
	// GetHistoricalContract nearest-strike lookups.
	contractCache map[contractCacheKey][]domain.HistoricalOptionChainRow

	loaded bool

	// Synthetic chain generator — populated when the BacktestConfig
	// enables synthetic chains. Nil when disabled, which preserves
	// byte-identical behavior with the pre-synthetic path.
	syntheticGen *SyntheticChainGenerator

	// Cache for synthetic results so a second request on the same key is
	// O(1). Lazily initialized on first synthetic generation. Keyed on
	// DTE window to prevent cross-strategy contamination.
	syntheticCache map[syntheticCacheKey][]domain.OptionContractSnapshot

	// Diagnostics: track how often each path actually served data, so
	// the runner can emit an end-of-run summary and flag accidentally
	// all-synthetic backtests.
	historicalHits int
	syntheticHits  int
}

// NewHistoricalOptionsAdapter creates an adapter that bridges historical
// option data into the live OptionsMarketDataPort interface.
func NewHistoricalOptionsAdapter(repo ports.HistoricalOptionsPort, clockFn func() time.Time) *HistoricalOptionsAdapter {
	return &HistoricalOptionsAdapter{
		repo:    repo,
		clockFn: clockFn,
		log:     zerolog.Nop(),
	}
}

// SetSyntheticGenerator attaches a synthetic chain generator used as a
// fallback when neither the in-memory cache nor the DB returns rows for
// a given (symbol, date, right). A nil argument disables the fallback,
// which preserves the pre-synthetic behavior (empty chain -> equity
// fallback in the risk sizer).
func (a *HistoricalOptionsAdapter) SetSyntheticGenerator(gen *SyntheticChainGenerator) {
	a.syntheticGen = gen
}

// SetLogger attaches a structured logger for cache diagnostics.
func (a *HistoricalOptionsAdapter) SetLogger(log zerolog.Logger) {
	a.log = log.With().Str("component", "hist_options_cache").Logger()
}

// PreLoad bulk-loads all option chain data for the given symbols and date range
// into memory. After a successful call, GetOptionChain serves from cache.
// If loading fails, the adapter falls back to per-query DB lookups gracefully.
func (a *HistoricalOptionsAdapter) PreLoad(ctx context.Context, symbols []domain.Symbol, from, to time.Time) error {
	start := time.Now()

	rows, err := a.repo.GetHistoricalChainBulk(ctx, symbols, from, to)
	if err != nil {
		return fmt.Errorf("hist_options_cache: pre-load bulk query: %w", err)
	}

	chainCache := make(map[optionCacheKey][]domain.OptionContractSnapshot, len(rows)/10)
	contractCache := make(map[contractCacheKey][]domain.HistoricalOptionChainRow, len(rows)/10)

	for _, r := range rows {
		callPut := "Call"
		if r.Right == domain.OptionRightPut {
			callPut = "Put"
		}
		dateStr := r.Date.Format("2006-01-02")

		// Build chain cache (for GetOptionChain).
		chainKey := optionCacheKey{
			Symbol: string(r.Symbol),
			Date:   dateStr,
			Right:  callPut,
		}

		contract, cErr := domain.NewOptionContract(
			string(r.Symbol),
			r.Expiration,
			r.Strike,
			r.Right,
			domain.OptionStyleAmerican,
		)
		if cErr != nil {
			continue
		}

		snap := domain.OptionContractSnapshot{
			OptionContract: contract,
			OptionQuote: domain.OptionQuote{
				Bid:             r.Bid,
				Ask:             r.Ask,
				Last:            r.Mid(),
				Timestamp:       r.Date,
				IsSyntheticLast: true, // DoltHub rows carry no trade prints
			},
			Greeks: domain.Greeks{
				Delta: r.Delta,
				Gamma: r.Gamma,
				Theta: r.Theta,
				Vega:  r.Vega,
				Rho:   r.Rho,
				IV:    r.IV,
			},
			OpenInterest: estimateOpenInterest(r),
		}
		chainCache[chainKey] = append(chainCache[chainKey], snap)

		// Build contract cache (for GetHistoricalContract).
		contractKey := contractCacheKey{
			Symbol: string(r.Symbol),
			Date:   dateStr,
			Right:  callPut,
		}
		contractCache[contractKey] = append(contractCache[contractKey], r)
	}

	// Estimate memory usage: ~200 bytes per snapshot + ~150 bytes per raw row.
	estimatedBytes := len(rows) * (int(unsafe.Sizeof(domain.OptionContractSnapshot{})) + int(unsafe.Sizeof(domain.HistoricalOptionChainRow{})))
	estimatedMB := estimatedBytes / (1024 * 1024)

	a.chainCache = chainCache
	a.contractCache = contractCache
	a.loaded = true

	a.log.Info().
		Int("total_rows", len(rows)).
		Int("chain_keys", len(chainCache)).
		Int("contract_keys", len(contractCache)).
		Int("estimated_mb", estimatedMB).
		Dur("load_time", time.Since(start)).
		Msg("options chain pre-loaded into memory")

	return nil
}

// IsLoaded reports whether the cache has been populated.
func (a *HistoricalOptionsAdapter) IsLoaded() bool {
	return a.loaded
}

// Stats returns how many option-chain requests were served from historical
// data vs the synthetic generator. Intended for end-of-run diagnostics;
// a backtest whose syntheticHits dominate historicalHits is effectively
// running on fabricated prices.
func (a *HistoricalOptionsAdapter) Stats() (historicalHits, syntheticHits int) {
	return a.historicalHits, a.syntheticHits
}

// GetOptionChain implements ports.OptionsMarketDataPort by querying the
// historical options repo for the backtest's current simulated date.
// When the cache is loaded, this is a zero-DB O(1) map lookup.
//
// If both cache and DB return empty and a synthetic generator is attached,
// the adapter generates a synthetic chain via Black-Scholes for the given
// DTE window, caches it, and returns it. The expiry argument remains
// supported for interface-compat but is ignored by the synthetic path,
// which uses [minDTE, maxDTE] to span multiple weekly expiries.
func (a *HistoricalOptionsAdapter) GetOptionChain(
	ctx context.Context,
	underlying domain.Symbol,
	expiry time.Time,
	right domain.OptionRight,
	minDTE, maxDTE int,
) ([]domain.OptionContractSnapshot, error) {
	now := a.clockFn()
	callPut := "Call"
	if right == domain.OptionRightPut {
		callPut = "Put"
	}
	key := optionCacheKey{
		Symbol: string(underlying),
		Date:   now.Format("2006-01-02"),
		Right:  callPut,
	}

	if a.loaded {
		if snaps, ok := a.chainCache[key]; ok && hasExpiryInDTERange(snaps, now, minDTE, maxDTE) {
			a.historicalHits++
			return snaps, nil
		}
	} else {
		snaps, err := a.getOptionChainFromDB(ctx, underlying, now, right)
		if err != nil {
			return nil, err
		}
		if hasExpiryInDTERange(snaps, now, minDTE, maxDTE) {
			a.historicalHits++
			return snaps, nil
		}
	}

	// Synthetic fallback — fires when no cached/DB row has an expiry
	// inside [minDTE, maxDTE]. DoltHub coverage is monthly-only so a
	// DTE-5..14 strategy sees 231 rows for MU but zero in-range; the
	// selector would reject every contract on DTE alone. Synthetic
	// generates the missing weeklies so the strategy has something to
	// pick. When the DB DOES have in-range rows (longer-DTE strategies
	// like overnight_z_v1), the cache/DB path above is returned as-is
	// and synthetic stays dormant — byte-identical to the pre-fallback
	// behavior.
	return a.generateSynthetic(ctx, key, underlying, now, right, minDTE, maxDTE)
}

// hasExpiryInDTERange reports whether any snapshot's expiry lies within
// [asOf+minDTE, asOf+maxDTE]. Used as the cache/DB short-circuit so we
// only fall back to synthetic when real data can't satisfy the strategy's
// DTE window. A zero max means "no upper bound" (defensive default).
func hasExpiryInDTERange(snaps []domain.OptionContractSnapshot, asOf time.Time, minDTE, maxDTE int) bool {
	if len(snaps) == 0 {
		return false
	}
	if maxDTE <= 0 {
		maxDTE = 3650
	}
	lo := asOf.AddDate(0, 0, minDTE)
	hi := asOf.AddDate(0, 0, maxDTE+1)
	for _, s := range snaps {
		exp := s.Expiry
		if !exp.Before(lo) && exp.Before(hi) {
			return true
		}
	}
	return false
}

// generateSynthetic runs the BSM-based generator (if attached), caches the
// result under the per-day + DTE-window key, and returns it. Returns nil
// when no generator is attached or the generator produces no output (e.g.
// missing spot) — both are legitimate "no data" outcomes, not errors.
func (a *HistoricalOptionsAdapter) generateSynthetic(
	ctx context.Context,
	chainKey optionCacheKey,
	underlying domain.Symbol,
	asOf time.Time,
	right domain.OptionRight,
	minDTE, maxDTE int,
) ([]domain.OptionContractSnapshot, error) {
	if a.syntheticGen == nil {
		return nil, nil
	}
	synthKey := syntheticCacheKey{
		Symbol: chainKey.Symbol,
		Date:   chainKey.Date,
		Right:  chainKey.Right,
		MinDTE: minDTE,
		MaxDTE: maxDTE,
	}
	if cached, ok := a.syntheticCache[synthKey]; ok {
		a.syntheticHits++
		return cached, nil
	}
	snaps, err := a.syntheticGen.GenerateChain(ctx, underlying, asOf, right, minDTE, maxDTE)
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 {
		return nil, nil
	}
	a.syntheticHits++
	if a.syntheticCache == nil {
		a.syntheticCache = make(map[syntheticCacheKey][]domain.OptionContractSnapshot, 64)
	}
	a.syntheticCache[synthKey] = snaps
	a.log.Info().
		Str("symbol", string(underlying)).
		Str("date", synthKey.Date).
		Str("right", string(right)).
		Int("contracts", len(snaps)).
		Int("min_dte", minDTE).
		Int("max_dte", maxDTE).
		Msg("synthetic chain generated")
	return snaps, nil
}

// getOptionChainFromDB is the original DB-backed implementation, used as
// fallback when the cache is not loaded.
func (a *HistoricalOptionsAdapter) getOptionChainFromDB(
	ctx context.Context,
	underlying domain.Symbol,
	now time.Time,
	right domain.OptionRight,
) ([]domain.OptionContractSnapshot, error) {
	minDTE := 1
	maxDTE := 365

	rows, err := a.repo.GetHistoricalChain(ctx, underlying, now, right, minDTE, maxDTE)
	if err != nil {
		return nil, err
	}

	snapshots := make([]domain.OptionContractSnapshot, 0, len(rows))
	for _, r := range rows {
		contract, cErr := domain.NewOptionContract(
			string(r.Symbol),
			r.Expiration,
			r.Strike,
			r.Right,
			domain.OptionStyleAmerican,
		)
		if cErr != nil {
			continue
		}

		snapshots = append(snapshots, domain.OptionContractSnapshot{
			OptionContract: contract,
			OptionQuote: domain.OptionQuote{
				Bid:             r.Bid,
				Ask:             r.Ask,
				Last:            r.Mid(),
				Timestamp:       r.Date,
				IsSyntheticLast: true,
			},
			Greeks: domain.Greeks{
				Delta: r.Delta,
				Gamma: r.Gamma,
				Theta: r.Theta,
				Vega:  r.Vega,
				Rho:   r.Rho,
				IV:    r.IV,
			},
			OpenInterest: estimateOpenInterest(r),
		})
	}

	return snapshots, nil
}

// GetHistoricalContract satisfies ports.HistoricalOptionsPort by doing an
// in-memory nearest-strike lookup when the cache is loaded, otherwise
// delegating to the underlying repository.
func (a *HistoricalOptionsAdapter) GetHistoricalContract(
	ctx context.Context,
	symbol domain.Symbol,
	date time.Time,
	strike float64,
	expiry time.Time,
	right domain.OptionRight,
) (*domain.HistoricalOptionChainRow, error) {
	if a.loaded {
		callPut := "Call"
		if right == domain.OptionRightPut {
			callPut = "Put"
		}
		key := contractCacheKey{
			Symbol: string(symbol),
			Date:   date.Format("2006-01-02"),
			Right:  callPut,
		}
		rows, ok := a.contractCache[key]
		if !ok || len(rows) == 0 {
			return nil, fmt.Errorf("hist_options_cache: no cached contract for %s on %s", symbol, date.Format("2006-01-02"))
		}

		// Find closest strike within max($2, 2% of requested strike) and
		// expiry within +/-7 days. The absolute $2 floor keeps behavior
		// intact for sub-$100 names; the 2% relative term prevents a
		// nonsense match for names trading in the hundreds (where $2
		// misses every listed strike).
		strikeTol := 2.0
		if rel := 0.02 * strike; rel > strikeTol {
			strikeTol = rel
		}
		var best *domain.HistoricalOptionChainRow
		bestStrikeDist := 999999.0
		bestExpiryDist := 999
		for i := range rows {
			r := &rows[i]
			strikeDist := abs(r.Strike - strike)
			if strikeDist > strikeTol {
				continue
			}
			expiryDist := absDays(r.Expiration, expiry)
			if expiryDist > 7 {
				continue
			}
			if best == nil ||
				strikeDist < bestStrikeDist ||
				(strikeDist == bestStrikeDist && expiryDist < bestExpiryDist) {
				best = r
				bestStrikeDist = strikeDist
				bestExpiryDist = expiryDist
			}
		}
		if best == nil {
			return nil, fmt.Errorf("hist_options_cache: no matching contract for %s strike=%.2f expiry=%s on %s",
				symbol, strike, expiry.Format("2006-01-02"), date.Format("2006-01-02"))
		}
		return best, nil
	}

	// Fallback to DB.
	return a.repo.GetHistoricalContract(ctx, symbol, date, strike, expiry, right)
}

// GetHistoricalChain delegates to the underlying repo (not cached by key shape).
func (a *HistoricalOptionsAdapter) GetHistoricalChain(
	ctx context.Context,
	symbol domain.Symbol,
	date time.Time,
	right domain.OptionRight,
	minDTE, maxDTE int,
) ([]domain.HistoricalOptionChainRow, error) {
	return a.repo.GetHistoricalChain(ctx, symbol, date, right, minDTE, maxDTE)
}

// GetHistoricalChainBulk delegates to the underlying repo.
func (a *HistoricalOptionsAdapter) GetHistoricalChainBulk(
	ctx context.Context,
	symbols []domain.Symbol,
	from, to time.Time,
) ([]domain.HistoricalOptionChainRow, error) {
	return a.repo.GetHistoricalChainBulk(ctx, symbols, from, to)
}

// HasData delegates to the underlying repo.
func (a *HistoricalOptionsAdapter) HasData(
	ctx context.Context,
	symbol domain.Symbol,
	date time.Time,
) (bool, error) {
	return a.repo.HasData(ctx, symbol, date)
}

// SaveBatch delegates to the underlying repo.
func (a *HistoricalOptionsAdapter) SaveBatch(
	ctx context.Context,
	rows []domain.HistoricalOptionChainRow,
) error {
	return a.repo.SaveBatch(ctx, rows)
}

// estimateOpenInterest returns a reasonable OI estimate from historical data.
// DoltHub data may not include OI, so we default to a value that passes
// the min_open_interest filter (typically 100) for liquid contracts.
func estimateOpenInterest(r domain.HistoricalOptionChainRow) int {
	// If bid and ask are both present with reasonable spread, assume liquid.
	if r.Bid > 0 && r.Ask > 0 && r.Ask > r.Bid {
		spread := (r.Ask - r.Bid) / r.Ask
		if spread < 0.20 {
			return 500 // liquid
		}
		return 50 // illiquid
	}
	return 10 // very illiquid / no quotes
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func absDays(a, b time.Time) int {
	d := int(a.Sub(b).Hours() / 24)
	if d < 0 {
		return -d
	}
	return d
}

// marketBarReader is the minimal subset of the repository needed for the
// spot-price lookup. Declaring it locally keeps the backtest package
// independent of the full RepositoryPort interface.
type marketBarReader interface {
	GetMarketBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
}

// lookupSpot returns the most recent close for the symbol on-or-before
// asOf, preferring 1d bars over 1m bars. Returns 0 with a nil error when
// no bars are available — synthetic generator treats that as "no chain".
func lookupSpot(ctx context.Context, repo marketBarReader, symbol domain.Symbol, asOf time.Time) (float64, error) {
	// Try 1d first: widest and cheapest lookup.
	dayStart := time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, asOf.Location())
	dayEnd := dayStart.Add(24 * time.Hour)
	bars, err := repo.GetMarketBars(ctx, symbol, domain.Timeframe("1d"), dayStart.AddDate(0, 0, -7), dayEnd)
	if err == nil && len(bars) > 0 {
		return bars[len(bars)-1].Close, nil
	}
	// Fall back to 1m: last bar on-or-before asOf within the last 2 days.
	from := asOf.Add(-48 * time.Hour)
	mbars, err := repo.GetMarketBars(ctx, symbol, domain.Timeframe("1m"), from, asOf.Add(time.Minute))
	if err != nil {
		return 0, err
	}
	if len(mbars) == 0 {
		return 0, nil
	}
	return mbars[len(mbars)-1].Close, nil
}
