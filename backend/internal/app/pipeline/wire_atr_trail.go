package pipeline

import (
	"github.com/oh-my-opentrade/backend/internal/config"
)

// PosMonitorATRTrailSetter is the slice of *positionmonitor.Service surface
// that ATR-trail-config wiring needs.
type PosMonitorATRTrailSetter interface {
	SetATRTrailConfig(
		enabled bool,
		atrPeriod, lookbackDays, lookbackDaysCrypto, minHistoryDays int,
		tercileLow, tercileHigh, insufficientHistMult float64,
		tercileMultipliers []float64,
	)
}

// WireATRTrailConfig wires the ATR-bucketed PREMIUM_TRAIL multiplier
// (2026-04-16 MRVL/SOXL premature-exit fix) into the position monitor.
// Positions are stamped with `atr_trail_mult` in CustomState at fill
// time; the tick loop reads it to scale premium-trail thresholds.
//
// Wired identically across all modes from the same config source so
// backtest and omo-replay see the same exit behavior as live (closes
// #40). The config's Enabled flag is the operator kill-switch — passing
// the config with Enabled=false is the documented no-op path that
// preserves byte-identical behavior via the EvalContext default of 1.0.
func (p *Pipeline) WireATRTrailConfig(posMon PosMonitorATRTrailSetter, cfg config.ATRTrailConfig) {
	posMon.SetATRTrailConfig(
		cfg.Enabled,
		cfg.ATRPeriod,
		cfg.ATRLookbackDays,
		cfg.ATRLookbackDaysCrypto,
		cfg.MinHistoryDays,
		cfg.TercileLowPctile,
		cfg.TercileHighPctile,
		cfg.InsufficientHistoryMultiplier,
		cfg.TercileMultipliers,
	)
}
