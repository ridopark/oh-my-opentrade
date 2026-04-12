package strategy_test

import (
	"testing"

	s "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
)

// ─── EMA Stack ───────────────────────────────────────────────────────────────

func TestScoreEMAStack_FullStack_Long(t *testing.T) {
	bar := s.Bar{Close: 105}
	ind := s.IndicatorData{EMA9: 104, EMA21: 103, EMA50: 102}
	r := s.ScoreEMAStack(bar, ind, true)
	assert.Equal(t, 15, r.Score)
	assert.Contains(t, r.Factors, "ema_stack")
}

func TestScoreEMAStack_Partial_Long(t *testing.T) {
	bar := s.Bar{Close: 105}
	ind := s.IndicatorData{EMA9: 104, EMA21: 106, EMA50: 102} // EMA9 < EMA21
	r := s.ScoreEMAStack(bar, ind, true)
	assert.Equal(t, 10, r.Score)
	assert.Contains(t, r.Factors, "ema_partial")
}

func TestScoreEMAStack_None_Long(t *testing.T) {
	bar := s.Bar{Close: 100}
	ind := s.IndicatorData{EMA9: 105, EMA21: 106, EMA50: 107} // all above price
	r := s.ScoreEMAStack(bar, ind, true)
	assert.Equal(t, 0, r.Score)
}

func TestScoreEMAStack_FullStack_Short(t *testing.T) {
	bar := s.Bar{Close: 95}
	ind := s.IndicatorData{EMA9: 96, EMA21: 97, EMA50: 98}
	r := s.ScoreEMAStack(bar, ind, false)
	assert.Equal(t, 15, r.Score)
	assert.Contains(t, r.Factors, "ema_stack")
}

func TestScoreEMAStack_MissingEMAs(t *testing.T) {
	r := s.ScoreEMAStack(s.Bar{Close: 100}, s.IndicatorData{EMA9: 99}, false)
	assert.Equal(t, 0, r.Score)
}

// ─── ADX ─────────────────────────────────────────────────────────────────────

func TestScoreADX_Levels(t *testing.T) {
	tests := []struct {
		adx   float64
		score int
		tag   string
	}{
		{35, 15, "adx_strong"},
		{30, 15, "adx_strong"},
		{27, 12, "adx_trend"},
		{25, 12, "adx_trend"},
		{22, 8, "adx_ok"},
		{20, 8, "adx_ok"},
		{17, 4, ""},
		{10, 0, ""},
		{0, 0, ""},
	}
	for _, tt := range tests {
		r := s.ScoreADX(s.IndicatorData{ADX: tt.adx})
		assert.Equal(t, tt.score, r.Score, "ADX=%.0f", tt.adx)
		if tt.tag != "" {
			assert.Contains(t, r.Factors, tt.tag)
		}
	}
}

// ─── RSI ─────────────────────────────────────────────────────────────────────

func TestScoreRSI_Long(t *testing.T) {
	ideal := s.ScoreRSI(s.IndicatorData{RSI: 55}, true)
	assert.Equal(t, 10, ideal.Score)
	assert.Contains(t, ideal.Factors, "rsi_ideal")

	ok := s.ScoreRSI(s.IndicatorData{RSI: 40}, true)
	assert.Equal(t, 5, ok.Score)

	bad := s.ScoreRSI(s.IndicatorData{RSI: 80}, true)
	assert.Equal(t, 0, bad.Score)
}

func TestScoreRSI_Short(t *testing.T) {
	ideal := s.ScoreRSI(s.IndicatorData{RSI: 45}, false)
	assert.Equal(t, 10, ideal.Score)

	ok := s.ScoreRSI(s.IndicatorData{RSI: 60}, false)
	assert.Equal(t, 5, ok.Score)

	bad := s.ScoreRSI(s.IndicatorData{RSI: 20}, false)
	assert.Equal(t, 0, bad.Score)
}

func TestScoreRSI_Zero(t *testing.T) {
	r := s.ScoreRSI(s.IndicatorData{RSI: 0}, true)
	assert.Equal(t, 0, r.Score)
}

// ─── Volume ──────────────────────────────────────────────────────────────────

