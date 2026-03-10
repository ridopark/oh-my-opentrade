package builtin_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func aiScalperParams() map[string]any {
	return map[string]any{
		"rsi_long":           30.0,
		"rsi_short":          70.0,
		"stoch_long":         20.0,
		"stoch_short":        80.0,
		"rsi_exit_mid":       50.0,
		"allow_regimes":      []any{"BALANCE", "REVERSAL"},
		"cooldown_seconds":   60,
		"max_trades_per_day": 10,
		"ai_enabled":         true,
		"ai_timeout_seconds": 3,
		"ai_min_confidence":  0.65,
		"size_mult_min":      0.5,
		"size_mult_base":     1.0,
		"size_mult_max":      1.5,
	}
}

func TestAIScalperStrategy_Meta(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	meta := s.Meta()
	assert.Equal(t, "ai_scalping_v1", meta.ID.String())
	assert.Equal(t, "1.0.0", meta.Version.String())
	assert.Equal(t, "AI-Enhanced Scalping", meta.Name)
	assert.Equal(t, "system", meta.Author)
}

func TestAIScalperStrategy_WarmupBars(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	assert.Equal(t, 30, s.WarmupBars())
}

func TestAIScalperStrategy_Init_Fresh(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)
	require.NotNil(t, st)
	_, ok := st.(*builtin.AIScalperState)
	assert.True(t, ok, "Init should return *AIScalperState")
}

func TestAIScalperStrategy_ImplementsInterface(t *testing.T) {
	var _ strat.Strategy = (*builtin.AIScalperStrategy)(nil)
}

func TestAIScalperStrategy_LongEntry(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	// Bar 1: set prev stochastic K<D
	scalperSt := st.(*builtin.AIScalperState)
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 25, StochK: 15, StochD: 18})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now(), Close: 100}, st)
	require.NoError(t, err)
	assert.Empty(t, signals)

	// Bar 2: cross up and mean-reversion long thresholds met
	scalperSt = st.(*builtin.AIScalperState)
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 25, StochK: 19, StochD: 18})
	st, signals, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now().Add(time.Minute), Close: 101.2345}, st)
	require.NoError(t, err)

	require.Len(t, signals, 1)
	sig := signals[0]
	assert.Equal(t, strat.SignalEntry, sig.Type)
	assert.Equal(t, strat.SideBuy, sig.Side)
	assert.Equal(t, "AAPL", sig.Symbol)
	assert.Equal(t, "ai_scalp_long", sig.Tags["setup"])
	assert.Equal(t, "up", sig.Tags["cross"])
	assert.Equal(t, "true", sig.Tags["ai_requested"])
	assert.NotEmpty(t, sig.Tags["ai_request_id"])
	assert.Equal(t, "101.2345000000", sig.Tags["ref_price"])
}

func TestAIScalperStrategy_ShortEntry(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	scalperSt := st.(*builtin.AIScalperState)
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 75, StochK: 85, StochD: 82})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now(), Close: 100}, st)
	require.NoError(t, err)
	assert.Empty(t, signals)

	scalperSt = st.(*builtin.AIScalperState)
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 75, StochK: 81, StochD: 82})
	st, signals, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now().Add(time.Minute), Close: 99.5000}, st)
	require.NoError(t, err)

	require.Len(t, signals, 1)
	sig := signals[0]
	assert.Equal(t, strat.SignalEntry, sig.Type)
	assert.Equal(t, strat.SideSell, sig.Side)
	assert.Equal(t, "ai_scalp_short", sig.Tags["setup"])
	assert.Equal(t, "down", sig.Tags["cross"])
}

func TestAIScalperStrategy_NoEntryInTrend(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	regimes := map[string]strat.AnchorRegime{"5m": {Type: "TREND", Strength: 0.8}}

	scalperSt := st.(*builtin.AIScalperState)
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 25, StochK: 15, StochD: 18, AnchorRegimes: regimes})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now(), Close: 100}, st)
	require.NoError(t, err)

	scalperSt = st.(*builtin.AIScalperState)
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 25, StochK: 19, StochD: 18, AnchorRegimes: regimes})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now().Add(time.Minute), Close: 101}, st)
	require.NoError(t, err)
	assert.Empty(t, signals)
}

func TestAIScalperStrategy_ExitLong_RSIMid(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	scalperSt := st.(*builtin.AIScalperState)
	scalperSt.PositionSide = strat.SideBuy
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 55, StochK: 10, StochD: 12})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now(), Close: 101}, st)
	require.NoError(t, err)
	require.Len(t, signals, 1)

	sig := signals[0]
	assert.Equal(t, strat.SignalExit, sig.Type)
	assert.Equal(t, strat.SideSell, sig.Side)
	assert.Equal(t, "ai_scalp_exit", sig.Tags["setup"])
}

