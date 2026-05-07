package builtin

import (
	"testing"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
)

// TestAVWAP_BarTouchTags_BoundaryCases asserts the ema_low_below_ema /
// ema_high_above_ema booleans correctly encode bar position vs the EMA
// across the four boundary cases: entirely above, entirely below,
// straddling, and exactly touching one side.
func TestAVWAP_BarTouchTags_BoundaryCases(t *testing.T) {
	cases := []struct {
		name        string
		barLow      float64
		barHigh     float64
		emaValue    float64
		wantLowBel  string
		wantHighAbv string
	}{
		{
			name:        "entirely_above",
			barLow:      101.5,
			barHigh:     102.5,
			emaValue:    100.0,
			wantLowBel:  "0",
			wantHighAbv: "1",
		},
		{
			name:        "entirely_below",
			barLow:      97.5,
			barHigh:     98.5,
			emaValue:    100.0,
			wantLowBel:  "1",
			wantHighAbv: "0",
		},
		{
			name:        "straddling",
			barLow:      99.0,
			barHigh:     101.0,
			emaValue:    100.0,
			wantLowBel:  "1",
			wantHighAbv: "1",
		},
		{
			name:        "low_exactly_touches_ema",
			barLow:      100.0,
			barHigh:     102.0,
			emaValue:    100.0,
			wantLowBel:  "1",
			wantHighAbv: "1",
		},
		{
			name:        "high_exactly_touches_ema",
			barLow:      98.0,
			barHigh:     100.0,
			emaValue:    100.0,
			wantLowBel:  "1",
			wantHighAbv: "1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &AVWAPState{
				EMAReady: true,
				EMAValue: tc.emaValue,
				Indicators: start.IndicatorData{ATR: 1.0},
			}
			ec := entryContext{
				cfg: AVWAPConfig{EMADiagEnabled: true},
				bar: start.Bar{
					Time:  time.Now(),
					Low:   tc.barLow,
					High:  tc.barHigh,
					Close: (tc.barLow + tc.barHigh) / 2,
				},
			}
			tags := map[string]string{}
			s.appendEMADiagTags(ec, tags)
			assert.Equal(t, tc.wantLowBel, tags["ema_low_below_ema"], "ema_low_below_ema for %s", tc.name)
			assert.Equal(t, tc.wantHighAbv, tags["ema_high_above_ema"], "ema_high_above_ema for %s", tc.name)
		})
	}
}
