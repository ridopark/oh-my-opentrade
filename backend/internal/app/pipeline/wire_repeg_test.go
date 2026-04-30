package pipeline_test

import (
	"context"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/pipeline"
	"github.com/oh-my-opentrade/backend/internal/app/positionmonitor"
)

type recordingPosMon struct {
	notifier positionmonitor.RepegNotifier
	calls    int
}

func (r *recordingPosMon) SetRepegNotifier(n positionmonitor.RepegNotifier) {
	r.notifier = n
	r.calls++
}

type stubRepegNotifier struct{}

func (stubRepegNotifier) MarkRepegCancel(string) bool { return false }
func (stubRepegNotifier) RepegOrderInPlace(_ context.Context, _ string, _ float64) (bool, error) {
	return false, nil
}

func TestPipeline_WireRepegNotifier(t *testing.T) {
	for _, mode := range []pipeline.Mode{pipeline.ModeLive, pipeline.ModeBacktest, pipeline.ModeReplay} {
		t.Run(string(mode), func(t *testing.T) {
			p := pipeline.New(mode)
			posMon := &recordingPosMon{}
			notifier := stubRepegNotifier{}

			p.WireRepegNotifier(posMon, notifier)

			if posMon.calls != 1 {
				t.Errorf("SetRepegNotifier called %d times; want 1", posMon.calls)
			}
			if posMon.notifier != notifier {
				t.Error("SetRepegNotifier did not receive the passed notifier")
			}
		})
	}
}
