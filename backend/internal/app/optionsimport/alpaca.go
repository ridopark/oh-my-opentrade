package optionsimport

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/options"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// alpacaOptionsClient is the slice of *alpaca.RESTClient that AlpacaService
// uses. Defined here (not in ports/) because the backfill is the only
// consumer; promoting to a port is speculative until a second
// implementation lands. The plan explicitly rejects a port for these
// methods (see plan's Code Organization notes: "No port for
// ListOptionContractsAsOf/GetOptionDayBar").
type alpacaOptionsClient interface {
	ListOptionContractsAsOf(ctx context.Context, underlying domain.Symbol, asOf time.Time, dteRangeDays int) ([]domain.OptionContract, error)
	GetOptionDayBar(ctx context.Context, dataURL string, occSymbol domain.Symbol, date time.Time) (*domain.MarketBar, error)
}

// SpotLookup returns the underlying close on the given date. Returning
// (0, nil) signals "no spot available, skip this date" without bubbling
// an error through the per-symbol worker — that lets a partially-covered
// universe still produce rows for the dates that do have spots.
type SpotLookup func(ctx context.Context, sym domain.Symbol, date time.Time) (float64, error)

// AlpacaConfig parameterizes the historical backfill. Zero values pick
// the plan's defaults: 60-day DTE range, 8 symbol workers, 5% spread
// fallback, 4.5% risk-free rate.
type AlpacaConfig struct {
	DTERangeDays     int
	MaxConcurrency   int
	DefaultSpreadPct float64
	SymbolSpreadPct  map[string]float64 // per-underlying override; falls back to DefaultSpreadPct
	RiskFreeRate     float64
}

// AlpacaService backfills historical_option_chain from Alpaca: for each
// (symbol, date) in the requested window it enumerates the OCC strikes
// that were listed as of that date, fetches each contract's 1-day bar,
// inverts IV from the close via Newton-Raphson BSM, recomputes Greeks at
// the recovered IV, and persists. Idempotent — dates already covered by
// HasData are skipped.
//
// Per plan principle "skip-don't-default": rows where IV inversion
// fails to converge, the option close is at the $0.01 floor, or any
// Greek goes NaN/Inf are dropped entirely rather than written with
// fallback values that would silently mislead the contract picker.
type AlpacaService struct {
	client  alpacaOptionsClient
	dataURL string
	repo    ports.HistoricalOptionsPort
	spot    SpotLookup
	cfg     AlpacaConfig
	log     zerolog.Logger
}

// NewAlpacaService wires the importer with sensible defaults from the
// plan: 60-day DTE window, 8 symbol workers, 5% fallback spread, 4.5%
// risk-free rate.
func NewAlpacaService(
	client alpacaOptionsClient,
	dataURL string,
	repo ports.HistoricalOptionsPort,
	spot SpotLookup,
	cfg AlpacaConfig,
	log zerolog.Logger,
) *AlpacaService {
	if cfg.DTERangeDays == 0 {
		cfg.DTERangeDays = 60
	}
	if cfg.MaxConcurrency == 0 {
		cfg.MaxConcurrency = 8
	}
	if cfg.DefaultSpreadPct == 0 {
		cfg.DefaultSpreadPct = 0.05
	}
	if cfg.RiskFreeRate == 0 {
		cfg.RiskFreeRate = 0.045
	}
	return &AlpacaService{
		client:  client,
		dataURL: dataURL,
		repo:    repo,
		spot:    spot,
		cfg:     cfg,
		log:     log.With().Str("component", "alpaca_options_import").Logger(),
	}
}

// AlpacaCaptureResult classifies a single (symbol, date) outcome so the
// caller can sum rollups across the worker pool without re-deriving from
// logs.
type AlpacaCaptureResult int

const (
	AlpacaCaptureSaved AlpacaCaptureResult = iota
	AlpacaCaptureSkipped
)

// Run iterates each (symbol, date) over [from, to] for every supplied
// symbol and persists rows. Symbols are processed concurrently up to
// MaxConcurrency; per-date work inside a symbol is sequential so the
// rate limiter on the underlying client doesn't see bursts that exceed
// its budget.
func (s *AlpacaService) Run(ctx context.Context, symbols []string, from, to time.Time) error {
	if len(symbols) == 0 {
		return fmt.Errorf("alpaca_import: at least one symbol required")
	}
	if to.Before(from) {
		return fmt.Errorf("alpaca_import: 'to' (%s) is before 'from' (%s)",
			to.Format("2006-01-02"), from.Format("2006-01-02"))
	}

	sem := make(chan struct{}, s.cfg.MaxConcurrency)
	var wg sync.WaitGroup
	var saved, skipped, failed atomic.Int32

	for _, sym := range symbols {
		wg.Add(1)
		go func(sym string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ss, sk, ff := s.runSymbol(ctx, sym, from, to)
			saved.Add(int32(ss))
			skipped.Add(int32(sk))
			failed.Add(int32(ff))
		}(sym)
	}
	wg.Wait()

	s.log.Info().
		Int("symbols", len(symbols)).
		Str("from", from.Format("2006-01-02")).
		Str("to", to.Format("2006-01-02")).
		Int32("saved_dates", saved.Load()).
		Int32("skipped_dates", skipped.Load()).
		Int32("failed_dates", failed.Load()).
		Msg("alpaca historical backfill complete")
	return nil
}

func (s *AlpacaService) runSymbol(ctx context.Context, sym string, from, to time.Time) (saved, skipped, failed int) {
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		select {
		case <-ctx.Done():
			return saved, skipped, failed
		default:
		}

		result, err := s.CaptureDate(ctx, sym, d)
		switch {
		case err != nil:
			s.log.Warn().Err(err).Str("symbol", sym).Str("date", d.Format("2006-01-02")).
				Msg("alpaca capture failed for date")
			failed++
		case result == AlpacaCaptureSkipped:
			skipped++
		case result == AlpacaCaptureSaved:
			saved++
		}
	}
	return saved, skipped, failed
}

