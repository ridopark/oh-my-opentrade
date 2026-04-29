package strategy_test

import (
	"testing"

	s "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Diagnostic-field assertions — every Score* must populate
// Components[0].SubScore (matching r.Score) and Components[0].Inputs with
// the documented numeric keys, so a SQL diff on payload.confluence.components
// can attribute a live/backtest gate divergence to the specific input that
// disagreed.

func assertSubScore(t *testing.T, r s.ConfluenceResult) {
	t.Helper()
	require.NotEmpty(t, r.Components, "scorer must populate Components even when score is 0")
	assert.Equal(t, r.Score, r.Components[0].SubScore, "Components[0].SubScore must match r.Score")
}

func assertInputsKeys(t *testing.T, r s.ConfluenceResult, keys ...string) {
	t.Helper()
	require.NotEmpty(t, r.Components)
	require.NotNil(t, r.Components[0].Inputs, "Inputs map must be populated")
	for _, k := range keys {
		_, ok := r.Components[0].Inputs[k]
		assert.True(t, ok, "Inputs missing key %q (keys present: %v)", k, r.Components[0].Inputs)
	}
}

func TestScoreEMAStack_DiagFields(t *testing.T) {
	bar := s.Bar{Close: 105}
	ind := s.IndicatorData{EMA9: 104, EMA21: 103, EMA50: 102}
	r := s.ScoreEMAStack(bar, ind, true)
	assertSubScore(t, r)
	assertInputsKeys(t, r, "close", "ema9", "ema21", "ema50", "count")
	assert.Equal(t, 105.0, r.Components[0].Inputs["close"])
	assert.Equal(t, 104.0, r.Components[0].Inputs["ema9"])
}

func TestScoreADX_DiagFields(t *testing.T) {
	r := s.ScoreADX(s.IndicatorData{ADX: 30})
	assertSubScore(t, r)
	assertInputsKeys(t, r, "adx")
	assert.Equal(t, 30.0, r.Components[0].Inputs["adx"])
}

func TestScoreRSI_DiagFields(t *testing.T) {
	r := s.ScoreRSI(s.IndicatorData{RSI: 55}, true)
	assertSubScore(t, r)
	assertInputsKeys(t, r, "rsi")
	assert.Equal(t, 55.0, r.Components[0].Inputs["rsi"])
}

func TestScoreVolume_DiagFields(t *testing.T) {
	r := s.ScoreVolume(s.Bar{Volume: 1500}, s.IndicatorData{VolumeSMA: 1000})
	assertSubScore(t, r)
	assertInputsKeys(t, r, "volume", "volumeSMA", "ratio")
	assert.Equal(t, 1500.0, r.Components[0].Inputs["volume"])
	assert.Equal(t, 1000.0, r.Components[0].Inputs["volumeSMA"])
	assert.InDelta(t, 1.5, r.Components[0].Inputs["ratio"], 1e-9)
}

func TestScoreCandle_DiagFields(t *testing.T) {
	bar := s.Bar{Open: 98.5, High: 102, Low: 98, Close: 101.5, Volume: 1000}
	r := s.ScoreCandle(bar, true)
	assertSubScore(t, r)
	assertInputsKeys(t, r, "bodyRatio", "range", "directional")
	assert.InDelta(t, 4.0, r.Components[0].Inputs["range"], 1e-9)
	assert.Equal(t, 1.0, r.Components[0].Inputs["directional"]) // bullish close → 1
}

func TestScoreBB_DiagFields(t *testing.T) {
	r := s.ScoreBB(s.IndicatorData{BBPercentB: 0.65}, true)
	assertSubScore(t, r)
	assertInputsKeys(t, r, "percentB")
	assert.Equal(t, 0.65, r.Components[0].Inputs["percentB"])
}

func TestScoreHTFBias_DiagFields(t *testing.T) {
	ind := s.IndicatorData{HTF: map[string]s.HTFIndicator{"1d": {Bias: "BULLISH"}}}
	r := s.ScoreHTFBias(ind, true)
	assertSubScore(t, r)
	assertInputsKeys(t, r, "bias")
}

func TestScoreVWAP_DiagFields(t *testing.T) {
	r := s.ScoreVWAP(s.Bar{Close: 105}, s.IndicatorData{VWAP: 100}, true)
	assertSubScore(t, r)
	assertInputsKeys(t, r, "close", "vwap", "distancePct")
	assert.Equal(t, 105.0, r.Components[0].Inputs["close"])
	assert.Equal(t, 100.0, r.Components[0].Inputs["vwap"])
}

func TestScoreDarkPool_DiagFields(t *testing.T) {
	ind := s.IndicatorData{
		DPRatio:         0.35,
		DPRatioZScore:   1.8,
		DPBuyRatio:      0.6,
		DPLargePrintPct: 0.15,
	}
	r := s.ScoreDarkPool(ind, true)
	assertSubScore(t, r)
	assertInputsKeys(t, r, "dpRatio", "dpRatioZScore", "dpBuyRatio", "dpLargePrintPct")
	assert.Equal(t, 0.35, r.Components[0].Inputs["dpRatio"])
	assert.Equal(t, 1.8, r.Components[0].Inputs["dpRatioZScore"])
	assert.Equal(t, 0.6, r.Components[0].Inputs["dpBuyRatio"])
	assert.Equal(t, 0.15, r.Components[0].Inputs["dpLargePrintPct"])
}

func TestScoreRetestQuality_DiagFields(t *testing.T) {
	r := s.ScoreRetestQuality(2, 0.30, 50, 100, 0.8, true)
	assertSubScore(t, r)
	assertInputsKeys(t, r, "retestBarCount", "pullbackDepthPct", "volRatio", "confirmBodyRatio")
	assert.Equal(t, 2.0, r.Components[0].Inputs["retestBarCount"])
	assert.Equal(t, 0.30, r.Components[0].Inputs["pullbackDepthPct"])
	assert.InDelta(t, 0.5, r.Components[0].Inputs["volRatio"], 1e-9) // 50/100
	assert.Equal(t, 0.8, r.Components[0].Inputs["confirmBodyRatio"])
}
