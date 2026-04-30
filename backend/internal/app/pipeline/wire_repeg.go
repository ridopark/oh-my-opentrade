package pipeline

import (
	"github.com/oh-my-opentrade/backend/internal/app/positionmonitor"
)

// PosMonitorRepegSetter is the slice of *positionmonitor.Service surface
// that re-peg-notifier wiring needs. Declared as an interface so test
// doubles can verify the wiring without constructing a full position
// monitor.
type PosMonitorRepegSetter interface {
	SetRepegNotifier(positionmonitor.RepegNotifier)
}

// WireRepegNotifier connects the position monitor's re-peg suppression
// hook to the execution service's MarkRepegCancel method. Without this
// wiring, when handleExitTimeout cancels a live limit for re-peg, the
// cancel ack triggers a dust sweep against a position whose qty may
// already be reflected in a sibling fill — the SOFI phantom-short
// 2026-04-16 incident. See cmd/omo-core/services.go for the original
// production rationale; this method centralizes the call so backtest
// and omo-replay paths receive identical wiring (closes #39).
//
// Wired identically across all modes — re-peg semantics are
// broker-agnostic. SimBroker won't fire re-peg events under today's
// fill model, so the call is harmless on backtest/replay; it makes
// the path consistent with live and future-proof against new broker
// adapters that emit re-pegs.
func (p *Pipeline) WireRepegNotifier(posMon PosMonitorRepegSetter, notifier positionmonitor.RepegNotifier) {
	posMon.SetRepegNotifier(notifier)
}
