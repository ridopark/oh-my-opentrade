package http

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/oh-my-opentrade/backend/internal/app/backtest"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// backtestRunMeta is the frozen run config captured at POST /backtest/run time
// so the history row reflects what the user *asked for*, not what later file
// edits might say. Passed to the Runner's finalizer via closure.
type backtestRunMeta struct {
	id            string
	strategies    []string
	symbols       []string
	periodStart   time.Time
	periodEnd     time.Time
	initialEquity float64
	slippageBPS   int
	noAI          bool
	dnaSnapshot   map[string]any
}

// derefSharpe flattens a nullable Sharpe for the history row. The DB column is
// non-nullable float — nil (insufficient data) becomes 0 here, matching the
// pre-nullable behavior of existing historical rows.
func derefSharpe(s *float64) float64 {
	if s == nil {
		return 0
	}
	return *s
}

// saveBacktestHistory runs in its own goroutine off the Runner's finalizer.
// It has a 10s timeout so a slow DB can't leak goroutines, and all errors
// are logged — the backtest run itself has already completed by the time
// this is called, so save failures are non-fatal.
func saveBacktestHistory(repo ports.BacktestHistoryPort, meta backtestRunMeta, res *backtest.Result, log zerolog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Backtest IDs from Runner use the "bt-<hex>" form. The history table
	// expects UUIDs. Derive a deterministic UUID from the runner ID so the
	// same run always maps to the same row and so the handler can look it
	// up later via /backtest/history/{id}.
	runUUID := uuidFromRunID(meta.id).String()

	expectancy := 0.0
	if res.TradeCount > 0 {
		expectancy = res.TotalPnL / float64(res.TradeCount)
	}

	equityCurve := make([]ports.BacktestEquityPoint, len(res.EquityCurve))
	for i, p := range res.EquityCurve {
		equityCurve[i] = ports.BacktestEquityPoint{T: p.T, Eq: p.Eq}
	}

	row := ports.BacktestRunRow{
		ID:            runUUID,
		RanAt:         time.Now().UTC(),
		Strategies:    meta.strategies,
		Symbols:       meta.symbols,
		PeriodStart:   meta.periodStart,
		PeriodEnd:     meta.periodEnd,
		InitialEquity: meta.initialEquity,
		SlippageBPS:   meta.slippageBPS,
		NoAI:          meta.noAI,
		PF:            res.ProfitFactor,
		WinRate:       res.WinRate,
		Expectancy:    expectancy,
		MaxDrawdown:   res.MaxDrawdown,
		Sharpe:        derefSharpe(res.SharpeRatio),
		TradeCount:    res.TradeCount,
		WinCount:      res.WinCount,
		LossCount:     res.LossCount,
		NetPnL:        res.TotalPnL,
		TotalReturn:   res.TotalReturn,
		FinalEquity:   res.FinalEquity,
		EquityCurve:   equityCurve,
		DNASnapshot:   meta.dnaSnapshot,
		Tags:          []string{},
	}

	trades := make([]ports.BacktestTradeRow, 0, len(res.Trades))
	for i, t := range res.Trades {
		trades = append(trades, ports.BacktestTradeRow{
			Seq:           i,
			Symbol:        t.Symbol,
			Side:          t.Side,
			Direction:     t.Direction,
			Quantity:      t.Quantity,
			Price:         t.Price,
			FilledAt:      t.FilledAt,
			PnL:           t.PnL,
			StrategyID:    t.Strategy,
			Rationale:     t.Rationale,
			Regime:        t.Regime,
			VIXBucket:     t.VIXBucket,
			MarketContext: t.MarketContext,
		})
	}

	if err := repo.Save(ctx, row, trades); err != nil {
		log.Error().Err(err).Str("backtest_id", meta.id).Msg("backtest history save failed")
		return
	}
	log.Info().
		Str("backtest_id", meta.id).
		Str("run_uuid", runUUID).
		Int("trades", len(trades)).
		Msg("backtest history saved")
}

// uuidFromRunID produces a deterministic UUID from a Runner ID string so the
// same backtest maps to the same history row. Uses uuid.NewSHA1 under a
// fixed namespace.
func uuidFromRunID(runID string) uuid.UUID {
	return uuid.NewSHA1(uuidBacktestNamespace, []byte(runID))
}

// uuidBacktestNamespace is a fixed v4 UUID used as the SHA-1 namespace for
// deriving backtest history row IDs from runner IDs. Do not change once
// rows exist on disk — it would orphan historical lookups.
var uuidBacktestNamespace = uuid.MustParse("6b3c2a55-9e2c-4d9f-b4b1-0f5f0f5f0f5f")
