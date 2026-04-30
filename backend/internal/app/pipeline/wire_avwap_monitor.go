package pipeline

import (
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// AVWAPMonitorWiring bundles the four functions and the anchor list
// the monitor needs for its standalone-AVWAP path. The bundle exists
// because all five setters must be called together to produce a
// working state — wiring three of four leaves the path silently
// degraded (e.g., resolved anchors but no prev-day bars to seed them).
//
// SessionRefresherFn may be nil on backtest/replay where session data
// is loaded once at run start and not refreshed mid-run; the monitor's
// SetSessionRefresherFn accepts nil.
type AVWAPMonitorWiring struct {
	AVWAPFn            func(symbol string) map[string]float64
	AnchorResolverFn   func(symbol string, barTime time.Time, anchors []string) map[string]time.Time
	SessionRefresherFn func(symbol string, barTime time.Time)
	PrevDayBarsFn      func(symbol string, since, until time.Time) []start.Bar
	Anchors            []string
}

// DefaultAVWAPAnchors returns the standard anchor set the monitor's
// standalone-AVWAP path resolves for streaming symbols: session_open,
// pd_high, pd_low. Returned as a fresh slice so callers can mutate it
// without affecting the package state.
func DefaultAVWAPAnchors() []string {
	return []string{"session_open", "pd_high", "pd_low"}
}

// MonitorAVWAPSetter is the slice of *monitor.Service surface that
// AVWAP wiring needs.
type MonitorAVWAPSetter interface {
	SetAVWAPFn(fn func(symbol string) map[string]float64)
	SetAnchorResolverFn(fn func(symbol string, barTime time.Time, anchors []string) map[string]time.Time)
	SetSessionRefresherFn(fn func(symbol string, barTime time.Time))
	SetPrevDayBarsFn(fn func(symbol string, since, until time.Time) []start.Bar)
	SetAVWAPAnchors(anchors []string)
}

// WireAVWAPMonitor wires the monitor's standalone-AVWAP path so the
// monitor can compute anchored VWAP for ALL streaming symbols, not
// just those with active strategy instances. Without this, symbols
// used as references (correlation peers, tide-tracker SPY/QQQ,
// rotation candidates not yet promoted to strategies) have empty AVWAP
// state in the enriched-bar event stream.
//
// Live wires this with the configured sessionResolver, sessionRefreshFn,
// and prevDayBarsFn (closes #42 — backtest and omo-replay previously
// did not call any of these setters on the monitor, so monitor-side
// AVWAP was empty for non-strategy symbols on those paths).
func (p *Pipeline) WireAVWAPMonitor(monitor MonitorAVWAPSetter, wiring AVWAPMonitorWiring) {
	monitor.SetAVWAPFn(wiring.AVWAPFn)
	monitor.SetAnchorResolverFn(wiring.AnchorResolverFn)
	monitor.SetSessionRefresherFn(wiring.SessionRefresherFn)
	monitor.SetPrevDayBarsFn(wiring.PrevDayBarsFn)
	monitor.SetAVWAPAnchors(wiring.Anchors)
}
