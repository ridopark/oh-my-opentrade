package strategy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/options"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	stratports "github.com/oh-my-opentrade/backend/internal/ports/strategy"
)

// revalStateTTL is how long a degraded revaluation blocks new entries.
// After the position closes, no new revaluations fire; this TTL ensures
// the block auto-expires rather than persisting indefinitely.
const revalStateTTL = 10 * time.Minute

// defaultExitCooldown is how long after an exit fill new entry signals
// are blocked for the same symbol. Prevents whipsaw sell-then-rebuy
// across strategy instances.
// 15 minutes: industry standard for 1-min bar strategies is 3-5 bars minimum;
// 15 min provides sufficient price discovery and prevents churn loops.
const defaultExitCooldown = 15 * time.Minute

const maxConsecutiveLosses = 3
const circuitBreakerCooldown = 60 * time.Minute

const aiDirectionMinConfidence = 0.5

var etLocation *time.Location

func init() {
	var err error
	etLocation, err = time.LoadLocation("America/New_York")
	if err != nil {
		panic("failed to load America/New_York timezone: " + err.Error())
	}
}

func isCryptoRTH(now time.Time) bool {
	et := now.In(etLocation)
	if et.Weekday() == time.Saturday || et.Weekday() == time.Sunday {
		return false
	}
	hour := et.Hour()
	return hour >= 8 && hour < 17
}

type lossRecord struct {
	Count      int
	LastLossAt time.Time
	EntryPrice float64
}

// RiskSizer subscribes to SignalEnriched events and converts enriched signals
// into OrderIntents after applying position sizing and risk checks.
type RiskSizer struct {
	eventBus             ports.EventBusPort
	specStore            stratports.SpecStore
	mu                   sync.RWMutex
	accountEquity        float64
	revalState           sync.Map // symbol (string) → *domain.RiskRevaluation
	exitCooldowns        sync.Map // symbol (string) → time.Time (last exit fill timestamp)
	lossTrackers         sync.Map // symbol (string) → *lossRecord
	logger               *slog.Logger
	nowFn                func() time.Time
	exitCooldownDuration time.Duration
	optionsMarket        ports.OptionsMarketDataPort
	contractSelector     *options.ContractSelectionService
}

func NewRiskSizer(eventBus ports.EventBusPort, specStore stratports.SpecStore, equity float64, logger *slog.Logger) *RiskSizer {
	if logger == nil {
		logger = slog.Default()
	}
	if equity <= 0 {
		equity = 100000.0
	}
	return &RiskSizer{
		eventBus:             eventBus,
		specStore:            specStore,
		accountEquity:        equity,
		logger:               logger.With("component", "risk_sizer"),
		nowFn:                time.Now,
		exitCooldownDuration: defaultExitCooldown,
	}
}

func (rs *RiskSizer) SetNowFn(fn func() time.Time)    { rs.nowFn = fn }
func (rs *RiskSizer) SetExitCooldown(d time.Duration) { rs.exitCooldownDuration = d }

func (rs *RiskSizer) SetOptionsMarket(m ports.OptionsMarketDataPort) { rs.optionsMarket = m }
func (rs *RiskSizer) SetContractSelector(s *options.ContractSelectionService) {
	rs.contractSelector = s
}