func TestScoreVolume_Levels(t *testing.T) {
	tests := []struct {
		vol, sma float64
		score    int
	}{
		{2000, 1000, 8},  // 2.0x → surge
		{1500, 1000, 8},  // 1.5x → surge
		{1300, 1000, 6},  // 1.3x → above avg
		{1100, 1000, 4},  // 1.1x → normal
		{900, 1000, 2},   // 0.9x → below
		{500, 1000, 0},   // 0.5x → too low
	}
	for _, tt := range tests {
		r := s.ScoreVolume(s.Bar{Volume: tt.vol}, s.IndicatorData{VolumeSMA: tt.sma})
		assert.Equal(t, tt.score, r.Score, "vol=%.0f sma=%.0f", tt.vol, tt.sma)
	}
}

func TestScoreVolume_ZeroSMA(t *testing.T) {
	r := s.ScoreVolume(s.Bar{Volume: 1000}, s.IndicatorData{VolumeSMA: 0})
	assert.Equal(t, 0, r.Score)
}

// ─── Candle ──────────────────────────────────────────────────────────────────

func TestScoreCandle_StrongDirectional(t *testing.T) {
	// Body ratio = |101-99|/(102-98) = 2/4 = 0.5... need > 0.7
	bar := s.Bar{Open: 98.5, High: 102, Low: 98, Close: 101.5, Volume: 1000}
	r := s.ScoreCandle(bar, true) // bullish close
	// body = 3.0, range = 4.0, ratio = 0.75 > 0.7 → strong
	assert.Equal(t, 8, r.Score)
	assert.Contains(t, r.Factors, "candle_strong")
}

func TestScoreCandle_WrongDirection(t *testing.T) {
	bar := s.Bar{Open: 101, High: 102, Low: 98, Close: 99} // bearish
	r := s.ScoreCandle(bar, true)                            // want long
	assert.Equal(t, 0, r.Score)
}

func TestScoreCandle_Doji(t *testing.T) {
	bar := s.Bar{Open: 100, High: 103, Low: 97, Close: 100.1} // tiny body
	r := s.ScoreCandle(bar, true)
	assert.LessOrEqual(t, r.Score, 4, "doji should score low")
}

func TestScoreCandle_FlatBar(t *testing.T) {
	bar := s.Bar{Open: 100, High: 100, Low: 100, Close: 100}
	r := s.ScoreCandle(bar, true)
	assert.Equal(t, 0, r.Score)
}

// ─── BB ──────────────────────────────────────────────────────────────────────

func TestScoreBB_Long_Trending(t *testing.T) {
	r := s.ScoreBB(s.IndicatorData{BBPercentB: 0.65}, true)
	assert.Equal(t, 7, r.Score)
	assert.Contains(t, r.Factors, "bb_trend")
}

func TestScoreBB_Long_Overextended(t *testing.T) {
	r := s.ScoreBB(s.IndicatorData{BBPercentB: 1.1}, true)
	assert.Equal(t, 0, r.Score)
}

func TestScoreBB_Short_Trending(t *testing.T) {
	r := s.ScoreBB(s.IndicatorData{BBPercentB: 0.35}, false)
	assert.Equal(t, 7, r.Score)
}

// ─── HTF Bias ────────────────────────────────────────────────────────────────

func TestScoreHTFBias_Matching(t *testing.T) {
	ind := s.IndicatorData{HTF: map[string]s.HTFIndicator{"1d": {Bias: "BULLISH"}}}
	r := s.ScoreHTFBias(ind, true)
	assert.Equal(t, 8, r.Score)
	assert.Contains(t, r.Factors, "htf_agree")
}

func TestScoreHTFBias_Neutral(t *testing.T) {
	ind := s.IndicatorData{HTF: map[string]s.HTFIndicator{"1d": {Bias: "NEUTRAL"}}}
	r := s.ScoreHTFBias(ind, true)
	assert.Equal(t, 4, r.Score)
}

func TestScoreHTFBias_Opposing(t *testing.T) {
	ind := s.IndicatorData{HTF: map[string]s.HTFIndicator{"1d": {Bias: "BEARISH"}}}
	r := s.ScoreHTFBias(ind, true)
	assert.Equal(t, 0, r.Score)
}

func TestScoreHTFBias_Missing(t *testing.T) {
	r := s.ScoreHTFBias(s.IndicatorData{}, true)
	assert.Equal(t, 0, r.Score)
}

// ─── VWAP ────────────────────────────────────────────────────────────────────

