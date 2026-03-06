package positionmonitor

import (
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// Evaluate dispatches to the appropriate exit rule evaluator.
// Returns (triggered bool, reason string).
// All evaluators are pure functions — no side effects, no I/O.
func Evaluate(rule domain.ExitRule, pos *domain.MonitoredPosition, currentPrice float64, now time.Time) (bool, string) {
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

// etLocation returns the America/New_York timezone.
func etLocation() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}