func (rs *RiskSizer) Start(ctx context.Context) error {
	if err := rs.eventBus.Subscribe(ctx, domain.EventSignalEnriched, rs.handleSignal); err != nil {
		return fmt.Errorf("risk sizer: failed to subscribe to SignalEnriched: %w", err)
	}
	if err := rs.eventBus.Subscribe(ctx, domain.EventRiskRevaluated, rs.handleRevaluation); err != nil {
		return fmt.Errorf("risk sizer: failed to subscribe to RiskRevaluated: %w", err)
	}
	if err := rs.eventBus.SubscribeAsync(ctx, domain.EventFillReceived, rs.handleFillForCooldown); err != nil {
		return fmt.Errorf("risk sizer: failed to subscribe to FillReceived: %w", err)
	}
	rs.logger.Info("risk sizer subscribed to SignalEnriched, RiskRevaluated, and FillReceived events")
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

func (rs *RiskSizer) handleRevaluation(_ context.Context, event domain.Event) error {
	reval, ok := event.Payload.(domain.RiskRevaluationEvent)
	if !ok {
		return nil
	}

	sym := string(reval.Symbol)
	if reval.ThesisStatus == domain.ThesisIntact {
		rs.revalState.Delete(sym)
		return nil
	}

	rs.revalState.Store(sym, &reval.RiskRevaluation)
	return nil
}

func (rs *RiskSizer) handleFillForCooldown(_ context.Context, event domain.Event) error {
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		return nil
	}
	symbol, _ := payload["symbol"].(string)
	side, _ := payload["side"].(string)
	if symbol == "" {
		return nil
	}

	price, _ := payload["price"].(float64)

	if side == "BUY" && price > 0 {
		raw, _ := rs.lossTrackers.LoadOrStore(symbol, &lossRecord{})
		rec := raw.(*lossRecord)
		rec.EntryPrice = price
		return nil
	}

	if side != "SELL" {
		return nil
	}

	rs.exitCooldowns.Store(symbol, rs.nowFn())
	rs.logger.Info("exit cooldown set",
		"symbol", symbol,
		"cooldown", rs.exitCooldownDuration.String(),
	)

	if price > 0 {
		raw, _ := rs.lossTrackers.LoadOrStore(symbol, &lossRecord{})
		rec := raw.(*lossRecord)
		if rec.EntryPrice > 0 && price < rec.EntryPrice {
			rec.Count++
			rec.LastLossAt = rs.nowFn()
			rs.logger.Warn("consecutive loss recorded",
				"symbol", symbol,
				"count", rec.Count,
				"entry_price", rec.EntryPrice,
				"exit_price", price,
			)
		} else if rec.EntryPrice > 0 {
			rec.Count = 0
		}
		rec.EntryPrice = 0
	}

	return nil
}

func (rs *RiskSizer) isSymbolInCooldown(symbol string) (time.Time, bool) {
	raw, ok := rs.exitCooldowns.Load(symbol)
	if !ok {
		return time.Time{}, false
	}
	exitTime, ok := raw.(time.Time)
	if !ok {
		return time.Time{}, false
	}
	if rs.nowFn().Sub(exitTime) > rs.exitCooldownDuration {
		rs.exitCooldowns.Delete(symbol)
		return time.Time{}, false
	}
	return exitTime, true
}

func (rs *RiskSizer) isCircuitBroken(symbol string) (*lossRecord, bool) {
	raw, ok := rs.lossTrackers.Load(symbol)
	if !ok {
		return nil, false
	}
	rec, ok := raw.(*lossRecord)
	if !ok {
		return nil, false
	}
	if rec.Count < maxConsecutiveLosses {
		return nil, false
	}
	if rs.nowFn().Sub(rec.LastLossAt) > circuitBreakerCooldown {
		rs.lossTrackers.Delete(symbol)
		return nil, false
	}
	return rec, true
}

func (rs *RiskSizer) isSymbolDegraded(symbol string) (*domain.RiskRevaluation, bool) {
	raw, ok := rs.revalState.Load(symbol)
	if !ok {
		return nil, false
	}
	reval, ok := raw.(*domain.RiskRevaluation)
	if !ok {
		return nil, false
	}
	if rs.nowFn().Sub(reval.EvaluatedAt) > revalStateTTL {
		rs.revalState.Delete(symbol)
		return nil, false
	}
	return reval, true
}