func TestAIScalperStrategy_ExitLong_RegimeFlipTrend(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	regimes := map[string]strat.AnchorRegime{"5m": {Type: "TREND", Strength: 0.9}}

	scalperSt := st.(*builtin.AIScalperState)
	scalperSt.PositionSide = strat.SideBuy
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 40, StochK: 10, StochD: 12, AnchorRegimes: regimes})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now(), Close: 101}, st)
	require.NoError(t, err)
	require.Len(t, signals, 1)
	assert.Equal(t, strat.SignalExit, signals[0].Type)
	assert.Equal(t, strat.SideSell, signals[0].Side)
	assert.Equal(t, "TREND", signals[0].Tags["regime_5m"])
}

func TestAIScalperStrategy_ExitShort_RSIMid(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	scalperSt := st.(*builtin.AIScalperState)
	scalperSt.PositionSide = strat.SideSell
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 45, StochK: 90, StochD: 88})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now(), Close: 99}, st)
	require.NoError(t, err)
	require.Len(t, signals, 1)
	assert.Equal(t, strat.SignalExit, signals[0].Type)
	assert.Equal(t, strat.SideBuy, signals[0].Side)
}

func TestAIScalperStrategy_CooldownPreventsEntry(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	// Enter long on bar 2.
	scalperSt := st.(*builtin.AIScalperState)
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 25, StochK: 15, StochD: 18})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now(), Close: 100}, st)
	require.NoError(t, err)

	scalperSt = st.(*builtin.AIScalperState)
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 25, StochK: 19, StochD: 18})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now().Add(time.Minute), Close: 101}, st)
	require.NoError(t, err)
	require.Len(t, signals, 1)

	// Next bar should be blocked by cooldown.
	scalperSt = st.(*builtin.AIScalperState)
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 25, StochK: 15, StochD: 18})
	st, signals, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now().Add(2 * time.Minute), Close: 102}, st)
	require.NoError(t, err)
	assert.Empty(t, signals)
}

func TestAIScalperStrategy_MaxTradesPerDay(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	scalperSt := st.(*builtin.AIScalperState)
	scalperSt.TradesToday = scalperSt.Config.MaxTradesPerDay

	scalperSt.SetIndicators(strat.IndicatorData{RSI: 25, StochK: 15, StochD: 18})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now(), Close: 100}, st)
	require.NoError(t, err)

	scalperSt = st.(*builtin.AIScalperState)
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 25, StochK: 19, StochD: 18})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now().Add(time.Minute), Close: 101}, st)
	require.NoError(t, err)
	assert.Empty(t, signals)
}

func TestAIScalperStrategy_OnEvent_AIVeto(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	scalperSt := st.(*builtin.AIScalperState)
	scalperSt.PositionSide = strat.SideBuy
	scalperSt.LastBarClose = 101.1111
	scalperSt.PendingAIRequestID = "req1"

	st2, signals, err := s.OnEvent(ctx, "AAPL", builtin.AIDebateResult{RequestID: "req1", Verdict: "veto", Confidence: 0.9}, st)
	require.NoError(t, err)
	require.Len(t, signals, 1)
	sig := signals[0]
	assert.Equal(t, strat.SignalFlat, sig.Type)
	assert.Equal(t, strat.SideSell, sig.Side)
	assert.Equal(t, "ai_veto", sig.Tags["setup"])
	assert.Equal(t, "veto", sig.Tags["ai_verdict"])

	scalperSt2 := st2.(*builtin.AIScalperState)
	assert.Equal(t, strat.Side(""), scalperSt2.PositionSide)
	assert.Equal(t, "", scalperSt2.PendingAIRequestID)
}

func TestAIScalperStrategy_OnEvent_AIAdjustSizing(t *testing.T) {
	cases := []struct {
		name       string
		verdict    string
		confidence float64
		posSide    strat.Side
		wantMult   string
	}{
		{name: "bull_long_scales_up", verdict: "bull", confidence: 0.90, posSide: strat.SideBuy, wantMult: "1.50"},
		{name: "bear_long_scales_down", verdict: "bear", confidence: 0.90, posSide: strat.SideBuy, wantMult: "0.50"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := builtin.NewAIScalperStrategy()
			ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
			st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
			require.NoError(t, err)

			scalperSt := st.(*builtin.AIScalperState)
			scalperSt.PositionSide = tc.posSide
			scalperSt.LastBarClose = 99.0000
			scalperSt.PendingAIRequestID = "req42"

			st2, signals, err := s.OnEvent(ctx, "AAPL", builtin.AIDebateResult{RequestID: "req42", Verdict: tc.verdict, Confidence: tc.confidence}, st)
			require.NoError(t, err)
			require.Len(t, signals, 1)

			sig := signals[0]
			assert.Equal(t, strat.SignalAdjust, sig.Type)
			assert.Equal(t, tc.posSide, sig.Side)
			assert.Equal(t, tc.verdict, sig.Tags["ai_verdict"])
			assert.Equal(t, tc.wantMult, sig.Tags["size_mult"])

			scalperSt2 := st2.(*builtin.AIScalperState)
			assert.Equal(t, "", scalperSt2.PendingAIRequestID)
		})
	}
}

