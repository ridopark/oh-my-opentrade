package pipeline_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/pipeline"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

type recordingMonitorAVWAPSetter struct {
	avwapFnCalls            int
	anchorResolverFnCalls   int
	sessionRefresherFnCalls int
	prevDayBarsFnCalls      int
	anchorsCalls            int

	avwapFn            func(symbol string) map[string]float64
	anchorResolverFn   func(symbol string, barTime time.Time, anchors []string) map[string]time.Time
	sessionRefresherFn func(symbol string, barTime time.Time)
	prevDayBarsFn      func(symbol string, since, until time.Time) []start.Bar
	anchors            []string
}

func (r *recordingMonitorAVWAPSetter) SetAVWAPFn(fn func(symbol string) map[string]float64) {
	r.avwapFn = fn
	r.avwapFnCalls++
}

func (r *recordingMonitorAVWAPSetter) SetAnchorResolverFn(fn func(symbol string, barTime time.Time, anchors []string) map[string]time.Time) {
	r.anchorResolverFn = fn
	r.anchorResolverFnCalls++
}

func (r *recordingMonitorAVWAPSetter) SetSessionRefresherFn(fn func(symbol string, barTime time.Time)) {
	r.sessionRefresherFn = fn
	r.sessionRefresherFnCalls++
}

func (r *recordingMonitorAVWAPSetter) SetPrevDayBarsFn(fn func(symbol string, since, until time.Time) []start.Bar) {
	r.prevDayBarsFn = fn
	r.prevDayBarsFnCalls++
}

func (r *recordingMonitorAVWAPSetter) SetAVWAPAnchors(anchors []string) {
	r.anchors = anchors
	r.anchorsCalls++
}

func TestPipeline_WireAVWAPMonitor(t *testing.T) {
	wiring := pipeline.AVWAPMonitorWiring{
		AVWAPFn:            func(string) map[string]float64 { return map[string]float64{"pd_high": 100} },
		AnchorResolverFn:   func(string, time.Time, []string) map[string]time.Time { return nil },
		SessionRefresherFn: func(string, time.Time) {},
		PrevDayBarsFn:      func(string, time.Time, time.Time) []start.Bar { return nil },
		Anchors:            []string{"session_open", "pd_high", "pd_low"},
	}

	for _, mode := range []pipeline.Mode{pipeline.ModeLive, pipeline.ModeBacktest, pipeline.ModeReplay} {
		t.Run(string(mode), func(t *testing.T) {
			p := pipeline.New(mode)
			monitor := &recordingMonitorAVWAPSetter{}

			p.WireAVWAPMonitor(monitor, wiring)

			if monitor.avwapFnCalls != 1 {
				t.Errorf("SetAVWAPFn called %d times; want 1", monitor.avwapFnCalls)
			}
			if monitor.anchorResolverFnCalls != 1 {
				t.Errorf("SetAnchorResolverFn called %d times; want 1", monitor.anchorResolverFnCalls)
			}
			if monitor.sessionRefresherFnCalls != 1 {
				t.Errorf("SetSessionRefresherFn called %d times; want 1", monitor.sessionRefresherFnCalls)
			}
			if monitor.prevDayBarsFnCalls != 1 {
				t.Errorf("SetPrevDayBarsFn called %d times; want 1", monitor.prevDayBarsFnCalls)
			}
			if monitor.anchorsCalls != 1 {
				t.Errorf("SetAVWAPAnchors called %d times; want 1", monitor.anchorsCalls)
			}
			if !reflect.DeepEqual(monitor.anchors, wiring.Anchors) {
				t.Errorf("anchors = %v; want %v", monitor.anchors, wiring.Anchors)
			}
			// Function identity: avwapFn must round-trip the value the
			// pipeline received. Calling it through the setter proves the
			// closure flowed through (Go func values aren't directly
			// comparable, so call-then-check is the idiomatic test).
			if got := monitor.avwapFn("AAPL"); got["pd_high"] != 100 {
				t.Errorf("avwapFn produced %v; want pd_high=100", got)
			}
		})
	}
}

func TestPipeline_WireAVWAPMonitor_NilSessionRefresherIsAcceptable(t *testing.T) {
	// Backtest and omo-replay pass SessionRefresherFn=nil because session
	// data is loaded once at run start and not refreshed. The monitor's
	// SetSessionRefresherFn accepts nil; pipeline must pass it through
	// without erroring.
	p := pipeline.New(pipeline.ModeBacktest)
	monitor := &recordingMonitorAVWAPSetter{}

	wiring := pipeline.AVWAPMonitorWiring{
		AVWAPFn:            func(string) map[string]float64 { return nil },
		AnchorResolverFn:   func(string, time.Time, []string) map[string]time.Time { return nil },
		SessionRefresherFn: nil,
		PrevDayBarsFn:      func(string, time.Time, time.Time) []start.Bar { return nil },
		Anchors:            []string{"pd_high"},
	}

	p.WireAVWAPMonitor(monitor, wiring)

	if monitor.sessionRefresherFnCalls != 1 {
		t.Errorf("SetSessionRefresherFn should still be called once with nil; got %d", monitor.sessionRefresherFnCalls)
	}
	if monitor.sessionRefresherFn != nil {
		t.Error("SessionRefresherFn should propagate as nil")
	}
}