func (rs *RiskSizer) handleSignal(ctx context.Context, event domain.Event) error {
	enrichment, ok := event.Payload.(domain.SignalEnrichment)
	if !ok {
		return nil
	}

	sigRef := enrichment.Signal

	if sigRef.SignalType == "flat" {
		return nil
	}

	strategyID, hasStrategyID := parseStrategyIDFromInstance(start.InstanceID(sigRef.StrategyInstanceID))

	if enrichment.AIDirectionConflict(aiDirectionMinConfidence) {
		strategyName := "unknown"
		if hasStrategyID {
			strategyName = strategyID.String()
		}
		signalDirection := domain.DirectionLong
		if sigRef.Side == start.SideSell.String() {
			signalDirection = domain.DirectionShort
		}
		rs.logger.Warn("AI direction gate: signal rejected",
			"symbol", sigRef.Symbol,
			"signal_side", sigRef.Side,
			"ai_direction", string(enrichment.Direction),
			"ai_rationale", enrichment.Rationale,
			"confidence", enrichment.Confidence,
			"min_confidence", aiDirectionMinConfidence,
		)
		rejection := domain.OrderIntentEventPayload{
			ID:        uuid.NewString(),
			Symbol:    sigRef.Symbol,
			Direction: string(signalDirection),
			Strategy:  strategyName,
			Reason:    "ai_direction_conflict: AI recommended " + string(enrichment.Direction),
			Status:    domain.OrderIntentStatusRejected,
		}
		rs.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, rejection.ID, rejection)
		return nil
	}

	if enrichment.Status == domain.EnrichmentVetoed && sigRef.SignalType == start.SignalEntry.String() {
		strategyName := "unknown"
		if hasStrategyID {
			strategyName = strategyID.String()
		}
		signalDir := domain.DirectionLong
		if sigRef.Side == start.SideSell.String() {
			signalDir = domain.DirectionShort
		}
		rs.logger.Warn("veto gate: entry blocked — enrichment vetoed",
			"symbol", sigRef.Symbol,
			"rationale", enrichment.Rationale,
			"confidence", enrichment.Confidence,
		)
		rejection := domain.OrderIntentEventPayload{
			ID:        uuid.NewString(),
			Symbol:    sigRef.Symbol,
			Direction: string(signalDir),
			Strategy:  strategyName,
			Reason:    "veto: " + enrichment.Rationale,
			Status:    domain.OrderIntentStatusRejected,
		}
		rs.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, rejection.ID, rejection)
		return nil
	}

	if sigRef.SignalType == start.SignalEntry.String() {
		if reval, degraded := rs.isSymbolDegraded(sigRef.Symbol); degraded {
			strategyName := "unknown"
			if hasStrategyID {
				strategyName = strategyID.String()
			}
			signalDir := domain.DirectionLong
			if sigRef.Side == start.SideSell.String() {
				signalDir = domain.DirectionShort
			}
			rs.logger.Warn("revaluation gate: entry blocked — thesis degraded",
				"symbol", sigRef.Symbol,
				"thesis_status", string(reval.ThesisStatus),
				"reval_action", string(reval.Action),
				"reval_confidence", reval.Confidence,
			)
			rejection := domain.OrderIntentEventPayload{
				ID:        uuid.NewString(),
				Symbol:    sigRef.Symbol,
				Direction: string(signalDir),
				Strategy:  strategyName,
				Reason:    fmt.Sprintf("revaluation_gate: thesis %s (confidence %.0f%%)", reval.ThesisStatus, reval.Confidence*100),
				Status:    domain.OrderIntentStatusRejected,
			}
			rs.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, rejection.ID, rejection)
			return nil
		}
	}

	if sigRef.SignalType == start.SignalEntry.String() {
		if exitTime, coolingDown := rs.isSymbolInCooldown(sigRef.Symbol); coolingDown {
			strategyName := "unknown"
			if hasStrategyID {
				strategyName = strategyID.String()
			}
			signalDir := domain.DirectionLong
			if sigRef.Side == start.SideSell.String() {
				signalDir = domain.DirectionShort
			}
			rs.logger.Warn("exit cooldown gate: entry blocked — recent exit",
				"symbol", sigRef.Symbol,
				"last_exit_at", exitTime,
				"cooldown", rs.exitCooldownDuration.String(),
			)
			rejection := domain.OrderIntentEventPayload{
				ID:        uuid.NewString(),
				Symbol:    sigRef.Symbol,
				Direction: string(signalDir),
				Strategy:  strategyName,
				Reason:    fmt.Sprintf("exit_cooldown: last exit %.0fs ago (cooldown %s)", rs.nowFn().Sub(exitTime).Seconds(), rs.exitCooldownDuration),
				Status:    domain.OrderIntentStatusRejected,
			}
			rs.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, rejection.ID, rejection)
			return nil
		}
	}

	if sigRef.SignalType == start.SignalEntry.String() {
		if rec, broken := rs.isCircuitBroken(sigRef.Symbol); broken {
			strategyName := "unknown"
			if hasStrategyID {
				strategyName = strategyID.String()
			}
			signalDir := domain.DirectionLong
			if sigRef.Side == start.SideSell.String() {
				signalDir = domain.DirectionShort
			}
			cooldownLeft := circuitBreakerCooldown - rs.nowFn().Sub(rec.LastLossAt)
			rs.logger.Warn("circuit breaker gate: entry blocked — consecutive losses",
				"symbol", sigRef.Symbol,
				"consecutive_losses", rec.Count,
				"cooldown_remaining", cooldownLeft.Round(time.Second).String(),
			)
			rejection := domain.OrderIntentEventPayload{
				ID:        uuid.NewString(),
				Symbol:    sigRef.Symbol,
				Direction: string(signalDir),
				Strategy:  strategyName,
				Reason:    fmt.Sprintf("circuit_breaker: %d consecutive losses (cooldown %s remaining)", rec.Count, cooldownLeft.Round(time.Second)),
				Status:    domain.OrderIntentStatusRejected,
			}
			rs.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, rejection.ID, rejection)
			return nil
		}
	}

	var spec *stratports.Spec
	if rs.specStore != nil && hasStrategyID {
		got, err := rs.specStore.GetLatest(ctx, strategyID)
		if err == nil {
			spec = got
		} else {
			rs.logger.Debug("spec lookup failed; using defaults", "strategy_id", strategyID.String(), "error", err)
		}
	}

	params := map[string]any(nil)
	exitRules := []domain.ExitRule(nil)
	if spec != nil {
		params = spec.ParamsForSymbol(sigRef.Symbol)
		exitRules = spec.ExitRulesForSymbol(sigRef.Symbol)
	}

	limitOffsetBPS := 5
	stopBPS := 25
	riskPerTradeBPS := 10
	maxSlippageBPS := 10
	if params != nil {
		if v, ok := extractInt(params, "limit_offset_bps"); ok {
			limitOffsetBPS = v
		}
		if v, ok := extractInt(params, "stop_bps"); ok {
			stopBPS = v
		}
		if v, ok := extractInt(params, "risk_per_trade_bps"); ok {
			riskPerTradeBPS = v
		} else if v, ok := extractInt(params, "max_risk_bps"); ok {
			riskPerTradeBPS = v
		}
		if v, ok := extractInt(params, "max_slippage_bps"); ok {
			maxSlippageBPS = v
		}
		if !isCryptoRTH(rs.nowFn()) {
			if v, ok := extractInt(params, "limit_offset_bps_offhours"); ok {
				limitOffsetBPS = v
			}
			if v, ok := extractInt(params, "max_slippage_bps_offhours"); ok {
				maxSlippageBPS = v
			}
		}
	}

	dynCfg := extractDynamicRiskConfig(params)

	if sigRef.SignalType == start.SignalEntry.String() {
		profile := domain.ComputeRiskProfile(
			domain.BaseRiskParams{RiskPerTradeBPS: riskPerTradeBPS, StopBPS: stopBPS},
			enrichment,
			dynCfg,
		)

		if profile.Gated {
			rs.logger.Info("signal gated by dynamic risk",
				"symbol", sigRef.Symbol,
				"confidence", enrichment.Confidence,
				"reason", profile.GateReason,
			)
			stratName := "unknown"
			if hasStrategyID {
				stratName = strategyID.String()
			}
			gated := domain.SignalGatedPayload{
				Symbol:     sigRef.Symbol,
				Side:       sigRef.Side,
				SignalType: sigRef.SignalType,
				Strategy:   stratName,
				Confidence: enrichment.Confidence,
				Reason:     profile.GateReason,
			}
			rs.emit(ctx, domain.EventSignalGated, event.TenantID, event.EnvMode, uuid.NewString(), gated)
			return nil
		}

		if dynCfg.Enabled {
			rs.logger.Info("dynamic risk applied",
				"strategy", strategyID.String(),
				"symbol", sigRef.Symbol,
				"confidence", enrichment.Confidence,
				"risk_modifier", string(enrichment.RiskModifier),
				"scale_factor", profile.ScaleFactor,
				"base_risk_bps", riskPerTradeBPS,
				"adjusted_risk_bps", profile.RiskPerTradeBPS,
				"base_stop_bps", stopBPS,
				"adjusted_stop_bps", profile.StopBPS,
			)
		}

		riskPerTradeBPS = profile.RiskPerTradeBPS
		stopBPS = profile.StopBPS
	}

	refStr, ok := sigRef.Tags["ref_price"]
	if !ok || strings.TrimSpace(refStr) == "" {
		rs.logger.Warn("signal missing ref_price; skipping", "instance_id", sigRef.StrategyInstanceID, "symbol", sigRef.Symbol, "type", sigRef.SignalType, "side", sigRef.Side)
		return nil
	}
	refPrice, err := strconv.ParseFloat(refStr, 64)
	if err != nil || refPrice <= 0 {
		rs.logger.Warn("signal has invalid ref_price; skipping", "ref_price", refStr, "instance_id", sigRef.StrategyInstanceID, "symbol", sigRef.Symbol, "error", err)
		return nil
	}

	var limitPrice, stopLoss float64
	if sigRef.SignalType == start.SignalEntry.String() {
		limitMult := 1.0 + float64(limitOffsetBPS)/10000.0
		stopMult := 1.0 - float64(stopBPS)/10000.0
		if sigRef.Side == start.SideSell.String() {
			limitMult = 1.0 - float64(limitOffsetBPS)/10000.0
			stopMult = 1.0 + float64(stopBPS)/10000.0
		}
		limitPrice = refPrice * limitMult
		stopLoss = refPrice * stopMult

		if spStr, ok := sigRef.Tags["stop_price"]; ok {
			if sp, err := strconv.ParseFloat(spStr, 64); err == nil && sp > 0 {
				stopLoss = sp
			}
		}
	} else {
		limitPrice = refPrice
		stopLoss = 0
	}

	rs.mu.RLock()
	equity := rs.accountEquity
	rs.mu.RUnlock()

	maxRiskUSD := (float64(riskPerTradeBPS) / 10000.0) * equity
	riskPerShare := math.Abs(limitPrice - stopLoss)
	qty := 0.0
	if riskPerShare > 0 && maxRiskUSD > 0 {
		qty = maxRiskUSD / riskPerShare
	}
	if qty <= 0 {
		qty = maxRiskUSD / limitPrice
	}
	if qty <= 0 {
		rs.logger.Warn("computed zero quantity; skipping", "symbol", sigRef.Symbol, "equity", equity, "limit_price", limitPrice)
		return nil
	}

	maxPositionBPS := 1000
	if params != nil {
		if v, ok := extractInt(params, "max_position_bps"); ok && v > 0 {
			maxPositionBPS = v
		}
	}
	if limitPrice > 0 {
		maxNotional := (float64(maxPositionBPS) / 10000.0) * equity
		maxQty := maxNotional / limitPrice
		if qty > maxQty {
			rs.logger.Info("position size clamped by max_position_bps",
				"strategy", strategyID.String(),
				"symbol", sigRef.Symbol,
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
	if sigRef.Side == start.SideSell.String() {
		if sigRef.SignalType == start.SignalExit.String() {
			direction = domain.DirectionCloseLong
		} else {
			direction = domain.DirectionShort
		}
	}

	strategyName := "unknown"
	if hasStrategyID {
		strategyName = strategyID.String()
	}

	// Options branch: when the strategy has options enabled, route through
	// the options pipeline instead of creating an equity OrderIntent.
	if sigRef.SignalType == start.SignalEntry.String() &&
		spec != nil && spec.Options != nil && spec.Options.Enabled &&
		rs.optionsMarket != nil {
		return rs.handleOptionsSignal(ctx, event, enrichment, sigRef, spec, params, exitRules, direction, strategyName, refPrice, limitPrice, equity)
	}

	intentID := uuid.New()
	rationale := enrichment.Rationale
	if rationale == "" {
		rationale = fmt.Sprintf("signal: %s %s strength=%.2f", sigRef.SignalType, sigRef.Side, enrichment.Confidence)
	}
	intent, err := domain.NewOrderIntent(
		intentID,
		event.TenantID,
		event.EnvMode,
		domain.Symbol(sigRef.Symbol),
		direction,
		limitPrice,
		stopLoss,
		maxSlippageBPS,
		qty,
		strategyName,
		rationale,
		enrichment.Confidence,
		intentID.String(),
	)
	if err != nil {
		return fmt.Errorf("risk sizer: failed to create order intent: %w", err)
	}

	if domain.Symbol(sigRef.Symbol).IsCryptoSymbol() {
		intent.AssetClass = domain.AssetClassCrypto
	} else {
		intent.AssetClass = domain.AssetClassEquity
	}

	intent.Meta = map[string]string{
		"bull":              enrichment.BullArgument,
		"bear":              enrichment.BearArgument,
		"judge":             enrichment.JudgeReasoning,
		"enrichment_status": string(enrichment.Status),
		"risk_modifier":     string(enrichment.RiskModifier),
		"dynamic_stop_bps":  strconv.Itoa(stopBPS),
	}

	if params != nil {
		propagateGuardParams(params, intent.Meta)
	}

	if len(exitRules) > 0 {
		type ruleWire struct {
			Type   string             `json:"type"`
			Params map[string]float64 `json:"params"`
		}
		wire := make([]ruleWire, len(exitRules))
		for i, r := range exitRules {
			wire[i] = ruleWire{Type: string(r.Type), Params: r.Params}
		}
		if raw, err := json.Marshal(wire); err == nil {
			intent.Meta["exit_rules"] = string(raw)
		}

		atr, _ := strconv.ParseFloat(sigRef.Tags["ind_atr"], 64)
		vwap, _ := strconv.ParseFloat(sigRef.Tags["ind_vwap"], 64)
		vwapSD, _ := strconv.ParseFloat(sigRef.Tags["ind_vwap_sd"], 64)

		for _, r := range exitRules {
			switch r.Type {
			case domain.ExitRuleVolatilityStop:
				if mult := r.Param("atr_multiplier", 0); mult > 0 && atr > 0 {
					intent.Meta["exit_price_volatility_stop"] = fmt.Sprintf("%.2f", limitPrice-(atr*mult))
				}
			case domain.ExitRuleSDTarget:
				if sd := r.Param("sd_level", 0); sd > 0 && vwap > 0 && vwapSD > 0 {
					intent.Meta["exit_price_sd_target"] = fmt.Sprintf("%.2f", vwap+(sd*vwapSD))
				}
			case domain.ExitRuleTrailingStop:
				if pct := r.Param("pct", 0); pct > 0 {
					intent.Meta["exit_price_trailing_stop"] = fmt.Sprintf("%.2f", limitPrice*(1-pct))
				}
			case domain.ExitRuleProfitTarget:
				if pct := r.Param("pct", 0); pct > 0 {
					intent.Meta["exit_price_profit_target"] = fmt.Sprintf("%.2f", limitPrice*(1+pct))
				}
			case domain.ExitRuleStepStop:
				intent.Meta["exit_price_step_stop"] = fmt.Sprintf("%.2f", limitPrice)
			case domain.ExitRuleStagnationExit:
				if sdThresh := r.Param("sd_threshold", 1.0); vwap > 0 && vwapSD > 0 {
					intent.Meta["exit_price_stagnation"] = fmt.Sprintf("%.2f", vwap+(sdThresh*vwapSD))
				}
			}
		}
	}

	rs.emit(ctx, domain.EventOrderIntentCreated, event.TenantID, event.EnvMode, intentID.String(), intent)
	return nil
}

func (rs *RiskSizer) handleOptionsSignal(
	ctx context.Context,
	event domain.Event,
	enrichment domain.SignalEnrichment,
	sigRef domain.SignalRef,
	spec *stratports.Spec,
	params map[string]any,
	exitRules []domain.ExitRule,
	direction domain.Direction,
	strategyName string,
	refPrice float64,
	limitPrice float64,
	equity float64,
) error {
	optRight := domain.OptionRightCall
	if direction == domain.DirectionShort {
		optRight = domain.OptionRightPut
	}

	regime := domain.RegimeTrend
	if regStr, ok := sigRef.Tags["regime_5m"]; ok && regStr != "none" {
		if parsed, err := domain.NewRegimeType(regStr); err == nil {
			regime = parsed
		}
	}

	targetDTE := spec.Options.Defaults.MinDTE +
		(spec.Options.Defaults.MaxDTE-spec.Options.Defaults.MinDTE)/2
	targetExpiry := rs.nowFn().AddDate(0, 0, targetDTE)

	chain, err := rs.optionsMarket.GetOptionChain(
		ctx,
		domain.Symbol(sigRef.Symbol),
		targetExpiry,
		optRight,
	)
	if err != nil {
		rs.logger.Error("options chain fetch failed",
			"symbol", sigRef.Symbol,
			"option_right", string(optRight),
			"error", err,
		)
		return nil
	}
	if len(chain) == 0 {
		rs.logger.Warn("empty options chain — skipping options path",
			"symbol", sigRef.Symbol,
			"option_right", string(optRight),
			"target_expiry", targetExpiry,
		)
		return nil
	}

	selector := rs.buildContractSelector(spec.Options)
	best, err := selector.SelectBestContract(direction, regime, chain)
	if err != nil {
		rs.logger.Warn("no suitable option contract found",
			"symbol", sigRef.Symbol,
			"option_right", string(optRight),
			"regime", string(regime),
			"error", err,
		)
		return nil
	}

	midPrice := (best.Bid + best.Ask) / 2
	if midPrice <= 0 {
		midPrice = best.Last
	}
	if midPrice <= 0 {
		rs.logger.Warn("option contract has no valid price — skipping",
			"contract", string(best.ContractSymbol),
		)
		return nil
	}

	riskPerTradeBPS := 10
	if v, ok := extractInt(params, "risk_per_trade_bps"); ok {
		riskPerTradeBPS = v
	} else if v, ok := extractInt(params, "max_risk_bps"); ok {
		riskPerTradeBPS = v
	}

	maxRiskUSD := (float64(riskPerTradeBPS) / 10000.0) * equity
	premiumPerContract := midPrice * float64(best.Multiplier)
	qty := math.Floor(maxRiskUSD / premiumPerContract)
	if qty <= 0 {
		rs.logger.Warn("option contract premium exceeds risk budget — skipping trade",
			"contract", string(best.ContractSymbol),
			"premium_per_contract", premiumPerContract,
			"max_risk_usd", maxRiskUSD,
			"risk_per_trade_bps", riskPerTradeBPS,
		)
		return nil
	}
	maxLossUSD := premiumPerContract * qty

	inst, err := domain.NewInstrument(
		domain.InstrumentTypeOption,
		string(best.ContractSymbol),
		sigRef.Symbol,
	)
	if err != nil {
		return fmt.Errorf("risk sizer: failed to create option instrument: %w", err)
	}

	intentID := uuid.New()
	rationale := enrichment.Rationale
	if rationale == "" {
		rationale = fmt.Sprintf("option: %s %s delta=%.2f DTE=%d",
			optRight, best.ContractSymbol, best.Delta, int(best.Expiry.Sub(rs.nowFn()).Hours()/24))
	}

	intent, err := domain.NewOptionOrderIntent(
		intentID,
		event.TenantID,
		event.EnvMode,
		inst,
		domain.DirectionLong,
		midPrice,
		qty,
		strategyName,
		rationale,
		enrichment.Confidence,
		intentID.String(),
		maxLossUSD,
	)
	if err != nil {
		return fmt.Errorf("risk sizer: failed to create option order intent: %w", err)
	}

	intent.AssetClass = domain.AssetClassEquity
	intent.Meta = map[string]string{
		"instrument_type":   "OPTION",
		"option_right":      string(optRight),
		"underlying":        sigRef.Symbol,
		"strike":            fmt.Sprintf("%.2f", best.Strike),
		"expiry":            best.Expiry.Format("2006-01-02"),
		"delta_at_entry":    fmt.Sprintf("%.4f", best.Delta),
		"iv_at_entry":       fmt.Sprintf("%.4f", best.IV),
		"premium":           fmt.Sprintf("%.2f", midPrice),
		"open_interest":     strconv.Itoa(best.OpenInterest),
		"enrichment_status": string(enrichment.Status),
		"risk_modifier":     string(enrichment.RiskModifier),
		"bull":              enrichment.BullArgument,
		"bear":              enrichment.BearArgument,
		"judge":             enrichment.JudgeReasoning,
	}

	if len(exitRules) > 0 {
		type ruleWire struct {
			Type   string             `json:"type"`
			Params map[string]float64 `json:"params"`
		}
		wire := make([]ruleWire, len(exitRules))
		for i, r := range exitRules {
			wire[i] = ruleWire{Type: string(r.Type), Params: r.Params}
		}
		if raw, err := json.Marshal(wire); err == nil {
			intent.Meta["exit_rules"] = string(raw)
		}
	}

	rs.logger.Info("options order intent created",
		"symbol", sigRef.Symbol,
		"contract", string(best.ContractSymbol),
		"right", string(optRight),
		"strike", best.Strike,
		"expiry", best.Expiry.Format("2006-01-02"),
		"delta", best.Delta,
		"premium", midPrice,
		"qty", qty,
		"max_loss_usd", maxLossUSD,
	)

	rs.emit(ctx, domain.EventOrderIntentCreated, event.TenantID, event.EnvMode, intentID.String(), intent)
	return nil
}

func (rs *RiskSizer) buildContractSelector(cfg *domain.OptionsConfig) *options.ContractSelectionService {
	if rs.contractSelector != nil {
		return rs.contractSelector
	}
	regimes := cfg.ToRegimeConstraintsMap()
	return options.NewContractSelectionServiceWithRegimes(cfg.Defaults, regimes, rs.nowFn)
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
func parseStrategyIDFromInstance(instanceID start.InstanceID) (start.StrategyID, bool) {
	parts := strings.SplitN(string(instanceID), ":", 3)
	if len(parts) < 1 {
		return "", false
	}
	sid, err := start.NewStrategyID(parts[0])
	if err != nil {
		return "", false
	}
	return sid, true
}

func extractDynamicRiskConfig(params map[string]any) domain.DynamicRiskConfig {
	cfg := domain.DefaultDynamicRiskConfig()
	if params == nil {
		return cfg
	}

	if v, ok := extractBool(params, "dynamic_risk.enabled"); ok {
		cfg.Enabled = v
	}
	if v, ok := extractFloat(params, "dynamic_risk.min_confidence"); ok {
		cfg.MinConfidence = v
	}
	if v, ok := extractFloat(params, "dynamic_risk.risk_scale_min"); ok {
		cfg.RiskScaleMin = v
	}
	if v, ok := extractFloat(params, "dynamic_risk.risk_scale_max"); ok {
		cfg.RiskScaleMax = v
	}
	if v, ok := extractFloat(params, "dynamic_risk.stop_tight_mult"); ok {
		cfg.StopTightMult = v
	}
	if v, ok := extractFloat(params, "dynamic_risk.stop_wide_mult"); ok {
		cfg.StopWideMult = v
	}
	if v, ok := extractFloat(params, "dynamic_risk.size_tight_mult"); ok {
		cfg.SizeTightMult = v
	}
	if v, ok := extractFloat(params, "dynamic_risk.size_wide_mult"); ok {
		cfg.SizeWideMult = v
	}

	return cfg
}

var guardParamKeys = []string{
	"max_spread_bps",
	"allowed_hours_start",
	"allowed_hours_end",
	"allowed_hours_tz",
	"skip_weekends",
}

func propagateGuardParams(params map[string]any, meta map[string]string) {
	for _, key := range guardParamKeys {
		v, ok := params[key]
		if !ok {
			continue
		}
		meta[key] = fmt.Sprintf("%v", v)
	}
}

func extractFloat(params map[string]any, key string) (float64, bool) {
	v, ok := params[key]
	if !ok {
		return 0, false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), true
	}
	return 0, false
}

func extractBool(params map[string]any, key string) (bool, bool) {
	v, ok := params[key]
	if !ok {
		return false, false
	}
	if b, ok := v.(bool); ok {
		return b, true
	}
	return false, false
}
