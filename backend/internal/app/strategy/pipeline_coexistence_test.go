package strategy_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/debate"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	stratports "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPipelineCoexistence_BothPipelinesIndependent(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	debateAdvisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{
		Direction:      domain.DirectionLong,
		Confidence:     0.88,
		Rationale:      "debate rationale",
		BullArgument:   "bull",
		BearArgument:   "bear",
		JudgeReasoning: "judge",
	}}
	enricherAdvisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{
		Direction:      domain.DirectionLong,
		Confidence:     0.91,
		Rationale:      "signal rationale",
		BullArgument:   "b",
		BearArgument:   "br",
		JudgeReasoning: "j",
	}}

	debateSvc := debate.NewService(bus, debateAdvisor, nil, 0.7, zerolog.Nop())
	require.NoError(t, debateSvc.Start(ctx))

	enricher := strategy.NewSignalDebateEnricher(bus, enricherAdvisor, nil)
	require.NoError(t, enricher.Start(ctx))

	store := &fakeSpecStore{spec: &stratports.Spec{Params: map[string]any{
		"limit_offset_bps":   int64(0),
		"stop_bps":           int64(250),
		"risk_per_trade_bps": int64(10),
	}}}
	rs := strategy.NewRiskSizer(bus, store, 100000, slog.Default())
	require.NoError(t, rs.Start(ctx))

	orderIntents := subscribeOrderIntentCreated(t, bus)
	signalEnriched := subscribeSignalEnriched(t, bus)

	setup := monitor.SetupCondition{
		Symbol:    "BTCUSD",
		Timeframe: "1h",
		Direction: domain.DirectionLong,
		Trigger:   "RSI_Oversold",
		Snapshot:  domain.IndicatorSnapshot{Time: time.Now(), Symbol: "BTCUSD", Timeframe: "1h", RSI: 42.0, StochK: 30.0, StochD: 28.0, EMA9: 50100.0, EMA21: 49900.0, VWAP: 50000.0, Volume: 1000.0, VolumeSMA: 800.0},
		Regime:    domain.MarketRegime{Symbol: "BTCUSD", Timeframe: "1h", Type: domain.RegimeTrend, Since: time.Now().Add(-time.Hour), Strength: 0.8},
	}
	envMode := mustEnvMode(t)
	setupEv, err := domain.NewEvent(domain.EventSetupDetected, "t1", envMode, "setup-1", setup)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *setupEv))

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, err := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.8, map[string]string{"ref_price": "100"})
	require.NoError(t, err)
	publishSignalCreated(t, bus, sig)

	intents := waitForEvents(t, orderIntents, 2)
	_ = waitForEvents(t, signalEnriched, 1)

	require.Equal(t, 1, debateAdvisor.calls)
	require.Equal(t, 1, enricherAdvisor.calls)

	var gotDebate, gotAVWAP *domain.OrderIntent
	for i := range intents {
		intent, ok := intents[i].Payload.(domain.OrderIntent)
		require.True(t, ok)
		if intent.Strategy == "RSI_Oversold" {
			ii := intent
			gotDebate = &ii
		}
		if intent.Strategy == "avwap_v1" {
			ii := intent
			gotAVWAP = &ii
		}
	}
	require.NotNil(t, gotDebate, "expected debate pipeline OrderIntentCreated")
	require.NotNil(t, gotAVWAP, "expected avwap pipeline OrderIntentCreated")

	assert.Equal(t, domain.Symbol("BTCUSD"), gotDebate.Symbol)
	assert.Equal(t, "debate rationale", gotDebate.Rationale)
	assert.InDelta(t, 0.88, gotDebate.Confidence, 0.0000001)

	assert.Equal(t, domain.Symbol("AAPL"), gotAVWAP.Symbol)
	assert.Equal(t, "signal rationale", gotAVWAP.Rationale)
	assert.InDelta(t, 0.91, gotAVWAP.Confidence, 0.0000001)
	assert.Equal(t, domain.DirectionLong, gotAVWAP.Direction)
}

func TestPipelineCoexistence_SetupDetectedDoesNotTriggerEnricher(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	debateAdvisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{
		Direction:  domain.DirectionLong,
		Confidence: 0.9,
		Rationale:  "debate ok",
	}}
	enricherAdvisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{Confidence: 0.99}}

	debateSvc := debate.NewService(bus, debateAdvisor, nil, 0.7, zerolog.Nop())
	require.NoError(t, debateSvc.Start(ctx))

	enricher := strategy.NewSignalDebateEnricher(bus, enricherAdvisor, nil)
	require.NoError(t, enricher.Start(ctx))

	store := &fakeSpecStore{spec: &stratports.Spec{Params: map[string]any{
		"limit_offset_bps":   int64(0),
		"stop_bps":           int64(250),
		"risk_per_trade_bps": int64(10),
	}}}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	require.NoError(t, rs.Start(ctx))

	orderIntents := subscribeOrderIntentCreated(t, bus)
	signalEnriched := subscribeSignalEnriched(t, bus)

	setup := monitor.SetupCondition{
		Symbol:    "BTCUSD",
		Timeframe: "1h",
		Direction: domain.DirectionLong,
		Trigger:   "RSI_Oversold",
		Snapshot:  domain.IndicatorSnapshot{Time: time.Now(), Symbol: "BTCUSD", Timeframe: "1h", RSI: 42.0, StochK: 30.0, StochD: 28.0, EMA9: 50100.0, EMA21: 49900.0, VWAP: 50000.0, Volume: 1000.0, VolumeSMA: 800.0},
		Regime:    domain.MarketRegime{Symbol: "BTCUSD", Timeframe: "1h", Type: domain.RegimeTrend, Since: time.Now().Add(-time.Hour), Strength: 0.8},
	}
	envMode := mustEnvMode(t)
	setupEv, err := domain.NewEvent(domain.EventSetupDetected, "t1", envMode, "setup-only", setup)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *setupEv))

	evs := waitForEvents(t, orderIntents, 1)
	intent := evs[0].Payload.(domain.OrderIntent)
	assert.Equal(t, "RSI_Oversold", intent.Strategy)
	assert.Equal(t, 1, debateAdvisor.calls)
	assert.Equal(t, 0, enricherAdvisor.calls)

	noEventsReceived(t, signalEnriched)
	noEventsReceived(t, orderIntents)
}

