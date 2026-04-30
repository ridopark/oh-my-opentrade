package pipeline_test

import (
	"context"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/pipeline"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

type recordingNotifierSetter struct {
	notifier ports.NotifierPort
	calls    int
}

func (r *recordingNotifierSetter) SetNotifier(n ports.NotifierPort) {
	r.notifier = n
	r.calls++
}

type stubLiveNotifier struct{ name string }

func (s stubLiveNotifier) Notify(_ context.Context, _, _ string) error { return nil }

func TestPipeline_WireNotifiers_LiveUsesCallerNotifier(t *testing.T) {
	p := pipeline.New(pipeline.ModeLive)
	posMon := &recordingNotifierSetter{}
	live := stubLiveNotifier{name: "multi"}

	p.WireNotifiers(posMon, live)

	if posMon.calls != 1 {
		t.Errorf("SetNotifier called %d times; want 1", posMon.calls)
	}
	if posMon.notifier != live {
		t.Errorf("notifier = %v; want stubLiveNotifier{name:multi}", posMon.notifier)
	}
}

func TestPipeline_WireNotifiers_BacktestForcesNullNotifier(t *testing.T) {
	for _, mode := range []pipeline.Mode{pipeline.ModeBacktest, pipeline.ModeReplay} {
		t.Run(string(mode), func(t *testing.T) {
			p := pipeline.New(mode)
			posMon := &recordingNotifierSetter{}
			live := stubLiveNotifier{name: "multi"}

			// Even when caller passes a real-looking notifier, non-live modes
			// must install NullNotifier so backtest sweeps cannot light up
			// operator alert channels (audit #41).
			p.WireNotifiers(posMon, live)

			if posMon.calls != 1 {
				t.Errorf("SetNotifier called %d times; want 1", posMon.calls)
			}
			if _, ok := posMon.notifier.(pipeline.NullNotifier); !ok {
				t.Errorf("notifier = %T; want pipeline.NullNotifier", posMon.notifier)
			}
		})
	}
}

func TestPipeline_WireNotifiers_NonLiveAcceptsNil(t *testing.T) {
	p := pipeline.New(pipeline.ModeBacktest)
	posMon := &recordingNotifierSetter{}

	p.WireNotifiers(posMon, nil)

	if posMon.calls != 1 {
		t.Errorf("SetNotifier called %d times; want 1", posMon.calls)
	}
	if _, ok := posMon.notifier.(pipeline.NullNotifier); !ok {
		t.Errorf("notifier = %T; want pipeline.NullNotifier", posMon.notifier)
	}
}

func TestNullNotifier_NotifyIsNoOp(t *testing.T) {
	if err := (pipeline.NullNotifier{}).Notify(context.Background(), "tenant", "msg"); err != nil {
		t.Errorf("NullNotifier.Notify returned err = %v; want nil", err)
	}
	if err := (pipeline.NullNotifier{}).NotifyWithImage(context.Background(), "tenant", "msg", ports.Attachment{}); err != nil {
		t.Errorf("NullNotifier.NotifyWithImage returned err = %v; want nil", err)
	}
}
