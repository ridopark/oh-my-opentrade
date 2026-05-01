// Package indicator owns the canonical technical-indicator calculator
// shared by monitor, strategy runner, and activation paths. A
// sync.RWMutex guards the wrapped calc and the last-snapshot map;
// LastSnapshot takes the read lock, Update and WarmUp take the write
// lock. Last-snapshot bookkeeping lives in this package because the
// per-key state on monitor.IndicatorCalculator is unexported.
package indicator
