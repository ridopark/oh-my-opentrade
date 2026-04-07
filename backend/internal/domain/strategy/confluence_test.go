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
	a := s.ConfluenceResult{Score: 30, Factors: []string{"ema_stack", "adx_strong"}}
	b := s.ConfluenceResult{Score: 12, Factors: []string{"macd_specific"}}
	merged := s.MergeConfluence(a, b)
	assert.Equal(t, 42, merged.Score)
	assert.Len(t, merged.Factors, 3)
	assert.Contains(t, merged.Factors, "macd_specific")
}

func TestMergeConfluence_Empty(t *testing.T) {
	r := s.MergeConfluence()
	assert.Equal(t, 0, r.Score)
	assert.Empty(t, r.Factors)
}

func TestConfluenceResult_FormatDetail(t *testing.T) {
	r := s.ConfluenceResult{Score: 50, Factors: []string{"ema_stack", "adx_strong", "vwap_aligned"}}
	assert.Equal(t, "ema_stack+adx_strong+vwap_aligned", r.FormatDetail())
}