func TestScoreVWAP_Aligned_Long(t *testing.T) {
	r := s.ScoreVWAP(s.Bar{Close: 105}, s.IndicatorData{VWAP: 100}, true)
	assert.Equal(t, 7, r.Score)
	assert.Contains(t, r.Factors, "vwap_aligned")
}

func TestScoreVWAP_Misaligned_Long(t *testing.T) {
	r := s.ScoreVWAP(s.Bar{Close: 95}, s.IndicatorData{VWAP: 100}, true)
	assert.Equal(t, 0, r.Score)
}

func TestScoreVWAP_ZeroVWAP(t *testing.T) {
	r := s.ScoreVWAP(s.Bar{Close: 100}, s.IndicatorData{VWAP: 0}, true)
	assert.Equal(t, 0, r.Score)
}

// ─── ComputeBaseConfluence ───────────────────────────────────────────────────

func TestComputeBaseConfluence_AllPerfect(t *testing.T) {
	bar := s.Bar{Open: 98.5, High: 102, Low: 98, Close: 101.5, Volume: 1800}
	ind := s.IndicatorData{
		EMA9: 101, EMA21: 100, EMA50: 99,
		ADX: 30, RSI: 55,
		VolumeSMA:  1000,
		BBPercentB: 0.65,
		VWAP:       100,
		HTF:        map[string]s.HTFIndicator{"1d": {Bias: "BULLISH"}},
	}
	r := s.ComputeBaseConfluence(bar, ind, true)
	// 15 + 15 + 10 + 8 + 8 + 7 + 8 + 7 = 78
	assert.Equal(t, 78, r.Score)
	assert.Contains(t, r.Factors, "ema_stack")
	assert.Contains(t, r.Factors, "adx_strong")
	assert.Contains(t, r.Factors, "rsi_ideal")
	assert.Contains(t, r.Factors, "vol_surge")
	assert.Contains(t, r.Factors, "candle_strong")
	assert.Contains(t, r.Factors, "bb_trend")
	assert.Contains(t, r.Factors, "htf_agree")
	assert.Contains(t, r.Factors, "vwap_aligned")
}

func TestComputeBaseConfluence_Empty(t *testing.T) {
	r := s.ComputeBaseConfluence(s.Bar{}, s.IndicatorData{}, true)
	assert.Equal(t, 0, r.Score)
	assert.Empty(t, r.Factors)
}

// ─── MergeConfluence ─────────────────────────────────────────────────────────

func TestMergeConfluence(t *testing.T) {
	a := s.ConfluenceResult{Score: 30, Factors: []string{"ema_stack", "adx_strong"},
		Components: []s.ComponentScore{{Name: "ema_stack", Group: "technical", Weight: 15, Fired: true}}}
	b := s.ConfluenceResult{Score: 12, Factors: []string{"macd_specific"},
		Components: []s.ComponentScore{{Name: "macd_specific", Group: "technical", Weight: 12, Fired: true}}}
	merged := s.MergeConfluence(a, b)
	assert.Equal(t, 42, merged.Score)
	assert.Len(t, merged.Factors, 3)
	assert.Contains(t, merged.Factors, "macd_specific")
	assert.Len(t, merged.Components, 2)
	assert.Equal(t, "ema_stack", merged.Components[0].Name)
	assert.Equal(t, "macd_specific", merged.Components[1].Name)
}

func TestMergeConfluence_Empty(t *testing.T) {
	r := s.MergeConfluence()
	assert.Equal(t, 0, r.Score)
	assert.Empty(t, r.Factors)
	assert.Empty(t, r.Components)
}

func TestConfluenceResult_FormatDetail(t *testing.T) {
	r := s.ConfluenceResult{Score: 50, Factors: []string{"ema_stack", "adx_strong", "vwap_aligned"}}
	assert.Equal(t, "ema_stack+adx_strong+vwap_aligned", r.FormatDetail())
}

// ─── Retest Quality ─────────────────────────────────────────────────────────

func TestScoreRetestQuality_Perfect(t *testing.T) {
	r := s.ScoreRetestQuality(2, 0.30, 50, 100, 0.8, true)
	assert.Equal(t, 16, r.Score) // 5+5+3+3
	assert.Contains(t, r.Factors, "rq_speed")
	assert.Contains(t, r.Factors, "rq_shallow")
	assert.Contains(t, r.Factors, "rq_dryup")
	assert.Contains(t, r.Factors, "rq_confirm")
}

