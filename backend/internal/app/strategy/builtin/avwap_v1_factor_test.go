package builtin

import (
	"testing"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// factorTestState constructs an AVWAPState with a single HVN bin populated
// at price index 5000 (the anchor floor offset chosen so binIdx-priceIdx
// math matches across tests). Returns the state plus the bar.Close that
// would yield distATR=0 (i.e. price exactly at the bin center).
func factorTestState(t *testing.T, atr, anchor, binBps float64, hvnBins []int) *AVWAPState {
	t.Helper()
	s := &AVWAPState{
		Indicators: start.IndicatorData{ATR: atr},
	}
	s.hvn.Reset(binBps)
	for _, bin := range hvnBins {
		if s.hvn.hvnSet == nil {
			s.hvn.hvnSet = make(map[int]struct{})
		}
		s.hvn.hvnSet[bin] = struct{}{}
	}
	s.hvn.anchor = anchor
	return s
}

// barAtBinCenter returns the bar.Close that places priceIdx exactly at
// targetIdx for the (anchor, binBps) pair the state was constructed
// with. Use to drive distATR to a specific value in a fixture.
func barAtBinCenter(anchor, binBps float64, targetIdx int) start.Bar {
	binWidth := anchor * binBps * 1e-4
	if binWidth < 0.01 {
		binWidth = 0.01
	}
	anchorFloor := anchor - 5000*binWidth
	close := anchorFloor + (float64(targetIdx)+0.5)*binWidth
	return start.Bar{Time: time.Now(), Close: close, High: close, Low: close}
}

func factorEntryContext(cfg AVWAPConfig, bar start.Bar) entryContext {
	return entryContext{
		cfg:    cfg,
		bar:    bar,
		symbol: "AAPL",
	}
}

func factorEntrySignal(side start.Side) *start.Signal {
	id, _ := start.NewInstanceID("avwap_v4_equity:1.0.0:AAPL")
	sig, _ := start.NewSignal(id, "AAPL", start.SignalEntry, side, 1.0, map[string]string{
		"setup": "avwap_breakout",
	})
	return &sig
}

// 1. Defaults OFF: applyHVNVeto returns the input signal unchanged with
//    no factor tags.
func TestAVWAP_HVNFactor_Off_PassThrough(t *testing.T) {
	cfg := AVWAPConfig{HVNFactorEnabled: false, HVNFactorShadow: false}
	s := factorTestState(t, 1.0, 100.0, 10.0, []int{5000})
	bar := barAtBinCenter(100.0, 10.0, 5000) // distATR == 0 (right on HVN)
	sig := factorEntrySignal(start.SideBuy)
	got, err := s.applyHVNVeto(factorEntryContext(cfg, bar), sig, nil)
	require.NoError(t, err)
	require.NotNil(t, got, "factor disabled should pass signal through even at distATR=0")
	assert.Empty(t, got.Tags["hvn_factor_would_block"], "no factor tag when factor is disabled")
	assert.Empty(t, got.Tags["hvn_factor_dist_atr"], "no factor tag when factor is disabled")
}

// 2. Shadow mode: tag emitted, signal still returned (not blocked).
func TestAVWAP_HVNFactor_Shadow_TagsButPasses(t *testing.T) {
	cfg := AVWAPConfig{
		HVNFactorEnabled:    false,
		HVNFactorShadow:     true,
		HVNFactorLongOnly:   true,
		HVNFactorNearATRMax: 0.5,
	}
	s := factorTestState(t, 1.0, 100.0, 10.0, []int{5000})
	bar := barAtBinCenter(100.0, 10.0, 5000)
	sig := factorEntrySignal(start.SideBuy)
	got, err := s.applyHVNVeto(factorEntryContext(cfg, bar), sig, nil)
	require.NoError(t, err)
	require.NotNil(t, got, "shadow mode never blocks")
	assert.Equal(t, "1", got.Tags["hvn_factor_would_block"], "shadow must tag the would-block decision")
	assert.NotEmpty(t, got.Tags["hvn_factor_dist_atr"], "shadow must emit dist_atr for the harness")
}

// 3. Active mode + near-HVN entry: signal blocked.
func TestAVWAP_HVNFactor_Active_BlocksNearHVN(t *testing.T) {
	cfg := AVWAPConfig{
		HVNFactorEnabled:    true,
		HVNFactorShadow:     false,
		HVNFactorLongOnly:   true,
		HVNFactorNearATRMax: 0.5,
	}
	s := factorTestState(t, 1.0, 100.0, 10.0, []int{5000})
	bar := barAtBinCenter(100.0, 10.0, 5000) // distATR = 0
	sig := factorEntrySignal(start.SideBuy)
	got, err := s.applyHVNVeto(factorEntryContext(cfg, bar), sig, nil)
	require.NoError(t, err)
	assert.Nil(t, got, "active near-HVN must block the entry")
}

// 4. Active mode + far-HVN entry: signal returned.
func TestAVWAP_HVNFactor_Active_PassesFarHVN(t *testing.T) {
	cfg := AVWAPConfig{
		HVNFactorEnabled:    true,
		HVNFactorShadow:     false,
		HVNFactorLongOnly:   true,
		HVNFactorNearATRMax: 0.5,
	}
	s := factorTestState(t, 1.0, 100.0, 10.0, []int{5000})
	// Place price 100 bins away => distATR = 100 * 0.01 / 1.0 = 1.0 (far)
	bar := barAtBinCenter(100.0, 10.0, 5100)
	sig := factorEntrySignal(start.SideBuy)
	got, err := s.applyHVNVeto(factorEntryContext(cfg, bar), sig, nil)
	require.NoError(t, err)
	require.NotNil(t, got, "far-HVN entry must pass the factor")
	assert.Empty(t, got.Tags["hvn_factor_would_block"], "no would-block tag when factor would not fire")
}

// 5. LongOnly gate: SHORT entries pass when long_only=true.
func TestAVWAP_HVNFactor_LongOnlyGate(t *testing.T) {
	bar := barAtBinCenter(100.0, 10.0, 5000)
	sigShort := factorEntrySignal(start.SideSell)

	cfgLongOnly := AVWAPConfig{
		HVNFactorEnabled:    true,
		HVNFactorLongOnly:   true,
		HVNFactorNearATRMax: 0.5,
	}
	s := factorTestState(t, 1.0, 100.0, 10.0, []int{5000})
	got, err := s.applyHVNVeto(factorEntryContext(cfgLongOnly, bar), sigShort, nil)
	require.NoError(t, err)
	require.NotNil(t, got, "long_only=true must skip SHORT entries")

	cfgBoth := cfgLongOnly
	cfgBoth.HVNFactorLongOnly = false
	s2 := factorTestState(t, 1.0, 100.0, 10.0, []int{5000})
	got2, err := s2.applyHVNVeto(factorEntryContext(cfgBoth, bar), factorEntrySignal(start.SideSell), nil)
	require.NoError(t, err)
	assert.Nil(t, got2, "long_only=false must let factor fire on SHORT entries")
}

// 6. Chain order: cofire blocks first; HVN doesn't see a nil signal.
//    applyHVNVeto must early-return when sig is nil (cofire already
//    blocked).
func TestAVWAP_HVNAndCofire_ChainOrder_NilPassthrough(t *testing.T) {
	cfg := AVWAPConfig{HVNFactorEnabled: true, HVNFactorNearATRMax: 0.5, HVNFactorLongOnly: true}
	s := factorTestState(t, 1.0, 100.0, 10.0, []int{5000})
	bar := barAtBinCenter(100.0, 10.0, 5000)
	got, err := s.applyHVNVeto(factorEntryContext(cfg, bar), nil, nil)
	require.NoError(t, err)
	assert.Nil(t, got, "nil sig in must produce nil sig out (chain order with cofire)")
}

// 7. EMA factor OFF: hold bars unchanged, no would-extend tag.
func TestAVWAP_EMAFactor_Off_HoldBarsUnchanged(t *testing.T) {
	cfg := AVWAPConfig{
		ExitHoldBars:               2,
		EMAFactorEnabled:           false,
		EMAFactorShadow:            false,
		EMAFactorMaxBarsSinceCross: 12,
		EMAFactorMinBreachATR:      1.0,
		EMAFactorHoldBarsMult:      1.5,
		Anchors:                    []string{"session_open"},
	}
	s := &AVWAPState{
		Symbol:                 "AAPL",
		PositionSide:           start.SideBuy,
		AboveCount:             map[string]int{"session_open": 0},
		BelowCount:             map[string]int{"session_open": 5}, // exit threshold met
		AVWAPCrossBarsSince:    map[string]int{"session_open": 3},
		AVWAPCrossBreachMaxATR: map[string]float64{"session_open": 2.0},
	}
	id, _ := start.NewInstanceID("avwap_v4_equity:1.0.0:AAPL")
	ec := entryContext{
		cfg:        cfg,
		bar:        start.Bar{Time: time.Now(), Close: 100.0},
		symbol:     "AAPL",
		instanceID: id,
		now:        time.Now(),
		cooldown:   time.Minute,
		regimeTag:  "TREND_UP",
	}
	sig, err := s.evaluateBasicExit(ec)
	require.NoError(t, err)
	require.NotNil(t, sig, "exit must fire at hold_bars threshold")
	assert.Empty(t, sig.Tags["ema_factor_would_extend"], "no would-extend tag when factor is off")
	assert.Empty(t, sig.Tags["ema_factor_extended"], "no extended tag when factor is off")
	assert.Empty(t, sig.Tags["ema_factor_favorable"], "no favorable tag when factor is off")
}

// 8. EMA factor SHADOW: would-extend tagged, but exit fires at original
//    hold_bars (no actual extension).
func TestAVWAP_EMAFactor_Shadow_TagsButHoldBarsUnchanged(t *testing.T) {
	cfg := AVWAPConfig{
		ExitHoldBars:               2,
		EMAFactorEnabled:           false,
		EMAFactorShadow:            true,
		EMAFactorMaxBarsSinceCross: 12,
		EMAFactorMinBreachATR:      1.0,
		EMAFactorHoldBarsMult:      1.5,
		Anchors:                    []string{"session_open"},
	}
	s := &AVWAPState{
		Symbol:                 "AAPL",
		PositionSide:           start.SideBuy,
		AboveCount:             map[string]int{"session_open": 0},
		BelowCount:             map[string]int{"session_open": 2}, // exact threshold; not extended
		AVWAPCrossBarsSince:    map[string]int{"session_open": 3},
		AVWAPCrossBreachMaxATR: map[string]float64{"session_open": 2.0},
	}
	id, _ := start.NewInstanceID("avwap_v4_equity:1.0.0:AAPL")
	ec := entryContext{
		cfg:        cfg,
		bar:        start.Bar{Time: time.Now(), Close: 100.0},
		symbol:     "AAPL",
		instanceID: id,
		now:        time.Now(),
		cooldown:   time.Minute,
		regimeTag:  "TREND_UP",
	}
	sig, err := s.evaluateBasicExit(ec)
	require.NoError(t, err)
	require.NotNil(t, sig, "shadow mode must NOT extend; exit fires at original threshold")
	assert.Equal(t, "1", sig.Tags["ema_factor_would_extend"], "shadow must tag the would-extend decision")
	assert.Empty(t, sig.Tags["ema_factor_extended"], "shadow must not tag extended (that is the active-mode tag)")
}

// 9. EMA factor ACTIVE: hold bars extended; exit deferred until extended
//    threshold reached.
func TestAVWAP_EMAFactor_Active_HoldBarsExtended(t *testing.T) {
	cfg := AVWAPConfig{
		ExitHoldBars:               2,
		EMAFactorEnabled:           true,
		EMAFactorShadow:            false,
		EMAFactorMaxBarsSinceCross: 12,
		EMAFactorMinBreachATR:      1.0,
		EMAFactorHoldBarsMult:      1.5, // 2 * 1.5 = 3 effective
		Anchors:                    []string{"session_open"},
	}
	id, _ := start.NewInstanceID("avwap_v4_equity:1.0.0:AAPL")
	makeState := func(belowCount int) *AVWAPState {
		return &AVWAPState{
			Symbol:                 "AAPL",
			PositionSide:           start.SideBuy,
			AboveCount:             map[string]int{"session_open": 0},
			BelowCount:             map[string]int{"session_open": belowCount},
			AVWAPCrossBarsSince:    map[string]int{"session_open": 3},
			AVWAPCrossBreachMaxATR: map[string]float64{"session_open": 2.0},
		}
	}
	makeEC := func() entryContext {
		return entryContext{
			cfg:        cfg,
			bar:        start.Bar{Time: time.Now(), Close: 100.0},
			symbol:     "AAPL",
			instanceID: id,
			now:        time.Now(),
			cooldown:   time.Minute,
			regimeTag:  "TREND_UP",
		}
	}

	// At belowCount=2: original threshold reached, but extended (1.5x) =
	// 3, so exit must NOT fire yet.
	sigEarly, err := makeState(2).evaluateBasicExit(makeEC())
	require.NoError(t, err)
	assert.Nil(t, sigEarly, "active extension must defer exit when belowCount < extended threshold")

	// At belowCount=3: extended threshold reached, exit fires.
	sigLate, err := makeState(3).evaluateBasicExit(makeEC())
	require.NoError(t, err)
	require.NotNil(t, sigLate, "exit must fire at extended threshold")
	assert.Equal(t, "1", sigLate.Tags["ema_factor_extended"], "active mode must tag extended exits")
	assert.Empty(t, sigLate.Tags["ema_factor_would_extend"], "active mode must NOT use would-extend tag")
}
