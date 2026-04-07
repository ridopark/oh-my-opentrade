package gate

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- GateResult ---

func TestGateResult_Error(t *testing.T) {
	r := &GateResult{GateName: "vix", Reason: "VIX too high"}
	assert.Equal(t, "vix: VIX too high", r.Error())

	// Verify it satisfies the error interface.
	var err error = r
	assert.NotNil(t, err)
}

// --- Chain ---

type passGate struct{ name string }

func (g *passGate) Name() string                                              { return g.name }
func (g *passGate) Check(_ context.Context, _ *MonitorGateContext) *GateResult { return nil }

type blockGate struct {
	name   string
	reason string
}

func (g *blockGate) Name() string { return g.name }
func (g *blockGate) Check(_ context.Context, _ *MonitorGateContext) *GateResult {
	return &GateResult{GateName: g.name, Reason: g.reason}
}

func TestChain_AllPass(t *testing.T) {
	chain := NewMonitorGateChain(
		[]MonitorGate{&passGate{"a"}, &passGate{"b"}},
		zerolog.Nop(),
	)
	result := chain.Run(context.Background(), &MonitorGateContext{})
	assert.Nil(t, result)
}

func TestChain_ShortCircuits(t *testing.T) {
	chain := NewMonitorGateChain(
		[]MonitorGate{
			&passGate{"a"},
			&blockGate{"b", "blocked"},
			&passGate{"c"},
		},
		zerolog.Nop(),
	)
	result := chain.Run(context.Background(), &MonitorGateContext{})
	require.NotNil(t, result)
	assert.Equal(t, "b", result.GateName)
	assert.Equal(t, "blocked", result.Reason)
}

func TestChain_Names(t *testing.T) {
	chain := NewMonitorGateChain(
		[]MonitorGate{&passGate{"x"}, &passGate{"y"}},
		zerolog.Nop(),
	)
	assert.Equal(t, []string{"x", "y"}, chain.Names())
}

// --- Registry ---