func TestScoreRetestQuality_Worst(t *testing.T) {
	r := s.ScoreRetestQuality(10, 0.70, 120, 100, 0.3, false)
	assert.Equal(t, 0, r.Score)
	assert.Empty(t, r.Factors)
}

func TestScoreRetestQuality_FastButDeep(t *testing.T) {
	r := s.ScoreRetestQuality(3, 0.55, 50, 100, 0.7, true)
	assert.Equal(t, 11, r.Score) // 5+0+3+3
}

func TestScoreRetestQuality_SlowButShallow(t *testing.T) {
	r := s.ScoreRetestQuality(8, 0.30, 90, 100, 0.5, true)
	assert.Equal(t, 5, r.Score) // 0+5+0+0
}

func TestScoreRetestQuality_ZeroBreakoutVolume(t *testing.T) {
	r := s.ScoreRetestQuality(3, 0.30, 50, 0, 0.8, true)
	assert.Equal(t, 13, r.Score) // 5+5+0+3 (volume factor skipped)
}

// ─── Dark Pool ──────────────────────────────────────────────────────────────

func TestScoreDarkPool(t *testing.T) {
	tests := []struct {
		name    string
		ind     s.IndicatorData
		isLong  bool
		score   int
		factors []string
	}{
		{
			name:   "no DP data (DPRatio=0)",
			ind:    s.IndicatorData{DPRatio: 0},
			isLong: true,
			score:  0,
		},
		{
			name:    "high DP ratio only",
			ind:     s.IndicatorData{DPRatio: 0.55, DPBuyRatio: 0.50, DPLargePrintPct: 0.05},
			isLong:  true,
			score:   3,
			factors: []string{"dp_elevated"},
		},
		{
			name:    "long + buy pressure",
			ind:     s.IndicatorData{DPRatio: 0.30, DPBuyRatio: 0.65, DPLargePrintPct: 0.05},
			isLong:  true,
			score:   4,
			factors: []string{"dp_buy"},
		},
		{
			name:    "short + sell pressure",
			ind:     s.IndicatorData{DPRatio: 0.30, DPBuyRatio: 0.35, DPLargePrintPct: 0.05},
			isLong:  false,
			score:   4,
			factors: []string{"dp_sell"},
		},
		{
			name:    "large prints only",
			ind:     s.IndicatorData{DPRatio: 0.30, DPBuyRatio: 0.50, DPLargePrintPct: 0.20},
			isLong:  true,
			score:   3,
			factors: []string{"dp_blocks"},
		},
		{
			name:    "all factors combined long",
			ind:     s.IndicatorData{DPRatio: 0.55, DPBuyRatio: 0.65, DPLargePrintPct: 0.20},
			isLong:  true,
			score:   10,
			factors: []string{"dp_elevated", "dp_buy", "dp_blocks"},
		},
		{
			name:    "all factors combined short",
			ind:     s.IndicatorData{DPRatio: 0.55, DPBuyRatio: 0.35, DPLargePrintPct: 0.20},
			isLong:  false,
			score:   10,
			factors: []string{"dp_elevated", "dp_sell", "dp_blocks"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := s.ScoreDarkPool(tt.ind, tt.isLong)
			assert.Equal(t, tt.score, r.Score)
			if len(tt.factors) == 0 {
				assert.Empty(t, r.Factors)
			} else {
				assert.Equal(t, tt.factors, r.Factors)
			}
		})
	}
}