func TestAIScalperStrategy_OnEvent_UnmatchedRequestID(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	scalperSt := st.(*builtin.AIScalperState)
	scalperSt.PositionSide = strat.SideBuy
	scalperSt.PendingAIRequestID = "req_good"

	st2, signals, err := s.OnEvent(ctx, "AAPL", builtin.AIDebateResult{RequestID: "req_bad", Verdict: "veto", Confidence: 0.9}, st)
	require.NoError(t, err)
	assert.Empty(t, signals)
	assert.Equal(t, "req_good", st2.(*builtin.AIScalperState).PendingAIRequestID)
}

func TestAIScalperStrategy_OnEvent_UnrecognizedEvent(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	st2, signals, err := s.OnEvent(ctx, "AAPL", "random", st)
	require.NoError(t, err)
	assert.Empty(t, signals)
	assert.Equal(t, st, st2)
}

func TestAIScalperState_MarshalUnmarshal(t *testing.T) {
	now := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	st := &builtin.AIScalperState{
		Symbol:             "AAPL",
		Indicators:         strat.IndicatorData{RSI: 55, StochK: 70, StochD: 65, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}},
		PrevStochK:         70,
		PrevStochD:         65,
		TradesToday:        3,
		CooldownUntil:      now.Add(30 * time.Second),
		PositionSide:       strat.SideBuy,
		PendingAIRequestID: "req",
		LastAIVerdict:      "bull",
		LastAIConfidence:   0.77,
		LastAIAt:           now.Add(-time.Minute),
		LastSizeMult:       1.5,
		LastBarClose:       101.0001,
		Config:             builtin.AIScalperConfig{RSILong: 30, RSIShort: 70, StochLong: 20, StochShort: 80, RSIExitMid: 50, AllowRegimes: []string{"BALANCE"}, CooldownSeconds: 60, MaxTradesPerDay: 10, AIEnabled: true, AITimeoutSeconds: 3, AIMinConfidence: 0.65, SizeMultMin: 0.5, SizeMultBase: 1.0, SizeMultMax: 1.5},
	}

	data, err := st.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &builtin.AIScalperState{}
	require.NoError(t, restored.Unmarshal(data))

	assert.Equal(t, st.Symbol, restored.Symbol)
	assert.Equal(t, st.Indicators.RSI, restored.Indicators.RSI)
	assert.Equal(t, st.Indicators.AnchorRegimes["5m"].Type, restored.Indicators.AnchorRegimes["5m"].Type)
	assert.Equal(t, st.PrevStochK, restored.PrevStochK)
	assert.Equal(t, st.TradesToday, restored.TradesToday)
	assert.True(t, st.CooldownUntil.Equal(restored.CooldownUntil))
	assert.Equal(t, st.PositionSide, restored.PositionSide)
	assert.Equal(t, st.PendingAIRequestID, restored.PendingAIRequestID)
	assert.Equal(t, st.LastAIVerdict, restored.LastAIVerdict)
	assert.Equal(t, st.LastAIConfidence, restored.LastAIConfidence)
	assert.True(t, st.LastAIAt.Equal(restored.LastAIAt))
	assert.Equal(t, st.LastSizeMult, restored.LastSizeMult)
	assert.Equal(t, st.LastBarClose, restored.LastBarClose)
	assert.Equal(t, st.Config, restored.Config)
}

func TestAIScalperStrategy_OnEvent_FillConfirmation(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	scalperSt := st.(*builtin.AIScalperState)
	scalperSt.PendingEntry = strat.SideBuy
	scalperSt.PendingEntryAt = ctx.Now()

	st2, signals, err := s.OnEvent(ctx, "AAPL", strat.FillConfirmation{Symbol: "AAPL", Side: strat.SideBuy, Price: 150.0, Quantity: 10}, st)
	require.NoError(t, err)
	assert.Empty(t, signals)

	scalperSt2 := st2.(*builtin.AIScalperState)
	assert.Equal(t, strat.SideBuy, scalperSt2.PositionSide, "fill should promote PendingEntry to PositionSide")
	assert.Equal(t, strat.Side(""), scalperSt2.PendingEntry, "PendingEntry should be cleared after fill")
	assert.True(t, scalperSt2.PendingEntryAt.IsZero(), "PendingEntryAt should be reset")
}

