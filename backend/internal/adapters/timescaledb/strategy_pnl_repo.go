package timescaledb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

const (
	queryUpsertStrategyDailyPnL = `INSERT INTO strategy_daily_pnl
		(date, account_id, env_mode, strategy, realized_pnl, fees, trade_count, win_count, loss_count, gross_profit, gross_loss)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (account_id, env_mode, strategy, date) DO UPDATE SET
			realized_pnl = EXCLUDED.realized_pnl,
			fees = EXCLUDED.fees,
			trade_count = EXCLUDED.trade_count,
			win_count = EXCLUDED.win_count,
			loss_count = EXCLUDED.loss_count,
			gross_profit = EXCLUDED.gross_profit,
			gross_loss = EXCLUDED.gross_loss`

	querySelectStrategyDailyPnL = `SELECT date, strategy, realized_pnl, fees, trade_count, win_count, loss_count, gross_profit, gross_loss
		FROM strategy_daily_pnl
		WHERE account_id = $1 AND env_mode = $2 AND strategy = $3 AND date >= $4 AND date <= $5
		ORDER BY date`

	queryInsertStrategyEquityPoint = `INSERT INTO strategy_equity_points
		(time, account_id, env_mode, strategy, equity, realized_pnl_to_date, fees_to_date, trade_count_to_date)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	querySelectStrategyEquityCurve = `SELECT time, strategy, equity, realized_pnl_to_date, fees_to_date, trade_count_to_date
		FROM strategy_equity_points
		WHERE account_id = $1 AND env_mode = $2 AND strategy = $3 AND time >= $4 AND time <= $5
		ORDER BY time`

	queryInsertStrategySignalEvent = `INSERT INTO strategy_signal_events
		(ts, account_id, env_mode, strategy, signal_id, symbol, kind, side, status, reason, confidence, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

	querySelectStrategyDashboardSummary = `SELECT
		COALESCE(SUM(realized_pnl), 0),
		COALESCE(SUM(fees), 0),
		COALESCE(SUM(trade_count), 0),
		COALESCE(SUM(win_count), 0),
		COALESCE(SUM(loss_count), 0),
		COALESCE(SUM(gross_profit), 0),
		COALESCE(SUM(gross_loss), 0)
		FROM strategy_daily_pnl
		WHERE account_id = $1 AND env_mode = $2 AND strategy = $3 AND date >= $4 AND date <= $5`

	querySelectSymbolAttribution = `SELECT
		t.symbol,
		COALESCE(SUM(CASE WHEN t.side = 'SELL' THEN (t.price * t.quantity) ELSE -(t.price * t.quantity) END), 0) AS realized_pnl,
		COUNT(*) AS trade_count,
		COUNT(*) FILTER (WHERE t.side = 'SELL' AND t.price > 0) AS win_count,
		0 AS loss_count
		FROM trades t
		WHERE t.account_id = $1 AND t.env_mode = $2 AND t.strategy = $3 AND t.time >= $4 AND t.time <= $5
		GROUP BY t.symbol
		ORDER BY realized_pnl DESC`

	querySelectAllStrategySummaries = `SELECT
		strategy,
		COALESCE(SUM(realized_pnl), 0),
		COALESCE(SUM(fees), 0),
		COALESCE(SUM(trade_count), 0),
		COALESCE(SUM(win_count), 0),
		COALESCE(SUM(loss_count), 0),
		COALESCE(SUM(gross_profit), 0),
		COALESCE(SUM(gross_loss), 0)
		FROM strategy_daily_pnl
		WHERE account_id = $1 AND env_mode = $2 AND date >= $3 AND date <= $4
		GROUP BY strategy
		ORDER BY SUM(realized_pnl) DESC`
)

// UpsertStrategyDailyPnL inserts or updates the per-strategy daily P&L record.
func (r *PnLRepository) UpsertStrategyDailyPnL(ctx context.Context, pnl domain.StrategyDailyPnL) error {
	_, err := r.db.ExecContext(ctx, queryUpsertStrategyDailyPnL,
		pnl.Day, pnl.TenantID, string(pnl.EnvMode), pnl.Strategy,
		pnl.RealizedPnL, pnl.Fees, pnl.TradeCount,
		pnl.WinCount, pnl.LossCount, pnl.GrossProfit, pnl.GrossLoss,
	)
	if err != nil {
		r.log.Error().Err(err).
			Time("date", pnl.Day).
			Str("strategy", pnl.Strategy).
			Msg("failed to upsert strategy daily P&L")
		return fmt.Errorf("timescaledb: upsert strategy daily pnl: %w", err)
	}
	return nil
}

// GetStrategyDailyPnL retrieves daily P&L records for a specific strategy within a date range.
func (r *PnLRepository) GetStrategyDailyPnL(ctx context.Context, tenantID string, envMode domain.EnvMode, strategy string, from, to time.Time) ([]domain.StrategyDailyPnL, error) {
	rows, err := r.db.QueryContext(ctx, querySelectStrategyDailyPnL, tenantID, string(envMode), strategy, from, to)
	if err != nil {
		r.log.Error().Err(err).
			Str("strategy", strategy).
			Msg("failed to query strategy daily P&L")
		return nil, fmt.Errorf("timescaledb: get strategy daily pnl: %w", err)
	}
	defer rows.Close()

	var results []domain.StrategyDailyPnL
	for rows.Next() {
		var pnl domain.StrategyDailyPnL
		if err := rows.Scan(
			&pnl.Day, &pnl.Strategy, &pnl.RealizedPnL, &pnl.Fees,
			&pnl.TradeCount, &pnl.WinCount, &pnl.LossCount,
			&pnl.GrossProfit, &pnl.GrossLoss,
		); err != nil {
			return nil, fmt.Errorf("timescaledb: scan strategy daily pnl: %w", err)
		}
		pnl.TenantID = tenantID
		pnl.EnvMode = envMode
		results = append(results, pnl)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate strategy daily pnl: %w", err)
	}
	return results, nil
}

// SaveStrategyEquityPoint appends a point to the per-strategy equity curve.
func (r *PnLRepository) SaveStrategyEquityPoint(ctx context.Context, pt domain.StrategyEquityPoint) error {
	_, err := r.db.ExecContext(ctx, queryInsertStrategyEquityPoint,
		pt.Time, pt.TenantID, string(pt.EnvMode), pt.Strategy,
		pt.Equity, pt.RealizedPnLToDate, pt.FeesToDate, pt.TradeCountToDate,
	)
	if err != nil {
		r.log.Error().Err(err).
			Str("strategy", pt.Strategy).
			Msg("failed to save strategy equity point")
		return fmt.Errorf("timescaledb: save strategy equity point: %w", err)
	}
	return nil
}

// GetStrategyEquityCurve retrieves per-strategy equity curve points within a time range.
func (r *PnLRepository) GetStrategyEquityCurve(ctx context.Context, tenantID string, envMode domain.EnvMode, strategy string, from, to time.Time) ([]domain.StrategyEquityPoint, error) {
	rows, err := r.db.QueryContext(ctx, querySelectStrategyEquityCurve, tenantID, string(envMode), strategy, from, to)
	if err != nil {
		r.log.Error().Err(err).
			Str("strategy", strategy).
			Msg("failed to query strategy equity curve")
		return nil, fmt.Errorf("timescaledb: get strategy equity curve: %w", err)
	}
	defer rows.Close()

	var results []domain.StrategyEquityPoint
	for rows.Next() {
		var pt domain.StrategyEquityPoint
		if err := rows.Scan(
			&pt.Time, &pt.Strategy, &pt.Equity,
			&pt.RealizedPnLToDate, &pt.FeesToDate, &pt.TradeCountToDate,
		); err != nil {
			return nil, fmt.Errorf("timescaledb: scan strategy equity point: %w", err)
		}
		pt.TenantID = tenantID
		pt.EnvMode = envMode
		results = append(results, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate strategy equity curve: %w", err)
	}
	return results, nil
}

// SaveStrategySignalEvent appends a signal lifecycle event.
func (r *PnLRepository) SaveStrategySignalEvent(ctx context.Context, evt domain.StrategySignalEvent) error {
	payloadBytes, err := json.Marshal(evt.Payload)
	if err != nil {
		payloadBytes = nil
	}
	_, err = r.db.ExecContext(ctx, queryInsertStrategySignalEvent,
		evt.TS, evt.TenantID, string(evt.EnvMode), evt.Strategy,
		evt.SignalID, evt.Symbol, evt.Kind, evt.Side,
		string(evt.Status), evt.Reason, evt.Confidence, payloadBytes,
	)
	if err != nil {
		r.log.Error().Err(err).
			Str("strategy", evt.Strategy).
			Str("signal_id", evt.SignalID).
			Msg("failed to save strategy signal event")
		return fmt.Errorf("timescaledb: save strategy signal event: %w", err)
	}
	return nil
}

// GetStrategySignalEvents retrieves signal events with optional filters and keyset pagination.
func (r *PnLRepository) GetStrategySignalEvents(ctx context.Context, q ports.StrategySignalQuery) (ports.StrategySignalPage, error) {
	var b strings.Builder
	b.WriteString(`SELECT ts, strategy, signal_id, symbol, kind, side, status, reason, confidence, payload
		FROM strategy_signal_events
		WHERE account_id = $1 AND env_mode = $2 AND ts >= $3 AND ts <= $4`)

	args := []any{q.TenantID, string(q.EnvMode), q.From, q.To}
	argIdx := 5

	if q.Strategy != "" {
		fmt.Fprintf(&b, " AND strategy = $%d", argIdx)
		args = append(args, q.Strategy)
		argIdx++
	}

	if q.Symbol != "" {
		fmt.Fprintf(&b, " AND symbol = $%d", argIdx)
		args = append(args, q.Symbol)
		argIdx++
	}
	if q.CursorTime != nil && q.CursorID != "" {
		fmt.Fprintf(&b, " AND (ts, signal_id) < ($%d, $%d)", argIdx, argIdx+1)
		args = append(args, *q.CursorTime, q.CursorID)
		argIdx += 2
	}

	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	fmt.Fprintf(&b, " ORDER BY ts DESC, signal_id DESC LIMIT $%d", argIdx)
	args = append(args, limit+1)

	rows, err := r.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		r.log.Error().Err(err).
			Str("strategy", q.Strategy).
			Msg("failed to query strategy signal events")
		return ports.StrategySignalPage{}, fmt.Errorf("timescaledb: get strategy signal events: %w", err)
	}
	defer rows.Close()

	var events []domain.StrategySignalEvent
	for rows.Next() {
		var evt domain.StrategySignalEvent
		var payload []byte
		if err := rows.Scan(
			&evt.TS, &evt.Strategy, &evt.SignalID, &evt.Symbol,
			&evt.Kind, &evt.Side, &evt.Status, &evt.Reason,
			&evt.Confidence, &payload,
		); err != nil {
			return ports.StrategySignalPage{}, fmt.Errorf("timescaledb: scan strategy signal event: %w", err)
		}
		evt.TenantID = q.TenantID
		evt.EnvMode = q.EnvMode
		if payload != nil {
			evt.Payload = json.RawMessage(payload)
		}
		events = append(events, evt)
	}
	if err := rows.Err(); err != nil {
		return ports.StrategySignalPage{}, fmt.Errorf("timescaledb: iterate strategy signal events: %w", err)
	}

	var nextCursor string
	if len(events) > limit {
		last := events[limit-1]
		cursorData := fmt.Sprintf("%s|%s", last.TS.Format(time.RFC3339Nano), last.SignalID)
		nextCursor = base64.URLEncoding.EncodeToString([]byte(cursorData))
		events = events[:limit]
	}

	return ports.StrategySignalPage{Items: events, NextCursor: nextCursor}, nil
}

// GetStrategyDashboard computes an aggregated dashboard for a strategy within a date range.
func (r *PnLRepository) GetStrategyDashboard(ctx context.Context, tenantID string, envMode domain.EnvMode, strategy string, from, to time.Time) (domain.StrategyDashboard, error) {
	dash := domain.StrategyDashboard{Strategy: strategy}

	// 1. Summary aggregation from strategy_daily_pnl.
	row := r.db.QueryRowContext(ctx, querySelectStrategyDashboardSummary, tenantID, string(envMode), strategy, from, to)
	if err := row.Scan(
		&dash.Summary.TotalRealizedPnL,
		&dash.Summary.TotalFees,
		&dash.Summary.TotalTrades,
		&dash.Summary.WinCount,
		&dash.Summary.LossCount,
		&dash.Summary.GrossProfit,
		&dash.Summary.GrossLoss,
	); err != nil {
		return dash, fmt.Errorf("timescaledb: get strategy dashboard summary: %w", err)
	}

	// Compute derived metrics.
	if dash.Summary.TotalTrades > 0 {
		dash.Summary.WinRate = float64(dash.Summary.WinCount) / float64(dash.Summary.TotalTrades)
	}
	if dash.Summary.GrossLoss != 0 {
		dash.Summary.ProfitFactor = dash.Summary.GrossProfit / -dash.Summary.GrossLoss
	}

	// 2. Daily P&L series.
	dailyPnL, err := r.GetStrategyDailyPnL(ctx, tenantID, envMode, strategy, from, to)
	if err != nil {
		return dash, err
	}
	dash.DailyPnL = dailyPnL

	// 3. Equity curve.
	equityCurve, err := r.GetStrategyEquityCurve(ctx, tenantID, envMode, strategy, from, to)
	if err != nil {
		return dash, err
	}
	dash.EquityCurve = equityCurve

	// 4. By-symbol attribution from trades table.
	attrRows, err := r.db.QueryContext(ctx, querySelectSymbolAttribution, tenantID, string(envMode), strategy, from, to)
	if err != nil {
		r.log.Error().Err(err).
			Str("strategy", strategy).
			Msg("failed to query symbol attribution")
		return dash, fmt.Errorf("timescaledb: get symbol attribution: %w", err)
	}
	defer attrRows.Close()

	for attrRows.Next() {
		var attr domain.SymbolAttribution
		if err := attrRows.Scan(&attr.Symbol, &attr.RealizedPnL, &attr.TradeCount, &attr.WinCount, &attr.LossCount); err != nil {
			return dash, fmt.Errorf("timescaledb: scan symbol attribution: %w", err)
		}
		dash.BySymbol = append(dash.BySymbol, attr)
	}
	if err := attrRows.Err(); err != nil {
		return dash, fmt.Errorf("timescaledb: iterate symbol attribution: %w", err)
	}

	return dash, nil
}

func (r *PnLRepository) ListStrategySummaries(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.StrategySummaryRow, error) {
	rows, err := r.db.QueryContext(ctx, querySelectAllStrategySummaries, tenantID, string(envMode), from, to)
	if err != nil {
		r.log.Error().Err(err).Msg("failed to query strategy summaries")
		return nil, fmt.Errorf("timescaledb: list strategy summaries: %w", err)
	}
	defer rows.Close()

	var results []domain.StrategySummaryRow
	for rows.Next() {
		var row domain.StrategySummaryRow
		if err := rows.Scan(
			&row.Strategy, &row.RealizedPnL, &row.Fees,
			&row.TotalTrades, &row.WinCount, &row.LossCount,
			&row.GrossProfit, &row.GrossLoss,
		); err != nil {
			return nil, fmt.Errorf("timescaledb: scan strategy summary: %w", err)
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate strategy summaries: %w", err)
	}
	return results, nil
}

func (r *PnLRepository) ListSymbolAttribution(ctx context.Context, tenantID string, envMode domain.EnvMode, strategy string, from, to time.Time) ([]domain.SymbolAttribution, error) {
	var b strings.Builder
	b.WriteString(`SELECT t.symbol,
		COALESCE(SUM(CASE WHEN t.side = 'SELL' THEN (t.price * t.quantity) ELSE -(t.price * t.quantity) END), 0) AS realized_pnl,
		COUNT(*) AS trade_count,
		COUNT(*) FILTER (WHERE t.side = 'SELL' AND t.price > 0) AS win_count,
		0 AS loss_count
		FROM trades t
		WHERE t.account_id = $1 AND t.env_mode = $2 AND t.time >= $3 AND t.time <= $4`)

	args := []any{tenantID, string(envMode), from, to}
	if strategy != "" {
		b.WriteString(` AND t.strategy = $5`)
		args = append(args, strategy)
	}
	b.WriteString(` GROUP BY t.symbol ORDER BY realized_pnl DESC`)

	rows, err := r.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		r.log.Error().Err(err).Msg("failed to query symbol attribution")
		return nil, fmt.Errorf("timescaledb: list symbol attribution: %w", err)
	}
	defer rows.Close()

	var results []domain.SymbolAttribution
	for rows.Next() {
		var attr domain.SymbolAttribution
		if err := rows.Scan(&attr.Symbol, &attr.RealizedPnL, &attr.TradeCount, &attr.WinCount, &attr.LossCount); err != nil {
			return nil, fmt.Errorf("timescaledb: scan symbol attribution: %w", err)
		}
		results = append(results, attr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate symbol attribution: %w", err)
	}
	return results, nil
}