func TestScoreDarkPool_ZScore(t *testing.T) {
	tests := []struct {
		name    string
		ind     s.IndicatorData
		isLong  bool
		score   int
		factors []string
	}{
		{
			name:    "z-score elevated triggers dp_elevated",
			ind:     s.IndicatorData{DPRatio: 0.40, DPRatioZScore: 2.0, DPBuyRatio: 0.50, DPLargePrintPct: 0.05},
			isLong:  true,
			score:   3,
			factors: []string{"dp_elevated"},
		},
		{
			name:   "z-score below threshold and ratio below 0.50 — no elevated",
			ind:    s.IndicatorData{DPRatio: 0.40, DPRatioZScore: 1.0, DPBuyRatio: 0.50, DPLargePrintPct: 0.05},
			isLong: true,
			score:  0,
		},
		{
			name:    "z-score elevated + directional + blocks",
			ind:     s.IndicatorData{DPRatio: 0.40, DPRatioZScore: 1.5, DPBuyRatio: 0.65, DPLargePrintPct: 0.20},
			isLong:  true,
			score:   10,
			factors: []string{"dp_elevated", "dp_buy", "dp_blocks"},
		},
		{
			name:   "negative z-score, high static ratio — no fallback when z-score is non-zero",
			ind:    s.IndicatorData{DPRatio: 0.55, DPRatioZScore: -0.5, DPBuyRatio: 0.50, DPLargePrintPct: 0.05},
			isLong: true,
			score:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := s.ScoreDarkPool(tt.ind, tt.isLong)
			assert.Equal(t, tt.score, r.Score)
			if len(tt.factors) == 0 {
				assert.Empty(t, r.Factors)
			} else {
				assert.Equal(t, tt.factors, r.Factors)
			}
		})
	}
}

// ─── DP Veto ────────────────────────────────────────────────────────────────

func TestDPVeto(t *testing.T) {
	tests := []struct {
		name            string
		ind             s.IndicatorData
		isLong          bool
		buyRatioMin     float64
		sellRatioMax    float64
		expectBlocked   bool
		expectReasonPfx string
	}{
		{
			name:          "no DP data — no veto",
			ind:           s.IndicatorData{DPRatio: 0},
			isLong:        true,
			buyRatioMin:   0.45,
			sellRatioMax:  0.55,
			expectBlocked: false,
		},
		{
			name:            "long vetoed — low buy ratio",
			ind:             s.IndicatorData{DPRatio: 0.40, DPBuyRatio: 0.30},
			isLong:          true,
			buyRatioMin:     0.45,
			sellRatioMax:    0.55,
			expectBlocked:   true,
			expectReasonPfx: "dp_veto_long",
		},
		{
			name:          "long not vetoed — buy ratio above min",
			ind:           s.IndicatorData{DPRatio: 0.40, DPBuyRatio: 0.50},
			isLong:        true,
			buyRatioMin:   0.45,
			sellRatioMax:  0.55,
			expectBlocked: false,
		},
		{
			name:            "short vetoed — high buy ratio",
			ind:             s.IndicatorData{DPRatio: 0.40, DPBuyRatio: 0.70},
			isLong:          false,
			buyRatioMin:     0.45,
			sellRatioMax:    0.55,
			expectBlocked:   true,
			expectReasonPfx: "dp_veto_short",
		},
		{
			name:          "short not vetoed — buy ratio below max",
			ind:           s.IndicatorData{DPRatio: 0.40, DPBuyRatio: 0.45},
			isLong:        false,
			buyRatioMin:   0.45,
			sellRatioMax:  0.55,
			expectBlocked: false,
		},
		{
			name:          "boundary — long buy ratio exactly at min",
			ind:           s.IndicatorData{DPRatio: 0.40, DPBuyRatio: 0.45},
			isLong:        true,
			buyRatioMin:   0.45,
			sellRatioMax:  0.55,
			expectBlocked: false,
		},
		{
			name:            "boundary — short buy ratio exactly at max + epsilon",
			ind:             s.IndicatorData{DPRatio: 0.40, DPBuyRatio: 0.56},
			isLong:          false,
			buyRatioMin:     0.45,
			sellRatioMax:    0.55,
			expectBlocked:   true,
			expectReasonPfx: "dp_veto_short",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocked, reason := s.DPVeto(tt.ind, tt.isLong, tt.buyRatioMin, tt.sellRatioMax)
			assert.Equal(t, tt.expectBlocked, blocked)
			if tt.expectReasonPfx != "" {
				assert.Contains(t, reason, tt.expectReasonPfx)
			}
			if !tt.expectBlocked {
				assert.Empty(t, reason)
			}
		})
	}
}

// ─── DP Sizing Multiplier ───────────────────────────────────────────────────

