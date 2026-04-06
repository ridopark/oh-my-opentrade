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

	BarDuration time.Duration
}

// Evaluate dispatches to the appropriate exit rule evaluator.
// Returns (triggered bool, reason string).
// All evaluators are pure functions — no side effects, no I/O.
func Evaluate(rule domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64, now time.Time, ctx EvalContext) (bool, string) {
	switch rule.Type {
	case domain.ExitRuleTrailingStop:
		return evaluateTrailingStop(rule, pos, currentPrice)
	case domain.ExitRuleSwingStop:
		return evaluateSwingStop(rule, pos, currentPrice, ctx)
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
		return evaluateVolatilityStop(rule, pos, currentPrice, ctx, now)
	case domain.ExitRuleSDTarget:
		return evaluateSDTarget(rule, pos, currentPrice, ctx, now)
	case domain.ExitRuleStepStop:
		return evaluateStepStop(rule, pos, currentPrice, ctx, now)
	case domain.ExitRuleStagnationExit:
		return evaluateStagnationExit(rule, pos, currentPrice, now, ctx)
	case domain.ExitRuleBreakevenStop:
		return evaluateBreakevenStop(rule, pos, currentPrice)
	case domain.ExitRuleDTEFloor:
		return evaluateDTEFloor(rule, pos, now)
	case domain.ExitRuleExpiryWatch:
		return evaluateExpiryWatch(rule, pos, now)
	case domain.ExitRuleTieredTP:
		return evaluateTieredTP(rule, pos, currentPrice)
	case domain.ExitRuleTimePartial:
		return evaluateTimePartial(rule, pos, currentPrice, now)
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

// evaluateSwingStop implements a price-action trailing stop using recent swing lows/highs.
// Per Brian Shannon: place stop at the low of the dip, trail using higher lows.
// Uses CustomState ring buffer to track recent prices.
//
// Buffer is ATR-based when atr_buffer_mult > 0 and ATR is available (from EvalContext).
// Falls back to fixed buffer_bps during warmup (ATR=0) or when atr_buffer_mult is not set.
//
// Params:
//
//	"lookback"        — bars to look back for swing low/high (default 5)
//	"buffer_bps"      — fallback buffer in bps when ATR unavailable (default 10)
//	"atr_buffer_mult" — ATR multiplier for dynamic buffer (default 0 = disabled)
//	"min_bars"        — min bars before stop activates (default 1)
func evaluateSwingStop(rule domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64, ctx EvalContext) (bool, string) {
	if pos.CustomState == nil {
		pos.CustomState = make(map[string]float64)
	}

	lookback := int(rule.Param("lookback", 5))
	if lookback < 2 {
		lookback = 5
	}
	minBars := int(rule.Param("min_bars", 1))

	// Dynamic buffer: prefer ATR-based, fall back to fixed bps during warmup
	atrMult := rule.Param("atr_buffer_mult", 0)
	bufferBPS := rule.Param("buffer_bps", 10)
	var bufferAbs float64
	if atrMult > 0 && ctx.ATR > 0 {
		bufferAbs = ctx.ATR * atrMult
	} else {
		bufferAbs = currentPrice * bufferBPS / 10000.0
	}

	pos.CustomState["swing_trail_bars"]++
	barCount := int(pos.CustomState["swing_trail_bars"])

	ringIdx := int(pos.CustomState["swing_ring_idx"])
	if pos.IsShort() {
		pos.CustomState[fmt.Sprintf("swing_high_%d", ringIdx)] = currentPrice
	} else {
		pos.CustomState[fmt.Sprintf("swing_low_%d", ringIdx)] = currentPrice
	}
	pos.CustomState["swing_ring_idx"] = float64((ringIdx + 1) % lookback)

	if barCount < minBars {
		return false, ""
	}

	stopLevel := pos.CustomState["swing_trail_stop"]

	if pos.IsShort() {
		swingHigh := 0.0
		for i := 0; i < lookback; i++ {
			h := pos.CustomState[fmt.Sprintf("swing_high_%d", i)]
			if h > swingHigh {
				swingHigh = h
			}
		}
		if swingHigh > 0 {
			newStop := swingHigh + bufferAbs
			if stopLevel == 0 || newStop < stopLevel {
				stopLevel = newStop
				pos.CustomState["swing_trail_stop"] = stopLevel
			}
		}
		if stopLevel > 0 && currentPrice >= stopLevel {
			return true, fmt.Sprintf("swing_stop: price %.4f >= stop %.4f (swing_high=%.4f, buffer=%.2f, atr=%.2f)",
				currentPrice, stopLevel, swingHigh, bufferAbs, ctx.ATR)
		}
	} else {
		swingLow := 0.0
		for i := 0; i < lookback; i++ {
			l := pos.CustomState[fmt.Sprintf("swing_low_%d", i)]
			if l > 0 && (swingLow == 0 || l < swingLow) {
				swingLow = l
			}
		}
		if swingLow > 0 {
			newStop := swingLow - bufferAbs
			if newStop > stopLevel {
				stopLevel = newStop
				pos.CustomState["swing_trail_stop"] = stopLevel
			}
		}
		if stopLevel > 0 && currentPrice <= stopLevel {
			return true, fmt.Sprintf("swing_stop: price %.4f <= stop %.4f (swing_low=%.4f, buffer=%.2f, atr=%.2f)",
				currentPrice, stopLevel, swingLow, bufferAbs, ctx.ATR)
		}
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

	// For options: EntryPrice is set to the UNDERLYING price by the position
	// monitor. So UnrealizedPnLPct computes based on the underlying move,
	// which is correct for triggering a stop on adverse underlying movement.
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
func evaluateVolatilityStop(rule domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64, ctx EvalContext, now time.Time) (bool, string) {
	graceDur := resolveGracePeriod(rule, ctx.BarDuration)
	if now.Sub(pos.EntryTime) < graceDur {
		return false, ""
	}

	mult := rule.Param("atr_multiplier", 0)
	if mult <= 0 {
		return false, ""
	}
	if ctx.ATR <= 0 {
		return false, ""
	}

	if pos.IsShort() {
		stopPrice := pos.LowWaterMark + (ctx.ATR * mult)
		if currentPrice >= stopPrice {
			return true, fmt.Sprintf("volatility_stop: price %.4f >= stop %.4f (lwm=%.4f, ATR=%.6f, mult=%.1f)",
				currentPrice, stopPrice, pos.LowWaterMark, ctx.ATR, mult)
		}
	} else {
		stopPrice := pos.HighWaterMark - (ctx.ATR * mult)
		if currentPrice <= stopPrice {
			return true, fmt.Sprintf("volatility_stop: price %.4f <= stop %.4f (hwm=%.4f, ATR=%.6f, mult=%.1f)",
				currentPrice, stopPrice, pos.HighWaterMark, ctx.ATR, mult)
		}
	}
	return false, ""
}

// evaluateSDTarget triggers when price reaches the VWAP + sd_level × SD band.
// For long positions, this is a profit target when price rises to the upper band.
//
// Params:
//
//	"sd_level" — SD multiplier for the target band (e.g. 2.0 = VWAP + 2.0*SD)
func resolveGracePeriod(rule domain.ExitRule, ctxBarDur time.Duration) time.Duration {
	barMinutes := rule.Param("bar_minutes", 0)
	var barDur time.Duration
	switch {
	case barMinutes > 0:
		barDur = time.Duration(barMinutes) * time.Minute
	case ctxBarDur > 0:
		barDur = ctxBarDur
	default:
		barDur = time.Minute
	}
	minHoldBars := rule.Param("min_hold_bars", 0)
	if minHoldBars <= 0 {
		minHoldBars = 2
	}
	return time.Duration(minHoldBars) * barDur
}

func evaluateSDTarget(rule domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64, ctx EvalContext, now time.Time) (bool, string) {
	if now.Sub(pos.EntryTime) < resolveGracePeriod(rule, ctx.BarDuration) {
		return false, ""
	}
	sdLevel := rule.Param("sd_level", 0)
	if sdLevel <= 0 {
		return false, ""
	}
	if len(ctx.SDBands) == 0 {
		return false, ""
	}

	upperBand, ok := ctx.SDBands[sdLevel]
	if !ok {
		return false, ""
	}

	// For shorts (equity short or long put), target the lower band.
	if pos.IsShort() {
		// Mirror the upper band around VWAP to get the lower band
		lowerBand := 2*ctx.VWAPValue - upperBand
		if currentPrice <= lowerBand {
			return true, fmt.Sprintf("sd_target: price %.4f <= -%.1f SD band %.4f (vwap=%.4f)",
				currentPrice, sdLevel, lowerBand, ctx.VWAPValue)
		}
	} else if currentPrice >= upperBand {
		return true, fmt.Sprintf("sd_target: price %.4f >= +%.1f SD band %.4f (vwap=%.4f)",
			currentPrice, sdLevel, upperBand, ctx.VWAPValue)
	}
	return false, ""
}

// evaluateStepStop triggers when price drops below a dynamically ratcheted stop level.
// The stop level is set by the tick loop in service.go (NOT here) based on SD bands crossed.
// This evaluator only READS pos.CustomState["step_stop_level"] — it never mutates state.
//
// Params: none (stop level comes from CustomState, set by tick loop)
func evaluateStepStop(rule domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64, ctx EvalContext, now time.Time) (bool, string) {
	graceDur := resolveGracePeriod(rule, ctx.BarDuration)
	if now.Sub(pos.EntryTime) < graceDur {
		return false, ""
	}

	if pos.CustomState == nil {
		return false, ""
	}
	stopLevel := pos.CustomState["step_stop_level"]
	if stopLevel <= 0 {
		return false, ""
	}

	if pos.IsShort() {
		if currentPrice >= stopLevel {
			lowestSD := pos.CustomState["lowest_sd_crossed"]
			return true, fmt.Sprintf("step_stop: price %.4f >= stop %.4f (lowest_sd=-%.1f, vwap=%.4f)",
				currentPrice, stopLevel, lowestSD, ctx.VWAPValue)
		}
	} else {
		if currentPrice <= stopLevel {
			highestSD := pos.CustomState["highest_sd_crossed"]
			return true, fmt.Sprintf("step_stop: price %.4f <= stop %.4f (highest_sd=+%.1f, vwap=%.4f)",
				currentPrice, stopLevel, highestSD, ctx.VWAPValue)
		}
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

	if pos.IsShort() {
		updateStepStopShort(pos, currentPrice, ctx)
	} else {
		updateStepStopLong(pos, currentPrice, ctx)
	}
}

// updateStepStopLong ratchets the step-stop level UP as price crosses higher SD bands.
func updateStepStopLong(pos *domain.MonitoredPosition, currentPrice float64, ctx EvalContext) {
	levels := []float64{3.0, 2.5, 2.0, 1.5, 1.0}
	prevHighest := pos.CustomState["highest_sd_crossed"]

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
		// Breakeven with buffer — 0.15% below entry to avoid slippage stop-outs
		newStop = pos.EntryPrice * 0.9985
	} else {
		lockLevel := newHighest - 1.0
		if lockPrice, exists := ctx.SDBands[lockLevel]; exists {
			newStop = lockPrice
		} else {
			newStop = pos.EntryPrice * 0.9985
		}
	}

	if newStop > pos.CustomState["step_stop_level"] {
		pos.CustomState["step_stop_level"] = newStop
	}
}

// updateStepStopShort ratchets the step-stop level DOWN as price crosses lower SD bands.
// Lower SD bands are mirrored from upper bands: lowerBand = 2*VWAP - upperBand.
func updateStepStopShort(pos *domain.MonitoredPosition, currentPrice float64, ctx EvalContext) {
	levels := []float64{3.0, 2.5, 2.0, 1.5, 1.0}
	prevLowest := pos.CustomState["lowest_sd_crossed"]

	// Compute lower bands by mirroring upper bands around VWAP
	lowerBands := make(map[float64]float64, len(ctx.SDBands))
	for level, upperPrice := range ctx.SDBands {
		lowerBands[level] = 2*ctx.VWAPValue - upperPrice
	}

	newLowest := prevLowest
	for _, level := range levels {
		bandPrice, ok := lowerBands[level]
		if !ok {
			continue
		}
		if currentPrice <= bandPrice && level > newLowest {
			newLowest = level
			break
		}
	}

	if newLowest <= prevLowest {
		return
	}

	pos.CustomState["lowest_sd_crossed"] = newLowest

	// For shorts, stop is set ABOVE entry (protecting against adverse moves up).
	// -1.0 SD crossed → stop = entry + 0.15% buffer (breakeven with room)
	// -2.0 SD crossed → stop = -1.0 SD band (lock in profit at -1 SD)
	var newStop float64
	if newLowest <= 1.0 {
		// Breakeven with buffer — 0.15% above entry to avoid slippage stop-outs
		newStop = pos.EntryPrice * 1.0015
	} else {
		lockLevel := newLowest - 1.0
		if lockPrice, exists := lowerBands[lockLevel]; exists {
			newStop = lockPrice
		} else {
			newStop = pos.EntryPrice * 1.0015
		}
	}

	// For shorts, stop ratchets DOWN (tighter = lower price that locks more profit)
	currentStop := pos.CustomState["step_stop_level"]
	if currentStop <= 0 || newStop < currentStop {
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
	// Re-enabled for options — without this, losing options trades have no
	// time-based exit and bleed until EOD. The profit gate uses underlying P&L%
	// which is appropriate since pos.EntryPrice is the underlying price.

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
		if bandPrice, ok := ctx.SDBands[sdThreshold]; ok {
			if pos.IsShort() {
				// For shorts, check if price reached the lower band
				lowerBand := 2*ctx.VWAPValue - bandPrice
				if currentPrice <= lowerBand {
					return false, ""
				}
			} else if currentPrice >= bandPrice {
				return false, ""
			}
		}
	}

	// Profit gate: if the position is profitable beyond the threshold,
	// skip the stagnation exit and let the trailing stop protect gains.
	profitGatePct := rule.Param("profit_gate_pct", 0)
	if profitGatePct > 0 && pos.EntryPrice > 0 {
		pnlPct := pos.UnrealizedPnLPct(currentPrice)
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
		if pos.IsShort() {
			// For shorts, breakeven stop is slightly BELOW entry (adverse = price going up).
			// bufferPct gives us a small profit cushion: stop = entry * (1 - buffer).
			pos.CustomState["breakeven_stop_level"] = pos.EntryPrice * (1 - bufferPct)
		} else {
			pos.CustomState["breakeven_stop_level"] = pos.EntryPrice * (1 + bufferPct)
		}
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
	if pos.IsShort() {
		if currentPrice >= stopLevel {
			return true, fmt.Sprintf("breakeven_stop: price %.4f >= stop %.4f (entry=%.4f, short)",
				currentPrice, stopLevel, pos.EntryPrice)
		}
	} else {
		if currentPrice <= stopLevel {
			return true, fmt.Sprintf("breakeven_stop: price %.4f <= stop %.4f (entry=%.4f, buffer=%.4f)",
				currentPrice, stopLevel, pos.EntryPrice, stopLevel-pos.EntryPrice)
		}
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

// evaluateTieredTP implements a two-tier take-profit with trailing stop on the remainder.
//
// Tier 1: Exit first_tier_pct (default 0.5) of position when PnL reaches first_tier_rr × R.
// Tier 2: After tier 1, trail the remainder at trail_pct from high-water mark.
//
// R (risk distance) is derived from initial_risk_pct param or CustomState["initial_stop_distance"].
//
// CustomState keys used:
//
//	"tiered_tp_tier1_taken"   — 1.0 after tier 1 fires
//	"tiered_tp_exit_qty_frac" — fraction of pos.Quantity to exit (read by triggerExit)
//	"tiered_tp_hwm"           — high-water mark for tier-2 trailing
func evaluateTieredTP(rule domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64) (bool, string) {
	firstTierPct := rule.Param("first_tier_pct", 0.5)
	firstTierRR := rule.Param("first_tier_rr", 1.5)
	trailPct := rule.Param("trail_pct", 0.02)
	initialRiskPct := rule.Param("initial_risk_pct", 0.005)

	if pos.EntryPrice == 0 || currentPrice == 0 {
		return false, ""
	}

	// Compute R (risk distance as fraction of entry price).
	riskPct := initialRiskPct
	if d := pos.CustomState["initial_stop_distance"]; d > 0 {
		riskPct = d
	}

	pnl := pos.UnrealizedPnLPct(currentPrice)
	tier1Taken := pos.CustomState["tiered_tp_tier1_taken"] != 0

	if !tier1Taken {
		// Tier 1: check if PnL reached first_tier_rr × R.
		target := firstTierRR * riskPct
		if pnl >= target {
			pos.CustomState["tiered_tp_exit_qty_frac"] = firstTierPct
			pos.CustomState["tiered_tp_tier1_taken"] = 1.0
			// Initialize HWM for tier-2 trailing.
			pos.CustomState["tiered_tp_hwm"] = currentPrice
			return true, fmt.Sprintf("tiered_tp_tier1: pnl %.2f%% >= %.1fR target %.2f%% — exiting %.0f%%",
				pnl*100, firstTierRR, target*100, firstTierPct*100)
		}
		return false, ""
	}

	// Tier 2: trailing stop on remainder.
	hwm := pos.CustomState["tiered_tp_hwm"]
	if pos.IsShort() {
		if currentPrice < hwm || hwm == 0 {
			hwm = currentPrice
			pos.CustomState["tiered_tp_hwm"] = hwm
		}
		trailLevel := hwm * (1.0 + trailPct)
		if currentPrice >= trailLevel {
			pos.CustomState["tiered_tp_exit_qty_frac"] = 1.0
			return true, fmt.Sprintf("tiered_tp_tier2_trail: price %.4f >= trail %.4f (lwm=%.4f, trail=%.1f%%)",
				currentPrice, trailLevel, hwm, trailPct*100)
		}
	} else {
		if currentPrice > hwm {
			hwm = currentPrice
			pos.CustomState["tiered_tp_hwm"] = hwm
		}
		trailLevel := hwm * (1.0 - trailPct)
		if currentPrice <= trailLevel {
			pos.CustomState["tiered_tp_exit_qty_frac"] = 1.0
			return true, fmt.Sprintf("tiered_tp_tier2_trail: price %.4f <= trail %.4f (hwm=%.4f, trail=%.1f%%)",
				currentPrice, trailLevel, hwm, trailPct*100)
		}
	}
	return false, ""
}

// evaluateTimePartial exits a fraction of the position after holding for N minutes while in profit.
//
// Params:
//
//	"minutes"        — minimum hold time before triggering (default 60)
//	"partial_pct"    — fraction of position to exit (default 0.5)
//	"min_profit_pct" — minimum unrealized PnL to trigger (default 0.001 = 0.1%)
//
// CustomState keys used:
//
//	"time_partial_taken"      — 1.0 after this rule fires (prevents re-triggering)
//	"time_partial_exit_qty_frac" — fraction to exit (read by triggerExit)
func evaluateTimePartial(rule domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64, now time.Time) (bool, string) {
	if pos.CustomState["time_partial_taken"] != 0 {
		return false, ""
	}

	minutes := rule.Param("minutes", 60)
	partialPct := rule.Param("partial_pct", 0.5)
	minProfitPct := rule.Param("min_profit_pct", 0.001)

	held := now.Sub(pos.EntryTime).Minutes()
	if held < minutes {
		return false, ""
	}

	pnl := pos.UnrealizedPnLPct(currentPrice)
	if pnl < minProfitPct {
		return false, ""
	}

	pos.CustomState["time_partial_exit_qty_frac"] = partialPct
	pos.CustomState["time_partial_taken"] = 1.0
	return true, fmt.Sprintf("time_partial: held %.0f min >= %.0f min, pnl %.2f%% >= %.2f%% — exiting %.0f%%",
		held, minutes, pnl*100, minProfitPct*100, partialPct*100)
}

func etLocation() *time.Location {
	return domain.NYLocation()
}

