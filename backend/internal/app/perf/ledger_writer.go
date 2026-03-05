package perf

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// positionEntry tracks the average entry price and quantity for one symbol
// within a tenant+envMode context. Used to compute realized P&L on sells.
type positionEntry struct {
	avgEntry float64
	quantity float64
}

// LedgerWriter subscribes to FillReceived events and maintains the daily P&L
// ledger and equity curve. It is the single writer for both daily_pnl and
// equity_curve tables.
type LedgerWriter struct {
	eventBus ports.EventBusPort
	pnlRepo  ports.PnLPort
	broker   ports.BrokerPort
	log      zerolog.Logger
	metrics  *metrics.Metrics

	mu            sync.Mutex
	dailyPnL      map[string]*dailyAccum // key: tenantID:envMode:date
	positions     map[string]*positionEntry // key: tenantID:envMode:symbol
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
		positions:     make(map[string]*positionEntry),
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

// SetMetrics wires Prometheus metrics into the ledger writer.
func (lw *LedgerWriter) SetMetrics(m *metrics.Metrics) { lw.metrics = m }

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

	// Compute realized P&L using position tracking.
	// BUY = opening position: update avg entry, no realized P&L.
	// SELL = closing position: realized P&L = (sell_price - avg_entry) × quantity.
	posKey := fmt.Sprintf("%s:%s:%s", event.TenantID, string(event.EnvMode), symbol)
	var fillPnL float64
	switch side {
	case "buy", "BUY":
		pos := lw.positions[posKey]
		if pos == nil {
			pos = &positionEntry{}
			lw.positions[posKey] = pos
		}
		// Update weighted average entry price.
		totalCost := pos.avgEntry*pos.quantity + price*quantity
		pos.quantity += quantity
		if pos.quantity > 0 {
			pos.avgEntry = totalCost / pos.quantity
		}
		fillPnL = 0 // opening a position has no realized P&L
	case "sell", "SELL":
		pos := lw.positions[posKey]
		if pos != nil && pos.quantity > 0 {
			sellQty := quantity
			if sellQty > pos.quantity {
				sellQty = pos.quantity
			}
			fillPnL = (price - pos.avgEntry) * sellQty
			pos.quantity -= sellQty
			if pos.quantity <= 0 {
				pos.quantity = 0
				pos.avgEntry = 0
			}
		} else {
			// No tracked position — cannot compute realized P&L, record zero.
			lw.log.Warn().Str("symbol", symbol).Msg("ledger writer: sell without tracked position, recording zero P&L")
			fillPnL = 0
		}
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

	// Update Prometheus P&L gauges.
	if lw.metrics != nil {
		lw.metrics.PnL.RealizedUSD.Set(accum.realizedPnL)
		lw.metrics.PnL.DayUSD.Set(accum.realizedPnL)
		lw.metrics.PnL.DayDDUSD.Set(accum.maxDrawdown * lw.peakEquity)
		lw.metrics.PnL.EquityUSD.Set(currentEquity)
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
