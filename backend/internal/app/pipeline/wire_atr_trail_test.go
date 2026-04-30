package pipeline_test

import (
	"reflect"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/pipeline"
	"github.com/oh-my-opentrade/backend/internal/config"
)

// recordingATRTrail records the SetATRTrailConfig args back into a
// config.ATRTrailConfig struct so the test can assert with a single
// DeepEqual instead of 9 field-by-field comparisons. ATRTimeframe and
// Bucketing on config.ATRTrailConfig are intentionally not propagated
// by SetATRTrailConfig (see service.go:368-385) and stay zero-valued.
type recordingATRTrail struct {
	received config.ATRTrailConfig
	calls    int
}

func (r *recordingATRTrail) SetATRTrailConfig(
	enabled bool,
	atrPeriod, lookbackDays, lookbackDaysCrypto, minHistoryDays int,
	tercileLow, tercileHigh, insufficientHistMult float64,
	tercileMultipliers []float64,
) {
	r.received = config.ATRTrailConfig{
		Enabled:                       enabled,
		ATRPeriod:                     atrPeriod,
		ATRLookbackDays:               lookbackDays,
		ATRLookbackDaysCrypto:         lookbackDaysCrypto,
		MinHistoryDays:                minHistoryDays,
		TercileLowPctile:              tercileLow,
		TercileHighPctile:             tercileHigh,
		InsufficientHistoryMultiplier: insufficientHistMult,
		TercileMultipliers:            tercileMultipliers,
	}
	r.calls++
}

func TestPipeline_WireATRTrailConfig(t *testing.T) {
	cfg := config.ATRTrailConfig{
		Enabled:                       true,
		ATRPeriod:                     14,
		ATRLookbackDays:               60,
		ATRLookbackDaysCrypto:         30,
		MinHistoryDays:                20,
		TercileLowPctile:              0.33,
		TercileHighPctile:             0.66,
		InsufficientHistoryMultiplier: 1.0,
		TercileMultipliers:            []float64{0.8, 1.0, 1.2},
	}

	for _, mode := range []pipeline.Mode{pipeline.ModeLive, pipeline.ModeBacktest, pipeline.ModeReplay} {
		t.Run(string(mode), func(t *testing.T) {
			p := pipeline.New(mode)
			posMon := &recordingATRTrail{}

			p.WireATRTrailConfig(posMon, cfg)

			if posMon.calls != 1 {
				t.Errorf("SetATRTrailConfig called %d times; want 1", posMon.calls)
			}
			if !reflect.DeepEqual(posMon.received, cfg) {
				t.Errorf("received = %+v; want %+v", posMon.received, cfg)
			}
		})
	}
}

func TestPipeline_WireATRTrailConfig_DisabledIsNoOp(t *testing.T) {
	p := pipeline.New(pipeline.ModeLive)
	posMon := &recordingATRTrail{}

	p.WireATRTrailConfig(posMon, config.ATRTrailConfig{Enabled: false})

	if posMon.calls != 1 {
		t.Errorf("SetATRTrailConfig still called once with Enabled=false; got %d", posMon.calls)
	}
	if posMon.received.Enabled {
		t.Error("Enabled should propagate as false")
	}
}
