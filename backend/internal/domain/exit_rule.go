package domain

import (
	"fmt"
	"strings"
	"time"
)

// ExitRuleType identifies the kind of exit condition for active position monitoring.
type ExitRuleType string

const (
	ExitRuleTrailingStop    ExitRuleType = "TRAILING_STOP"
	ExitRuleProfitTarget    ExitRuleType = "PROFIT_TARGET"
	ExitRuleTimeExit        ExitRuleType = "TIME_EXIT"
	ExitRuleEODFlatten      ExitRuleType = "EOD_FLATTEN"
	ExitRuleMaxHoldingTime  ExitRuleType = "MAX_HOLDING_TIME"
	ExitRuleMaxLoss         ExitRuleType = "MAX_LOSS"
	ExitRuleVolatilityStop  ExitRuleType = "VOLATILITY_STOP"
	ExitRuleSDTarget        ExitRuleType = "SD_TARGET"
	ExitRuleStepStop        ExitRuleType = "STEP_STOP"
	ExitRuleStagnationExit  ExitRuleType = "STAGNATION_EXIT"
	ExitRuleBreakevenStop   ExitRuleType = "BREAKEVEN_STOP"
	ExitRuleDTEFloor        ExitRuleType = "DTE_FLOOR"
	ExitRuleExpiryWatch     ExitRuleType = "EXPIRY_WATCH"
	ExitRuleSwingStop       ExitRuleType = "SWING_STOP"
	ExitRuleTieredTP        ExitRuleType = "TIERED_TP"
	ExitRuleTimePartial     ExitRuleType = "TIME_PARTIAL"
	ExitRulePremiumStop     ExitRuleType = "PREMIUM_STOP"     // exit if premium drops X% from entry
	ExitRulePremiumTrail    ExitRuleType = "PREMIUM_TRAIL"    // trail X% from premium high-water mark
	ExitRulePremiumTarget   ExitRuleType = "PREMIUM_TARGET"   // exit if premium rises X% from entry
	ExitRuleFastFail        ExitRuleType = "FAST_FAIL_EXIT"   // exit if no MFE progress after N minutes
	ExitRuleChandelierTrail ExitRuleType = "CHANDELIER_TRAIL" // trail giveback_pct of MFE once above activate_pct
	ExitRuleCopytradeSTC    ExitRuleType = "COPYTRADE_STC"    // synthetic: partial/full close driven by copytrade author STC
)

func (e ExitRuleType) String() string { return string(e) }

// RequiresPrice reports whether this exit rule type needs a live, non-stale
// price to evaluate. Time-only rules (MAX_HOLDING_TIME, EOD_FLATTEN) return
// false so the position monitor can fire them even during data outages.
func (e ExitRuleType) RequiresPrice() bool {
	switch e {
	case ExitRuleMaxHoldingTime, ExitRuleEODFlatten, ExitRuleDTEFloor, ExitRuleExpiryWatch:
		return false
	default:
		return true
	}
}

// NewExitRuleType validates an exit rule type string.
func NewExitRuleType(s string) (ExitRuleType, error) {
	switch ExitRuleType(s) {
	case ExitRuleTrailingStop, ExitRuleProfitTarget, ExitRuleTimeExit,
		ExitRuleEODFlatten, ExitRuleMaxHoldingTime, ExitRuleMaxLoss,
		ExitRuleVolatilityStop, ExitRuleSDTarget, ExitRuleStepStop,
		ExitRuleStagnationExit, ExitRuleBreakevenStop,
		ExitRuleDTEFloor, ExitRuleExpiryWatch,
		ExitRuleSwingStop,
		ExitRuleTieredTP, ExitRuleTimePartial,
		ExitRulePremiumStop, ExitRulePremiumTrail, ExitRulePremiumTarget,
		ExitRuleFastFail, ExitRuleChandelierTrail, ExitRuleCopytradeSTC:
		return ExitRuleType(s), nil
	default:
		return "", fmt.Errorf("invalid exit rule type: %q", s)
	}
}

// ExitRule is a single configurable exit condition attached to a monitored position.
type ExitRule struct {
	Type   ExitRuleType
	Params map[string]float64
}

