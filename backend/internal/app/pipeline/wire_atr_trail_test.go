package pipeline_test

import (
	"reflect"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/pipeline"
	"github.com/oh-my-opentrade/backend/internal/config"
)

type recordingATRTrail struct {
	enabled                      bool
	atrPeriod                    int
	lookbackDays                 int
	lookbackDaysCrypto           int
	minHistoryDays               int
	tercileLow                   float64
	tercileHigh                  float64
	insufficientHistoryMult      float64
	tercileMultipliers           []float64
	calls                        int
}

func (r *recordingATRTrail) SetATRTrailConfig(
	enabled bool,
	atrPeriod, lookbackDays, lookbackDaysCrypto, minHistoryDays int,
	tercileLow, tercileHigh, insufficientHistMult float64,
	tercileMultipliers []float64,
) {
	r.enabled = enabled
	r.atrPeriod = atrPeriod
	r.lookbackDays = lookbackDays
	r.lookbackDaysCrypto = lookbackDaysCrypto
	r.minHistoryDays = minHistoryDays
	r.tercileLow = tercileLow
	r.tercileHigh = tercileHigh
	r.insufficientHistoryMult = insufficientHistMult
	r.tercileMultipliers = tercileMultipliers
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
			if posMon.enabled != cfg.Enabled {
				t.Errorf("Enabled = %v; want %v", posMon.enabled, cfg.Enabled)
			}
			if posMon.atrPeriod != cfg.ATRPeriod {
				t.Errorf("ATRPeriod = %d; want %d", posMon.atrPeriod, cfg.ATRPeriod)
			}
			if posMon.lookbackDays != cfg.ATRLookbackDays {
				t.Errorf("ATRLookbackDays = %d; want %d", posMon.lookbackDays, cfg.ATRLookbackDays)
			}
			if posMon.lookbackDaysCrypto != cfg.ATRLookbackDaysCrypto {
				t.Errorf("ATRLookbackDaysCrypto = %d; want %d", posMon.lookbackDaysCrypto, cfg.ATRLookbackDaysCrypto)
			}
			if posMon.minHistoryDays != cfg.MinHistoryDays {
				t.Errorf("MinHistoryDays = %d; want %d", posMon.minHistoryDays, cfg.MinHistoryDays)
			}
			if posMon.tercileLow != cfg.TercileLowPctile {
				t.Errorf("TercileLowPctile = %v; want %v", posMon.tercileLow, cfg.TercileLowPctile)
			}
			if posMon.tercileHigh != cfg.TercileHighPctile {
				t.Errorf("TercileHighPctile = %v; want %v", posMon.tercileHigh, cfg.TercileHighPctile)
			}
			if posMon.insufficientHistoryMult != cfg.InsufficientHistoryMultiplier {
				t.Errorf("InsufficientHistoryMultiplier = %v; want %v", posMon.insufficientHistoryMult, cfg.InsufficientHistoryMultiplier)
			}
			if !reflect.DeepEqual(posMon.tercileMultipliers, cfg.TercileMultipliers) {
				t.Errorf("TercileMultipliers = %v; want %v", posMon.tercileMultipliers, cfg.TercileMultipliers)
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
	if posMon.enabled {
		t.Error("Enabled should propagate as false")
	}
}