// CaptureDate runs the per-(symbol, date) backfill: skip-if-covered,
// fetch contracts, compute and persist the row batch.
func (s *AlpacaService) CaptureDate(ctx context.Context, sym string, date time.Time) (AlpacaCaptureResult, error) {
	day := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)

	has, err := s.repo.HasData(ctx, domain.Symbol(sym), day)
	if err != nil {
		return 0, fmt.Errorf("HasData %s %s: %w", sym, day.Format("2006-01-02"), err)
	}
	if has {
		return AlpacaCaptureSkipped, nil
	}

	spot, err := s.spot(ctx, domain.Symbol(sym), day)
	if err != nil {
		return 0, fmt.Errorf("spot lookup %s %s: %w", sym, day.Format("2006-01-02"), err)
	}
	if spot <= 0 {
		// No underlying close → can't invert IV; skip without erroring.
		return AlpacaCaptureSkipped, nil
	}

	contracts, err := s.client.ListOptionContractsAsOf(ctx, domain.Symbol(sym), day, s.cfg.DTERangeDays)
	if err != nil {
		return 0, fmt.Errorf("list contracts %s %s: %w", sym, day.Format("2006-01-02"), err)
	}
	if len(contracts) == 0 {
		return AlpacaCaptureSkipped, nil
	}

	spread := s.cfg.DefaultSpreadPct
	if v, ok := s.cfg.SymbolSpreadPct[sym]; ok && v > 0 {
		spread = v
	}
	halfSpread := spread / 2.0

	rows := make([]domain.HistoricalOptionChainRow, 0, len(contracts))
	for _, contract := range contracts {
		row, ok := s.buildRow(ctx, sym, day, spot, halfSpread, contract)
		if !ok {
			continue
		}
		rows = append(rows, row)
	}

	if len(rows) == 0 {
		return AlpacaCaptureSkipped, nil
	}

	if err := s.repo.SaveBatch(ctx, rows); err != nil {
		return 0, fmt.Errorf("SaveBatch %s %s: %w", sym, day.Format("2006-01-02"), err)
	}
	s.log.Info().
		Str("symbol", sym).
		Str("date", day.Format("2006-01-02")).
		Int("rows", len(rows)).
		Msg("alpaca historical capture saved")
	return AlpacaCaptureSaved, nil
}

// buildRow runs the per-contract pipeline: fetch day bar, derive IV +
// Greeks, gate on the skip-don't-default rules, return the row. ok=false
// means "drop this contract"; the caller treats every drop as silent so
// one bad strike doesn't poison the rest of the batch.
func (s *AlpacaService) buildRow(
	ctx context.Context,
	sym string,
	day time.Time,
	spot, halfSpread float64,
	contract domain.OptionContract,
) (domain.HistoricalOptionChainRow, bool) {
	bar, err := s.client.GetOptionDayBar(ctx, s.dataURL, contract.ContractSymbol, day)
	if err != nil {
		s.log.Debug().Err(err).Str("occ", string(contract.ContractSymbol)).
			Msg("day bar fetch failed, skipping contract")
		return domain.HistoricalOptionChainRow{}, false
	}
	if bar == nil {
		return domain.HistoricalOptionChainRow{}, false
	}
	// 0.01 is Alpaca's quote/trade price floor — these rows have zero
	// information content and would converge to wildly wrong IV.
	if bar.Close <= 0.01 {
		return domain.HistoricalOptionChainRow{}, false
	}

	dteDays := int(contract.Expiry.Sub(day).Hours() / 24)
	if dteDays <= 0 {
		return domain.HistoricalOptionChainRow{}, false
	}
	tYears := float64(dteDays) / 365.0
	isCall := contract.Right == domain.OptionRightCall

	// Newton-Raphson IV inversion. ImpliedVol returns the chainIV
	// starting guess on convergence failure (intrinsic floor breached,
	// vega ~0, divergence) — detect that by re-pricing and checking the
	// residual against the observed close.
	iv := options.ImpliedVol(bar.Close, spot, contract.Strike, tYears, s.cfg.RiskFreeRate, isCall, 0.30)
	modelPrice, delta, gamma, theta := options.BSMPrice(spot, contract.Strike, tYears, s.cfg.RiskFreeRate, iv, isCall)
	if math.IsNaN(iv) || math.IsInf(iv, 0) {
		return domain.HistoricalOptionChainRow{}, false
	}
	// Residual gate: 5 cents tolerance is generous. The synthetic chain
	// uses the same threshold as its acceptance window in backtests.
	if math.Abs(modelPrice-bar.Close) > 0.05 {
		return domain.HistoricalOptionChainRow{}, false
	}
	vega := options.BSMVega(spot, contract.Strike, tYears, s.cfg.RiskFreeRate, iv)
	if math.IsNaN(delta) || math.IsNaN(gamma) || math.IsNaN(theta) || math.IsNaN(vega) {
		return domain.HistoricalOptionChainRow{}, false
	}

	bid := bar.Close * (1 - halfSpread)
	ask := bar.Close * (1 + halfSpread)
	if bid < 0.01 {
		bid = 0.01
	}

	return domain.HistoricalOptionChainRow{
		Date:       day,
		Symbol:     domain.Symbol(sym),
		Expiration: contract.Expiry,
		Strike:     contract.Strike,
		Right:      contract.Right,
		Bid:        bid,
		Ask:        ask,
		IV:         iv,
		Delta:      delta,
		Gamma:      gamma,
		Theta:      theta,
		Vega:       vega,
	}, true
}
