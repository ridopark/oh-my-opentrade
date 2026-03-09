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

type positionEntry struct {
	avgEntry float64
	quantity float64
	entryAt  time.Time
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
	dailyPnL      map[string]*dailyAccum    // key: tenantID:envMode:date
	positions     map[string]*positionEntry // key: tenantID:envMode:symbol
	peakEquity    float64
	accountEquity float64

	// Per-strategy tracking (dual-write).
	stratDailyPnL  map[string]*stratDayAccum // key: tenantID:envMode:strategy:date
	stratPositions map[string]*positionEntry // key: tenantID:envMode:strategy:symbol
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

// stratDayAccum accumulates per-strategy realized P&L for a single day.
type stratDayAccum struct {
	day         time.Time
	tenantID    string
	envMode     domain.EnvMode
	strategy    string
	realizedPnL float64
	tradeCount  int
	winCount    int
	lossCount   int
	grossProfit float64
	grossLoss   float64
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
		eventBus:       eventBus,
		pnlRepo:        pnlRepo,
		broker:         broker,
		log:            log,
		dailyPnL:       make(map[string]*dailyAccum),
		positions:      make(map[string]*positionEntry),
		peakEquity:     accountEquity,
		accountEquity:  accountEquity,
		stratDailyPnL:  make(map[string]*stratDayAccum),
		stratPositions: make(map[string]*positionEntry),
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

// Start bootstraps positions from the broker and subscribes to FillReceived events.
func (lw *LedgerWriter) Start(ctx context.Context, tenantID string, envMode domain.EnvMode) error {
	positions, err := lw.broker.GetPositions(ctx, tenantID, envMode)
	if err != nil {
		return fmt.Errorf("perf: ledger writer failed to bootstrap positions from broker: %w", err)
	}

	lw.mu.Lock()
	now := time.Now()
	for _, pos := range positions {
		if pos.Quantity <= 0 || pos.Side == "sell" || pos.Side == "short" {
			continue
		}
		posKey := fmt.Sprintf("%s:%s:%s", tenantID, string(envMode), string(pos.Symbol))
		lw.positions[posKey] = &positionEntry{
			avgEntry: pos.Price,
			quantity: pos.Quantity,
			entryAt:  now,
		}
		lw.log.Info().
			Str("symbol", string(pos.Symbol)).
			Float64("qty", pos.Quantity).
			Float64("avg_entry", pos.Price).
			Msg("bootstrapped position from broker")
	}
	lw.mu.Unlock()

	if err := lw.eventBus.SubscribeAsync(ctx, domain.EventFillReceived, lw.handleFill); err != nil {
		return fmt.Errorf("perf: ledger writer failed to subscribe to FillReceived: %w", err)
	}
	lw.log.Info().Int("bootstrapped", len(positions)).Msg("ledger writer started")
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
	strategy, _ := payload["strategy"].(string)

	if symbol == "" || quantity == 0 {
		return nil
	}

	lw.mu.Lock()
	defer lw.mu.Unlock()

	now := time.Now().UTC()

	posKey := fmt.Sprintf("%s:%s:%s", event.TenantID, string(event.EnvMode), symbol)
	var fillPnL float64
	var realizedPayload *domain.TradeRealizedPayload
	switch side {
	case "buy", "BUY":
		pos := lw.positions[posKey]
		if pos == nil {
			pos = &positionEntry{entryAt: now}
			lw.positions[posKey] = pos
		}
		totalCost := pos.avgEntry*pos.quantity + price*quantity
		pos.quantity += quantity
		if pos.quantity > 0 {
			pos.avgEntry = totalCost / pos.quantity
		}
		fillPnL = 0
	case "sell", "SELL":
		pos := lw.positions[posKey]
		if pos != nil && pos.quantity > 0 {
			sellQty := quantity
			if sellQty > pos.quantity {
				sellQty = pos.quantity
			}
			entryPrice := pos.avgEntry
			entryAt := pos.entryAt
			fillPnL = (price - entryPrice) * sellQty

			var pnlPct float64
			if entryPrice > 0 {
				pnlPct = (price - entryPrice) / entryPrice * 100
			}

			realizedPayload = &domain.TradeRealizedPayload{
				Symbol:       domain.Symbol(symbol),
				Side:         side,
				Quantity:     sellQty,
				ExitPrice:    price,
				EntryPrice:   entryPrice,
				RealizedPnL:  fillPnL,
				PnLPct:       pnlPct,
				Strategy:     strategy,
				HoldDuration: now.Sub(entryAt),
			}

			pos.quantity -= sellQty
			if pos.quantity <= 0 {
				pos.quantity = 0
				pos.avgEntry = 0
			}
		} else {
			lw.log.Warn().Str("symbol", symbol).Msg("ledger writer: sell without tracked position, recording zero P&L")
			fillPnL = 0
		}
	}

	if realizedPayload != nil {
		lw.emitTradeRealized(ctx, event.TenantID, event.EnvMode, symbol, *realizedPayload)
	}

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

	// --- Per-strategy dual-write ---
	if strategy != "" {
		stratPosKey := fmt.Sprintf("%s:%s:%s:%s", event.TenantID, string(event.EnvMode), strategy, symbol)
		var stratFillPnL float64
		switch side {
		case "buy", "BUY":
			sPos := lw.stratPositions[stratPosKey]
			if sPos == nil {
				sPos = &positionEntry{}
				lw.stratPositions[stratPosKey] = sPos
			}
			totalCost := sPos.avgEntry*sPos.quantity + price*quantity
			sPos.quantity += quantity
			if sPos.quantity > 0 {
				sPos.avgEntry = totalCost / sPos.quantity
			}
			stratFillPnL = 0
		case "sell", "SELL":
			sPos := lw.stratPositions[stratPosKey]
			if sPos != nil && sPos.quantity > 0 {
				sellQty := quantity
				if sellQty > sPos.quantity {
					sellQty = sPos.quantity
				}
				stratFillPnL = (price - sPos.avgEntry) * sellQty
				sPos.quantity -= sellQty
				if sPos.quantity <= 0 {
					sPos.quantity = 0
					sPos.avgEntry = 0
				}
			}
		}

		// Get or create per-strategy daily accumulator.
		stratDateKey := fmt.Sprintf("%s:%s:%s:%s", event.TenantID, string(event.EnvMode), strategy, now.Format("2006-01-02"))
		sAccum, sExists := lw.stratDailyPnL[stratDateKey]
		if !sExists {
			sAccum = &stratDayAccum{
				day:      time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
				tenantID: event.TenantID,
				envMode:  event.EnvMode,
				strategy: strategy,
			}
			lw.stratDailyPnL[stratDateKey] = sAccum
		}
		sAccum.realizedPnL += stratFillPnL
		sAccum.tradeCount++
		if stratFillPnL > 0 {
			sAccum.winCount++
			sAccum.grossProfit += stratFillPnL
		} else if stratFillPnL < 0 {
			sAccum.lossCount++
			sAccum.grossLoss += stratFillPnL // negative value
		}

		// Persist per-strategy daily P&L.
		stratPnL := domain.StrategyDailyPnL{
			Day:         sAccum.day,
			TenantID:    sAccum.tenantID,
			EnvMode:     sAccum.envMode,
			Strategy:    sAccum.strategy,
			RealizedPnL: sAccum.realizedPnL,
			TradeCount:  sAccum.tradeCount,
			WinCount:    sAccum.winCount,
			LossCount:   sAccum.lossCount,
			GrossProfit: sAccum.grossProfit,
			GrossLoss:   sAccum.grossLoss,
		}
		if err := lw.pnlRepo.UpsertStrategyDailyPnL(ctx, stratPnL); err != nil {
			lw.log.Error().Err(err).Str("symbol", symbol).Str("strategy", strategy).Msg("ledger writer: failed to upsert strategy daily P&L")
		}

		// Persist per-strategy equity point.
		stratPt := domain.StrategyEquityPoint{
			Time:              now,
			TenantID:          event.TenantID,
			EnvMode:           event.EnvMode,
			Strategy:          strategy,
			Equity:            sAccum.realizedPnL, // strategy equity = cumulative realized P&L for this strategy
			RealizedPnLToDate: sAccum.realizedPnL,
			TradeCountToDate:  sAccum.tradeCount,
		}
		if err := lw.pnlRepo.SaveStrategyEquityPoint(ctx, stratPt); err != nil {
			lw.log.Error().Err(err).Str("symbol", symbol).Str("strategy", strategy).Msg("ledger writer: failed to save strategy equity point")
		}
	}

	lw.log.Info().
		Str("symbol", symbol).
		Str("side", side).
		Str("strategy", strategy).
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

func (lw *LedgerWriter) emitTradeRealized(ctx context.Context, tenantID string, envMode domain.EnvMode, symbol string, payload domain.TradeRealizedPayload) {
	idempotencyKey := fmt.Sprintf("REALIZED:%s:%s:%s:%d", tenantID, envMode, symbol, time.Now().UnixNano())
	ev, err := domain.NewEvent(domain.EventTradeRealized, tenantID, envMode, idempotencyKey, payload)
	if err != nil {
		lw.log.Warn().Err(err).Str("symbol", symbol).Msg("ledger writer: failed to create TradeRealized event")
		return
	}
	if pubErr := lw.eventBus.Publish(ctx, *ev); pubErr != nil {
		lw.log.Warn().Err(pubErr).Str("symbol", symbol).Msg("ledger writer: failed to publish TradeRealized event")
	}
}
