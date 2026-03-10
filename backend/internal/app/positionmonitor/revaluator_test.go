package positionmonitor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockRiskAssessor struct {
	result *domain.RiskRevaluation
	err    error
	calls  int
}

func (m *mockRiskAssessor) AssessPosition(_ context.Context, _ domain.MonitoredPosition, _ domain.IndicatorSnapshot, _ domain.MarketRegime) (*domain.RiskRevaluation, error) {
	m.calls++
	return m.result, m.err
}

func newTestRevaluator(t *testing.T, bus *mockEventBus, assessor *mockRiskAssessor) (*Revaluator, *Service) {
	t.Helper()
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())
	posMonitor := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop())

	snapshotFn := func(symbol string) (domain.IndicatorSnapshot, bool) {
		return domain.IndicatorSnapshot{EMA9: 155.0, EMA21: 150.0, RSI: 55, VWAP: 152.0}, true
	}

	reval := NewRevaluator(
		posMonitor, assessor, bus, snapshotFn, nil,
		5*time.Minute, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
	)
	return reval, posMonitor
}

func TestRevaluator_HandleSignalEnriched_CachesThesisForEntrySignals(t *testing.T) {
	bus := &mockEventBus{}
	reval, _ := newTestRevaluator(t, bus, &mockRiskAssessor{})

	enrichment := domain.SignalEnrichment{
		Signal:         domain.SignalRef{Symbol: "AAPL", SignalType: "entry", Side: "buy"},
		Status:         domain.EnrichmentOK,
		Confidence:     0.85,
		Rationale:      "strong breakout setup",
		Direction:      domain.DirectionLong,
		BullArgument:   "high volume breakout above resistance",
		BearArgument:   "overbought RSI",
		JudgeReasoning: "momentum favors bulls",
		RiskModifier:   domain.RiskModifierNormal,
	}

	ev, err := domain.NewEvent(domain.EventSignalEnriched, "tenant-1", domain.EnvModePaper, "enrich-1", enrichment)
	require.NoError(t, err)

	require.NoError(t, reval.handleSignalEnriched(context.Background(), *ev))

	raw, ok := reval.pendingTheses.Load("AAPL")
	require.True(t, ok, "thesis should be cached for AAPL")

	thesis := raw.(*domain.EntryThesis)
	assert.Equal(t, "high volume breakout above resistance", thesis.BullArgument)
	assert.Equal(t, "overbought RSI", thesis.BearArgument)
	assert.Equal(t, "momentum favors bulls", thesis.JudgeReasoning)
	assert.InDelta(t, 0.85, thesis.Confidence, 0.001)
	assert.Equal(t, domain.RiskModifierNormal, thesis.RiskModifier)
	assert.Equal(t, domain.DirectionLong, thesis.Direction)
}

func TestRevaluator_HandleSignalEnriched_IgnoresExitSignals(t *testing.T) {
	bus := &mockEventBus{}
	reval, _ := newTestRevaluator(t, bus, &mockRiskAssessor{})

	enrichment := domain.SignalEnrichment{
		Signal: domain.SignalRef{Symbol: "AAPL", SignalType: "exit", Side: "sell"},
		Status: domain.EnrichmentOK,
	}

	ev, err := domain.NewEvent(domain.EventSignalEnriched, "tenant-1", domain.EnvModePaper, "enrich-2", enrichment)
	require.NoError(t, err)
	require.NoError(t, reval.handleSignalEnriched(context.Background(), *ev))

	_, ok := reval.pendingTheses.Load("AAPL")
	assert.False(t, ok, "exit signals should not be cached")
}

func TestRevaluator_HandleSignalEnriched_IgnoresNonOKEnrichments(t *testing.T) {
	bus := &mockEventBus{}
	reval, _ := newTestRevaluator(t, bus, &mockRiskAssessor{})

	for _, status := range []domain.EnrichmentStatus{domain.EnrichmentTimeout, domain.EnrichmentError, domain.EnrichmentSkipped} {
		enrichment := domain.SignalEnrichment{
			Signal: domain.SignalRef{Symbol: "AAPL", SignalType: "entry", Side: "buy"},
			Status: status,
		}

		ev, err := domain.NewEvent(domain.EventSignalEnriched, "tenant-1", domain.EnvModePaper, fmt.Sprintf("enrich-%s", status), enrichment)
		require.NoError(t, err)
		require.NoError(t, reval.handleSignalEnriched(context.Background(), *ev))

		_, ok := reval.pendingTheses.Load("AAPL")
		assert.False(t, ok, "enrichment with status %s should not be cached", status)
	}
}

