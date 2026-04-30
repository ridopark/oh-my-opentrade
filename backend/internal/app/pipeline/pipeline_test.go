package pipeline_test

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/pipeline"
)

func TestMode_String(t *testing.T) {
	cases := []struct {
		mode pipeline.Mode
		want string
	}{
		{pipeline.ModeLive, "live"},
		{pipeline.ModeBacktest, "backtest"},
		{pipeline.ModeReplay, "replay"},
	}
	for _, c := range cases {
		if got := c.mode.String(); got != c.want {
			t.Errorf("Mode(%q).String() = %q; want %q", c.mode, got, c.want)
		}
	}
}

func TestMode_IsBacktest(t *testing.T) {
	if pipeline.ModeLive.IsBacktest() {
		t.Error("ModeLive should not be backtest")
	}
	if !pipeline.ModeBacktest.IsBacktest() {
		t.Error("ModeBacktest should be backtest")
	}
	if !pipeline.ModeReplay.IsBacktest() {
		t.Error("ModeReplay should be backtest")
	}
}

func TestPipeline_NewMode(t *testing.T) {
	p := pipeline.New(pipeline.ModeLive)
	if p.Mode() != pipeline.ModeLive {
		t.Errorf("Mode() = %v; want ModeLive", p.Mode())
	}
}
