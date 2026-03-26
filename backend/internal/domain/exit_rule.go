package domain

import (
	"fmt"
	"strings"
	"time"
)

// ExitRuleType identifies the kind of exit condition for active position monitoring.
type ExitRuleType string

const (
	ExitRuleTrailingStop   ExitRuleType = "TRAILING_STOP"
	ExitRuleProfitTarget   ExitRuleType = "PROFIT_TARGET"
	ExitRuleTimeExit       ExitRuleType = "TIME_EXIT"
	ExitRuleEODFlatten     ExitRuleType = "EOD_FLATTEN"
	ExitRuleMaxHoldingTime ExitRuleType = "MAX_HOLDING_TIME"
	ExitRuleMaxLoss        ExitRuleType = "MAX_LOSS"
	ExitRuleVolatilityStop ExitRuleType = "VOLATILITY_STOP"
	ExitRuleSDTarget       ExitRuleType = "SD_TARGET"
	ExitRuleStepStop       ExitRuleType = "STEP_STOP"
	ExitRuleStagnationExit ExitRuleType = "STAGNATION_EXIT"
	ExitRuleBreakevenStop  ExitRuleType = "BREAKEVEN_STOP"
	ExitRuleDTEFloor       ExitRuleType = "DTE_FLOOR"
	ExitRuleExpiryWatch    ExitRuleType = "EXPIRY_WATCH"
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
		ExitRuleDTEFloor, ExitRuleExpiryWatch:
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
	ExitOrderID      string       // broker order ID of the active exit order (for cancel-and-chase)
	ExitRetryCount   int          // number of exit attempts; used to escalate price aggressiveness
	EntryThesis      *EntryThesis // nil if no AI enrichment was available at entry

	LastRevaluation   *RiskRevaluation `json:"lastRevaluation,omitempty"`
	LastRevaluationAt time.Time        `json:"lastRevaluationAt,omitempty"`

	InstrumentType InstrumentType `json:"instrumentType,omitempty"`
	OptionExpiry   time.Time      `json:"optionExpiry,omitempty"`
	OptionRight    string         `json:"optionRight,omitempty"`

	CustomState map[string]float64 `json:"customState,omitempty"`
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
		Symbol:           symbol,
		EntryPrice:       entryPrice,
		EntryTime:        entryTime,
		HighWaterMark:    entryPrice,
		LowWaterMark:     entryPrice,
		Strategy:         strategy,
		AssetClass:       assetClass,
		ExitRules:        exitRules,
		InitialExitRules: initialRules,
		TenantID:         tenantID,
		EnvMode:          envMode,
		Quantity:         quantity,
		CustomState:      make(map[string]float64),
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

// IsShort returns true if this is a short position (sell side).
func (mp *MonitoredPosition) IsShort() bool {
	return strings.EqualFold(mp.Side, "SELL")
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
