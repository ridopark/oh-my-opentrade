package positionmonitor

import (
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// EvalContext carries indicator data and mutable position state into exit rule
// evaluators. Existing evaluators ignore these fields; future evaluators
// (VOLATILITY_STOP, SD_TARGET, STEP_STOP, STAGNATION_EXIT) will consume them.
type EvalContext struct {
	// ATR is the latest Average True Range (period-14) value computed on 1m bar close.
	// Zero during warmup (< 15 bars) — evaluators must guard against this.
	ATR float64

	// VWAPValue is the current session VWAP price level.
	VWAPValue float64

	// SDBands maps standard-deviation multipliers to their absolute price levels.
	// e.g. {1.0: 151.20, 2.0: 152.40, 2.5: 153.00} for a VWAP of 150.00.
	// Nil or empty during warmup.
	SDBands map[float64]float64
}

// Evaluate dispatches to the appropriate exit rule evaluator.
// Returns (triggered bool, reason string).
// All evaluators are pure functions — no side effects, no I/O.
func Evaluate(rule domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64, now time.Time, ctx EvalContext) (bool, string) {
	switch rule.Type {
	case domain.ExitRuleTrailingStop:
		return evaluateTrailingStop(rule, pos, currentPrice)
	case domain.ExitRuleProfitTarget:
		return evaluateProfitTarget(rule, pos, currentPrice)
	case domain.ExitRuleTimeExit:
		return evaluateTimeExit(rule, pos, now)
	case domain.ExitRuleEODFlatten:
		return evaluateEODFlatten(rule, pos, now)
	case domain.ExitRuleMaxHoldingTime:
		return evaluateMaxHoldingTime(rule, pos, now)
	case domain.ExitRuleMaxLoss:
		return evaluateMaxLoss(rule, pos, currentPrice)
	case domain.ExitRuleVolatilityStop:
		return evaluateVolatilityStop(rule, pos, currentPrice, ctx)
	case domain.ExitRuleSDTarget:
		return evaluateSDTarget(rule, pos, currentPrice, ctx, now)
	case domain.ExitRuleStepStop:
		return evaluateStepStop(rule, pos, currentPrice, ctx)
	case domain.ExitRuleStagnationExit:
		return evaluateStagnationExit(rule, pos, currentPrice, now, ctx)
	case domain.ExitRuleBreakevenStop:
		return evaluateBreakevenStop(rule, pos, currentPrice)
	case domain.ExitRuleDTEFloor:
		return evaluateDTEFloor(rule, pos, now)
	case domain.ExitRuleExpiryWatch:
		return evaluateExpiryWatch(rule, pos, now)
	default:
		return false, ""
	}
}

// evaluateTrailingStop triggers when drawdown from high-water mark exceeds the threshold.
//
// Params:
//
//	"pct" — trailing stop percentage as a decimal (e.g. 0.02 = 2%)
func evaluateTrailingStop(rule domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64) (bool, string) {
	pct := rule.Param("pct", 0)
	if pct <= 0 {
		return false, ""
	}

	drawdown := pos.DrawdownFromHighPct(currentPrice)
	if drawdown >= pct {
		return true, fmt.Sprintf("trailing_stop: drawdown %.2f%% >= threshold %.2f%% (high=%.4f, current=%.4f)",
			drawdown*100, pct*100, pos.HighWaterMark, currentPrice)
	}
	return false, ""
}

// evaluateProfitTarget triggers when unrealized P&L exceeds the target.
//
// Params:
//
//	"pct" — profit target as a decimal (e.g. 0.03 = 3%)
func evaluateProfitTarget(rule domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64) (bool, string) {
	pct := rule.Param("pct", 0)
	if pct <= 0 {
		return false, ""
	}

	pnl := pos.UnrealizedPnLPct(currentPrice)
	if pnl >= pct {
		return true, fmt.Sprintf("profit_target: pnl %.2f%% >= target %.2f%% (entry=%.4f, current=%.4f)",
			pnl*100, pct*100, pos.EntryPrice, currentPrice)
	}
	return false, ""
}

// evaluateTimeExit triggers at a specific time of day (RTH-aware).
//
// Params:
//
//	"hour"   — exit hour in ET (e.g. 15 for 3:00 PM ET)
//	"minute" — exit minute in ET (e.g. 45 for XX:45)
func evaluateTimeExit(rule domain.ExitRule, pos *domain.MonitoredPosition, now time.Time) (bool, string) {
	hour := int(rule.Param("hour", 0))
	minute := int(rule.Param("minute", 0))
	if hour == 0 && minute == 0 {
		return false, ""
	}

	loc := etLocation()
	nowET := now.In(loc)

	// Only trigger on the same trading day as entry (or any day if the position spans days).
	if nowET.Hour() > hour || (nowET.Hour() == hour && nowET.Minute() >= minute) {
		return true, fmt.Sprintf("time_exit: current %02d:%02d ET >= threshold %02d:%02d ET",
			nowET.Hour(), nowET.Minute(), hour, minute)
	}
	return false, ""
}

// evaluateEODFlatten triggers N minutes before market close.
//
// Params:
//
//	"minutes_before_close" — minutes before session close to flatten (default: 5)
func evaluateEODFlatten(rule domain.ExitRule, pos *domain.MonitoredPosition, now time.Time) (bool, string) {
	// EOD_FLATTEN is not applicable to 24/7 markets (crypto has no session close).
	if pos.AssetClass == domain.AssetClassCrypto {
		return false, ""
	}

	minutesBefore := rule.Param("minutes_before_close", 5)
	if minutesBefore <= 0 {
		return false, ""
	}

	cal := domain.CalendarFor(pos.AssetClass)
	if !cal.IsOpen(now) {
		return false, ""
	}

	sessionClose := cal.SessionClose(now)
	flattenTime := sessionClose.Add(-time.Duration(minutesBefore) * time.Minute)

	if now.After(flattenTime) || now.Equal(flattenTime) {
		return true, fmt.Sprintf("eod_flatten: %s is within %.0f minutes of session close %s",
			now.Format("15:04:05"), minutesBefore, sessionClose.Format("15:04:05"))
	}
	return false, ""
}

// evaluateMaxHoldingTime triggers when the position has been held longer than the threshold.
//
// Params:
//
//	"minutes" — maximum holding time in minutes
func evaluateMaxHoldingTime(rule domain.ExitRule, pos *domain.MonitoredPosition, now time.Time) (bool, string) {
	maxMinutes := rule.Param("minutes", 0)
	if maxMinutes <= 0 {
		return false, ""
	}

	held := now.Sub(pos.EntryTime).Minutes()
	if held >= maxMinutes {
		return true, fmt.Sprintf("max_holding_time: held %.1f min >= limit %.1f min",
			held, maxMinutes)
	}
	return false, ""
}

// evaluateMaxLoss triggers when unrealized loss exceeds the threshold.
//
// Params:
//
//	"pct" — maximum loss percentage as a decimal (e.g. 0.02 = 2%)
func evaluateMaxLoss(rule domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64) (bool, string) {
	pct := rule.Param("pct", 0)
	if pct <= 0 {
		return false, ""
	}

	pnl := pos.UnrealizedPnLPct(currentPrice)
	// pnl is negative when losing money.
	if pnl <= -pct {
		return true, fmt.Sprintf("max_loss: loss %.2f%% >= limit %.2f%% (entry=%.4f, current=%.4f)",
			-pnl*100, pct*100, pos.EntryPrice, currentPrice)
	}
	return false, ""
}

// evaluateVolatilityStop triggers when price drops below high-water mark minus ATR × multiplier.
// This is a true trailing stop that uses the highest price reached, not entry price.
//
// Params:
//
//	"atr_multiplier" — multiplier for ATR distance (e.g. 1.5 = stop at hwm - 1.5*ATR)
func evaluateVolatilityStop(rule domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64, ctx EvalContext) (bool, string) {
	mult := rule.Param("atr_multiplier", 0)
	if mult <= 0 {
		return false, ""
	}
	if ctx.ATR <= 0 {
		return false, ""
	}

	stopPrice := pos.HighWaterMark - (ctx.ATR * mult)
	if currentPrice <= stopPrice {
		return true, fmt.Sprintf("volatility_stop: price %.4f <= stop %.4f (hwm=%.4f, ATR=%.6f, mult=%.1f)",
			currentPrice, stopPrice, pos.HighWaterMark, ctx.ATR, mult)
	}
	return false, ""
}

// evaluateSDTarget triggers when price reaches the VWAP + sd_level × SD band.
// For long positions, this is a profit target when price rises to the upper band.
//
// Params:
//
//	"sd_level" — SD multiplier for the target band (e.g. 2.0 = VWAP + 2.0*SD)
func evaluateSDTarget(rule domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64, ctx EvalContext, now time.Time) (bool, string) {
	if now.Sub(pos.EntryTime) < 2*time.Minute {
		return false, ""
	}
	sdLevel := rule.Param("sd_level", 0)
	if sdLevel <= 0 {
		return false, ""
	}
	if len(ctx.SDBands) == 0 {
		return false, ""
	}

	targetPrice, ok := ctx.SDBands[sdLevel]
	if !ok {
		return false, ""
	}

	if currentPrice >= targetPrice {
		return true, fmt.Sprintf("sd_target: price %.4f >= +%.1f SD band %.4f (vwap=%.4f)",
			currentPrice, sdLevel, targetPrice, ctx.VWAPValue)
	}
	return false, ""
}

// evaluateStepStop triggers when price drops below a dynamically ratcheted stop level.
// The stop level is set by the tick loop in service.go (NOT here) based on SD bands crossed.
// This evaluator only READS pos.CustomState["step_stop_level"] — it never mutates state.
//
// Params: none (stop level comes from CustomState, set by tick loop)
func evaluateStepStop(_ domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64, ctx EvalContext) (bool, string) {
	if pos.CustomState == nil {
		return false, ""
	}
	stopLevel := pos.CustomState["step_stop_level"]
	if stopLevel <= 0 {
		return false, ""
	}

	if currentPrice <= stopLevel {
		highestSD := pos.CustomState["highest_sd_crossed"]
		return true, fmt.Sprintf("step_stop: price %.4f <= stop %.4f (highest_sd=+%.1f, vwap=%.4f)",
			currentPrice, stopLevel, highestSD, ctx.VWAPValue)
	}
	return false, ""
}

// UpdateStepStopState ratchets the step-stop level based on SD band crossings.
// Called from the tick loop BEFORE exit rule evaluation. Mutation is intentionally
// separated from the evaluator to maintain evaluator purity (Metis directive).
//
// Logic:
//
//	Price crosses +1.0 SD → stop = entry price (breakeven)
//	Price crosses +2.0 SD → stop = +1.0 SD band price
//	Price crosses +3.0 SD → stop = +2.0 SD band price
//
// The stop only ratchets UP (tightens), never down.
//
// minHoldBars suppresses ratcheting until at least that many 1-minute bars have
// elapsed since entry. This prevents an instant stop-out when the entry price is
// near a SD band and the stop would fire on the very first tick.
// Pass 0 (or a negative value) to disable the hold guard.
func UpdateStepStopState(pos *domain.MonitoredPosition, currentPrice float64, ctx EvalContext, now time.Time, minHoldBars float64) {
	if pos.CustomState == nil || len(ctx.SDBands) == 0 {
		return
	}

	// Honor min_hold_bars: suppress step-stop ratcheting until N 1-minute bars
	// have elapsed since entry.
	if minHoldBars > 0 && now.Sub(pos.EntryTime) < time.Duration(float64(time.Minute)*minHoldBars) {
		return
	}

	levels := []float64{3.0, 2.5, 2.0, 1.5, 1.0}
	prevHighest := pos.CustomState["highest_sd_crossed"]

	// Find the highest SD level crossed this tick (descending scan, first match wins)
	newHighest := prevHighest
	for _, level := range levels {
		bandPrice, ok := ctx.SDBands[level]
		if !ok {
			continue
		}
		if currentPrice >= bandPrice && level > newHighest {
			newHighest = level
			break
		}
	}

	if newHighest <= prevHighest {
		return
	}

	pos.CustomState["highest_sd_crossed"] = newHighest

	var newStop float64
	if newHighest <= 1.0 {
		newStop = pos.EntryPrice
	} else {
		lockLevel := newHighest - 1.0
		if lockPrice, exists := ctx.SDBands[lockLevel]; exists {
			newStop = lockPrice
		} else {
			newStop = pos.EntryPrice
		}
	}

	if newStop > pos.CustomState["step_stop_level"] {
		pos.CustomState["step_stop_level"] = newStop
	}
}

// evaluateStagnationExit triggers when a position fails to reach the target SD band
// within a time limit. Disabled once step-stop has activated (highest_sd_crossed > 0).
//
// Params:
//
//	"minutes"      — max minutes from entry before stagnation exit (e.g. 30)
//	"sd_threshold" — SD level that must be reached to avoid exit (default: 1.0)
func evaluateStagnationExit(rule domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64, now time.Time, ctx EvalContext) (bool, string) {
	minutes := rule.Param("minutes", 0)
	if minutes <= 0 {
		return false, ""
	}

	if pos.CustomState != nil && pos.CustomState["highest_sd_crossed"] > 0 {
		return false, ""
	}

	held := now.Sub(pos.EntryTime).Minutes()
	if held < minutes {
		return false, ""
	}

	sdThreshold := rule.Param("sd_threshold", 1.0)
	if len(ctx.SDBands) > 0 {
		if bandPrice, ok := ctx.SDBands[sdThreshold]; ok && currentPrice >= bandPrice {
			return false, ""
		}
	}

	// Profit gate: if the position is profitable beyond the threshold,
	// skip the stagnation exit and let the trailing stop protect gains.
	profitGatePct := rule.Param("profit_gate_pct", 0)
	if profitGatePct > 0 && pos.EntryPrice > 0 {
		pnlPct := (currentPrice - pos.EntryPrice) / pos.EntryPrice
		if pnlPct > profitGatePct {
			return false, ""
		}
	}

	return true, fmt.Sprintf("stagnation_exit: held %.1f min >= limit %.0f min without reaching +%.1f SD (price=%.4f, vwap=%.4f)",
		held, minutes, sdThreshold, currentPrice, ctx.VWAPValue)
}

// UpdateBreakevenStopState activates the breakeven stop once unrealized P&L
// crosses the activation threshold. Once activated, the stop level is fixed at
// entry price + buffer. Called from the tick loop BEFORE exit rule evaluation.
//
// Params (from rule):
//
//	"activation_pct" — P&L percentage that triggers activation (e.g. 0.003 = 0.3%)
//	"buffer_pct"     — buffer above entry as a decimal (e.g. 0.0005 = 0.05%)
func UpdateBreakevenStopState(pos *domain.MonitoredPosition, currentPrice float64, activationPct, bufferPct float64) {
	if pos.CustomState == nil || activationPct <= 0 {
		return
	}
	// Once activated, the stop level is locked — never re-calculate.
	if pos.CustomState["breakeven_activated"] > 0 {
		return
	}
	pnlPct := pos.UnrealizedPnLPct(currentPrice)
	if pnlPct >= activationPct {
		pos.CustomState["breakeven_activated"] = 1
		pos.CustomState["breakeven_stop_level"] = pos.EntryPrice * (1 + bufferPct)
	}
}

// evaluateBreakevenStop triggers when price drops below the breakeven stop level.
// The stop level is set by UpdateBreakevenStopState — this evaluator only reads state.
//
// Params: none (stop level comes from CustomState, set by tick loop)
func evaluateBreakevenStop(_ domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64) (bool, string) {
	if pos.CustomState == nil {
		return false, ""
	}
	if pos.CustomState["breakeven_activated"] == 0 {
		return false, ""
	}
	stopLevel := pos.CustomState["breakeven_stop_level"]
	if stopLevel <= 0 {
		return false, ""
	}
	if currentPrice <= stopLevel {
		return true, fmt.Sprintf("breakeven_stop: price %.4f <= stop %.4f (entry=%.4f, buffer=%.4f)",
			currentPrice, stopLevel, pos.EntryPrice, stopLevel-pos.EntryPrice)
	}
	return false, ""
}

func evaluateDTEFloor(rule domain.ExitRule, pos *domain.MonitoredPosition, now time.Time) (bool, string) {
	if pos.InstrumentType != domain.InstrumentTypeOption || pos.OptionExpiry.IsZero() {
		return false, ""
	}
	floor := int(rule.Param("dte", 7))
	if floor <= 0 {
		return false, ""
	}
	dte := int(pos.OptionExpiry.Sub(now).Hours() / 24)
	if dte <= floor {
		return true, fmt.Sprintf("dte_floor: %d days to expiry <= floor %d (expiry=%s)",
			dte, floor, pos.OptionExpiry.Format("2006-01-02"))
	}
	return false, ""
}

func evaluateExpiryWatch(rule domain.ExitRule, pos *domain.MonitoredPosition, now time.Time) (bool, string) {
	if pos.InstrumentType != domain.InstrumentTypeOption || pos.OptionExpiry.IsZero() {
		return false, ""
	}
	pctElapsed := rule.Param("pct_elapsed", 0.5)
	if pctElapsed <= 0 || pctElapsed > 1 {
		return false, ""
	}
	totalDuration := pos.OptionExpiry.Sub(pos.EntryTime)
	if totalDuration <= 0 {
		return false, ""
	}
	elapsed := now.Sub(pos.EntryTime)
	ratio := elapsed.Seconds() / totalDuration.Seconds()
	if ratio >= pctElapsed {
		dte := int(pos.OptionExpiry.Sub(now).Hours() / 24)
		return true, fmt.Sprintf("expiry_watch: %.0f%% of contract duration elapsed (%d DTE remaining, threshold %.0f%%)",
			ratio*100, dte, pctElapsed*100)
	}
	return false, ""
}

func etLocation() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}