func TestRevaluator_HandleFillReceived_AttachesThesisToPosition(t *testing.T) {
	bus := &mockEventBus{}
	reval, posMonitor := newTestRevaluator(t, bus, &mockRiskAssessor{})

	posMonitor.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		FilledAt:   time.Now(),
		Strategy:   "ai_scalping",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})
	require.Equal(t, 1, posMonitor.PositionCount())

	thesis := &domain.EntryThesis{
		BullArgument:   "strong breakout",
		BearArgument:   "high RSI",
		JudgeReasoning: "momentum wins",
		Confidence:     0.80,
		RiskModifier:   domain.RiskModifierTight,
		Direction:      domain.DirectionLong,
	}
	reval.pendingTheses.Store("AAPL", thesis)

	fillPayload := map[string]any{
		"symbol":   "AAPL",
		"side":     "BUY",
		"price":    float64(150.0),
		"quantity": float64(10.0),
	}
	ev, err := domain.NewEvent(domain.EventFillReceived, "tenant-1", domain.EnvModePaper, "fill-1", fillPayload)
	require.NoError(t, err)

	require.NoError(t, reval.handleFillReceived(context.Background(), *ev))

	pos, ok := posMonitor.LookupPosition("AAPL")
	require.True(t, ok)
	require.NotNil(t, pos.EntryThesis)
	assert.Equal(t, "strong breakout", pos.EntryThesis.BullArgument)
	assert.Equal(t, domain.RiskModifierTight, pos.EntryThesis.RiskModifier)

	_, cached := reval.pendingTheses.Load("AAPL")
	assert.False(t, cached, "thesis should be consumed after fill")
}

func TestRevaluator_HandleFillReceived_IgnoresSellFills(t *testing.T) {
	bus := &mockEventBus{}
	reval, _ := newTestRevaluator(t, bus, &mockRiskAssessor{})

	reval.pendingTheses.Store("AAPL", &domain.EntryThesis{Confidence: 0.9})

	fillPayload := map[string]any{
		"symbol":   "AAPL",
		"side":     "SELL",
		"price":    float64(155.0),
		"quantity": float64(10.0),
	}
	ev, err := domain.NewEvent(domain.EventFillReceived, "tenant-1", domain.EnvModePaper, "fill-2", fillPayload)
	require.NoError(t, err)

	require.NoError(t, reval.handleFillReceived(context.Background(), *ev))

	_, stillCached := reval.pendingTheses.Load("AAPL")
	assert.True(t, stillCached, "thesis should not be consumed on SELL fill")
}

func TestRevaluator_HandleFillReceived_NoCachedThesis(t *testing.T) {
	bus := &mockEventBus{}
	reval, posMonitor := newTestRevaluator(t, bus, &mockRiskAssessor{})

	posMonitor.processFill(fillMsg{
		Symbol:     domain.Symbol("TSLA"),
		Side:       "BUY",
		Price:      200,
		Quantity:   5,
		FilledAt:   time.Now(),
		Strategy:   "ai_scalping",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})

	fillPayload := map[string]any{
		"symbol":   "TSLA",
		"side":     "BUY",
		"price":    float64(200.0),
		"quantity": float64(5.0),
	}
	ev, err := domain.NewEvent(domain.EventFillReceived, "tenant-1", domain.EnvModePaper, "fill-3", fillPayload)
	require.NoError(t, err)

	require.NoError(t, reval.handleFillReceived(context.Background(), *ev))

	pos, ok := posMonitor.LookupPosition("TSLA")
	require.True(t, ok)
	assert.Nil(t, pos.EntryThesis, "no thesis should be attached when none was cached")
}