func TestAIScalperStrategy_OnEvent_EntryRejection(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	scalperSt := st.(*builtin.AIScalperState)
	scalperSt.PendingEntry = strat.SideBuy
	scalperSt.PendingEntryAt = ctx.Now()

	st2, signals, err := s.OnEvent(ctx, "AAPL", strat.EntryRejection{Symbol: "AAPL", Side: strat.SideBuy, Reason: "DTBP exhausted"}, st)
	require.NoError(t, err)
	assert.Empty(t, signals)

	scalperSt2 := st2.(*builtin.AIScalperState)
	assert.Equal(t, strat.Side(""), scalperSt2.PositionSide, "PositionSide should remain empty after rejection")
	assert.Equal(t, strat.Side(""), scalperSt2.PendingEntry, "PendingEntry should be cleared after rejection")
	assert.True(t, scalperSt2.PendingEntryAt.IsZero(), "PendingEntryAt should be reset")
}

func TestAIScalperStrategy_PendingEntryBlocksExit(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	// Simulate a pending entry — position not confirmed yet.
	scalperSt := st.(*builtin.AIScalperState)
	// scalperSt.PositionSide is empty — entry not yet confirmed.
	scalperSt.PendingEntry = strat.SideBuy
	scalperSt.PendingEntryAt = ctx.Now()
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 55, StochK: 10, StochD: 12})

	// RSI > exit_mid would normally trigger exit, but PendingEntry should block it.
	st2, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now(), Close: 101}, st)
	require.NoError(t, err)
	assert.Empty(t, signals, "should not emit exit while PendingEntry is set")
	_ = st2
}

func TestAIScalperStrategy_PendingEntryBlocksNewEntry(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	// Set up cross for bar 1.
	scalperSt := st.(*builtin.AIScalperState)
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 25, StochK: 15, StochD: 18})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now(), Close: 100}, st)
	require.NoError(t, err)

	// Bar 2: triggers entry → sets PendingEntry.
	scalperSt = st.(*builtin.AIScalperState)
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 25, StochK: 19, StochD: 18})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now().Add(time.Minute), Close: 101}, st)
	require.NoError(t, err)
	require.Len(t, signals, 1)

	scalperSt = st.(*builtin.AIScalperState)
	assert.Equal(t, strat.SideBuy, scalperSt.PendingEntry, "PendingEntry should be set")
	assert.Equal(t, strat.Side(""), scalperSt.PositionSide, "PositionSide should remain empty until fill")

	// Bar 3: same conditions, but should be blocked because PendingEntry is set.
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 25, StochK: 15, StochD: 18})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now().Add(2 * time.Minute), Close: 100}, st)
	require.NoError(t, err)

	scalperSt = st.(*builtin.AIScalperState)
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 25, StochK: 19, StochD: 18})
	st, signals, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now().Add(3 * time.Minute), Close: 101}, st)
	require.NoError(t, err)
	assert.Empty(t, signals, "should not emit second entry while PendingEntry is set")
}

func TestAIScalperStrategy_PendingEntryTimeout(t *testing.T) {
	s := builtin.NewAIScalperStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", aiScalperParams(), nil)
	require.NoError(t, err)

	scalperSt := st.(*builtin.AIScalperState)
	scalperSt.PendingEntry = strat.SideBuy
	scalperSt.PendingEntryAt = ctx.Now().Add(-6 * time.Minute) // 6 min ago, past the 5 min timeout
	scalperSt.SetIndicators(strat.IndicatorData{RSI: 40, StochK: 50, StochD: 50})

	st2, _, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: ctx.Now(), Close: 100}, st)
	require.NoError(t, err)

	scalperSt2 := st2.(*builtin.AIScalperState)
	assert.Equal(t, strat.Side(""), scalperSt2.PendingEntry, "PendingEntry should be cleared after timeout")
	assert.True(t, scalperSt2.PendingEntryAt.IsZero(), "PendingEntryAt should be reset after timeout")
}

func TestAIScalperState_MarshalUnmarshal_PendingEntry(t *testing.T) {
	now := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	st := &builtin.AIScalperState{
		Symbol:         "AAPL",
		PositionSide:   strat.Side(""),
		PendingEntry:   strat.SideBuy,
		PendingEntryAt: now,
		Config:         builtin.AIScalperConfig{RSILong: 30, RSIShort: 70, StochLong: 20, StochShort: 80, RSIExitMid: 50, AllowRegimes: []string{"BALANCE"}, CooldownSeconds: 60, MaxTradesPerDay: 10},
	}

	data, err := st.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &builtin.AIScalperState{}
	require.NoError(t, restored.Unmarshal(data))

	assert.Equal(t, strat.SideBuy, restored.PendingEntry)
	assert.True(t, now.Equal(restored.PendingEntryAt))
	assert.Equal(t, strat.Side(""), restored.PositionSide)
}
