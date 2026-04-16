package gate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
)

// MacroEventChecker returns macro events falling inside a ± window
// around `around`. Implementations wrap ports.MacroCalendarPort.
type MacroEventChecker interface {
	EventsInWindow(ctx context.Context, around time.Time, windowMinutes int) ([]ports.MacroEvent, error)
}

// macroEventGate refuses new entries when a high-impact macro release is
// scheduled within ±N minutes of now. Exit intents always pass through
// (exits may be protective and must not be held hostage to a gate).
type macroEventGate struct {
	checker        MacroEventChecker
	windowMinutes  int
	blockedImpacts map[string]struct{}
	nowFn          func() time.Time
}

func newMacroEventGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
	window := deps.MacroEventBlackoutMinutes
	if window <= 0 {
		window = 30
	}
	impacts := deps.MacroEventImpacts
	if len(impacts) == 0 {
		impacts = []string{"high"}
	}
	set := make(map[string]struct{}, len(impacts))
	for _, s := range impacts {
		set[strings.ToLower(strings.TrimSpace(s))] = struct{}{}
	}
	return &macroEventGate{
		checker:        deps.MacroEventGuard,
		windowMinutes:  window,
		blockedImpacts: set,
		nowFn:          time.Now,
	}, nil
}

func (g *macroEventGate) Name() string { return "macro_event_gate" }

func (g *macroEventGate) Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
	if g.checker == nil {
		return nil
	}
	if gctx.Intent.Direction.IsExit() {
		return nil
	}
	now := g.nowFn()
	events, err := g.checker.EventsInWindow(ctx, now, g.windowMinutes)
	if err != nil {
		return nil
	}
	for _, ev := range events {
		impact := strings.ToLower(strings.TrimSpace(ev.Impact))
		if impact == "" {
			impact = "medium"
		}
		if _, ok := g.blockedImpacts[impact]; !ok {
			continue
		}
		delta := ev.ScheduledAt.Sub(now)
		return &GateResult{
			GateName: "macro_event_gate",
			Reason: fmt.Sprintf("macro_event: %s at %s (impact=%s, Δ=%s)",
				ev.Name,
				ev.ScheduledAt.UTC().Format("2006-01-02 15:04Z"),
				impact,
				roundDur(delta),
			),
		}
	}
	return nil
}

func roundDur(d time.Duration) time.Duration {
	if d < 0 {
		return -(-d).Round(time.Minute)
	}
	return d.Round(time.Minute)
}

// MacroCalendarAdapter wraps a MacroCalendarPort so it satisfies
// MacroEventChecker without forcing bootstrap sites to hand-roll a shim.
type MacroCalendarAdapter struct {
	Port ports.MacroCalendarPort
}

// EventsInWindow delegates to the underlying port.
func (a MacroCalendarAdapter) EventsInWindow(ctx context.Context, around time.Time, windowMinutes int) ([]ports.MacroEvent, error) {
	if a.Port == nil {
		return nil, nil
	}
	return a.Port.EventsInWindow(ctx, around, windowMinutes)
}