func TestPipelineCoexistence_SignalCreatedDoesNotTriggerDebateService(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	debateAdvisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{Confidence: 0.99, Rationale: "should not be called"}}
	enricherAdvisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{
		Direction:  domain.DirectionLong,
		Confidence: 0.93,
		Rationale:  "ai rationale",
	}}

	debateSvc := debate.NewService(bus, debateAdvisor, nil, 0.7, zerolog.Nop())
	require.NoError(t, debateSvc.Start(ctx))

	enricher := strategy.NewSignalDebateEnricher(bus, enricherAdvisor, nil)
	require.NoError(t, enricher.Start(ctx))

	store := &fakeSpecStore{spec: &stratports.Spec{Params: map[string]any{
		"limit_offset_bps":   int64(0),
		"stop_bps":           int64(250),
		"risk_per_trade_bps": int64(10),
	}}}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	require.NoError(t, rs.Start(ctx))

	orderIntents := subscribeOrderIntentCreated(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, err := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.8, map[string]string{"ref_price": "100"})
	require.NoError(t, err)
	publishSignalCreated(t, bus, sig)

	evs := waitForEvents(t, orderIntents, 1)
	intent := evs[0].Payload.(domain.OrderIntent)
	assert.Equal(t, "avwap_v1", intent.Strategy)
	assert.Equal(t, "ai rationale", intent.Rationale)
	assert.InDelta(t, 0.93, intent.Confidence, 0.0000001)
	assert.Equal(t, domain.DirectionLong, intent.Direction)

	assert.Equal(t, 0, debateAdvisor.calls)
	assert.Equal(t, 1, enricherAdvisor.calls)
	noEventsReceived(t, orderIntents)
}

func TestE2E_AVWAPSignal_AISuccess_ProducesEnrichedOrderIntent(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	advisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{
		Direction:      domain.DirectionLong,
		Confidence:     0.91,
		Rationale:      "AI enriched rationale",
		BullArgument:   "bull",
		BearArgument:   "bear",
		JudgeReasoning: "judge",
	}}
	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil)
	require.NoError(t, enricher.Start(ctx))

	store := &fakeSpecStore{spec: &stratports.Spec{Params: map[string]any{
		"limit_offset_bps":   int64(0),
		"stop_bps":           int64(250),
		"risk_per_trade_bps": int64(10),
	}}}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	require.NoError(t, rs.Start(ctx))

	orderIntents := subscribeOrderIntentCreated(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, err := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.42, map[string]string{"ref_price": "100"})
	require.NoError(t, err)
	publishSignalCreated(t, bus, sig)

	evs := waitForEvents(t, orderIntents, 1)
	intent, ok := evs[0].Payload.(domain.OrderIntent)
	require.True(t, ok)

	assert.Equal(t, "avwap_v1", intent.Strategy)
	assert.Equal(t, domain.Symbol("AAPL"), intent.Symbol)
	assert.Equal(t, domain.DirectionLong, intent.Direction)
	assert.Equal(t, "AI enriched rationale", intent.Rationale)
	assert.InDelta(t, 0.91, intent.Confidence, 0.0000001)
	assert.InDelta(t, 100.0, intent.LimitPrice, 0.0000001)
	assert.InDelta(t, 97.5, intent.StopLoss, 0.0000001)
	assert.Equal(t, 40.0, intent.Quantity)
}

func TestE2E_AVWAPSignal_AITimeout_ProducesFallbackOrderIntent(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	advisor := &fakeAIAdvisor{delay: 10 * time.Second}
	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil, strategy.WithDebateTimeout(50*time.Millisecond))
	require.NoError(t, enricher.Start(ctx))

	store := &fakeSpecStore{spec: &stratports.Spec{Params: map[string]any{
		"limit_offset_bps":   int64(0),
		"stop_bps":           int64(250),
		"risk_per_trade_bps": int64(10),
	}}}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	require.NoError(t, rs.Start(ctx))

	orderIntents := subscribeOrderIntentCreated(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, err := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.42, map[string]string{"ref_price": "100"})
	require.NoError(t, err)
	publishSignalCreated(t, bus, sig)

	evs := waitForEvents(t, orderIntents, 1)
	intent := evs[0].Payload.(domain.OrderIntent)
	assert.Equal(t, "avwap_v1", intent.Strategy)
	assert.Equal(t, domain.Symbol("AAPL"), intent.Symbol)
	assert.Equal(t, domain.DirectionLong, intent.Direction)
	assert.InDelta(t, 0.65, intent.Confidence, 0.0000001)
	assert.Contains(t, intent.Rationale, "timeout")
	assert.Equal(t, 40.0, intent.Quantity)
}