// NewExitRule creates a validated ExitRule.
func NewExitRule(ruleType ExitRuleType, params map[string]float64) (ExitRule, error) {
	if ruleType == "" {
		return ExitRule{}, fmt.Errorf("exit rule type is required")
	}
	if params == nil {
		params = make(map[string]float64)
	}
	return ExitRule{Type: ruleType, Params: params}, nil
}

// Param returns a parameter value with a fallback default.
func (r ExitRule) Param(key string, fallback float64) float64 {
	if v, ok := r.Params[key]; ok {
		return v
	}
	return fallback
}

// WidestActiveStopPct returns the maximum downside-stop distance across
// a slice of active exit rules, expressed as a fraction of entry. The
// per-position risk cap sizes the expected-loss check against this value
// because the loosest stop is what actually bounds realized loss — a
// tight MAX_LOSS loses its meaning if a sibling PREMIUM_TRAIL lets price
// drift 30% before firing.
//
// Source selection (stop_pct_source = "widest_active"):
//
//	MAX_LOSS           → param "pct"
//	PREMIUM_STOP       → param "threshold"   (premium drawdown from entry)
//	PREMIUM_TRAIL      → param "trail_pct"   (premium drawdown from HWM)
//	TRAILING_STOP      → param "pct"         (spot drawdown from HWM)
//	CHANDELIER_TRAIL   → param "giveback_pct"
//
// Returns (0, "") when no qualifying rule is present. The source label
// is surfaced in the rejection payload for operator forensics.
func WidestActiveStopPct(rules []ExitRule) (pct float64, source string) {
	for _, r := range rules {
		var v float64
		var label string
		switch r.Type {
		case ExitRuleMaxLoss:
			v = r.Param("pct", 0)
			label = "MAX_LOSS.pct"
		case ExitRulePremiumStop:
			v = r.Param("threshold", 0)
			label = "PREMIUM_STOP.threshold"
		case ExitRulePremiumTrail:
			v = r.Param("trail_pct", 0)
			label = "PREMIUM_TRAIL.trail_pct"
		case ExitRuleTrailingStop:
			v = r.Param("pct", 0)
			label = "TRAILING_STOP.pct"
		case ExitRuleChandelierTrail:
			v = r.Param("giveback_pct", 0)
			label = "CHANDELIER_TRAIL.giveback_pct"
		}
		if v > pct {
			pct = v
			source = label
		}
	}
	return pct, source
}

// EntryThesis captures the AI judge's reasoning at position entry time.
// Stored on MonitoredPosition so the Risk Agent can compare "what we believed
// at entry" vs "what's true now" during periodic re-evaluation.
type EntryThesis struct {
	BullArgument   string       `json:"bullArgument"`
	BearArgument   string       `json:"bearArgument"`
	JudgeReasoning string       `json:"judgeReasoning"`
	Rationale      string       `json:"rationale"`
	Confidence     float64      `json:"confidence"`
	RiskModifier   RiskModifier `json:"riskModifier"`
	Direction      Direction    `json:"direction"`
	EntryRegime    string       `json:"entryRegime"` // regime type at entry (e.g. "BALANCE", "TREND_UP")
}

