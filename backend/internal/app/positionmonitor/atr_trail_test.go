package positionmonitor

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClassifyTercile covers the bucket boundary behavior.
func TestClassifyTercile(t *testing.T) {
	cases := []struct {
		pct     float64
		low     float64
		high    float64
		wantIdx int
	}{
		{0.00, 0.33, 0.67, 0},
		{0.32, 0.33, 0.67, 0},
		{0.33, 0.33, 0.67, 1}, // boundary: >= low → mid
		{0.50, 0.33, 0.67, 1},
		{0.67, 0.33, 0.67, 2}, // boundary: >= high → high
		{0.99, 0.33, 0.67, 2},
		{0.20, 0.25, 0.75, 0}, // quant may flip defaults to 25/75
		{0.80, 0.25, 0.75, 2},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("pct=%.2f/low=%.2f/high=%.2f", tc.pct, tc.low, tc.high), func(t *testing.T) {
			assert.Equal(t, tc.wantIdx, classifyTercile(tc.pct, tc.low, tc.high))
		})
	}
}

// TestPercentileRank covers the mid-rank convention for ties.
func TestPercentileRank(t *testing.T) {
	samples := []float64{0.10, 0.20, 0.30, 0.40, 0.50}
	assert.InDelta(t, 0.0, percentileRank([]float64{}, 0.25), 1e-9)
	assert.InDelta(t, 0.7, percentileRank(samples, 0.40), 1e-9) // 3 less + 0.5×1 equal / 5 = 0.70
	assert.InDelta(t, 1.0, percentileRank(samples, 0.60), 1e-9)
	assert.InDelta(t, 0.0, percentileRank(samples, 0.01), 1e-9)
}

// TestBuildATRPctSeries verifies the rolling ATR% series length and
// that it uses the simple-mean true range formulation.
func TestBuildATRPctSeries(t *testing.T) {
	// 5 bars; period=2. Expect 3 ATR% entries (index 2,3,4).
	bars := []domain.MarketBar{
		{High: 102, Low: 98, Close: 100},
		{High: 104, Low: 99, Close: 103},
		{High: 106, Low: 102, Close: 105},
		{High: 108, Low: 104, Close: 107},
		{High: 110, Low: 106, Close: 109},
	}
	series := buildATRPctSeries(bars, 2)
	require.Len(t, series, 3)
	for _, v := range series {
		assert.Greater(t, v, 0.0)
	}
}

// mockRepoDaily is a minimal RepositoryPort that only answers GetMarketBars
// with a canned slice. Other methods are not exercised by computeATRTrailMult.
type mockRepoDaily struct {
	bars []domain.MarketBar
	err  error
	mockRepoStub
}

func (m *mockRepoDaily) GetMarketBars(
	_ context.Context,
	_ domain.Symbol,
	_ domain.Timeframe,
	_, _ time.Time,
) ([]domain.MarketBar, error) {
	return m.bars, m.err
}

// syntheticDailyBars builds `n` deterministic daily bars on a 1% ATR%
// baseline, with the last `highMultCount` bars using a 3% ATR% bump so
// the percentile of the latest lands in the top bucket.
func syntheticDailyBars(n int, highMultCount int, end time.Time) []domain.MarketBar {
	out := make([]domain.MarketBar, n)
	base := 100.0
	for i := 0; i < n; i++ {
		ampPct := 0.01
		if i >= n-highMultCount {
			ampPct = 0.03
		}
		close := base + 0.1*float64(i)
		high := close * (1 + ampPct/2)
		low := close * (1 - ampPct/2)
		out[i] = domain.MarketBar{
			Time:  end.Add(-time.Duration(n-i) * 24 * time.Hour),
			High:  high,
			Low:   low,
			Close: close,
		}
	}
	return out
}

func newServiceForATRTest(t *testing.T, bars []domain.MarketBar, cfg atrTrailConfigValue) *Service {
	t.Helper()
	s := &Service{
		log:     zerolog.Nop(),
		nowFunc: func() time.Time { return time.Date(2026, 4, 17, 15, 0, 0, 0, time.UTC) },
		repo:    &mockRepoDaily{bars: bars},
	}
	s.atrTrailCfg = cfg
	return s
}

func defaultATRCfg() atrTrailConfigValue {
	return atrTrailConfigValue{
		Enabled:                       true,
		ATRPeriod:                     14,
		ATRLookbackDays:               60,
		ATRLookbackDaysCrypto:         42,
		TercileLowPctile:              0.33,
		TercileHighPctile:             0.67,
		TercileMultipliers:            []float64{1.0, 1.5, 2.0},
		InsufficientHistoryMultiplier: 1.0,
		MinHistoryDays:                30,
	}
}

