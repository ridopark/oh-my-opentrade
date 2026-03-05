package perf

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// LedgerWriter subscribes to FillReceived events and maintains the daily P&L
// ledger and equity curve. It is the single writer for both daily_pnl and
// equity_curve tables.
type LedgerWriter struct {
	eventBus ports.EventBusPort
	pnlRepo  ports.PnLPort
	broker   ports.BrokerPort
	log      zerolog.Logger

	mu            sync.Mutex
	dailyPnL      map[string]*dailyAccum // key: tenantID:envMode:date
	peakEquity    float64
	accountEquity float64
}

// dailyAccum accumulates realized P&L and trade count for a single day.
type dailyAccum struct {
	date        time.Time
	tenantID    string
	envMode     domain.EnvMode
	realizedPnL float64
	tradeCount  int
	maxDrawdown float64
}

// NewLedgerWriter creates a new LedgerWriter.
func NewLedgerWriter(
	eventBus ports.EventBusPort,
	pnlRepo ports.PnLPort,
	broker ports.BrokerPort,
	accountEquity float64,
	log zerolog.Logger,
) *LedgerWriter {
	return &LedgerWriter{
		eventBus:      eventBus,
		pnlRepo:       pnlRepo,
		broker:        broker,
		log:           log,
		dailyPnL:      make(map[string]*dailyAccum),
		peakEquity:    accountEquity,
		accountEquity: accountEquity,
	}
}

// SetAccountEquity updates the account equity used for drawdown calculations.
func (lw *LedgerWriter) SetAccountEquity(equity float64) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	lw.accountEquity = equity
	if equity > lw.peakEquity {
		lw.peakEquity = equity
	}
}

// Start subscribes the ledger writer to FillReceived events.
func (lw *LedgerWriter) Start(ctx context.Context) error {
	if err := lw.eventBus.Subscribe(ctx, domain.EventFillReceived, lw.handleFill); err != nil {
		return fmt.Errorf("perf: ledger writer failed to subscribe to FillReceived: %w", err)
	}
	lw.log.Info().Msg("ledger writer subscribed to FillReceived events")
	return nil
}

// handleFill processes a single FillReceived event, updating P&L and equity.
func (lw *LedgerWriter) handleFill(ctx context.Context, event domain.Event) error {
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		lw.log.Warn().Msg("ledger writer: unexpected FillReceived payload type")
		return nil
	}

	symbol, _ := payload["symbol"].(string)
	side, _ := payload["side"].(string)
	quantity, _ := payload["quantity"].(float64)
	price, _ := payload["price"].(float64)

	if symbol == "" || quantity == 0 {
		return nil
	}

	lw.mu.Lock()
	defer lw.mu.Unlock()

	// Calculate realized P&L from this fill.
	// For paper trading: BUY side = opening position (no P&L), SELL side = closing position (P&L realized).
	// Simplified FIFO: we track net realized P&L incrementally on each sell.
	// A more sophisticated approach would match entry/exit pairs, but for MVP
	// we compute realized P&L as the fill notional with sign based on side.
	var fillPnL float64
	switch side {
	case "sell":
		// Selling closes a long — P&L depends on entry price which we don't track here.
		// The circuit breaker will query the daily_pnl table for cumulative realized P&L.
		// For now, record the notional value as a tracking entry; the actual P&L
		// calculation is done by comparing fills within the day.
		fillPnL = quantity * price // sell proceeds (positive)
	case "buy":
		fillPnL = -(quantity * price) // buy cost (negative)
	}

	// Get or create daily accumulator.
	now := time.Now().UTC()
	dateKey := fmt.Sprintf("%s:%s:%s", event.TenantID, string(event.EnvMode), now.Format("2006-01-02"))
	accum, exists := lw.dailyPnL[dateKey]
	if !exists {
		accum = &dailyAccum{
			date:     time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
			tenantID: event.TenantID,
			envMode:  event.EnvMode,
		}
		lw.dailyPnL[dateKey] = accum
	}

	accum.realizedPnL += fillPnL
	accum.tradeCount++

	// Update drawdown tracking.
	currentEquity := lw.accountEquity + accum.realizedPnL
	if currentEquity < lw.peakEquity {
		drawdown := (lw.peakEquity - currentEquity) / lw.peakEquity
		if drawdown > accum.maxDrawdown {
			accum.maxDrawdown = drawdown
		}
	}

	// Persist daily P&L.
	pnl := domain.DailyPnL{
		Date:        accum.date,
		TenantID:    accum.tenantID,
		EnvMode:     accum.envMode,
		RealizedPnL: accum.realizedPnL,
		TradeCount:  accum.tradeCount,
		MaxDrawdown: accum.maxDrawdown,
	}
	if err := lw.pnlRepo.UpsertDailyPnL(ctx, pnl); err != nil {
		lw.log.Error().Err(err).Str("symbol", symbol).Msg("ledger writer: failed to upsert daily P&L")
	}

	// Persist equity curve point.
	pt := domain.EquityPoint{
		Time:     now,
		TenantID: event.TenantID,
		EnvMode:  event.EnvMode,
		Equity:   currentEquity,
		Cash:     lw.accountEquity, // pre-trade cash baseline
		Drawdown: accum.maxDrawdown,
	}
	if err := lw.pnlRepo.SaveEquityPoint(ctx, pt); err != nil {
		lw.log.Error().Err(err).Str("symbol", symbol).Msg("ledger writer: failed to save equity point")
	}

	lw.log.Info().
		Str("symbol", symbol).
		Str("side", side).
		Float64("quantity", quantity).
		Float64("price", price).
		Float64("fill_pnl", fillPnL).
		Float64("daily_realized_pnl", accum.realizedPnL).
		Int("daily_trade_count", accum.tradeCount).
		Msg("ledger writer: fill recorded")

	return nil
}

// GetDailyRealizedPnL returns the in-memory cumulative realized P&L for today.
// This is used by the circuit breaker for fast lookups without DB queries.
func (lw *LedgerWriter) GetDailyRealizedPnL(tenantID string, envMode domain.EnvMode) float64 {
	lw.mu.Lock()
	defer lw.mu.Unlock()

	now := time.Now().UTC()
	dateKey := fmt.Sprintf("%s:%s:%s", tenantID, string(envMode), now.Format("2006-01-02"))
	accum, exists := lw.dailyPnL[dateKey]
	if !exists {
		return 0
	}
	return accum.realizedPnL
}
