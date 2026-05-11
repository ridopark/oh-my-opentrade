package strategy

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/observability/parity"
	"github.com/oh-my-opentrade/backend/internal/ports"
	stratports "github.com/oh-my-opentrade/backend/internal/ports/strategy"
)

// REJECTED_RISK_CAP is the machine-readable reason code emitted on
// OrderIntentRejected events when the per-position expected-loss cap
// reduces contracts to zero (or would, when RejectOnFloor is true).
// The string is stable — dashboards and alerting rules key on it.
const RejectedRiskCap = "REJECTED_RISK_CAP"

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

var (
	etLocation             *time.Location
	errOptionsChainEmpty  = errors.New("options chain empty")
	errOptionsChainFailed = errors.New("options chain fetch failed")
)

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

	// openOptionContractsLookup, when set, resolves an underlying ticker to
	// the list of currently-open option contracts so strategy-emitted exit
	// signals on options strategies can be translated from the underlying
	// symbol to the actual contract symbol the broker tracks. Wired by
	// bootstrap from positionmonitor.Service.ListOpenContractsByUnderlying.
	// Nil-safe: when nil the option-exit path falls through to the equity
	// path (legacy behavior), which is harmless for equity-only strategies.
	openOptionContractsLookup func(underlying string) []domain.MonitoredPosition

	// positionRiskCap is the per-position expected-loss cap configured via
	// [risk.position_cap] in YAML. Nil (or Enabled=false) short-circuits
	// the cap check so behavior is byte-identical to the pre-fix sizer.
	// Written once during wiring via SetPositionRiskCap; read on the hot
	// path under rs.mu alongside accountEquity.
	positionRiskCap config.PositionRiskCapConfig
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

// SetOpenOptionContractsLookup wires the underlying-to-open-contracts lookup
// used by the options-exit translation path. Safe to call before Start;
// nil leaves the path disabled (equity fallback).
func (rs *RiskSizer) SetOpenOptionContractsLookup(fn func(underlying string) []domain.MonitoredPosition) {
	rs.openOptionContractsLookup = fn
}

