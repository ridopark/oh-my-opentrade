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

// optionCacheKey indexes pre-loaded option chain snapshots by
// (symbol, date, right) for O(1) lookup during replay.
type optionCacheKey struct {
	Symbol string // e.g. "AAPL"
	Date   string // "2006-01-02"
	Right  string // "Call" or "Put"
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
				Bid:       r.Bid,
				Ask:       r.Ask,
				Last:      r.Mid(),
				Timestamp: r.Date,
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

// GetOptionChain implements ports.OptionsMarketDataPort by querying the
// historical options repo for the backtest's current simulated date.
// When the cache is loaded, this is a zero-DB O(1) map lookup.
func (a *HistoricalOptionsAdapter) GetOptionChain(
	ctx context.Context,
	underlying domain.Symbol,
	expiry time.Time,
	right domain.OptionRight,
) ([]domain.OptionContractSnapshot, error) {
	now := a.clockFn()

	if a.loaded {
		callPut := "Call"
		if right == domain.OptionRightPut {
			callPut = "Put"
		}
		key := optionCacheKey{
			Symbol: string(underlying),
			Date:   now.Format("2006-01-02"),
			Right:  callPut,
		}
		if snaps, ok := a.chainCache[key]; ok {
			return snaps, nil
		}
		return nil, nil // no data for this date — not an error
	}

	// Fallback: original per-query DB path.
	return a.getOptionChainFromDB(ctx, underlying, now, right)
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
				Bid:       r.Bid,
				Ask:       r.Ask,
				Last:      r.Mid(),
				Timestamp: r.Date,
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

		// Find closest strike within +/-2.0 and expiry within +/-7 days
		// (same logic as the SQL query in GetHistoricalContract).
		var best *domain.HistoricalOptionChainRow
		bestStrikeDist := 999.0
		bestExpiryDist := 999
		for i := range rows {
			r := &rows[i]
			strikeDist := abs(r.Strike - strike)
			if strikeDist > 2.0 {
				continue
			}
			expiryDist := absDays(r.Expiration, expiry)
			if expiryDist > 7 {
				continue
			}
			if best == nil || strikeDist < bestStrikeDist || (strikeDist == bestStrikeDist && expiryDist < bestExpiryDist) {
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
