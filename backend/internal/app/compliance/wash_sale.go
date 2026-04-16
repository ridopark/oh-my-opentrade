// Package compliance houses observational (non-gating) trading-compliance
// journals. Unlike gates, these components never block order flow — they
// record events for downstream reconciliation (accountant, auditor,
// regulator).
package compliance

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
)

// WashSaleWindow is the IRS §1091 wash-sale lookback/lookahead window.
// A loss realized on a covered security is disallowed if a substantially
// identical security was bought within 30 days before OR after the sale.
const WashSaleWindow = 30 * 24 * time.Hour

// BuyRecord is the minimal shape the journal needs to evaluate a wash
// sale: a buy on `Symbol` at `At` with an order id traceable back to
// the broker journal.
type BuyRecord struct {
	ID     string
	Symbol string
	At     time.Time
	Amount float64
}

// LossEvent describes a realized loss the journal should evaluate.
type LossEvent struct {
	TradeID    string
	Symbol     string
	RealizedAt time.Time
	Amount     float64 // positive USD magnitude of the loss (not signed)
}

// WashSaleRow is a persisted wash-sale match.
type WashSaleRow struct {
	Symbol            string
	LossTradeID       string
	LossRealizedAt    time.Time
	LossAmount        float64
	DisallowedAmount  float64
	TriggeringBuyID   string
	TriggeringBuyAt   time.Time
}

// BuyLookup returns buys for `symbol` whose `At` is within [from, to].
type BuyLookup interface {
	BuysInWindow(ctx context.Context, symbol string, from, to time.Time) ([]BuyRecord, error)
}

// JournalSink persists a wash-sale match.
type JournalSink interface {
	RecordWashSale(ctx context.Context, row WashSaleRow) error
}

// WashSaleJournal scans ±30 days of buys for matching symbols when a
// realized loss is reported and writes one row per match.
//
// This is purely observational — no gate consumes it. Callers hook it
// into whatever fires on realized-P&L (the execution service's
// LedgerWriter is the natural candidate) via OnRealizedLoss.
type WashSaleJournal struct {
	buys BuyLookup
	sink JournalSink
	log  zerolog.Logger
}

func NewWashSaleJournal(buys BuyLookup, sink JournalSink, log zerolog.Logger) *WashSaleJournal {
	return &WashSaleJournal{
		buys: buys,
		sink: sink,
		log:  log.With().Str("component", "wash_sale_journal").Logger(),
	}
}

// OnRealizedLoss evaluates `loss` and writes one row per matched buy in
// the ±30 day window. Returns the number of rows written. Positive-P&L
// (non-loss) events are ignored.
func (w *WashSaleJournal) OnRealizedLoss(ctx context.Context, loss LossEvent) (int, error) {
	if loss.Amount <= 0 {
		return 0, nil
	}
	if w.buys == nil {
		return 0, nil
	}
	from := loss.RealizedAt.Add(-WashSaleWindow)
	to := loss.RealizedAt.Add(WashSaleWindow)
	matches, err := w.buys.BuysInWindow(ctx, loss.Symbol, from, to)
	if err != nil {
		return 0, fmt.Errorf("wash_sale: lookup buys: %w", err)
	}
	if len(matches) == 0 {
		return 0, nil
	}
	written := 0
	for _, buy := range matches {
		// Skip the buy leg of the losing trade itself.
		if buy.ID == loss.TradeID {
			continue
		}
		// Explicit window boundary: the 30-day window is inclusive on
		// both edges (IRS guidance treats the 30th day as within the
		// disallowance period).
		if buy.At.Before(from) || buy.At.After(to) {
			continue
		}
		row := WashSaleRow{
			Symbol:           loss.Symbol,
			LossTradeID:      loss.TradeID,
			LossRealizedAt:   loss.RealizedAt,
			LossAmount:       loss.Amount,
			DisallowedAmount: loss.Amount, // full loss disallowed per match; accountant reconciles
			TriggeringBuyID:  buy.ID,
			TriggeringBuyAt:  buy.At,
		}
		if w.sink != nil {
			if err := w.sink.RecordWashSale(ctx, row); err != nil {
				w.log.Warn().Err(err).Str("symbol", loss.Symbol).Str("buy_id", buy.ID).Msg("wash_sale: sink write failed")
				continue
			}
		}
		written++
	}
	return written, nil
}