// MonitoredPosition tracks an open position with its high-water mark and exit rules.
// It is owned by the position monitor actor and must not be shared across goroutines.
type MonitoredPosition struct {
	Symbol           Symbol
	EntryPrice       float64
	EntryTime        time.Time
	HighWaterMark    float64
	LowWaterMark     float64
	Strategy         string
	AssetClass       AssetClass
	ExitRules        []ExitRule
	InitialExitRules []ExitRule // original config values; never modified after creation
	TenantID         string
	EnvMode          EnvMode
	Quantity         float64
	Side             string // "BUY" (long) or "SELL" (short) — set from fill side
	ExitPending      bool   // true when an exit intent has been emitted and is awaiting terminal outcome
	ExitPendingAt    time.Time
	ExitOrderID      string // broker order ID of the active exit order (for cancel-and-chase)

	// PendingExitOrderIDs is the authoritative set of broker-working exit
	// orders for this position. Populated by processExitSubmitted, drained by
	// processExitTerminal. Used to enforce "no parallel working exits" on the
	// escalate-to-market path. ExitOrderID (singular) retains its existing
	// meaning as the order currently being managed by handleExitTimeout.
	PendingExitOrderIDs map[string]struct{} `json:"-"`

	ExitRetryCount int          // number of exit attempts (market escalations); re-pegs do NOT increment this
	EntryThesis    *EntryThesis // nil if no AI enrichment was available at entry

	// Asymmetric exit timeout / re-peg state. A single "exit attempt" can
	// span several broker orders — the initial limit and up to N re-pegs
	// tightening toward mid. ExitRepegCount counts re-pegs within the
	// current attempt; it resets to 0 on market escalation. ExitWallStartedAt
	// pins the wall-clock start of the attempt so an adverse feed/stuck-broker
	// combo can't live-lock us in the re-peg sub-loop. ExitManaging is set
	// while the cancel-and-await goroutine owns the exit lifecycle; it
	// suppresses re-entrant handleExitTimeout from the tick loop.
	// ExitLastSentPrice carries the last limit price we sent so the next
	// re-peg can tighten by one tick rather than recomputing from scratch.
	ExitRepegCount    int       `json:"exitRepegCount,omitempty"`
	ExitWallStartedAt time.Time `json:"exitWallStartedAt,omitempty"`
	ExitManaging      bool      `json:"exitManaging,omitempty"`
	ExitLastSentPrice float64   `json:"exitLastSentPrice,omitempty"`

	// ExitCancelTimeoutCount counts consecutive cancel-and-await cycles that
	// did not reach a terminal broker status within the confirm window. Each
	// such cycle leaves the original broker order potentially live, so the
	// resubmit branch is suppressed until the count clears (next confirmed
	// terminal cancel or fill). After maxExitCancelTimeouts consecutive
	// unsafe cycles the position is left for manual intervention. Reset to 0
	// on any confirmed terminal cancel.
	ExitCancelTimeoutCount int `json:"exitCancelTimeoutCount,omitempty"`

	LastRevaluation   *RiskRevaluation `json:"lastRevaluation,omitempty"`
	LastRevaluationAt time.Time        `json:"lastRevaluationAt,omitempty"`

	InstrumentType InstrumentType `json:"instrumentType,omitempty"`
	OptionExpiry   time.Time      `json:"optionExpiry,omitempty"`
	OptionRight    string         `json:"optionRight,omitempty"`

	// StrategyExitsPriority, when true, tells the position monitor to skip
	// its own price-based exit rule evaluation for this position — the
	// strategy's OnBar exits are authoritative. Time-only rules
	// (MAX_HOLDING_TIME, EOD_FLATTEN) still fire as a safety net. Set
	// from the entry signal tag "strategy_exits_priority".
	StrategyExitsPriority bool `json:"strategyExitsPriority,omitempty"`

	// Sprint 5 combo tracking. When Legs is non-empty this is a multi-leg
	// BAG position. EntryPrice is the net premium paid (debit) or collected
	// (credit). Quantity is the combo count. LegFillPrices mirrors Legs
	// one-to-one with the per-leg fill prices used for P&L attribution.
	Legs          []ComboLeg `json:"legs,omitempty"`
	ComboType     ComboType  `json:"comboType,omitempty"`
	LegFillPrices []float64  `json:"legFillPrices,omitempty"`

	CustomState map[string]float64 `json:"customState,omitempty"`

	// Stored WITHOUT the sig_ prefix to match fill.SignalTags shape; re-prefixed
	// on copy into exit intent Meta.
	EntrySignalTags map[string]string `json:"entrySignalTags,omitempty"`
}

// IsCombo reports whether this is a multi-leg combo position.
func (mp *MonitoredPosition) IsCombo() bool {
	return len(mp.Legs) > 0
}

// HasExitInFlight reports whether an exit is currently in flight for this
// position anywhere in the pipeline — from triggerExit setting ExitPending
// through the broker's terminal event draining PendingExitOrderIDs. Used by
// the cross-reason arbitration guard in triggerExit and by the reconciler
// to suppress orphan alerts during the broker-ack/SaveTrade gap.
func (mp *MonitoredPosition) HasExitInFlight() bool {
	return mp.ExitPending || len(mp.PendingExitOrderIDs) > 0
}