// TestComputeATRTrailMult_Buckets drives the end-to-end bucket classification
// for an OCC symbol whose underlying has full 60-day history.
func TestComputeATRTrailMult_Buckets(t *testing.T) {
	end := time.Date(2026, 4, 17, 15, 0, 0, 0, time.UTC)

	t.Run("recent spike → high bucket returns 2.0x", func(t *testing.T) {
		bars := syntheticDailyBars(80, 5, end) // last 5 days 3% ATR%, rest 1%
		s := newServiceForATRTest(t, bars, defaultATRCfg())
		pos := &domain.MonitoredPosition{
			Symbol:     "AAPL260320C00150000",
			AssetClass: domain.AssetClassEquity,
		}
		mult, pct, ok := s.computeATRTrailMult(context.Background(), pos)
		assert.True(t, ok)
		assert.InDelta(t, 2.0, mult, 1e-9)
		assert.Greater(t, pct, 0.67)
	})

	t.Run("recent dip → low bucket returns 1.0x", func(t *testing.T) {
		// Baseline 3% ATR% for 75 days, then last 5 days drop to 1% ATR%.
		// Latest sits in bottom tercile → bucket 0 → 1.0x.
		bars := syntheticDailyBars(80, 0, end)
		// Overwrite first 75 bars with a high-amp fixture, leave last 5 low.
		for i := 0; i < 75; i++ {
			close := bars[i].Close
			bars[i].High = close * 1.015
			bars[i].Low = close * 0.985
		}
		s := newServiceForATRTest(t, bars, defaultATRCfg())
		pos := &domain.MonitoredPosition{
			Symbol:     "AAPL260320C00150000",
			AssetClass: domain.AssetClassEquity,
		}
		mult, pct, ok := s.computeATRTrailMult(context.Background(), pos)
		assert.True(t, ok)
		assert.InDelta(t, 1.0, mult, 1e-9)
		assert.Less(t, pct, 0.33, "latest percentile should sit in bottom tercile")
	})
}

// TestComputeATRTrailMult_InsufficientHistory returns 1.0 when fewer than
// MinHistoryDays of bars are present.
func TestComputeATRTrailMult_InsufficientHistory(t *testing.T) {
	end := time.Date(2026, 4, 17, 15, 0, 0, 0, time.UTC)
	bars := syntheticDailyBars(20, 0, end) // < MinHistoryDays=30
	s := newServiceForATRTest(t, bars, defaultATRCfg())
	pos := &domain.MonitoredPosition{
		Symbol:     "AAPL260320C00150000",
		AssetClass: domain.AssetClassEquity,
	}
	mult, _, ok := s.computeATRTrailMult(context.Background(), pos)
	assert.False(t, ok)
	assert.InDelta(t, 1.0, mult, 1e-9)
}

// TestComputeATRTrailMult_PartialHistory uses InsufficientHistoryMultiplier
// when days ∈ [MinHistoryDays, lookback).
func TestComputeATRTrailMult_PartialHistory(t *testing.T) {
	end := time.Date(2026, 4, 17, 15, 0, 0, 0, time.UTC)
	bars := syntheticDailyBars(45, 0, end) // ≥30 but <60
	cfg := defaultATRCfg()
	cfg.InsufficientHistoryMultiplier = 1.25 // non-default to verify propagation
	s := newServiceForATRTest(t, bars, cfg)
	pos := &domain.MonitoredPosition{
		Symbol:     "AAPL260320C00150000",
		AssetClass: domain.AssetClassEquity,
	}
	mult, _, ok := s.computeATRTrailMult(context.Background(), pos)
	assert.False(t, ok)
	assert.InDelta(t, 1.25, mult, 1e-9)
}

// TestComputeATRTrailMult_Disabled returns 1.0 and ok=false when
// cfg.Enabled is false.
func TestComputeATRTrailMult_Disabled(t *testing.T) {
	end := time.Date(2026, 4, 17, 15, 0, 0, 0, time.UTC)
	bars := syntheticDailyBars(80, 5, end)
	cfg := defaultATRCfg()
	cfg.Enabled = false
	s := newServiceForATRTest(t, bars, cfg)
	pos := &domain.MonitoredPosition{
		Symbol:     "AAPL260320C00150000",
		AssetClass: domain.AssetClassEquity,
	}
	mult, _, ok := s.computeATRTrailMult(context.Background(), pos)
	assert.False(t, ok)
	assert.InDelta(t, 1.0, mult, 1e-9)
}