func TestRevaluator_EvaluateAll_SkipsPositionsWithoutThesis(t *testing.T) {
	bus := &mockEventBus{}
	assessor := &mockRiskAssessor{
		result: &domain.RiskRevaluation{
			Symbol:       domain.Symbol("AAPL"),
			ThesisStatus: domain.ThesisIntact,
			Action:       domain.RiskActionHold,
			Confidence:   0.9,
			EvaluatedAt:  time.Now(),
		},
	}
	reval, posMonitor := newTestRevaluator(t, bus, assessor)

	posMonitor.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		FilledAt:   time.Now().Add(-10 * time.Minute),
		Strategy:   "ai_scalping",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})

	reval.evaluateAll(context.Background())

	assert.Equal(t, 0, assessor.calls, "should not call assessor for positions without thesis")
}

func TestRevaluator_EvaluateAll_SkipsExitPendingPositions(t *testing.T) {
	bus := &mockEventBus{}
	assessor := &mockRiskAssessor{
		result: &domain.RiskRevaluation{
			Symbol:       domain.Symbol("AAPL"),
			ThesisStatus: domain.ThesisIntact,
			Action:       domain.RiskActionHold,
			Confidence:   0.9,
			EvaluatedAt:  time.Now(),
		},
	}
	reval, posMonitor := newTestRevaluator(t, bus, assessor)

	posMonitor.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		FilledAt:   time.Now().Add(-10 * time.Minute),
		Strategy:   "ai_scalping",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})

	key := "tenant-1:Paper:AAPL"
	posMonitor.SetEntryThesis(key, &domain.EntryThesis{Confidence: 0.85, Direction: domain.DirectionLong})

	posMonitor.mu.Lock()
	posMonitor.positions[key].ExitPending = true
	posMonitor.mu.Unlock()

	reval.evaluateAll(context.Background())

	assert.Equal(t, 0, assessor.calls, "should not evaluate exit-pending positions")
}

func TestRevaluator_EvaluateAll_CallsAssessorWithThesis(t *testing.T) {
	bus := &mockEventBus{}
	assessor := &mockRiskAssessor{
		result: &domain.RiskRevaluation{
			Symbol:          domain.Symbol("AAPL"),
			ThesisStatus:    domain.ThesisDegrading,
			Action:          domain.RiskActionTighten,
			Confidence:      0.70,
			Reasoning:       "momentum fading",
			UpdatedModifier: domain.RiskModifierTight,
			EvaluatedAt:     time.Now(),
		},
	}
	reval, posMonitor := newTestRevaluator(t, bus, assessor)

	posMonitor.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		FilledAt:   time.Now().Add(-10 * time.Minute),
		Strategy:   "ai_scalping",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})

	key := "tenant-1:Paper:AAPL"
	posMonitor.SetEntryThesis(key, &domain.EntryThesis{
		BullArgument: "strong breakout",
		Confidence:   0.85,
		Direction:    domain.DirectionLong,
	})

	reval.evaluateAll(context.Background())

	assert.Equal(t, 1, assessor.calls, "should call assessor for position with thesis")
	assert.True(t, bus.publishedCount(domain.EventRiskRevaluated) >= 1, "should emit RiskRevaluated event")
}

func TestRevaluator_ApplyRevaluation_StoresResult(t *testing.T) {
	bus := &mockEventBus{}
	_, posMonitor := newTestRevaluator(t, bus, &mockRiskAssessor{})

	posMonitor.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		FilledAt:   time.Now(),
		Strategy:   "ai_scalping",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})

	key := "tenant-1:Paper:AAPL"
	evalTime := time.Now()
	result := &domain.RiskRevaluation{
		Symbol:          domain.Symbol("AAPL"),
		ThesisStatus:    domain.ThesisIntact,
		Action:          domain.RiskActionHold,
		Confidence:      0.90,
		Reasoning:       "thesis still valid",
		UpdatedModifier: domain.RiskModifierNormal,
		EvaluatedAt:     evalTime,
	}

	posMonitor.ApplyRevaluation(key, result)

	pos, ok := posMonitor.LookupPosition("AAPL")
	require.True(t, ok)
	require.NotNil(t, pos.LastRevaluation)
	assert.Equal(t, domain.ThesisIntact, pos.LastRevaluation.ThesisStatus)
	assert.Equal(t, domain.RiskActionHold, pos.LastRevaluation.Action)
	assert.Equal(t, evalTime, pos.LastRevaluationAt)
}