// ComboPnL aggregates P&L across legs of a combo position. `legPrices` is a
// slice of current per-leg prices, aligned 1:1 with mp.Legs. Returns the
// realized-or-marked P&L in dollars: for each leg, (currentPrice -
// entryFill) * |ratio| * quantity * multiplier, signed by whether the leg
// was bought (ratio > 0) or sold (ratio < 0). Missing leg entry prices or a
// length mismatch returns 0 to avoid injecting fabricated numbers.
func (mp *MonitoredPosition) ComboPnL(legPrices []float64) float64 {
	if !mp.IsCombo() {
		return 0
	}
	if len(legPrices) != len(mp.Legs) || len(mp.LegFillPrices) != len(mp.Legs) {
		return 0
	}
	const mult = 100.0 // equity option multiplier
	var pnl float64
	for i, leg := range mp.Legs {
		if leg.Ratio == 0 {
			continue
		}
		delta := legPrices[i] - mp.LegFillPrices[i]
		absRatio := leg.Ratio
		sign := 1.0
		if absRatio < 0 {
			absRatio = -absRatio
			sign = -1.0
		}
		pnl += sign * delta * float64(absRatio) * mp.Quantity * mult
	}
	return pnl
}

// ClosingLegs returns the leg slice required to close this combo position:
// every leg's ratio is inverted so a long becomes a sell-to-close and a
// short becomes a buy-to-close. The underlying/expiry/strike/right are
// preserved so the closing BAG contract matches exactly.
func (mp *MonitoredPosition) ClosingLegs() []ComboLeg {
	if !mp.IsCombo() {
		return nil
	}
	out := make([]ComboLeg, len(mp.Legs))
	for i, leg := range mp.Legs {
		inv := leg
		inv.Ratio = -leg.Ratio
		out[i] = inv
	}
	return out
}

// NewMonitoredPosition creates a MonitoredPosition with high/low water marks initialized to entry price.
func NewMonitoredPosition(
	symbol Symbol,
	entryPrice float64,
	entryTime time.Time,
	strategy string,
	assetClass AssetClass,
	exitRules []ExitRule,
	tenantID string,
	envMode EnvMode,
	quantity float64,
) (MonitoredPosition, error) {
	if entryPrice <= 0 {
		return MonitoredPosition{}, fmt.Errorf("entry price must be positive, got %v", entryPrice)
	}
	if quantity <= 0 {
		return MonitoredPosition{}, fmt.Errorf("quantity must be positive, got %v", quantity)
	}
	initialRules := make([]ExitRule, len(exitRules))
	for i, r := range exitRules {
		params := make(map[string]float64, len(r.Params))
		for k, v := range r.Params {
			params[k] = v
		}
		initialRules[i] = ExitRule{Type: r.Type, Params: params}
	}

	return MonitoredPosition{
		Symbol:              symbol,
		EntryPrice:          entryPrice,
		EntryTime:           entryTime,
		HighWaterMark:       entryPrice,
		LowWaterMark:        entryPrice,
		Strategy:            strategy,
		AssetClass:          assetClass,
		ExitRules:           exitRules,
		InitialExitRules:    initialRules,
		TenantID:            tenantID,
		EnvMode:             envMode,
		Quantity:            quantity,
		CustomState:         make(map[string]float64),
		PendingExitOrderIDs: make(map[string]struct{}),
	}, nil
}

// UpdateWaterMarks adjusts high/low water marks based on a new price observation.
func (mp *MonitoredPosition) UpdateWaterMarks(price float64) {
	if price > mp.HighWaterMark {
		mp.HighWaterMark = price
	}
	if price < mp.LowWaterMark {
		mp.LowWaterMark = price
	}
}

// IsShort returns true if the position profits from a price decline.
// This covers equity shorts (side=SELL) and option puts (long put = bearish).
func (mp *MonitoredPosition) IsShort() bool {
	return strings.EqualFold(mp.Side, "SELL") || strings.EqualFold(mp.OptionRight, "PUT")
}

