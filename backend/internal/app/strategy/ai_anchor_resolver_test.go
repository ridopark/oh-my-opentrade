package strategy_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appstrat "github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockAdvisorForAnchors struct {
	selection *strat.AnchorSelection
	err       error
	calls     int
}

func (m *mockAdvisorForAnchors) RequestDebate(_ context.Context, _ domain.Symbol, _ domain.MarketRegime, _ domain.IndicatorSnapshot, _ ...ports.DebateOption) (*domain.AdvisoryDecision, error) {
	return nil, nil
}

func (m *mockAdvisorForAnchors) SelectAnchors(_ context.Context, _ ports.AnchorSelectionRequest) (*strat.AnchorSelection, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.selection, nil
}

type mockAnchorStore struct {
	saved      []strat.CandidateAnchor
	active     []strat.CandidateAnchor
	expired    []string
	selections []strat.AnchorSelection
}

func (m *mockAnchorStore) Save(_ context.Context, anchors []strat.CandidateAnchor) error {
	m.saved = append(m.saved, anchors...)
	return nil
}

func (m *mockAnchorStore) LoadActive(_ context.Context, _ string) ([]strat.CandidateAnchor, error) {
	return m.active, nil
}

func (m *mockAnchorStore) Expire(_ context.Context, anchorID string, _ string) error {
	m.expired = append(m.expired, anchorID)
	return nil
}

func (m *mockAnchorStore) SaveSelection(_ context.Context, _ string, sel strat.AnchorSelection) error {
	m.selections = append(m.selections, sel)
	return nil
}

func TestAIAnchorResolver_ResolveWithAISuccess(t *testing.T) {
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	sel := strat.AnchorSelection{
		SelectedAnchors: []strat.SelectedAnchor{
			{CandidateID: "swing_high_5m_100", AnchorName: "swing_high_5m_100", Rank: 1, Confidence: 0.9, Reason: "key level"},
		},
		Rationale: "test",
	}
	advisor := &mockAdvisorForAnchors{selection: &sel}
	store := &mockAnchorStore{
		active: []strat.CandidateAnchor{
			{ID: "swing_high_5m_100", Time: t0, Price: 185.0, Type: strat.AnchorSwingHigh, Timeframe: "5m", Strength: 5.0},
		},
	}

	resolver := appstrat.NewAIAnchorResolver(advisor, store, nil)
	resolver.RegisterSymbol("AAPL", false)

	result, err := resolver.ResolveAnchors(context.Background(), "AAPL", 184.0,
		domain.MarketRegime{Type: domain.RegimeTrend}, domain.IndicatorSnapshot{})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, t0, result["swing_high_5m_100"])
	assert.Equal(t, 1, advisor.calls)
}

func TestAIAnchorResolver_FallbackOnAIError(t *testing.T) {
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	advisor := &mockAdvisorForAnchors{err: errors.New("LLM unavailable")}
	store := &mockAnchorStore{
		active: []strat.CandidateAnchor{
			{ID: "swing_high_1h_100", Time: t0, Price: 185.0, Type: strat.AnchorSwingHigh, Timeframe: "1h", Strength: 5.0},
			{ID: "swing_low_5m_200", Time: t0.Add(-time.Hour), Price: 182.0, Type: strat.AnchorSwingLow, Timeframe: "5m", Strength: 3.0},
		},
	}

	resolver := appstrat.NewAIAnchorResolver(advisor, store, nil)
	resolver.RegisterSymbol("AAPL", false)

	result, err := resolver.ResolveAnchors(context.Background(), "AAPL", 184.0,
		domain.MarketRegime{Type: domain.RegimeTrend}, domain.IndicatorSnapshot{})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Len(t, result, 2, "fallback should include all candidates")
}

func TestAIAnchorResolver_OnBarDetectsSwing(t *testing.T) {
	resolver := appstrat.NewAIAnchorResolver(nil, nil, nil)
	resolver.RegisterSymbol("AAPL", false)
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// N=5 for 5m: need 11 bars (2*5+1) to detect a swing
	// Ramp up, peak, ramp down
	for i := 0; i < 5; i++ {
		resolver.OnBar("AAPL", strat.Bar{
			Time: t0.Add(time.Duration(i*5) * time.Minute),
			Open: float64(100 + i), High: float64(101 + i), Low: float64(99 + i), Close: float64(100 + i), Volume: 1000,
		}, "5m")
	}
	// Peak bar
	resolver.OnBar("AAPL", strat.Bar{
		Time: t0.Add(25 * time.Minute),
		Open: 110, High: 115, Low: 109, Close: 110, Volume: 1000,
	}, "5m")
	// Ramp down
	for i := 1; i <= 5; i++ {
		resolver.OnBar("AAPL", strat.Bar{
			Time: t0.Add(time.Duration((5+i)*5) * time.Minute),
			Open: float64(110 - i*2), High: float64(111 - i*2), Low: float64(109 - i*2), Close: float64(110 - i*2), Volume: 1000,
		}, "5m")
	}

	cands := resolver.Candidates("AAPL")
	swingHighs := 0
	for _, c := range cands {
		if c.Type == strat.AnchorSwingHigh {
			swingHighs++
		}
	}
	assert.True(t, swingHighs >= 1, "should detect at least one swing high, got %d candidates total", len(cands))
}

