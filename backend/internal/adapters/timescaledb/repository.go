package timescaledb

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

const (
	queryInsertMarketBar   = `INSERT INTO market_bars (time, account_id, env_mode, symbol, timeframe, open, high, low, close, volume, suspect) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT (symbol, timeframe, time) DO UPDATE SET open=EXCLUDED.open, high=EXCLUDED.high, low=EXCLUDED.low, close=EXCLUDED.close, volume=EXCLUDED.volume, suspect=EXCLUDED.suspect`
	querySelectMarketBars  = `SELECT time, symbol, timeframe, open, high, low, close, volume, suspect FROM market_bars WHERE symbol = $1 AND timeframe = $2 AND time >= $3 AND time < $4 ORDER BY time`
	queryInsertTrade       = `INSERT INTO trades (time, account_id, env_mode, trade_id, symbol, side, quantity, price, commission, status) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	querySelectTrades      = `SELECT time, trade_id, symbol, side, quantity, price, commission, status FROM trades WHERE account_id = $1 AND env_mode = $2 AND time >= $3 AND time <= $4 ORDER BY time`
	queryInsertStrategyDNA = `INSERT INTO strategy_dna_history (time, account_id, env_mode, strategy_id, version, parameters, performance) VALUES ($1, $2, $3, $4, $5, $6, $7)`
	querySelectLatestDNA   = `SELECT time, strategy_id, version, parameters, performance FROM strategy_dna_history WHERE account_id = $1 AND env_mode = $2 ORDER BY time DESC LIMIT 1`
	queryInsertOrder     = `INSERT INTO orders (time, account_id, env_mode, intent_id, broker_order_id, symbol, side, quantity, limit_price, stop_loss, status) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT (broker_order_id) DO NOTHING`
	queryUpdateOrderFill = `UPDATE orders SET status = 'filled', filled_at = $2, filled_price = $3, filled_qty = $4 WHERE broker_order_id = $1`
)

// SaveMarketBar saves a single OHLCV candle.
// Market data is shared across all tenants; account_id is stored as empty string.
// env_mode is always Paper since market bars are feed data, not account-specific.
func (r *Repository) SaveMarketBar(ctx context.Context, bar domain.MarketBar) error {
	_, err := r.db.ExecContext(ctx, queryInsertMarketBar, bar.Time, "", string(domain.EnvModePaper), string(bar.Symbol), string(bar.Timeframe), bar.Open, bar.High, bar.Low, bar.Close, bar.Volume, bar.Suspect)
	if err != nil {
		r.log.Error().Err(err).
			Str("symbol", string(bar.Symbol)).
			Str("timeframe", string(bar.Timeframe)).
			Time("bar_time", bar.Time).
			Msg("failed to save market bar")
		return fmt.Errorf("timescaledb: save market bar: %w", err)
	}
	return nil
}

// SaveMarketBars upserts a batch of market bars in a single INSERT statement.
// It returns the number of bars processed. Bars with volume <= 0 are skipped.
func (r *Repository) SaveMarketBars(ctx context.Context, bars []domain.MarketBar) (int, error) {
	if len(bars) == 0 {
		return 0, nil
	}
	
	// Build batched INSERT ... VALUES (...), (...), ... ON CONFLICT DO UPDATE
	var b strings.Builder
	b.WriteString("INSERT INTO market_bars (time, account_id, env_mode, symbol, timeframe, open, high, low, close, volume, suspect) VALUES ")
	
	args := make([]any, 0, len(bars)*11)
	idx := 0
	for _, bar := range bars {
		if bar.Volume <= 0 {
			continue
		}
		if idx > 0 {
			b.WriteString(", ")
		}
		base := idx*11 + 1
		fmt.Fprintf(&b, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base, base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10)
		args = append(args, bar.Time, "", string(domain.EnvModePaper), string(bar.Symbol), string(bar.Timeframe), bar.Open, bar.High, bar.Low, bar.Close, bar.Volume, bar.Suspect)
		idx++
	}
	
	if idx == 0 {
		return 0, nil
	}
	
	b.WriteString(" ON CONFLICT (symbol, timeframe, time) DO UPDATE SET open=EXCLUDED.open, high=EXCLUDED.high, low=EXCLUDED.low, close=EXCLUDED.close, volume=EXCLUDED.volume, suspect=EXCLUDED.suspect")
	
	_, err := r.db.ExecContext(ctx, b.String(), args...)
	if err != nil {
		r.log.Error().Err(err).Int("batch_size", idx).Msg("failed to save market bars batch")
		return 0, fmt.Errorf("timescaledb: save market bars batch: %w", err)
	}
	return idx, nil
}

// GetMarketBars retrieves historical market bars.
// It returns the bars ordered by time ascending.
func (r *Repository) GetMarketBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	rows, err := r.db.QueryContext(ctx, querySelectMarketBars, string(symbol), string(timeframe), from, to)
	if err != nil {
		r.log.Error().Err(err).
			Str("symbol", string(symbol)).
			Str("timeframe", string(timeframe)).
			Msg("failed to query market bars")
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

// GetLatestMarketBarTime returns the most recent bar time for a given symbol and timeframe.
// Returns (nil, nil) if no bars exist.
func (r *Repository) GetLatestMarketBarTime(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe) (*time.Time, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT MAX(time) FROM market_bars WHERE symbol = $1 AND timeframe = $2",
		string(symbol), string(timeframe))
	
	var t *time.Time
	if err := row.Scan(&t); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("timescaledb: get latest market bar time: %w", err)
	}
	return t, nil
}

// SaveTrade saves a completed or in-progress trade execution.
// It persists the trade details including tenant and environment mode.
func (r *Repository) SaveTrade(ctx context.Context, trade domain.Trade) error {
	_, err := r.db.ExecContext(ctx, queryInsertTrade, trade.Time, trade.TenantID, string(trade.EnvMode), trade.TradeID, string(trade.Symbol), trade.Side, trade.Quantity, trade.Price, trade.Commission, trade.Status)
	if err != nil {
		r.log.Error().Err(err).
			Str("symbol", string(trade.Symbol)).
			Str("trade_id", trade.TradeID.String()).
			Str("tenant_id", trade.TenantID).
			Msg("failed to save trade")
		return fmt.Errorf("timescaledb: save trade: %w", err)
	}
	return nil
}

// GetTrades retrieves trades for a given tenant and environment mode.
// It filters records by the specified tenant and environment, and sets those fields on the returned trades.
func (r *Repository) GetTrades(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.Trade, error) {
	rows, err := r.db.QueryContext(ctx, querySelectTrades, tenantID, string(envMode), from, to)
	if err != nil {
		r.log.Error().Err(err).
			Str("tenant_id", tenantID).
			Str("env_mode", string(envMode)).
			Msg("failed to query trades")
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
		r.log.Error().Err(err).
			Str("strategy_id", dna.ID.String()).
			Int("version", dna.Version).
			Msg("failed to save strategy DNA")
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
		r.log.Error().Err(err).
			Str("tenant_id", tenantID).
			Str("env_mode", string(envMode)).
			Msg("failed to scan strategy DNA row")
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

// SaveOrder persists a submitted broker order.
func (r *Repository) SaveOrder(ctx context.Context, order domain.BrokerOrder) error {
	_, err := r.db.ExecContext(ctx, queryInsertOrder,
		order.Time, order.TenantID, string(order.EnvMode),
		order.IntentID, order.BrokerOrderID,
		string(order.Symbol), order.Side,
		order.Quantity, order.LimitPrice, order.StopLoss,
		order.Status,
	)
	if err != nil {
		r.log.Error().Err(err).
			Str("broker_order_id", order.BrokerOrderID).
			Msg("failed to save order")
		return fmt.Errorf("timescaledb: save order: %w", err)
	}
	return nil
}

// UpdateOrderFill marks an order as filled with execution details.
func (r *Repository) UpdateOrderFill(ctx context.Context, brokerOrderID string, filledAt time.Time, filledPrice, filledQty float64) error {
	_, err := r.db.ExecContext(ctx, queryUpdateOrderFill, brokerOrderID, filledAt, filledPrice, filledQty)
	if err != nil {
		r.log.Error().Err(err).
			Str("broker_order_id", brokerOrderID).
			Msg("failed to update order fill")
		return fmt.Errorf("timescaledb: update order fill: %w", err)
	}
	return nil
}

// ListTrades retrieves trades with optional filters and keyset pagination.
func (r *Repository) ListTrades(ctx context.Context, q ports.TradeQuery) (ports.TradePage, error) {
	var b strings.Builder
	b.WriteString(`SELECT time, trade_id, symbol, side, quantity, price, commission, status
		FROM trades WHERE account_id = $1 AND env_mode = $2 AND time >= $3 AND time <= $4`)

	args := []any{q.TenantID, string(q.EnvMode), q.From, q.To}
	argIdx := 5

	if q.Symbol != "" {
		fmt.Fprintf(&b, " AND symbol = $%d", argIdx)
		args = append(args, q.Symbol)
		argIdx++
	}
	if q.Side != "" {
		fmt.Fprintf(&b, " AND side = $%d", argIdx)
		args = append(args, q.Side)
		argIdx++
	}
	if q.CursorTime != nil && q.CursorID != "" {
		fmt.Fprintf(&b, " AND (time, trade_id::text) < ($%d, $%d)", argIdx, argIdx+1)
		args = append(args, *q.CursorTime, q.CursorID)
		argIdx += 2
	}

	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	fmt.Fprintf(&b, " ORDER BY time DESC, trade_id DESC LIMIT $%d", argIdx)
	args = append(args, limit+1) // fetch one extra to detect next page

	rows, err := r.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		r.log.Error().Err(err).Msg("failed to list trades")
		return ports.TradePage{}, fmt.Errorf("timescaledb: list trades: %w", err)
	}
	defer rows.Close()

	var trades []domain.Trade
	for rows.Next() {
		var t domain.Trade
		var sym string
		if err := rows.Scan(&t.Time, &t.TradeID, &sym, &t.Side, &t.Quantity, &t.Price, &t.Commission, &t.Status); err != nil {
			return ports.TradePage{}, fmt.Errorf("timescaledb: scan trade row: %w", err)
		}
		t.Symbol = domain.Symbol(sym)
		t.TenantID = q.TenantID
		t.EnvMode = q.EnvMode
		trades = append(trades, t)
	}
	if err := rows.Err(); err != nil {
		return ports.TradePage{}, fmt.Errorf("timescaledb: iterate trades: %w", err)
	}

	var nextCursor string
	if len(trades) > limit {
		last := trades[limit-1]
		// Encode cursor as base64(time|trade_id)
		cursorData := fmt.Sprintf("%s|%s", last.Time.Format(time.RFC3339Nano), last.TradeID.String())
		nextCursor = base64.URLEncoding.EncodeToString([]byte(cursorData))
		trades = trades[:limit]
	}

	return ports.TradePage{Items: trades, NextCursor: nextCursor}, nil
}