// UnrealizedPnLPct returns the unrealized P&L as a percentage of entry price.
// For longs: (current - entry) / entry. For shorts: (entry - current) / entry.
func (mp *MonitoredPosition) UnrealizedPnLPct(currentPrice float64) float64 {
	if mp.EntryPrice == 0 {
		return 0
	}
	if mp.IsShort() {
		return (mp.EntryPrice - currentPrice) / mp.EntryPrice
	}
	return (currentPrice - mp.EntryPrice) / mp.EntryPrice
}

// DrawdownFromHighPct returns the adverse move from the best price as a percentage.
// For longs: (high - current) / high (price dropping from peak).
// For shorts: (current - low) / low (price rising from trough).
func (mp *MonitoredPosition) DrawdownFromHighPct(currentPrice float64) float64 {
	if mp.IsShort() {
		if mp.LowWaterMark == 0 {
			return 0
		}
		return (currentPrice - mp.LowWaterMark) / mp.LowWaterMark
	}
	if mp.HighWaterMark == 0 {
		return 0
	}
	return (mp.HighWaterMark - currentPrice) / mp.HighWaterMark
}

// EstimatePremiumStopLoss returns the dollar loss at a clean PREMIUM_STOP
// trigger for an option position: entry_premium × threshold × qty × 100.
// Returns (0, false) for non-options, when no PREMIUM_STOP rule is attached,
// or when the entry premium is missing — letting callers distinguish "no
// estimate available" from "estimate is zero".
func (mp *MonitoredPosition) EstimatePremiumStopLoss() (float64, bool) {
	if mp.InstrumentType != InstrumentTypeOption {
		return 0, false
	}
	entry, ok := mp.CustomState["option_premium"]
	if !ok || entry <= 0 {
		return 0, false
	}
	for _, rule := range mp.ExitRules {
		if rule.Type != ExitRulePremiumStop {
			continue
		}
		threshold := rule.Param("threshold", 0)
		if threshold <= 0 {
			return 0, false
		}
		return entry * threshold * mp.Quantity * 100, true
	}
	return 0, false
}

// HasBSMInputs reports whether CustomState carries the full set of inputs
// required to run the Black-Scholes-Merton premium recalculation: strike,
// expiry, entry-IV, and option right. Used by callers that need to
// distinguish "premium estimate is unavailable" from "premium went to
// zero" — the former is a data-availability problem (e.g. post-restart
// before BSM inputs are restored), the latter is a real exit signal.
func (mp *MonitoredPosition) HasBSMInputs() bool {
	if mp.CustomState == nil {
		return false
	}
	strike, hasStrike := mp.CustomState["strike"]
	_, hasExpiry := mp.CustomState["expiry_unix"]
	ivAtEntry, hasIV := mp.CustomState["iv_at_entry"]
	_, hasRight := mp.CustomState["is_call"]
	return hasStrike && hasExpiry && hasIV && hasRight && strike > 0 && ivAtEntry > 0
}