func TestDPSizingMultiplier(t *testing.T) {
	tests := []struct {
		name     string
		ind      s.IndicatorData
		isLong   bool
		maxBoost float64
		expected float64
	}{
		{
			name:     "zero z-score — no boost",
			ind:      s.IndicatorData{DPRatioZScore: 0, DPBuyRatio: 0.65},
			isLong:   true,
			maxBoost: 1.5,
			expected: 1.0,
		},
		{
			name:     "negative z-score — no boost",
			ind:      s.IndicatorData{DPRatioZScore: -1.0, DPBuyRatio: 0.65},
			isLong:   true,
			maxBoost: 1.5,
			expected: 1.0,
		},
		{
			name:     "high z-score but not aligned — no boost",
			ind:      s.IndicatorData{DPRatioZScore: 2.5, DPBuyRatio: 0.50},
			isLong:   true,
			maxBoost: 1.5,
			expected: 1.0,
		},
		{
			name:     "z-score 2.0 + aligned long — max boost",
			ind:      s.IndicatorData{DPRatioZScore: 2.0, DPBuyRatio: 0.65},
			isLong:   true,
			maxBoost: 1.5,
			expected: 1.5,
		},
		{
			name:     "z-score 2.0 + aligned short — max boost",
			ind:      s.IndicatorData{DPRatioZScore: 2.5, DPBuyRatio: 0.35},
			isLong:   false,
			maxBoost: 1.5,
			expected: 1.5,
		},
		{
			name:     "z-score 1.5 + aligned — half boost",
			ind:      s.IndicatorData{DPRatioZScore: 1.5, DPBuyRatio: 0.65},
			isLong:   true,
			maxBoost: 1.5,
			expected: 1.25,
		},
		{
			name:     "z-score 1.0 + aligned — no boost (below 1.5 threshold)",
			ind:      s.IndicatorData{DPRatioZScore: 1.0, DPBuyRatio: 0.65},
			isLong:   true,
			maxBoost: 1.5,
			expected: 1.0,
		},
		{
			name:     "max boost 2.0 — z-score 2.0 + aligned",
			ind:      s.IndicatorData{DPRatioZScore: 2.0, DPBuyRatio: 0.65},
			isLong:   true,
			maxBoost: 2.0,
			expected: 2.0,
		},
		{
			name:     "max boost 2.0 — z-score 1.5 + aligned — half boost",
			ind:      s.IndicatorData{DPRatioZScore: 1.5, DPBuyRatio: 0.65},
			isLong:   true,
			maxBoost: 2.0,
			expected: 1.5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.DPSizingMultiplier(tt.ind, tt.isLong, tt.maxBoost)
			assert.InDelta(t, tt.expected, got, 0.001)
		})
	}
}

// ─── ComponentScore attribution tests ──────────────────────────────────────

func TestComponentScore_EMAStack_AllFired(t *testing.T) {
	bar := s.Bar{Close: 105}
	ind := s.IndicatorData{EMA9: 104, EMA21: 103, EMA50: 102}
	r := s.ScoreEMAStack(bar, ind, true)
	assert.Len(t, r.Components, 1)
	cs := r.Components[0]
	assert.Equal(t, "ema_stack", cs.Name)
	assert.Equal(t, "technical", cs.Group)
	assert.Equal(t, 15, cs.Weight)
	assert.True(t, cs.Fired)
	assert.Equal(t, 3.0, cs.Value)
}

func TestComponentScore_EMAStack_NotFired(t *testing.T) {
	bar := s.Bar{Close: 100}
	ind := s.IndicatorData{EMA9: 105, EMA21: 106, EMA50: 107}
	r := s.ScoreEMAStack(bar, ind, true)
	assert.Len(t, r.Components, 1)
	assert.False(t, r.Components[0].Fired)
	assert.Equal(t, 0, r.Components[0].Weight)
}

func TestComponentScore_ADX_Strong(t *testing.T) {
	r := s.ScoreADX(s.IndicatorData{ADX: 35})
	assert.Len(t, r.Components, 1)
	cs := r.Components[0]
	assert.Equal(t, "adx_strong", cs.Name)
	assert.Equal(t, "technical", cs.Group)
	assert.Equal(t, 15, cs.Weight)
	assert.True(t, cs.Fired)
	assert.Equal(t, 35.0, cs.Value)
}

func TestComponentScore_RSI_Ideal(t *testing.T) {
	r := s.ScoreRSI(s.IndicatorData{RSI: 55}, true)
	assert.Len(t, r.Components, 1)
	cs := r.Components[0]
	assert.Equal(t, "rsi_ideal", cs.Name)
	assert.Equal(t, 10, cs.Weight)
	assert.True(t, cs.Fired)
	assert.Equal(t, 55.0, cs.Value)
}