// SetPositionRiskCap wires the per-position expected-loss cap configured
// via [risk.position_cap]. Safe to call before Start; takes effect on
// every subsequent signal. Passing a zero-value PositionRiskCapConfig
// (Enabled=false) disables the check — byte-identical to the pre-fix
// sizer for backward compatibility.
func (rs *RiskSizer) SetPositionRiskCap(c config.PositionRiskCapConfig) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.positionRiskCap = c
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

	limitOffsetBPS := 10
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

	// Floor equity quantities to whole shares so the order intent matches what
	// IBKR will actually report on the per-exec stream. Fractional intents on
	// equities led to dust-residual orphans (IWM 0.4486890237347403 on
	// 2026-05-06): broker fills the fractional share, our stream only captures
	// the integer leg, exit sells the integer, broker silently sweeps the dust,
	// DB carries an open long that the global reconciler can never heal.
	// Crypto keeps fractional sizing — BTC/ETH books expect it.
	if !domain.Symbol(sigRef.Symbol).IsCryptoSymbol() {
		qty = math.Floor(qty)
		if qty <= 0 {
			rs.logger.Warn("equity quantity floored to zero; skipping", "symbol", sigRef.Symbol, "limit_price", limitPrice)
			return nil
		}
	}

	direction := domain.DirectionLong
	if sigRef.SignalType == start.SignalExit.String() {
		// Use exit_direction from enrichment tags if set by reconciler;
		// default to CloseLong for backwards compatibility.
		direction = domain.DirectionCloseLong
		if ed, ok := sigRef.Tags["exit_direction"]; ok {
			direction = domain.Direction(ed)
		}
	} else if sigRef.Side == start.SideSell.String() {
		direction = domain.DirectionShort
	}

	strategyName := "unknown"
	if hasStrategyID {
		strategyName = strategyID.String()
	}

	// Options branch: when the strategy has options enabled, route through
	// the options pipeline instead of creating an equity OrderIntent.
	// Entries call handleOptionsSignal (chain fetch + contract selection) and
	// fall back to equity if the chain is empty or fetch fails. Exits call
	// handleOptionsExit, which translates the underlying-keyed signal into
	// per-contract close intents using the open-positions lookup.
	if spec != nil && spec.Options != nil && spec.Options.Enabled && rs.optionsMarket != nil {
		switch sigRef.SignalType {
		case start.SignalEntry.String():
			err := rs.handleOptionsSignal(ctx, event, enrichment, sigRef, spec, params, exitRules, direction, strategyName, refPrice, limitPrice, equity)
			if err == nil {
				return nil // options trade placed successfully
			}
			if errors.Is(err, errOptionsChainEmpty) || errors.Is(err, errOptionsChainFailed) {
				// Only fall back to equity if the strategy allows it.
				hasEquity := false
				for _, ac := range spec.Routing.AssetClasses {
					if strings.EqualFold(ac, "EQUITY") {
						hasEquity = true
						break
					}
				}
				if !hasEquity {
					rs.logger.Info("options chain empty, no equity fallback (asset_classes has no EQUITY)",
						"symbol", sigRef.Symbol,
					)
					return nil // skip trade entirely
				}
				rs.logger.Info("options fallback to equity",
					"symbol", sigRef.Symbol,
					"reason", err.Error(),
				)
				// Fall through to equity path below.
			} else {
				return err // real error
			}
		case start.SignalExit.String():
			return rs.handleOptionsExit(ctx, event, enrichment, sigRef, strategyName)
		}
	}

	intentID := uuid.New()
	rationale := enrichment.Rationale
	if rationale == "" {
		rationale = fmt.Sprintf("signal: %s %s strength=%.2f", sigRef.SignalType, sigRef.Side, enrichment.Confidence)
	}
	// Strategy-origin exits: attribute rationale as "strategy:<reason>" so the
	// trade log distinguishes them from position-monitor-driven "exit_monitor:*"
	// rationales. Opt-in via the "exit_origin=strategy" tag set by the
	// strategy when strategy_exits_priority is enabled.
	if sigRef.SignalType == start.SignalExit.String() && sigRef.Tags["exit_origin"] == "strategy" {
		if reason := sigRef.Tags["reason"]; reason != "" {
			rationale = "strategy:" + reason
		}
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
		"regime":            sigRef.Tags["regime_5m"],
		"vix_bucket":        sigRef.Tags["vix_bucket"],
		"market_context":    sigRef.Tags["market_context"],
	}
	// Propagate all signal tags into Meta for downstream consumers (backtest JSON, etc.).
	// Use "sig_" prefix to avoid collisions with existing Meta keys.
	for k, v := range sigRef.Tags {
		intent.Meta["sig_"+k] = v
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

	if parity.Enabled() {
		rs.parityDiagSized(intent, event)
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
	// Force-contract path: when the signal carries force_expiry + force_strike
	// + force_right tags (set by the copytrade strategy from a parsed Discord
	// message), skip delta/DTE screening and pin the exact contract. Callers
	// that do their own contract selection upstream use this path; normal
	// strategies leave these tags empty and go through ContractSelectionService.
	forced, forcedOK := extractForcedContract(sigRef.Tags)
	var optRight domain.OptionRight
	if forcedOK {
		optRight = forced.Right
	} else {
		optRight = domain.OptionRightCall
		if direction == domain.DirectionShort {
			optRight = domain.OptionRightPut
		}
	}

	regime := domain.RegimeTrend
	if regStr, ok := sigRef.Tags["regime_5m"]; ok && regStr != "none" {
		if parsed, err := domain.NewRegimeType(regStr); err == nil {
			regime = parsed
		}
	}

	var minDTE, maxDTE int
	var targetExpiry time.Time
	if forcedOK {
		// For a pinned contract, target the exact expiry and bracket the DTE
		// window tightly so the chain fetch (including backtest synthesizers)
		// returns just that expiry's strikes.
		targetExpiry = forced.Expiry
		days := int(math.Ceil(forced.Expiry.Sub(rs.nowFn()).Hours() / 24))
		if days < 0 {
			days = 0
		}
		minDTE, maxDTE = days, days
	} else {
		// Use the widest DTE range across defaults and all regime overrides
		// so the chain fetch covers all possible contract selection windows.
		minDTE = spec.Options.Defaults.MinDTE
		maxDTE = spec.Options.Defaults.MaxDTE
		for _, override := range spec.Options.RegimeOverrides {
			if override.MinDTE > 0 && override.MinDTE < minDTE {
				minDTE = override.MinDTE
			}
			if override.MaxDTE > maxDTE {
				maxDTE = override.MaxDTE
			}
		}
		targetDTE := minDTE + (maxDTE-minDTE)/2
		targetExpiry = rs.nowFn().AddDate(0, 0, targetDTE)
	}

	chain, err := rs.optionsMarket.GetOptionChain(
		ctx,
		domain.Symbol(sigRef.Symbol),
		targetExpiry,
		optRight,
		minDTE,
		maxDTE,
	)
	if err != nil {
		rs.logger.Error("options chain fetch failed",
			"symbol", sigRef.Symbol,
			"option_right", string(optRight),
			"error", err,
		)
		return errOptionsChainFailed
	}
	if len(chain) == 0 {
		rs.logger.Warn("empty options chain — falling back to equity",
			"symbol", sigRef.Symbol,
			"option_right", string(optRight),
			"target_expiry", targetExpiry,
		)
		return errOptionsChainEmpty
	}

	var best domain.OptionContractSnapshot
	if forcedOK {
		match, found := findPinnedContract(chain, forced.Expiry, forced.Strike)
		if !found {
			rs.logger.Warn("forced option contract not in chain — skipping",
				"symbol", sigRef.Symbol,
				"force_expiry", forced.Expiry.Format("2006-01-02"),
				"force_strike", forced.Strike,
				"force_right", string(forced.Right),
				"chain_size", len(chain),
			)
			// Do not fall through to equity — copytrade is contract-specific.
			return nil
		}
		best = match
	} else {
		selector := rs.buildContractSelector(spec.Options)
		picked, err := selector.SelectBestContract(direction, regime, chain)
		if err != nil {
			rs.logger.Warn("no suitable option contract found",
				"symbol", sigRef.Symbol,
				"option_right", string(optRight),
				"regime", string(regime),
				"chain_size", len(chain),
				"error", err,
			)
			return errOptionsChainEmpty // trigger equity fallback
		}
		best = picked
	}

	midPrice := (best.Bid + best.Ask) / 2
	if midPrice <= 0 {
		midPrice = best.Last
	}
	// Spread-aware limit price for options entry orders.
	// Default: mid + 60% of spread. Fallback: ask + 3% when spread is zero.
	spreadPct := 0.60
	if spec.Options.LimitSpreadPct != nil {
		spreadPct = *spec.Options.LimitSpreadPct
	}
	fallbackBPS := 300
	if spec.Options.LimitBufferBPS != nil {
		fallbackBPS = *spec.Options.LimitBufferBPS
	}
	var fillPrice float64
	spread := best.Ask - best.Bid
	switch {
	case event.EnvMode == domain.EnvModePaper && forcedOK && forced.RefPremium > 0:
		// Paper backtest pinned-fill path: simbroker will fill at the limit
		// regardless of the live ask, so the prior best.Ask > priceCap
		// rejection was gating on a price we never pay. We still emit at
		// the priceCap so the simbroker's fill matches the cap exactly.
		priceCap := forced.RefPremium * (1.0 + forced.BufferPct)
		fillPrice = priceCap
		rs.logger.Info("risk sizer: pinned entry to author ref premium + buffer",
			"strategy", strategyName,
			"contract", string(best.ContractSymbol),
			"ref_premium", forced.RefPremium,
			"buffer_pct", forced.BufferPct,
			"limit", fillPrice,
			"mid", midPrice,
			"ask", best.Ask,
		)
	case best.Ask > 0 && best.Bid > 0 && spread > 0:
		fillPrice = midPrice + spreadPct*spread
	case best.Ask > 0:
		fillPrice = best.Ask * (1.0 + float64(fallbackBPS)/10000.0)
	default:
		fillPrice = midPrice
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
	premiumPerContract := fillPrice * float64(best.Multiplier)
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
	// Cap contract count to limit spread drag on cheap options
	maxContracts := spec.Options.MaxContracts
	if maxContracts <= 0 {
		maxContracts = 15 // default cap
	}
	if qty > float64(maxContracts) {
		rs.logger.Info("capping option contracts",
			"symbol", sigRef.Symbol, "computed", qty, "cap", maxContracts)
		qty = float64(maxContracts)
	}

	// Per-position expected-loss cap (2026-04-16 MU incident fix).
	// Runs AFTER MaxContracts so the cap operates on the notional-sized
	// quantity. If cap reduces qty to 0 and RejectOnFloor is true, emit
	// a structured OrderIntentRejected with reason REJECTED_RISK_CAP and
	// bail. Disabled path is byte-identical to the pre-fix sizer.
	riskDecision := rs.applyPositionRiskCap(qty, premiumPerContract, exitRules, domain.InstrumentTypeOption)
	if !riskDecision.Disabled {
		if riskDecision.Qty <= 0 {
			reason := fmt.Sprintf("%s: expected_loss=%.2f > cap=%.2f (stop=%.2f%% via %s, budget=%.2f %s)",
				RejectedRiskCap,
				riskDecision.ComputedLossUSDRaw,
				riskDecision.CapUSD,
				riskDecision.StopPct*100,
				riskDecision.StopSource,
				riskDecision.DailyBudgetUSD,
				riskDecision.BudgetMode,
			)
			rs.logger.Warn("risk cap rejected trade — expected loss exceeds cap",
				"symbol", sigRef.Symbol,
				"contract", string(best.ContractSymbol),
				"reason", RejectedRiskCap,
				"expected_loss_usd", riskDecision.ComputedLossUSDRaw,
				"cap_usd", riskDecision.CapUSD,
				"stop_pct", riskDecision.StopPct,
				"stop_source", riskDecision.StopSource,
				"daily_budget_usd", riskDecision.DailyBudgetUSD,
				"budget_mode", riskDecision.BudgetMode,
			)
			if rs.positionRiskCap.RejectOnFloor {
				// Synthesize an OrderIntent-shaped payload for the rejection event.
				// ID uses a fresh UUID because no intent was constructed.
				rejection := domain.OrderIntentEventPayload{
					ID:        uuid.NewString(),
					Symbol:    sigRef.Symbol,
					Direction: string(direction),
					Strategy:  strategyName,
					Reason:    reason,
					Status:    domain.OrderIntentStatusRejected,
					Meta: map[string]string{
						"reject_code":         RejectedRiskCap,
						"computed_expected_loss": fmt.Sprintf("%.2f", riskDecision.ComputedLossUSDRaw),
						"cap_usd":             fmt.Sprintf("%.2f", riskDecision.CapUSD),
						"stop_pct":            fmt.Sprintf("%.6f", riskDecision.StopPct),
						"stop_source":         riskDecision.StopSource,
						"daily_budget_usd":    fmt.Sprintf("%.2f", riskDecision.DailyBudgetUSD),
						"budget_mode":         riskDecision.BudgetMode,
						"premium_per_contract": fmt.Sprintf("%.4f", premiumPerContract),
					},
				}
				rs.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, rejection.ID, rejection)
				return nil
			}
			// RejectOnFloor=false — operator chose to log-only and continue.
			// Fall through with original qty; this is a deliberate escape hatch.
		} else if riskDecision.Adjusted {
			rs.logger.Info("risk cap reduced contracts",
				"symbol", sigRef.Symbol,
				"pre_cap_qty", qty,
				"post_cap_qty", riskDecision.Qty,
				"expected_loss_usd", riskDecision.ComputedLossUSDNow,
				"cap_usd", riskDecision.CapUSD,
				"stop_pct", riskDecision.StopPct,
				"stop_source", riskDecision.StopSource,
			)
			qty = riskDecision.Qty
		}
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
	if override, ok := sigRef.Tags[domain.TagAuthorText]; ok && override != "" {
		rationale = override
	}
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
		fillPrice,
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

	staleSecs := 120 // default: cancel unfilled option orders after 120s
	if spec.Options.StaleCancelSecs != nil {
		staleSecs = *spec.Options.StaleCancelSecs
	}

	intent.AssetClass = domain.AssetClassEquity
	intent.Meta = map[string]string{
		"instrument_type":    "OPTION",
		"option_right":       string(optRight),
		"underlying":         sigRef.Symbol,
		"strike":             fmt.Sprintf("%.2f", best.Strike),
		"expiry":             best.Expiry.Format("2006-01-02"),
		"delta_at_entry":     fmt.Sprintf("%.4f", best.Delta),
		"iv_at_entry":        fmt.Sprintf("%.4f", best.IV),
		"premium":            fmt.Sprintf("%.2f", midPrice),
		"entry_date":         rs.nowFn().Format("2006-01-02"),
		"entry_underlying":   fmt.Sprintf("%.4f", refPrice),
		"open_interest":      strconv.Itoa(best.OpenInterest),
		"stale_cancel_secs":  strconv.Itoa(staleSecs),
		"enrichment_status":  string(enrichment.Status),
		"risk_modifier":      string(enrichment.RiskModifier),
		"bull":              enrichment.BullArgument,
		"bear":              enrichment.BearArgument,
		"judge":             enrichment.JudgeReasoning,
		"regime":            sigRef.Tags["regime_5m"],
		"vix_bucket":        sigRef.Tags["vix_bucket"],
		"market_context":    sigRef.Tags["market_context"],
	}
	// Propagate all signal tags into Meta for downstream consumers (backtest JSON, etc.).
	for k, v := range sigRef.Tags {
		intent.Meta["sig_"+k] = v
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

	if parity.Enabled() {
		rs.parityDiagSized(intent, event)
	}
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

// handleOptionsExit translates a strategy-emitted exit signal keyed by the
// underlying ticker into one CloseLong intent per open option contract under
// that underlying. Without this, position_gate filters by Symbol == intent.Symbol
// and rejects the equity-keyed exit with no_position_to_exit because the open
// position is keyed by the OCC contract symbol (e.g. MRVL260508C00162500).
//
// Multiple open contracts under the same underlying all close (plan default).
// position_gate's TryMarkInflightExit deduplicates against concurrent
// position-monitor exits (PREMIUM_STOP, CHANDELIER_TRAIL).
//
// When openOptionContractsLookup is unset or returns no positions, the call
// is a no-op (legitimate stale-exit case logged at INFO).
func (rs *RiskSizer) handleOptionsExit(
	ctx context.Context,
	event domain.Event,
	enrichment domain.SignalEnrichment,
	sigRef domain.SignalRef,
	strategyName string,
) error {
	if rs.openOptionContractsLookup == nil {
		rs.logger.Debug("options exit: open-contracts lookup not wired; skipping",
			"underlying", sigRef.Symbol,
		)
		return nil
	}

	contracts := rs.openOptionContractsLookup(sigRef.Symbol)
	if len(contracts) == 0 {
		rs.logger.Info("options exit: no open contracts to close",
			"underlying", sigRef.Symbol,
			"reason", sigRef.Tags["reason"],
		)
		return nil
	}

	rationale := strategyExitRationale(sigRef, enrichment)
	barTS := exitBarTimestamp(sigRef, rs.nowFn)

	for _, pos := range contracts {
		if err := rs.emitOptionExitIntent(ctx, event, pos, strategyName, rationale, enrichment.Confidence, barTS, sigRef.Tags); err != nil {
			rs.logger.Error("options exit: failed to emit close intent",
				"contract", string(pos.Symbol),
				"underlying", sigRef.Symbol,
				"error", err,
			)
		}
	}
	return nil
}

func (rs *RiskSizer) emitOptionExitIntent(
	ctx context.Context,
	event domain.Event,
	pos domain.MonitoredPosition,
	strategyName string,
	rationale string,
	confidence float64,
	barTS time.Time,
	signalTags map[string]string,
) error {
	intentID := uuid.New()
	idempotencyKey := fmt.Sprintf("STRATEGY_EXIT:%s:%s:%s:%d",
		event.TenantID, event.EnvMode, pos.Symbol, barTS.Unix())

	// Mirror the position_monitor.triggerExit pattern: build via NewOrderIntent
	// keyed by the OCC contract symbol so position_gate's Symbol-equality
	// filter matches. AssetClass = AssetClassEquity matches the existing
	// option-intent convention. OrderType=market overrides the limit field
	// at the broker, but NewOrderIntent's validator requires LimitPrice > 0
	// so the entry premium serves as a sane placeholder. Options always
	// close as CloseLong because long calls and long puts are both broker-
	// LONG positions.
	limitPrice := pos.EntryPrice
	if limitPrice <= 0 {
		limitPrice = 0.01
	}
	intent, err := domain.NewOrderIntent(
		intentID,
		event.TenantID,
		event.EnvMode,
		pos.Symbol,
		domain.DirectionCloseLong,
		limitPrice,
		0, // stopLoss not required for exits
		0, // maxSlippageBPS
		pos.Quantity,
		strategyName,
		rationale,
		confidence,
		idempotencyKey,
	)
	if err != nil {
		return fmt.Errorf("build option exit intent: %w", err)
	}
	intent.OrderType = "market"
	intent.TimeInForce = "day"
	intent.AssetClass = domain.AssetClassEquity

	intent.Meta = map[string]string{
		"instrument_type": string(domain.InstrumentTypeOption),
		"underlying":      string(domain.UnderlyingFromOCC(pos.Symbol)),
		"option_right":    pos.OptionRight,
		"expiry":          pos.OptionExpiry.Format("2006-01-02"),
		"exit_origin":     "strategy",
	}
	for k, v := range signalTags {
		intent.Meta["sig_"+k] = v
	}

	rs.logger.Info("options exit intent emitted",
		"contract", string(pos.Symbol),
		"underlying", string(domain.UnderlyingFromOCC(pos.Symbol)),
		"qty", pos.Quantity,
		"rationale", rationale,
	)

	if parity.Enabled() {
		rs.parityDiagSized(intent, event)
	}
	rs.emit(ctx, domain.EventOrderIntentCreated, event.TenantID, event.EnvMode, intentID.String(), intent)
	return nil
}

// strategyExitRationale formats the exit rationale tag as "strategy:<reason>"
// so the trade log distinguishes strategy-origin exits from position-monitor-
// driven exit_monitor:* rationales. Mirrors the equity-path tagging at the
// risk_sizer.go SignalExit branch.
func strategyExitRationale(sigRef domain.SignalRef, enrichment domain.SignalEnrichment) string {
	if reason := sigRef.Tags["reason"]; reason != "" {
		return "strategy:" + reason
	}
	if enrichment.Rationale != "" {
		return "strategy:" + enrichment.Rationale
	}
	return "strategy:exit"
}

// exitBarTimestamp pulls the signal bar timestamp from sigRef tags so the
// idempotency key is stable across re-fires of the same bar; falls back to
// nowFn when the tag is missing (best-effort dedup, the position-gate
// inflight-exit lock still guards against double-close at the broker layer).
func exitBarTimestamp(sigRef domain.SignalRef, nowFn func() time.Time) time.Time {
	if tsStr, ok := sigRef.Tags["bar_ts"]; ok && tsStr != "" {
		if t, err := time.Parse(time.RFC3339, tsStr); err == nil {
			return t
		}
	}
	return nowFn()
}

// riskCapDecision carries the outcome of applyPositionRiskCap so the
// caller can either resize the order or emit a structured rejection.
type riskCapDecision struct {
	Adjusted           bool    // true if Qty was reduced
	Qty                float64 // final contract count (0 means reject)
	StopPct            float64 // widest active stop used in the check
	StopSource         string  // which rule dominated the widest-active pick
	CapUSD             float64 // computed per-position cap in USD
	DailyBudgetUSD     float64 // effective daily loss budget used
	BudgetMode         string  // "account_pct" or "fixed_usd"
	ComputedLossUSDRaw float64 // expected loss at the ORIGINAL pre-cap qty
	ComputedLossUSDNow float64 // expected loss at the final qty
	Disabled           bool    // true when Enabled=false or AppliesTo excludes options — no check performed
}

// applyPositionRiskCap evaluates the per-position expected-loss cap
// against the sized order and returns an adjusted qty (possibly 0).
// The cap is:
//
//	cap_usd      = MaxPositionRiskFrac × daily_loss_budget
//	expected_loss = contracts × premium_per_contract × stop_pct
//
// If expected_loss > cap_usd, we reduce contracts to
// floor(cap_usd / (premium_per_contract × stop_pct)).  The guard
// short-circuits (no adjustment) when Enabled=false, when
// InstrumentType isn't in AppliesTo, when widest-active stop is 0
// (no loss sizing possible), or when equity is unavailable for
// account_pct mode (log-warn, no cap). This mirrors the quant spec
// exactly so backtests stay deterministic.
func (rs *RiskSizer) applyPositionRiskCap(
	qty float64,
	premiumPerContract float64,
	exitRules []domain.ExitRule,
	instrumentType domain.InstrumentType,
) riskCapDecision {
	rs.mu.RLock()
	cap := rs.positionRiskCap
	equity := rs.accountEquity
	rs.mu.RUnlock()

	decision := riskCapDecision{Qty: qty, BudgetMode: cap.Mode}
	if !cap.Enabled {
		decision.Disabled = true
		return decision
	}
	if !isInstrumentTypeApplicable(instrumentType, cap.AppliesTo) {
		decision.Disabled = true
		return decision
	}

	stopPct, stopSource := domain.WidestActiveStopPct(exitRules)
	decision.StopPct = stopPct
	decision.StopSource = stopSource
	if stopPct <= 0 {
		// No quantifiable stop → we cannot compute expected loss. Safer
		// to skip the cap than to reject everything lacking a stop rule.
		rs.logger.Warn("risk cap: no widest-active stop on exit rules — skipping cap",
			"rules", len(exitRules))
		decision.Disabled = true
		return decision
	}

	var budgetUSD float64
	switch strings.ToLower(cap.Mode) {
	case "fixed_usd":
		budgetUSD = cap.DailyLossBudgetUSD
	default: // "account_pct"
		if equity <= 0 {
			// Equity unavailable at sizing time (startup race) — quant
			// spec: short-circuit to no cap, log warning.
			rs.logger.Warn("risk cap: equity unavailable — skipping cap (account_pct mode)")
			decision.Disabled = true
			return decision
		}
		budgetUSD = equity * cap.DailyLossBudgetPct
	}
	decision.DailyBudgetUSD = budgetUSD

	capUSD := budgetUSD * cap.MaxPositionRiskFrac
	decision.CapUSD = capUSD

	lossPerContract := premiumPerContract * stopPct
	if lossPerContract <= 0 {
		decision.Disabled = true
		return decision
	}
	decision.ComputedLossUSDRaw = qty * lossPerContract

	if decision.ComputedLossUSDRaw <= capUSD {
		decision.ComputedLossUSDNow = decision.ComputedLossUSDRaw
		return decision
	}

	maxQty := math.Floor(capUSD / lossPerContract)
	if maxQty < qty {
		decision.Adjusted = true
		decision.Qty = maxQty
	}
	decision.ComputedLossUSDNow = decision.Qty * lossPerContract
	return decision
}

// isInstrumentTypeApplicable reports whether the cap should fire for
// the given instrument type. Empty list defaults to options-only per
// quant phase-1 spec.
func isInstrumentTypeApplicable(it domain.InstrumentType, appliesTo []string) bool {
	if len(appliesTo) == 0 {
		return it == domain.InstrumentTypeOption
	}
	for _, s := range appliesTo {
		switch strings.ToLower(s) {
		case "options":
			if it == domain.InstrumentTypeOption {
				return true
			}
		case "equity", "equities":
			if it == domain.InstrumentTypeEquity || it == "" {
				return true
			}
		case "crypto":
			if it == domain.InstrumentTypeCrypto {
				return true
			}
		}
	}
	return false
}

func (rs *RiskSizer) emit(ctx context.Context, eventType string, tenantID string, envMode domain.EnvMode, idempotencyKey string, payload any) {
	ev, err := domain.NewEvent(eventType, tenantID, envMode, idempotencyKey, payload)
	if err != nil {
		return
	}
	// Overwrite OccurredAt with rs.nowFn() so backtest events carry sim-time
	// (rs.nowFn returns currentBarTime). Downstream async subscribers (e.g.
	// execution.Service.handleIntent) read event.OccurredAt for DecidedAt;
	// without this override the event carries domain.NewEvent's time.Now()
	// default, which is wall clock and breaks causal consistency in backtest.
	if rs.nowFn != nil {
		ev.OccurredAt = rs.nowFn()
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

// parityDiagSized emits a parity-diag log line at risk-sizer emit time so
// live and backtest can be diffed at the contract-selection / quantity
// stage. Captures the inputs and outputs of risk sizing: signal symbol,
// direction, sized qty/price, and option metadata when present.
func (rs *RiskSizer) parityDiagSized(intent domain.OrderIntent, event domain.Event) {
	instrumentType := ""
	if intent.Instrument != nil {
		instrumentType = string(intent.Instrument.Type)
	}
	rs.logger.Info("parity-diag",
		"stage", parity.StageRiskSized,
		"symbol", string(intent.Symbol),
		"strategy", intent.Strategy,
		"direction", string(intent.Direction),
		"asset_class", string(intent.AssetClass),
		"instrument_type", instrumentType,
		"quantity", intent.Quantity,
		"limit_price", intent.LimitPrice,
		"stop_loss", intent.StopLoss,
		"premium", intent.Meta["premium"],
		"expiry", intent.Meta["expiry"],
		"option_right", intent.Meta["option_right"],
		"max_loss_usd", intent.Meta["max_loss_usd"],
		"env_mode", string(event.EnvMode))
}
