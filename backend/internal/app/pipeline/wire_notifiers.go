package pipeline

import (
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// PosMonitorNotifierSetter is the slice of *positionmonitor.Service surface
// that notifier wiring needs.
type PosMonitorNotifierSetter interface {
	SetNotifier(ports.NotifierPort)
}

// WireNotifiers connects the position monitor's notification sink. On
// ModeLive the caller's configured notifier (typically a
// notification.MultiNotifier fanning out to Telegram + Discord) is
// installed. On ModeBacktest and ModeReplay a NullNotifier is installed
// regardless of what the caller passed — backtest sweeps must not light
// up live operator alert channels, and the previous implicit policy
// ("non-live paths don't call SetNotifier") left the divergence
// undocumented (audit #41).
//
// liveNotifier may be nil on non-live modes; it is unused there.
func (p *Pipeline) WireNotifiers(posMon PosMonitorNotifierSetter, liveNotifier ports.NotifierPort) {
	if p.mode == ModeLive {
		posMon.SetNotifier(liveNotifier)
		return
	}
	posMon.SetNotifier(NullNotifier{})
}