func TestRevaluator_ApplyRevaluation_TightenModifiesExitRules(t *testing.T) {
	bus := &mockEventBus{}
	_, posMonitor := newTestRevaluator(t, bus, &mockRiskAssessor{})

	trailingStop, err := domain.NewExitRule(domain.ExitRuleTrailingStop, map[string]float64{"pct": 0.02})
	require.NoError(t, err)

	posMonitor.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		FilledAt:   time.Now(),
		Strategy:   "ai_scalping",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{trailingStop},
	})

	key := "tenant-1:Paper:AAPL"
	result := &domain.RiskRevaluation{
		Symbol:          domain.Symbol("AAPL"),
		ThesisStatus:    domain.ThesisDegrading,
		Action:          domain.RiskActionTighten,
		UpdatedModifier: domain.RiskModifierTight,
		EvaluatedAt:     time.Now(),
	}

	posMonitor.ApplyRevaluation(key, result)

	pos, ok := posMonitor.LookupPosition("AAPL")
	require.True(t, ok)
	// TIGHT multiplier is 0.90, so 0.02 * 0.90 = 0.018
	assert.InDelta(t, 0.018, pos.ExitRules[0].Params["pct"], 0.001)
}

func TestRevaluator_EndToEnd_EnrichThenFillAttachesThesis(t *testing.T) {
	bus := &mockEventBus{}
	assessor := &mockRiskAssessor{
		result: &domain.RiskRevaluation{
			Symbol:       domain.Symbol("NVDA"),
			ThesisStatus: domain.ThesisIntact,
			Action:       domain.RiskActionHold,
			Confidence:   0.85,
			EvaluatedAt:  time.Now(),
		},
	}
	reval, posMonitor := newTestRevaluator(t, bus, assessor)

	enrichment := domain.SignalEnrichment{
		Signal:         domain.SignalRef{Symbol: "NVDA", SignalType: "entry", Side: "buy"},
		Status:         domain.EnrichmentOK,
		Confidence:     0.90,
		Direction:      domain.DirectionLong,
		BullArgument:   "AI chip demand surge",
		BearArgument:   "valuation stretched",
		JudgeReasoning: "demand thesis dominates",
		RiskModifier:   domain.RiskModifierWide,
	}

	enrichEv, err := domain.NewEvent(domain.EventSignalEnriched, "tenant-1", domain.EnvModePaper, "enrich-e2e", enrichment)
	require.NoError(t, err)
	require.NoError(t, reval.handleSignalEnriched(context.Background(), *enrichEv))

	posMonitor.processFill(fillMsg{
		Symbol:     domain.Symbol("NVDA"),
		Side:       "BUY",
		Price:      900,
		Quantity:   2,
		FilledAt:   time.Now(),
		Strategy:   "ai_scalping",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})

	fillPayload := map[string]any{
		"symbol":   "NVDA",
		"side":     "BUY",
		"price":    float64(900.0),
		"quantity": float64(2.0),
	}
	fillEv, err := domain.NewEvent(domain.EventFillReceived, "tenant-1", domain.EnvModePaper, "fill-e2e", fillPayload)
	require.NoError(t, err)
	require.NoError(t, reval.handleFillReceived(context.Background(), *fillEv))

	pos, ok := posMonitor.LookupPosition("NVDA")
	require.True(t, ok)
	require.NotNil(t, pos.EntryThesis, "thesis should be attached after enrich → fill sequence")
	assert.Equal(t, "AI chip demand surge", pos.EntryThesis.BullArgument)
	assert.Equal(t, domain.RiskModifierWide, pos.EntryThesis.RiskModifier)
	assert.InDelta(t, 0.90, pos.EntryThesis.Confidence, 0.001)

	_, cached := reval.pendingTheses.Load("NVDA")
	assert.False(t, cached, "thesis should be consumed from cache")
}

func TestRevaluator_DefaultInterval(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())
	posMonitor := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop())

	reval := NewRevaluator(posMonitor, &mockRiskAssessor{}, bus, nil, nil, 0, "tenant-1", domain.EnvModePaper, zerolog.Nop())

	assert.Equal(t, 5*time.Minute, reval.interval, "negative/zero interval should default to 5 minutes")
}
