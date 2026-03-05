package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	stratports "github.com/oh-my-opentrade/backend/internal/ports/strategy"
)

// RiskSizer subscribes to SignalCreated events and converts strategy Signals
// into OrderIntents after applying position sizing and risk checks.
type RiskSizer struct {
	eventBus      ports.EventBusPort
	specStore     stratports.SpecStore
	mu            sync.RWMutex
	accountEquity float64
	logger        *slog.Logger
}

func NewRiskSizer(eventBus ports.EventBusPort, specStore stratports.SpecStore, equity float64, logger *slog.Logger) *RiskSizer {
	if logger == nil {
		logger = slog.Default()
	}
	if equity <= 0 {
		equity = 100000.0
	}
	return &RiskSizer{
		eventBus:      eventBus,
		specStore:     specStore,
		accountEquity: equity,
		logger:        logger.With("component", "risk_sizer"),
	}
}

// Start subscribes the RiskSizer to SignalCreated events on the event bus.
func (rs *RiskSizer) Start(ctx context.Context) error {
	if err := rs.eventBus.Subscribe(ctx, domain.EventSignalCreated, rs.handleSignal); err != nil {
		return fmt.Errorf("risk sizer: failed to subscribe to SignalCreated: %w", err)
	}
	rs.logger.Info("risk sizer subscribed to SignalCreated events")
	return nil
}

// SetAccountEquity updates the account equity used for position sizing.
// Safe to call concurrently.
func (rs *RiskSizer) SetAccountEquity(equity float64) {
	if equity <= 0 {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.accountEquity = equity
}

func (rs *RiskSizer) handleSignal(ctx context.Context, event domain.Event) error {
	sig, ok := event.Payload.(strat.Signal)
	if !ok {
		return nil
	}

	if !sig.Type.IsActionable() {
		return nil
	}

	strategyID, hasStrategyID := parseStrategyIDFromInstance(sig.StrategyInstanceID)
	var spec *stratports.Spec
	if rs.specStore != nil && hasStrategyID {
		got, err := rs.specStore.GetLatest(ctx, strategyID)
		if err == nil {
			spec = got
		} else {
			rs.logger.Debug("spec lookup failed; using defaults", "strategy_id", strategyID.String(), "error", err)
		}
	}

	limitOffsetBPS := 5
	stopBPS := 25
	riskPerTradeBPS := 10
	if spec != nil {
		if v, ok := extractInt(spec.Params, "limit_offset_bps"); ok {
			limitOffsetBPS = v
		}
		if v, ok := extractInt(spec.Params, "stop_bps"); ok {
			stopBPS = v
		}
		if v, ok := extractInt(spec.Params, "risk_per_trade_bps"); ok {
			riskPerTradeBPS = v
		} else if v, ok := extractInt(spec.Params, "max_risk_bps"); ok {
			riskPerTradeBPS = v
		}
	}

	refStr, ok := sig.Tags["ref_price"]
	if !ok || strings.TrimSpace(refStr) == "" {
		rs.logger.Warn("signal missing ref_price; skipping", "instance_id", sig.StrategyInstanceID.String(), "symbol", sig.Symbol, "type", sig.Type.String(), "side", sig.Side.String())
		return nil
	}
	refPrice, err := strconv.ParseFloat(refStr, 64)
	if err != nil || refPrice <= 0 {
		rs.logger.Warn("signal has invalid ref_price; skipping", "ref_price", refStr, "instance_id", sig.StrategyInstanceID.String(), "symbol", sig.Symbol, "error", err)
		return nil
	}

	limitPrice := refPrice
	stopLoss := refPrice
	if sig.Type == strat.SignalEntry {
		limitMult := 1.0 + float64(limitOffsetBPS)/10000.0
		stopMult := 1.0 - float64(stopBPS)/10000.0
		if sig.Side == strat.SideSell {
			limitMult = 1.0 - float64(limitOffsetBPS)/10000.0
			stopMult = 1.0 + float64(stopBPS)/10000.0
		}
		limitPrice = refPrice * limitMult
		stopLoss = refPrice * stopMult
	} else {
		limitPrice = refPrice
		stopLoss = refPrice
	}

	rs.mu.RLock()
	equity := rs.accountEquity
	rs.mu.RUnlock()

	maxRiskUSD := (float64(riskPerTradeBPS) / 10000.0) * equity
	riskPerShare := math.Abs(limitPrice - stopLoss)
	qty := 1.0
	if riskPerShare > 0 && maxRiskUSD > 0 {
		computed := math.Floor(maxRiskUSD / riskPerShare)
		if computed >= 1 {
			qty = computed
		}
	}

	// Clamp position size so notional exposure does not exceed max_position_bps of equity.
	maxPositionBPS := 1000 // default 10% of equity
	if spec != nil {
		if v, ok := extractInt(spec.Params, "max_position_bps"); ok && v > 0 {
			maxPositionBPS = v
		}
	}
	if limitPrice > 0 {
		maxNotional := (float64(maxPositionBPS) / 10000.0) * equity
		maxQty := math.Floor(maxNotional / limitPrice)
		if maxQty < 1 {
			maxQty = 1
		}
		if qty > maxQty {
			rs.logger.Info("position size clamped by max_position_bps",
				"original_qty", qty,
				"clamped_qty", maxQty,
				"max_position_bps", maxPositionBPS,
				"limit_price", limitPrice,
				"equity", equity,
			)
			qty = maxQty
		}
	}

	direction := domain.DirectionLong
	if sig.Side == strat.SideSell {
		direction = domain.DirectionShort
	}

	strategyName := "unknown"
	if hasStrategyID {
		strategyName = strategyID.String()
	}

	intentID := uuid.New()
	intent, err := domain.NewOrderIntent(
		intentID,
		event.TenantID,
		event.EnvMode,
		domain.Symbol(sig.Symbol),
		direction,
		limitPrice,
		stopLoss,
		10,
		qty,
		strategyName,
		fmt.Sprintf("signal: %s %s strength=%.2f", sig.Type, sig.Side, sig.Strength),
		sig.Strength,
		intentID.String(),
	)
	if err != nil {
		return fmt.Errorf("risk sizer: failed to create order intent: %w", err)
	}

	rs.emit(ctx, domain.EventOrderIntentCreated, event.TenantID, event.EnvMode, intentID.String(), intent)
	return nil
}

func (rs *RiskSizer) emit(ctx context.Context, eventType string, tenantID string, envMode domain.EnvMode, idempotencyKey string, payload any) {
	ev, err := domain.NewEvent(eventType, tenantID, envMode, idempotencyKey, payload)
	if err != nil {
		return
	}
	_ = rs.eventBus.Publish(ctx, *ev)
}

// parseStrategyIDFromInstance extracts the strategy ID from an InstanceID.
// InstanceID format: "strategy_id:version:symbol" or arbitrary string.
func parseStrategyIDFromInstance(instanceID strat.InstanceID) (strat.StrategyID, bool) {
	parts := strings.SplitN(string(instanceID), ":", 3)
	if len(parts) < 1 {
		return "", false
	}
	sid, err := strat.NewStrategyID(parts[0])
	if err != nil {
		return "", false
	}
	return sid, true
}