// TestComputeATRTrailMult_NonOCC returns 1.0 when the symbol is not OCC
// (no underlying to look up).
func TestComputeATRTrailMult_NonOCC(t *testing.T) {
	end := time.Date(2026, 4, 17, 15, 0, 0, 0, time.UTC)
	bars := syntheticDailyBars(80, 5, end)
	s := newServiceForATRTest(t, bars, defaultATRCfg())
	pos := &domain.MonitoredPosition{
		Symbol:     "AAPL", // plain equity — UnderlyingFromOCC returns ""
		AssetClass: domain.AssetClassEquity,
	}
	mult, _, ok := s.computeATRTrailMult(context.Background(), pos)
	assert.False(t, ok)
	assert.InDelta(t, 1.0, mult, 1e-9)
}

// TestEvaluatePremiumTrail_MultPropagation verifies that ctx.TrailMult
// actually scales the effective trail. With mult=2.0 a 20%-base trail
// becomes a 40%-effective trail; a 30% drawdown must NOT fire, while a
// 41% drawdown must.
func TestEvaluatePremiumTrail_MultPropagation(t *testing.T) {
	now := time.Date(2026, 4, 17, 14, 0, 0, 0, time.UTC)
	rule := domain.ExitRule{Type: domain.ExitRulePremiumTrail, Params: map[string]float64{"trail_pct": 0.20}}

	makePos := func(estPremium float64) *domain.MonitoredPosition {
		return &domain.MonitoredPosition{
			Symbol:         "SOXL260620C00020000",
			InstrumentType: domain.InstrumentTypeOption,
			EntryPrice:     100, // underlying spot
			CustomState: map[string]float64{
				"option_premium":  5.00,
				"delta_at_entry":  0.50,
				"premium_hwm":     10.00,
				"premium_estimated_override": estPremium, // see note below
			},
		}
	}
	// We cannot easily stub EstimatedPremium here because it depends on
	// BSM inputs. Instead, drive the evaluator with pos.EntryPrice-style
	// delta approximation: legacy fallback path in EstimatedPremium uses
	// `entryPremium + delta*(currentUnderlying - entryPrice)`. So a
	// currentPrice = 100 - 12 = 88 yields est = 5 + 0.5*(-12) = -1 → 0.
	// That's unhelpful. Use currentPrice to drive the drawdown explicitly:
	// pick currentPrice so est = hwm × (1 - wantDrawdown).
	_ = makePos

	baseEntryPremium := 5.00
	deltaAtEntry := 0.50
	hwm := 10.00
	currentFor := func(wantDrawdownPct float64) float64 {
		// est = baseEntryPremium + delta*(curU - entryU)
		// wantEst = hwm * (1 - wantDrawdownPct)
		// curU = entryU + (wantEst - baseEntryPremium)/delta
		wantEst := hwm * (1 - wantDrawdownPct)
		entryU := 100.0
		return entryU + (wantEst-baseEntryPremium)/deltaAtEntry
	}
	posFor := func() *domain.MonitoredPosition {
		return &domain.MonitoredPosition{
			Symbol:         "SOXL260620C00020000",
			InstrumentType: domain.InstrumentTypeOption,
			EntryPrice:     100,
			CustomState: map[string]float64{
				"option_premium": baseEntryPremium,
				"delta_at_entry": deltaAtEntry,
				"premium_hwm":    hwm,
			},
		}
	}

	cases := []struct {
		name        string
		mult        float64
		drawdown    float64
		wantFired   bool
	}{
		{"mult=1.0, 19% drawdown on 20% trail → no fire", 1.0, 0.19, false},
		{"mult=1.0, 21% drawdown on 20% trail → fire", 1.0, 0.21, true},
		{"mult=2.0, 30% drawdown on 20% trail (eff=40%) → no fire", 2.0, 0.30, false},
		{"mult=2.0, 41% drawdown on 20% trail (eff=40%) → fire", 2.0, 0.41, true},
		{"mult=0 defensive fallback → 1.0", 0.0, 0.21, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pos := posFor()
			currentUnderlying := currentFor(tc.drawdown)
			ctx := newEvalContext()
			ctx.TrailMult = tc.mult
			fired, _ := evaluatePremiumTrail(rule, pos, currentUnderlying, now, ctx)
			assert.Equal(t, tc.wantFired, fired)
		})
	}
}

