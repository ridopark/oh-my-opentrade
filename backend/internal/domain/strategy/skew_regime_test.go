package strategy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifySkewRegime(t *testing.T) {
	tests := []struct {
		name               string
		rr25d7d            float64
		atmIV7d            float64
		termSlope          float64
		rollingATMIVMedian float64
		want               SkewRegime
	}{
		{
			name:               "neutral: all metrics within normal bounds",
			rr25d7d:            -0.01,
			atmIV7d:            0.50,
			termSlope:          0.05,
			rollingATMIVMedian: 0.50,
			want:               SkewRegimeNeutral,
		},
		{
			name:               "neutral: zero rolling median falls back safely",
			rr25d7d:            -0.10,
			atmIV7d:            0.80,
			termSlope:          0.0,
			rollingATMIVMedian: 0.0,
			want:               SkewRegimeNeutral,
		},
		{
			name:               "dislocated: extreme term inversion overrides all",
			rr25d7d:            -0.05, // would be fearful otherwise
			atmIV7d:            0.70,
			termSlope:          -0.15,
			rollingATMIVMedian: 0.50,
			want:               SkewRegimeDislocated,
		},
		{
			name:               "dislocated: exactly at threshold boundary",
			rr25d7d:            0.0,
			atmIV7d:            0.50,
			termSlope:          -0.10001,
			rollingATMIVMedian: 0.50,
			want:               SkewRegimeDislocated,
		},
		{
			name:               "fearful: steep put skew + elevated IV",
			rr25d7d:            -0.04,
			atmIV7d:            0.65,
			termSlope:          0.02,
			rollingATMIVMedian: 0.50, // 0.65/0.50 = 1.30 > 1.20
			want:               SkewRegimeFearful,
		},
		{
			name:               "not fearful: steep put skew but IV not elevated",
			rr25d7d:            -0.04,
			atmIV7d:            0.55,
			termSlope:          0.02,
			rollingATMIVMedian: 0.50, // 0.55/0.50 = 1.10 < 1.20
			want:               SkewRegimeNeutral,
		},
		{
			name:               "not fearful: elevated IV but RR not steep enough",
			rr25d7d:            -0.02,
			atmIV7d:            0.65,
			termSlope:          0.02,
			rollingATMIVMedian: 0.50,
			want:               SkewRegimeNeutral,
		},
		{
			name:               "greedy: call skew + suppressed IV",
			rr25d7d:            0.02,
			atmIV7d:            0.40,
			termSlope:          0.08,
			rollingATMIVMedian: 0.50, // 0.40/0.50 = 0.80 < 0.85
			want:               SkewRegimeGreedy,
		},
		{
			name:               "not greedy: call skew but IV not suppressed",
			rr25d7d:            0.02,
			atmIV7d:            0.48,
			termSlope:          0.08,
			rollingATMIVMedian: 0.50, // 0.48/0.50 = 0.96 > 0.85
			want:               SkewRegimeNeutral,
		},
		{
			name:               "dislocated takes priority over fearful",
			rr25d7d:            -0.05,
			atmIV7d:            0.70,
			termSlope:          -0.20,
			rollingATMIVMedian: 0.50,
			want:               SkewRegimeDislocated,
		},
		{
			name:               "term slope exactly at boundary is not dislocated",
			rr25d7d:            0.0,
			atmIV7d:            0.50,
			termSlope:          -0.10,
			rollingATMIVMedian: 0.50,
			want:               SkewRegimeNeutral,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifySkewRegime(tt.rr25d7d, tt.atmIV7d, tt.termSlope, tt.rollingATMIVMedian)
			assert.Equal(t, tt.want, got, "regime mismatch for %s", tt.name)
		})
	}
}

func TestSkewRegime_String(t *testing.T) {
	tests := []struct {
		regime SkewRegime
		want   string
	}{
		{SkewRegimeNeutral, "neutral"},
		{SkewRegimeFearful, "fearful"},
		{SkewRegimeGreedy, "greedy"},
		{SkewRegimeDislocated, "dislocated"},
		{SkewRegime(99), "unknown(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.regime.String())
		})
	}
}
