package timescaledb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

const (
	queryInsertMarketBar   = `INSERT INTO market_bars (time, account_id, env_mode, symbol, timeframe, open, high, low, close, volume, suspect) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	querySelectMarketBars  = `SELECT time, symbol, timeframe, open, high, low, close, volume, suspect FROM market_bars WHERE symbol = $1 AND timeframe = $2 AND time >= $3 AND time <= $4 ORDER BY time`
	queryInsertTrade       = `INSERT INTO trades (time, account_id, env_mode, trade_id, symbol, side, quantity, price, commission, status) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	querySelectTrades      = `SELECT time, trade_id, symbol, side, quantity, price, commission, status FROM trades WHERE account_id = $1 AND env_mode = $2 AND time >= $3 AND time <= $4 ORDER BY time`
	queryInsertStrategyDNA = `INSERT INTO strategy_dna_history (time, account_id, env_mode, strategy_id, version, parameters, performance) VALUES ($1, $2, $3, $4, $5, $6, $7)`
	querySelectLatestDNA   = `SELECT time, strategy_id, version, parameters, performance FROM strategy_dna_history WHERE account_id = $1 AND env_mode = $2 ORDER BY time DESC LIMIT 1`
)

// SaveMarketBar saves a single OHLCV candle.
// It passes empty strings for account_id and env_mode because market data is shared across all tenants.
func (r *Repository) SaveMarketBar(ctx context.Context, bar domain.MarketBar) error {
	_, err := r.db.ExecContext(ctx, queryInsertMarketBar, bar.Time, "", "", string(bar.Symbol), string(bar.Timeframe), bar.Open, bar.High, bar.Low, bar.Close, bar.Volume, bar.Suspect)
	if err != nil {
		return fmt.Errorf("timescaledb: save market bar: %w", err)
	}
	return nil
}

// GetMarketBars retrieves historical market bars.
// It returns the bars ordered by time ascending.
func (r *Repository) GetMarketBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	rows, err := r.db.QueryContext(ctx, querySelectMarketBars, string(symbol), string(timeframe), from, to)
	if err != nil {
		return nil, fmt.Errorf("timescaledb: get market bars: %w", err)
	}
	defer rows.Close()

	var bars []domain.MarketBar
	for rows.Next() {
		var bar domain.MarketBar
		var sym, tf string
		if err := rows.Scan(&bar.Time, &sym, &tf, &bar.Open, &bar.High, &bar.Low, &bar.Close, &bar.Volume, &bar.Suspect); err != nil {
			return nil, fmt.Errorf("timescaledb: scan market bar: %w", err)
		}
		bar.Symbol = domain.Symbol(sym)
		bar.Timeframe = domain.Timeframe(tf)
		bars = append(bars, bar)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate market bars: %w", err)
	}
	return bars, nil
}

// SaveTrade saves a completed or in-progress trade execution.
// It persists the trade details including tenant and environment mode.
func (r *Repository) SaveTrade(ctx context.Context, trade domain.Trade) error {
	_, err := r.db.ExecContext(ctx, queryInsertTrade, trade.Time, trade.TenantID, string(trade.EnvMode), trade.TradeID, string(trade.Symbol), trade.Side, trade.Quantity, trade.Price, trade.Commission, trade.Status)
	if err != nil {
		return fmt.Errorf("timescaledb: save trade: %w", err)
	}
	return nil
}

// GetTrades retrieves trades for a given tenant and environment mode.
// It filters records by the specified tenant and environment, and sets those fields on the returned trades.
func (r *Repository) GetTrades(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.Trade, error) {
	rows, err := r.db.QueryContext(ctx, querySelectTrades, tenantID, string(envMode), from, to)
	if err != nil {
		return nil, fmt.Errorf("timescaledb: get trades: %w", err)
	}
	defer rows.Close()

	var trades []domain.Trade
	for rows.Next() {
		var trade domain.Trade
		var sym string
		if err := rows.Scan(&trade.Time, &trade.TradeID, &sym, &trade.Side, &trade.Quantity, &trade.Price, &trade.Commission, &trade.Status); err != nil {
			return nil, fmt.Errorf("timescaledb: scan trade: %w", err)
		}
		trade.Symbol = domain.Symbol(sym)
		trade.TenantID = tenantID
		trade.EnvMode = envMode
		trades = append(trades, trade)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate trades: %w", err)
	}
	return trades, nil
}

// SaveStrategyDNA saves a strategy configuration.
// It marshals the parameters and performance metrics before persisting.
func (r *Repository) SaveStrategyDNA(ctx context.Context, dna domain.StrategyDNA) error {
	paramsJSON, err := json.Marshal(dna.Parameters)
	if err != nil {
		return fmt.Errorf("timescaledb: marshal parameters: %w", err)
	}
	metricsJSON, err := json.Marshal(dna.PerformanceMetrics)
	if err != nil {
		return fmt.Errorf("timescaledb: marshal metrics: %w", err)
	}
	_, err = r.db.ExecContext(ctx, queryInsertStrategyDNA, time.Now().UTC(), dna.TenantID, string(dna.EnvMode), dna.ID, dna.Version, paramsJSON, metricsJSON)
	if err != nil {
		return fmt.Errorf("timescaledb: save strategy dna: %w", err)
	}
	return nil
}

// GetLatestStrategyDNA retrieves the most recent strategy configuration.
// It returns (nil, nil) when no DNA exists for the given tenant and environment mode.
func (r *Repository) GetLatestStrategyDNA(ctx context.Context, tenantID string, envMode domain.EnvMode) (*domain.StrategyDNA, error) {
	row := r.db.QueryRowContext(ctx, querySelectLatestDNA, tenantID, string(envMode))

	var t time.Time
	_ = t // time is scanned but not mapped — StrategyDNA tracks version, not timestamp
	var id uuid.UUID
	var version int
	var paramsJSON, metricsJSON json.RawMessage

	if err := row.Scan(&t, &id, &version, &paramsJSON, &metricsJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("timescaledb: scan strategy dna: %w", err)
	}

	var params map[string]any
	if err := json.Unmarshal(paramsJSON, &params); err != nil {
		return nil, fmt.Errorf("timescaledb: unmarshal parameters: %w", err)
	}
	var metrics map[string]float64
	if err := json.Unmarshal(metricsJSON, &metrics); err != nil {
		return nil, fmt.Errorf("timescaledb: unmarshal metrics: %w", err)
	}

	return &domain.StrategyDNA{
		ID:                 id,
		TenantID:           tenantID,
		EnvMode:            envMode,
		Version:            version,
		Parameters:         params,
		PerformanceMetrics: metrics,
	}, nil
}