// EstimatedPremium computes the current option premium using Black-Scholes-Merton
// recalculation when strike, expiry, IV, and option right are available in
// CustomState. Falls back to the legacy delta-linear approximation when BSM
// inputs are missing AND delta_at_entry is present (backward compatibility
// with older positions that pre-date BSM-input recording).
//
// The BSM approach accounts for gamma (convexity), theta (time decay), and all
// higher-order Greeks, fixing the 5-25% error that delta-linear produces for
// ATM options on 1-3% underlying moves.
//
// Returns 0 only when neither path can run — option_premium missing, or
// neither BSM nor delta inputs are recoverable. Callers that key safety
// behavior off the zero return must distinguish "BSM unavailable" from
// "premium went to zero" via HasBSMInputs() — see evaluatePremiumStop.
//
// Spread cost is subtracted using the same tiers as SimBroker:
// >=10 -> 0.3%, >=5 -> 0.5%, >=2 -> 0.8%, <2 -> 1.5%.
func (mp *MonitoredPosition) EstimatedPremium(currentUnderlyingPrice float64, now time.Time) float64 {
	if mp.InstrumentType != InstrumentTypeOption {
		return 0
	}
	if mp.CustomState == nil {
		return 0
	}
	entryPremium, ok := mp.CustomState["option_premium"]
	if !ok || entryPremium <= 0 {
		return 0
	}

	// Spread cost tiers (matching simbroker)
	spreadCost := spreadCostForPremium(entryPremium)

	// Primary path: BSM recalculation when full input set is available.
	if mp.HasBSMInputs() {
		strike := mp.CustomState["strike"]
		expiryUnix := mp.CustomState["expiry_unix"]
		ivAtEntry := mp.CustomState["iv_at_entry"]
		isCallVal := mp.CustomState["is_call"]

		expiryTime := time.Unix(int64(expiryUnix), 0)
		// Remaining DTE in years. Use market close (16:00 ET) as expiry time
		// for more accurate intraday theta.
		dteYears := expiryTime.Sub(now).Hours() / (365.25 * 24)
		if dteYears <= 0 {
			// Expired — premium is intrinsic value only.
			var intrinsic float64
			if isCallVal == 1.0 {
				intrinsic = currentUnderlyingPrice - strike
			} else {
				intrinsic = strike - currentUnderlyingPrice
			}
			if intrinsic < 0 {
				intrinsic = 0
			}
			est := intrinsic - spreadCost
			if est < 0 {
				est = 0
			}
			return est
		}

		isCall := isCallVal == 1.0
		const riskFreeRate = 0.045
		est := BSMPrice(currentUnderlyingPrice, strike, dteYears, riskFreeRate, ivAtEntry, isCall)
		est -= spreadCost
		if est < 0 {
			est = 0
		}
		return est
	}

	// Fallback: legacy delta-linear approximation. Requires delta_at_entry.
	delta, hasDelta := mp.CustomState["delta_at_entry"]
	if !hasDelta {
		return 0
	}
	underlyingMove := currentUnderlyingPrice - mp.EntryPrice
	est := entryPremium + delta*underlyingMove - spreadCost
	if est < 0 {
		est = 0
	}
	return est
}

// spreadCostForPremium returns the spread cost for a given entry premium,
// using tiered spread percentages matching SimBroker.
func spreadCostForPremium(entryPremium float64) float64 {
	var spreadPct float64
	switch {
	case entryPremium >= 10:
		spreadPct = 0.003
	case entryPremium >= 5:
		spreadPct = 0.005
	case entryPremium >= 2:
		spreadPct = 0.008
	default:
		spreadPct = 0.015
	}
	return entryPremium * spreadPct
}

// PositionKey returns a unique key for this position within a tenant/env scope.
func (mp *MonitoredPosition) PositionKey() string {
	return fmt.Sprintf("%s:%s:%s", mp.TenantID, mp.EnvMode, mp.Symbol)
}

// ValidateExitRules checks for contradictions across a set of exit rules.
// Returns an error if trailing stop percentage >= max loss percentage,
// which makes the trailing stop dead code (MAX_LOSS always fires first
// when HWM ≈ entry price).
func ValidateExitRules(rules []ExitRule) error {
	var trailingPct, maxLossPct float64
	var hasTrailing, hasMaxLoss bool

	for _, r := range rules {
		switch r.Type {
		case ExitRuleTrailingStop:
			trailingPct = r.Param("pct", 0)
			hasTrailing = true
		case ExitRuleMaxLoss:
			maxLossPct = r.Param("pct", 0)
			hasMaxLoss = true
		}
	}

	if hasTrailing && hasMaxLoss && trailingPct > 0 && maxLossPct > 0 {
		if trailingPct >= maxLossPct {
			return fmt.Errorf(
				"TRAILING_STOP pct (%.4f) must be less than MAX_LOSS pct (%.4f); "+
					"trailing stop is dead code when >= max_loss (MAX_LOSS always fires first when HWM ≈ entry)",
				trailingPct, maxLossPct)
		}
	}

	return nil
}

// ExitTriggered is the event payload when a position monitor exit condition fires.
type ExitTriggered struct {
	Symbol       Symbol       `json:"symbol"`
	Rule         ExitRuleType `json:"rule"`
	Reason       string       `json:"reason"`
	CurrentPrice float64      `json:"currentPrice"`
	EntryPrice   float64      `json:"entryPrice"`
	Strategy     string       `json:"strategy"`
	TenantID     string       `json:"tenantId"`
	EnvMode      EnvMode      `json:"envMode"`
}