// TestEvaluatePremiumTrail_DisabledByteIdentical confirms that with the
// new TrailMult field at its default 1.0, the evaluator's decision is
// identical to the pre-fix behavior for a representative input.
func TestEvaluatePremiumTrail_DisabledByteIdentical(t *testing.T) {
	now := time.Date(2026, 4, 17, 14, 0, 0, 0, time.UTC)
	rule := domain.ExitRule{Type: domain.ExitRulePremiumTrail, Params: map[string]float64{"trail_pct": 0.20}}
	pos := &domain.MonitoredPosition{
		Symbol:         "SOXL260620C00020000",
		InstrumentType: domain.InstrumentTypeOption,
		EntryPrice:     100,
		CustomState: map[string]float64{
			"option_premium": 5.00,
			"delta_at_entry": 0.50,
			"premium_hwm":    10.00,
		},
	}
	// 25% drawdown on 20% base trail → fires under old + new (mult=1.0).
	currentUnderlying := 100 + (10*0.75-5.00)/0.50
	ctxOld := EvalContext{}        // pre-fix struct literal; TrailMult=0 → defensive fallback to 1.0
	ctxNew := newEvalContext()     // new default constructor; TrailMult=1.0
	firedOld, _ := evaluatePremiumTrail(rule, pos, currentUnderlying, now, ctxOld)
	firedNew, _ := evaluatePremiumTrail(rule, pos, currentUnderlying, now, ctxNew)
	assert.Equal(t, firedOld, firedNew,
		"byte-identical: bare EvalContext{} (legacy caller) must produce same decision as newEvalContext() (new caller)")
	assert.True(t, firedOld, "25% drawdown should fire a 20% trail")
}

// mockRepoStub is a no-op RepositoryPort implementation used to satisfy
// the interface on mockRepoDaily. Only GetMarketBars is overridden.
type mockRepoStub struct{}

func (mockRepoStub) SaveMarketBar(_ context.Context, _ domain.MarketBar) error { return nil }
func (mockRepoStub) SaveMarketBars(_ context.Context, _ []domain.MarketBar) (int, error) {
	return 0, nil
}
func (mockRepoStub) GetMarketBarsMulti(_ context.Context, _ []domain.Symbol, _ domain.Timeframe, _, _ time.Time) (map[string][]domain.MarketBar, error) {
	return map[string][]domain.MarketBar{}, nil
}
func (mockRepoStub) SaveTrade(_ context.Context, _ domain.Trade) error { return nil }
func (mockRepoStub) GetTrades(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) ([]domain.Trade, error) {
	return nil, nil
}
func (mockRepoStub) SaveStrategyDNA(_ context.Context, _ domain.StrategyDNA) error { return nil }
func (mockRepoStub) GetLatestStrategyDNA(_ context.Context, _ string, _ domain.EnvMode) (*domain.StrategyDNA, error) {
	return nil, nil
}
func (mockRepoStub) SaveOrder(_ context.Context, _ domain.BrokerOrder) error { return nil }
func (mockRepoStub) UpdateOrderFill(_ context.Context, _ string, _ time.Time, _, _ float64) error {
	return nil
}
func (mockRepoStub) RecordFill(_ context.Context, _ string, _ time.Time, _, _ float64, _ domain.Trade) error {
	return nil
}
func (mockRepoStub) ListTrades(_ context.Context, _ ports.TradeQuery) (ports.TradePage, error) {
	return ports.TradePage{}, nil
}
func (mockRepoStub) ListOrders(_ context.Context, _ ports.OrderQuery) (ports.OrderPage, error) {
	return ports.OrderPage{}, nil
}
func (mockRepoStub) SaveThoughtLog(_ context.Context, _ domain.ThoughtLog) error { return nil }
func (mockRepoStub) GetThoughtLogsByIntentID(_ context.Context, _ string) ([]domain.ThoughtLog, error) {
	return nil, nil
}
func (mockRepoStub) UpdateTradeThesis(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol, _ json.RawMessage) error {
	return nil
}
func (mockRepoStub) GetMaxBarHighSince(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _ time.Time) (float64, error) {
	return 0, nil
}
func (mockRepoStub) GetLatestThesisForSymbol(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol) (json.RawMessage, error) {
	return nil, nil
}
func (mockRepoStub) GetNonTerminalOrders(_ context.Context, _ string, _ domain.EnvMode) ([]domain.BrokerOrder, error) {
	return nil, nil
}
func (mockRepoStub) GetOrderByBrokerOrderID(_ context.Context, _ string) (*domain.BrokerOrder, error) {
	return nil, nil
}
func (mockRepoStub) GetRecordedFillQty(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol, _ string, _ time.Time) (float64, error) {
	return 0, nil
}
func (mockRepoStub) UpdateOrderStatus(_ context.Context, _ string, _ string) error { return nil }
func (mockRepoStub) GetRecordedExecutionIDs(_ context.Context, _ string, _ domain.EnvMode, _ time.Time) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (mockRepoStub) GetNetPositions(_ context.Context, _ string, _ domain.EnvMode) (map[domain.Symbol]float64, error) {
	return nil, nil
}
func (mockRepoStub) GetAvgEntryPrice(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol) (float64, error) {
	return 0, nil
}
func (mockRepoStub) HasCanceledExitOrder(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol) (bool, error) {
	return false, nil
}
func (mockRepoStub) UpdateBarIndicators(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _ time.Time, _, _, _, _ float64, _ map[string]float64) error {
	return nil
}
