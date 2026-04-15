package timescaledb

import (
	"context"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/app/compliance"
	"github.com/rs/zerolog"
)

// WashSaleRepo persists compliance.WashSaleRow into the wash_sales table.
// Satisfies compliance.JournalSink.
type WashSaleRepo struct {
	db  DBTX
	log zerolog.Logger
}

func NewWashSaleRepo(db DBTX, log zerolog.Logger) *WashSaleRepo {
	return &WashSaleRepo{db: db, log: log}
}

const queryInsertWashSale = `INSERT INTO wash_sales (
    symbol, loss_trade_id, loss_realized_at, loss_amount,
    disallowed_amount, triggering_buy_id, triggering_buy_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)`

// RecordWashSale implements compliance.JournalSink.
func (r *WashSaleRepo) RecordWashSale(ctx context.Context, row compliance.WashSaleRow) error {
	if _, err := r.db.ExecContext(ctx, queryInsertWashSale,
		row.Symbol, row.LossTradeID, row.LossRealizedAt, row.LossAmount,
		row.DisallowedAmount, row.TriggeringBuyID, row.TriggeringBuyAt,
	); err != nil {
		return fmt.Errorf("wash_sale_repo: insert: %w", err)
	}
	return nil
}