func TestComponentScore_DarkPool_AllFired(t *testing.T) {
	ind := s.IndicatorData{DPRatio: 0.55, DPRatioZScore: 2.0, DPBuyRatio: 0.65, DPLargePrintPct: 0.20}
	r := s.ScoreDarkPool(ind, true)
	assert.Equal(t, 10, r.Score)
	assert.Len(t, r.Components, 3)

	elevated := r.Components[0]
	assert.Equal(t, "dp_elevated", elevated.Name)
	assert.Equal(t, "darkpool", elevated.Group)
	assert.Equal(t, 3, elevated.Weight)
	assert.True(t, elevated.Fired)

	buy := r.Components[1]
	assert.Equal(t, "dp_buy", buy.Name)
	assert.Equal(t, 4, buy.Weight)
	assert.True(t, buy.Fired)

	blocks := r.Components[2]
	assert.Equal(t, "dp_blocks", blocks.Name)
	assert.Equal(t, 3, blocks.Weight)
	assert.True(t, blocks.Fired)
}

func TestComponentScore_DarkPool_NoData(t *testing.T) {
	r := s.ScoreDarkPool(s.IndicatorData{DPRatio: 0}, true)
	assert.Equal(t, 0, r.Score)
	assert.Len(t, r.Components, 3)
	for _, cs := range r.Components {
		assert.False(t, cs.Fired)
		assert.Equal(t, "darkpool", cs.Group)
	}
}

func TestComponentScore_RetestQuality_Perfect(t *testing.T) {
	r := s.ScoreRetestQuality(2, 0.30, 50, 100, 0.8, true)
	assert.Equal(t, 16, r.Score)
	assert.Len(t, r.Components, 4)
	for _, cs := range r.Components {
		assert.Equal(t, "retest", cs.Group)
		assert.True(t, cs.Fired, "component %s should fire", cs.Name)
	}
}

func TestComponentScore_BaseConfluence_AllComponents(t *testing.T) {
	bar := s.Bar{Open: 98.5, High: 102, Low: 98, Close: 101.5, Volume: 1800}
	ind := s.IndicatorData{
		EMA9: 101, EMA21: 100, EMA50: 99,
		ADX: 30, RSI: 55,
		VolumeSMA:  1000,
		BBPercentB: 0.65,
		VWAP:       100,
		HTF:        map[string]s.HTFIndicator{"1d": {Bias: "BULLISH"}},
	}
	r := s.ComputeBaseConfluence(bar, ind, true)
	assert.Equal(t, 78, r.Score)
	assert.Len(t, r.Components, 8, "base confluence should produce exactly 8 components")
	groups := map[string]int{}
	for _, cs := range r.Components {
		groups[cs.Group]++
	}
	assert.Equal(t, 8, groups["technical"])
}

func TestComponentScore_BaseConfluence_Empty(t *testing.T) {
	r := s.ComputeBaseConfluence(s.Bar{}, s.IndicatorData{}, true)
	assert.Equal(t, 0, r.Score)
	assert.Len(t, r.Components, 8, "even with no data, all 8 components should be present")
	for _, cs := range r.Components {
		assert.False(t, cs.Fired)
	}
}

func TestMergeConfluence_Components(t *testing.T) {
	base := s.ComputeBaseConfluence(
		s.Bar{Open: 98.5, High: 102, Low: 98, Close: 101.5, Volume: 1800},
		s.IndicatorData{EMA9: 101, EMA21: 100, EMA50: 99, ADX: 30, RSI: 55, VolumeSMA: 1000, BBPercentB: 0.65, VWAP: 100, HTF: map[string]s.HTFIndicator{"1d": {Bias: "BULLISH"}}},
		true,
	)
	dp := s.ScoreDarkPool(s.IndicatorData{DPRatio: 0.55, DPRatioZScore: 2.0, DPBuyRatio: 0.65, DPLargePrintPct: 0.20}, true)
	merged := s.MergeConfluence(base, dp)
	assert.Equal(t, 88, merged.Score)
	assert.Len(t, merged.Components, 11, "8 base + 3 darkpool")
}
