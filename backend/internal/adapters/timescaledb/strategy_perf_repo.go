package timescaledb

import (
	"context"
	"math"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

const querySelectStrategyPerfOverall = `SELECT
	COALESCE(SUM(trade_count), 0),
	COALESCE(SUM(win_count), 0),
	COALESCE(SUM(loss_count), 0),
	COALESCE(SUM(realized_pnl), 0),
	COALESCE(SUM(gross_profit), 0),
	COALESCE(SUM(gross_loss), 0)
	FROM strategy_daily_pnl
	WHERE account_id = $1 AND env_mode = $2 AND strategy = $3 AND date >= $4`

// Phase A: strategy_daily_pnl has no symbol column, so per-symbol stats
// come from the trades table directly.
const querySelectStrategyPerfBySymbol = `SELECT
	COUNT(*) FILTER (WHERE side IN ('BUY', 'SELL')),
	COUNT(*) FILTER (WHERE side = 'SELL' AND price * quantity > 0),
	0,
	COALESCE(SUM(CASE WHEN side = 'SELL' THEN price * quantity ELSE -(price * quantity) END), 0),
	COALESCE(SUM(CASE WHEN side = 'SELL' AND price * quantity > 0 THEN price * quantity ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN side = 'SELL' AND price * quantity <= 0 THEN price * quantity ELSE 0 END), 0)
	FROM trades
	WHERE account_id = $1 AND env_mode = $2 AND strategy = $3 AND symbol = $4
	  AND status = 'FILLED' AND time >= $5`

// StrategyPerfRepo implements ports.StrategyPerformancePort using TimescaleDB.
type StrategyPerfRepo struct {
	db  DBTX
	log zerolog.Logger
}

func NewStrategyPerfRepo(db DBTX, log zerolog.Logger) *StrategyPerfRepo {
	return &StrategyPerfRepo{db: db, log: log}
}

func (r *StrategyPerfRepo) GetPerformanceSummary(
	ctx context.Context,
	tenantID string,
	envMode domain.EnvMode,
	strategy string,
	symbol string,
	lookback time.Duration,
) (*domain.StrategyPerformanceSummary, error) {
	since := time.Now().UTC().Add(-lookback)

	overall, err := r.queryStats(ctx, querySelectStrategyPerfOverall,
		tenantID, string(envMode), strategy, since)
	if err != nil {
		r.log.Error().Err(err).Str("strategy", strategy).Msg("failed to query overall strategy perf")
		return nil, err
	}
	if overall.TradeCount == 0 {
		return nil, nil
	}
	overall.Strategy = strategy
	overall.Period = lookback

	summary := &domain.StrategyPerformanceSummary{
		Strategy: strategy,
		Symbol:   symbol,
		Overall:  overall,
	}

	if symbol != "" {
		bySymbol, err := r.queryStats(ctx, querySelectStrategyPerfBySymbol,
			tenantID, string(envMode), strategy, symbol, since)
		if err != nil {
			r.log.Warn().Err(err).Str("strategy", strategy).Str("symbol", symbol).Msg("failed to query per-symbol perf, continuing with overall only")
		} else if bySymbol.TradeCount > 0 {
			bySymbol.Strategy = strategy
			bySymbol.Symbol = symbol
			bySymbol.Period = lookback
			summary.BySymbol = &bySymbol
		}
	}

	return summary, nil
}

func (r *StrategyPerfRepo) queryStats(ctx context.Context, query string, args ...any) (domain.StrategyRegimeStats, error) {
	var s domain.StrategyRegimeStats
	var grossProfit, grossLoss float64

	row := r.db.QueryRowContext(ctx, query, args...)
	if err := row.Scan(
		&s.TradeCount, &s.WinCount, &s.LossCount,
		&s.TotalPnL, &grossProfit, &grossLoss,
	); err != nil {
		return s, err
	}

	if s.TradeCount > 0 {
		s.WinRate = float64(s.WinCount) / float64(s.TradeCount)
	}

	// Expectancy = (WinRate × AvgWin) - (LossRate × AvgLoss)
	var avgWin, avgLoss float64
	if s.WinCount > 0 {
		avgWin = grossProfit / float64(s.WinCount)
	}
	if s.LossCount > 0 {
		avgLoss = math.Abs(grossLoss) / float64(s.LossCount)
	}
	if s.TradeCount > 0 {
		s.Expectancy = (s.WinRate * avgWin) - ((1 - s.WinRate) * avgLoss)
	}

	return s, nil
}