func TestRegistry_UnknownGate(t *testing.T) {
	r := NewMonitorGateRegistry()
	_, err := r.BuildChain([]GateConfig{{Name: "nonexistent"}}, nil, zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown monitor gate")
}

func TestRegistry_BuildChain(t *testing.T) {
	r := NewDefaultRegistry()
	chain, err := r.BuildChain(DefaultMonitorGateConfigs(), &GateDeps{}, zerolog.Nop())
	require.NoError(t, err)
	require.NotNil(t, chain)
	assert.Len(t, chain.Names(), 5)
}

// --- VIX Gate ---

func TestVIXGate(t *testing.T) {
	tests := []struct {
		name      string
		skipAbove float64
		vixLevel  float64
		wantBlock bool
	}{
		{"disabled", 0, 30, false},
		{"below threshold", 30, 25, false},
		{"at threshold", 30, 30, false},
		{"above threshold", 30, 35, true},
		{"unknown VIX", 30, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &vixGate{skipAbove: tt.skipAbove}
			gctx := &MonitorGateContext{VIXLevel: tt.vixLevel}
			result := g.Check(context.Background(), gctx)
			if tt.wantBlock {
				require.NotNil(t, result)
				assert.Equal(t, "vix", result.GateName)
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

// --- Regime Gate ---

func TestRegimeGate(t *testing.T) {
	tests := []struct {
		name      string
		allowed   []string
		regime    domain.RegimeType
		wantBlock bool
	}{
		{"empty allowed = pass all", nil, domain.RegimeTrend, false},
		{"allowed regime", []string{"TREND", "BALANCE"}, domain.RegimeTrend, false},
		{"blocked regime", []string{"TREND"}, domain.RegimeBalance, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := make(map[string]bool)
			for _, r := range tt.allowed {
				m[r] = true
			}
			g := &regimeGate{allowedRegimes: m}
			gctx := &MonitorGateContext{
				Regime: domain.MarketRegime{Type: tt.regime},
				Setup:  SetupInput{Symbol: "AAPL"},
			}
			result := g.Check(context.Background(), gctx)
			if tt.wantBlock {
				require.NotNil(t, result)
				assert.Equal(t, "regime", result.GateName)
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

func TestRegimeGate_AnchorTimeframe(t *testing.T) {
	g := &regimeGate{allowedRegimes: map[string]bool{"TREND": true}}
	gctx := &MonitorGateContext{
		Regime:       domain.MarketRegime{Type: domain.RegimeBalance}, // 1m = BALANCE
		ORBTimeframe: domain.Timeframe("5m"),
		Setup:        SetupInput{Symbol: "AAPL"},
		AnchorRegimes: map[string]domain.MarketRegime{
			"AAPL:5m": {Type: domain.RegimeTrend}, // 5m = TREND
		},
	}
	// Should use the 5m anchor (TREND) and pass.
	result := g.Check(context.Background(), gctx)
	assert.Nil(t, result)
}

// --- HTF Bias Gate ---

func TestHTFBiasGate(t *testing.T) {
	tests := []struct {
		name      string
		direction domain.Direction
		bias      string
		wantBlock bool
	}{
		{"long + bearish = blocked", domain.DirectionLong, "BEARISH", true},
		{"short + bullish = blocked", domain.DirectionShort, "BULLISH", true},
		{"long + bullish = pass", domain.DirectionLong, "BULLISH", false},
		{"short + bearish = pass", domain.DirectionShort, "BEARISH", false},
		{"neutral = pass", domain.DirectionLong, "NEUTRAL", false},
		{"missing bias = pass", domain.DirectionLong, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &htfBiasGate{}
			gctx := &MonitorGateContext{
				Setup: SetupInput{Direction: tt.direction},
				Snapshot: domain.IndicatorSnapshot{
					HTF: map[domain.Timeframe]domain.HTFData{
						"1d": {Bias: tt.bias},
					},
				},
			}
			result := g.Check(context.Background(), gctx)
			if tt.wantBlock {
				require.NotNil(t, result)
				assert.Equal(t, "htf_bias", result.GateName)
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

// --- Min ATR% Gate ---

func TestMinATRPctGate(t *testing.T) {
	tests := []struct {
		name      string
		minPct    float64
		dailyATR  float64
		close     float64
		wantBlock bool
	}{
		{"disabled", 0, 5, 100, false},
		{"above min", 2.0, 5, 100, false},     // 5/100*100 = 5%
		{"below min", 5.0, 2, 100, false},     // 2/100*100 = 2% < 5%  -- wait, this should block
		{"below min blocks", 5.0, 2, 100, true}, // correction
		{"zero ATR = pass", 5.0, 0, 100, false},
		{"zero close = pass", 5.0, 5, 0, false},
	}
	// Fix: "below min" and "below min blocks" overlap. Keep only correct ones.
	tests = []struct {
		name      string
		minPct    float64
		dailyATR  float64
		close     float64
		wantBlock bool
	}{
		{"disabled", 0, 5, 100, false},
		{"above min", 2.0, 5, 100, false},
		{"below min", 5.0, 2, 100, true},
		{"zero ATR = pass", 5.0, 0, 100, false},
		{"zero close = pass", 5.0, 5, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &minATRPctGate{minPct: tt.minPct}
			gctx := &MonitorGateContext{
				Bar: domain.MarketBar{Close: tt.close},
				Snapshot: domain.IndicatorSnapshot{
					HTF: map[domain.Timeframe]domain.HTFData{
						"1d": {DailyATR: tt.dailyATR},
					},
				},
			}
			result := g.Check(context.Background(), gctx)
			if tt.wantBlock {
				require.NotNil(t, result)
				assert.Equal(t, "min_atr_pct", result.GateName)
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

// --- DNA Approval Gate ---

type mockDNAChecker struct {
	approved bool
	err      error
}

func (m *mockDNAChecker) IsDNAApproved(_ context.Context, _ string) (bool, error) {
	return m.approved, m.err
}

func TestDNAApprovalGate(t *testing.T) {
	t.Run("nil checker passes", func(t *testing.T) {
		g := &dnaApprovalGate{}
		result := g.Check(context.Background(), &MonitorGateContext{StrategyKey: "orb"})
		assert.Nil(t, result)
	})

	t.Run("approved passes", func(t *testing.T) {
		g := &dnaApprovalGate{checker: &mockDNAChecker{approved: true}}
		result := g.Check(context.Background(), &MonitorGateContext{StrategyKey: "orb"})
		assert.Nil(t, result)
	})

	t.Run("not approved blocks", func(t *testing.T) {
		g := &dnaApprovalGate{checker: &mockDNAChecker{approved: false}}
		result := g.Check(context.Background(), &MonitorGateContext{StrategyKey: "orb"})
		require.NotNil(t, result)
		assert.Equal(t, "dna_approval", result.GateName)
	})

	t.Run("error passes through", func(t *testing.T) {
		g := &dnaApprovalGate{checker: &mockDNAChecker{err: assert.AnError}}
		result := g.Check(context.Background(), &MonitorGateContext{StrategyKey: "orb"})
		assert.Nil(t, result)
	})
}

// --- IndexTideTracker ---

func TestIndexTideTracker_OnBar(t *testing.T) {
	tracker := NewIndexTideTracker(2)
	now := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)

	// AAPL is tech -> QQQ. Feed QQQ bars.
	// First bar: not ready yet (warmup = 2).
	tracker.OnBar(domain.MarketBar{
		Symbol: "QQQ", Time: now,
		High: 500, Low: 498, Close: 499, Volume: 1000,
	})
	_, _, ready := tracker.GetTide("AAPL")
	assert.False(t, ready)

	// Second bar: now ready.
	tracker.OnBar(domain.MarketBar{
		Symbol: "QQQ", Time: now.Add(time.Minute),
		High: 501, Low: 499, Close: 500, Volume: 2000,
	})
	vwap, lastClose, ready := tracker.GetTide("AAPL")
	assert.True(t, ready)
	assert.InDelta(t, 500, lastClose, 0.01)
	assert.Greater(t, vwap, 0.0)
}

func TestIndexTideTracker_SessionReset(t *testing.T) {
	tracker := NewIndexTideTracker(1)
	day1 := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)

	// BAC is financial -> SPY. Feed SPY bars.
	tracker.OnBar(domain.MarketBar{
		Symbol: "SPY", Time: day1,
		High: 500, Low: 498, Close: 499, Volume: 1000,
	})
	vwap1, _, _ := tracker.GetTide("BAC")

	// New session day resets.
	tracker.OnBar(domain.MarketBar{
		Symbol: "SPY", Time: day2,
		High: 510, Low: 508, Close: 509, Volume: 500,
	})
	vwap2, _, _ := tracker.GetTide("BAC")
	assert.NotEqual(t, vwap1, vwap2)
}

func TestIndexTideTracker_IgnoresNonIndex(t *testing.T) {
	tracker := NewIndexTideTracker(1)
	tracker.OnBar(domain.MarketBar{
		Symbol: "AAPL", Time: time.Now(),
		High: 150, Low: 148, Close: 149, Volume: 1000,
	})
	// AAPL bars should not populate SPY state.
	_, _, ready := tracker.GetTide("MSFT")
	assert.False(t, ready)
}

// --- ReferenceIndex ---

func TestReferenceIndex(t *testing.T) {
	tests := []struct {
		sym  domain.Symbol
		want string
	}{
		{"NVDA", "QQQ"},   // semis -> QQQ
		{"AAPL", "QQQ"},   // tech -> QQQ
		{"CRM", "QQQ"},    // software -> QQQ
		{"SOFI", "QQQ"},   // fintech -> QQQ
		{"MARA", "QQQ"},   // crypto proxy -> QQQ
		{"TQQQ", "QQQ"},   // lev ETF -> QQQ
		{"QQQ", ""},       // self-reference
		{"SPY", ""},       // self-reference
		{"IWM", ""},       // broad ETF
		{"XLF", "SPY"},   // ETF -> SPY
		{"BAC", "SPY"},   // financial -> SPY (default)
		{"BA", "SPY"},    // industrial -> SPY
		{"UNKNOWN", "SPY"}, // other -> SPY
	}
	for _, tt := range tests {
		t.Run(string(tt.sym), func(t *testing.T) {
			assert.Equal(t, tt.want, ReferenceIndex(tt.sym))
		})
	}
}

// --- Market Tide Gate ---

func TestMarketTideGate(t *testing.T) {
	tracker := NewIndexTideTracker(1)
	now := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)

	// Seed SPY VWAP at ~500.
	tracker.OnBar(domain.MarketBar{
		Symbol: "SPY", Time: now,
		High: 500, Low: 500, Close: 500, Volume: 10000,
	})

	t.Run("no tracker passes", func(t *testing.T) {
		g := &marketTideGate{}
		result := g.Check(context.Background(), &MonitorGateContext{
			Setup: SetupInput{Symbol: "BAC", Direction: domain.DirectionLong},
		})
		assert.Nil(t, result)
	})

	t.Run("within neutral band passes", func(t *testing.T) {
		// SPY close is exactly at VWAP (0 bps deviation).
		g := &marketTideGate{tracker: tracker, neutralBandBps: 10}
		result := g.Check(context.Background(), &MonitorGateContext{
			Setup: SetupInput{Symbol: "BAC", Direction: domain.DirectionLong},
		})
		assert.Nil(t, result)
	})

	t.Run("self-reference passes", func(t *testing.T) {
		g := &marketTideGate{tracker: tracker, neutralBandBps: 10}
		result := g.Check(context.Background(), &MonitorGateContext{
			Setup: SetupInput{Symbol: "SPY", Direction: domain.DirectionLong},
		})
		assert.Nil(t, result) // SPY has no ref index
	})
}

// --- Param helpers ---

func TestExtractFloat64(t *testing.T) {
	assert.Equal(t, 1.5, extractFloat64(map[string]any{"x": 1.5}, "x", 0))
	assert.Equal(t, 3.0, extractFloat64(map[string]any{"x": 3}, "x", 0))
	assert.Equal(t, 0.0, extractFloat64(nil, "x", 0))
	assert.Equal(t, 5.0, extractFloat64(map[string]any{}, "x", 5))
}

func TestExtractInt(t *testing.T) {
	assert.Equal(t, 10, extractInt(map[string]any{"x": 10}, "x", 0))
	assert.Equal(t, 3, extractInt(map[string]any{"x": 3.0}, "x", 0))
	assert.Equal(t, 0, extractInt(nil, "x", 0))
}

func TestExtractStringSlice(t *testing.T) {
	assert.Equal(t, []string{"A", "B"}, extractStringSlice(map[string]any{"x": []string{"A", "B"}}, "x"))
	assert.Equal(t, []string{"A"}, extractStringSlice(map[string]any{"x": []any{"A"}}, "x"))
	assert.Nil(t, extractStringSlice(nil, "x"))
}