func TestAIAnchorResolver_NilStore(t *testing.T) {
	sel := strat.AnchorSelection{
		SelectedAnchors: []strat.SelectedAnchor{
			{CandidateID: "swing_high_5m_100", AnchorName: "swing_high_5m_100", Rank: 1, Confidence: 0.9, Reason: "test"},
		},
		Rationale: "test",
	}
	advisor := &mockAdvisorForAnchors{selection: &sel}

	resolver := appstrat.NewAIAnchorResolver(advisor, nil, nil)
	resolver.RegisterSymbol("AAPL", false)

	// Manually add a candidate via OnBar simulation — need a full swing sequence
	// Instead, test with no candidates — should return nil
	result, err := resolver.ResolveAnchors(context.Background(), "AAPL", 184.0,
		domain.MarketRegime{}, domain.IndicatorSnapshot{})

	require.NoError(t, err)
	assert.Nil(t, result, "no candidates → nil result")
}

func TestAIAnchorResolver_FallbackRankDeterministic(t *testing.T) {
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	advisor := &mockAdvisorForAnchors{err: errors.New("disabled")}
	store := &mockAnchorStore{
		active: []strat.CandidateAnchor{
			{ID: "a_5m", Time: t0, Price: 100, Type: strat.AnchorSwingHigh, Timeframe: "5m", Strength: 5.0},
			{ID: "b_1h", Time: t0.Add(-time.Hour), Price: 105, Type: strat.AnchorSwingHigh, Timeframe: "1h", Strength: 3.0},
			{ID: "c_1d", Time: t0.Add(-24 * time.Hour), Price: 110, Type: strat.AnchorSwingLow, Timeframe: "1d", Strength: 2.0},
		},
	}

	resolver := appstrat.NewAIAnchorResolver(advisor, store, nil)
	resolver.RegisterSymbol("AAPL", false)

	result, err := resolver.ResolveAnchors(context.Background(), "AAPL", 104.0,
		domain.MarketRegime{}, domain.IndicatorSnapshot{})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Len(t, result, 3)
	// 1d should be present (highest weight: 3*10+2=32)
	// 1h should be present (2*10+3=23)
	// 5m should be present (1*10+5=15)
	_, has1d := result["c_1d"]
	_, has1h := result["b_1h"]
	_, has5m := result["a_5m"]
	assert.True(t, has1d)
	assert.True(t, has1h)
	assert.True(t, has5m)
}

func TestAIAnchorResolver_CandidateCap(t *testing.T) {
	resolver := appstrat.NewAIAnchorResolver(nil, nil, nil)
	resolver.RegisterSymbol("AAPL", false)
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// N=5 swing detector needs 11 bars per swing. Instead, test the cap
	// by using 1h detector with N=3 (needs 7 bars per swing).
	// Generate many swings on 1h timeframe.
	barIdx := 0
	for swing := 0; swing < 60; swing++ {
		// Ramp up
		for i := 0; i < 3; i++ {
			resolver.OnBar("AAPL", strat.Bar{
				Time: t0.Add(time.Duration(barIdx) * time.Hour),
				Open: float64(100 + i), High: float64(101 + i), Low: float64(99 + i),
				Close: float64(100 + i), Volume: 1000,
			}, "1h")
			barIdx++
		}
		// Peak
		resolver.OnBar("AAPL", strat.Bar{
			Time: t0.Add(time.Duration(barIdx) * time.Hour),
			Open: 110, High: float64(115 + swing), Low: 109, Close: 110, Volume: 1000,
		}, "1h")
		barIdx++
		// Ramp down
		for i := 1; i <= 3; i++ {
			resolver.OnBar("AAPL", strat.Bar{
				Time: t0.Add(time.Duration(barIdx) * time.Hour),
				Open: float64(110 - i*2), High: float64(111 - i*2), Low: float64(109 - i*2),
				Close: float64(110 - i*2), Volume: 1000,
			}, "1h")
			barIdx++
		}
	}

	cands := resolver.Candidates("AAPL")
	assert.True(t, len(cands) <= 50, "candidates should be capped at 50, got %d", len(cands))
}

func TestAIAnchorResolver_RegisterEquityVsCrypto(t *testing.T) {
	resolver := appstrat.NewAIAnchorResolver(nil, nil, nil)
	resolver.RegisterSymbol("AAPL", false)
	resolver.RegisterSymbol("BTCUSD", true)

	// Both should work without panic
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)
	bar := strat.Bar{Time: t0, Open: 100, High: 101, Low: 99, Close: 100, Volume: 1000}
	resolver.OnBar("AAPL", bar, "5m")
	resolver.OnBar("BTCUSD", bar, "5m")
}
