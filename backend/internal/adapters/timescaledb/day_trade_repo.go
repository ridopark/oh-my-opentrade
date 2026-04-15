package timescaledb

import (
	"context"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/rs/zerolog"
)

// DayTradeRepo persists risk.DayTrade rows to the day_trades hypertable.
// Satisfies risk.DayTradeSink.
type DayTradeRepo struct {
	db  DBTX
	log zerolog.Logger
}

func NewDayTradeRepo(db DBTX, log zerolog.Logger) *DayTradeRepo {
	return &DayTradeRepo{db: db, log: log}
}

const queryInsertDayTrade = `INSERT INTO day_trades (
    account_id, trading_date, symbol, qty_traded, opened_at, closed_at
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT DO NOTHING`

const queryCountDayTrades = `SELECT COUNT(*) FROM day_trades
WHERE account_id = $1 AND trading_date = $2`

// RecordDayTrade implements risk.DayTradeSink.
func (r *DayTradeRepo) RecordDayTrade(ctx context.Context, dt risk.DayTrade) error {
	if _, err := r.db.ExecContext(ctx, queryInsertDayTrade,
		dt.AccountID, dt.TradingDate, dt.Symbol, dt.QtyTraded, dt.OpenedAt, dt.ClosedAt,
	); err != nil {
		return fmt.Errorf("day_trade_repo: insert: %w", err)
	}
	return nil
}

// CountByDate is a helper query used by reconciliation tools and tests.
func (r *DayTradeRepo) CountByDate(ctx context.Context, accountID string, tradingDate string) (int, error) {
	row := r.db.QueryRowContext(ctx, queryCountDayTrades, accountID, tradingDate)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("day_trade_repo: count: %w", err)
	}
	return n, nil
}
