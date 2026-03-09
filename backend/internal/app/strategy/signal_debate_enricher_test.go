package strategy_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAIAdvisor struct {
	decision       *domain.AdvisoryDecision
	err            error
	delay          time.Duration
	calls          int
	lastOpts       []ports.DebateOption
	lastRegime     domain.MarketRegime
	lastIndicators domain.IndicatorSnapshot
}

type mockRepository struct {
	mu          sync.Mutex
	thoughtLogs []domain.ThoughtLog
}

func (m *mockRepository) SaveMarketBar(context.Context, domain.MarketBar) error { return nil }

func (m *mockRepository) GetMarketBars(context.Context, domain.Symbol, domain.Timeframe, time.Time, time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}

func (m *mockRepository) SaveTrade(context.Context, domain.Trade) error { return nil }

func (m *mockRepository) GetTrades(context.Context, string, domain.EnvMode, time.Time, time.Time) ([]domain.Trade, error) {
	return nil, nil
}

func (m *mockRepository) SaveStrategyDNA(context.Context, domain.StrategyDNA) error { return nil }

func (m *mockRepository) GetLatestStrategyDNA(context.Context, string, domain.EnvMode) (*domain.StrategyDNA, error) {
	return nil, nil
}

func (m *mockRepository) SaveOrder(context.Context, domain.BrokerOrder) error { return nil }

func (m *mockRepository) UpdateOrderFill(context.Context, string, time.Time, float64, float64) error {
	return nil
}

func (m *mockRepository) ListTrades(context.Context, ports.TradeQuery) (ports.TradePage, error) {
	return ports.TradePage{}, nil
}

func (m *mockRepository) ListOrders(context.Context, ports.OrderQuery) (ports.OrderPage, error) {
	return ports.OrderPage{}, nil
}

func (m *mockRepository) SaveThoughtLog(_ context.Context, tl domain.ThoughtLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.thoughtLogs = append(m.thoughtLogs, tl)
	return nil
}

func (m *mockRepository) GetThoughtLogsByIntentID(context.Context, string) ([]domain.ThoughtLog, error) {
	return nil, nil
}
func (m *mockRepository) UpdateTradeThesis(context.Context, string, domain.EnvMode, domain.Symbol, json.RawMessage) error {
	return nil
}
func (m *mockRepository) GetMaxBarHighSince(context.Context, domain.Symbol, domain.Timeframe, time.Time) (float64, error) {
	return 0, nil
}

func (f *fakeAIAdvisor) RequestDebate(ctx context.Context, symbol domain.Symbol, regime domain.MarketRegime, indicators domain.IndicatorSnapshot, opts ...ports.DebateOption) (*domain.AdvisoryDecision, error) {
	f.calls++
	f.lastOpts = opts
	f.lastRegime = regime
	f.lastIndicators = indicators
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.decision, nil
}

func subscribeSignalEnriched(t *testing.T, bus *memory.Bus) <-chan domain.Event {
	t.Helper()
	ch := make(chan domain.Event, 10)
	ctx := context.Background()
	require.NoError(t, bus.Subscribe(ctx, domain.EventSignalEnriched, func(_ context.Context, ev domain.Event) error {
		ch <- ev
		return nil
	}))
	return ch
}

func noEventsReceived(t *testing.T, ch <-chan domain.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		require.FailNow(t, "unexpected event received", "type=%s", ev.Type)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSignalDebateEnricher_EntrySignal_AISuccess(t *testing.T) {
	bus := memory.NewBus()
	advisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{
		Direction:      domain.DirectionLong,
		Confidence:     0.91,
		Rationale:      "r",
		BullArgument:   "b",
		BearArgument:   "br",
		JudgeReasoning: "j",
	}}

	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil)
	require.NoError(t, enricher.Start(context.Background()))

	received := subscribeSignalEnriched(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.8, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	evs := waitForEvents(t, received, 1)
	require.Equal(t, domain.EventSignalEnriched, evs[0].Type)
	got, ok := evs[0].Payload.(domain.SignalEnrichment)
	require.True(t, ok)

	assert.Equal(t, domain.EnrichmentOK, got.Status)
	assert.InDelta(t, 0.91, got.Confidence, 0.0000001)
	assert.Equal(t, "r", got.Rationale)
	assert.Equal(t, domain.DirectionLong, got.Direction)
	assert.Equal(t, "b", got.BullArgument)
	assert.Equal(t, "br", got.BearArgument)
	assert.Equal(t, "j", got.JudgeReasoning)

	assert.Equal(t, string(iid), got.Signal.StrategyInstanceID)
	assert.Equal(t, "AAPL", got.Signal.Symbol)
	assert.Equal(t, strat.SignalEntry.String(), got.Signal.SignalType)
	assert.Equal(t, strat.SideBuy.String(), got.Signal.Side)
	assert.InDelta(t, 0.8, got.Signal.Strength, 0.0000001)
	assert.Equal(t, sig.Tags, got.Signal.Tags)
}

func TestSignalDebateEnricher_EntrySignal_AITimeout(t *testing.T) {
	bus := memory.NewBus()
	advisor := &fakeAIAdvisor{delay: 10 * time.Second}

	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil, strategy.WithDebateTimeout(100*time.Millisecond))
	require.NoError(t, enricher.Start(context.Background()))
	received := subscribeSignalEnriched(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.42, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	evs := waitForEvents(t, received, 1)
	got := evs[0].Payload.(domain.SignalEnrichment)
	assert.Equal(t, domain.EnrichmentTimeout, got.Status)
	assert.InDelta(t, sig.Strength, got.Confidence, 0.0000001)
	assert.NotEmpty(t, got.Rationale)
	assert.Equal(t, domain.DirectionLong, got.Direction)
	assert.Empty(t, got.BullArgument)
	assert.Empty(t, got.BearArgument)
	assert.Empty(t, got.JudgeReasoning)
}

func TestSignalDebateEnricher_EntrySignal_AIError(t *testing.T) {
	bus := memory.NewBus()
	advisor := &fakeAIAdvisor{err: errors.New("llm down")}

	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil, strategy.WithDebateTimeout(200*time.Millisecond))
	require.NoError(t, enricher.Start(context.Background()))
	received := subscribeSignalEnriched(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideSell, 0.77, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	evs := waitForEvents(t, received, 1)
	got := evs[0].Payload.(domain.SignalEnrichment)
	assert.Equal(t, domain.EnrichmentError, got.Status)
	assert.InDelta(t, sig.Strength, got.Confidence, 0.0000001)
	assert.NotEmpty(t, got.Rationale)
	assert.Equal(t, domain.DirectionShort, got.Direction)
}

func TestSignalDebateEnricher_ExitSignal_Skipped(t *testing.T) {
	bus := memory.NewBus()
	advisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{Confidence: 0.99}}

	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil)
	require.NoError(t, enricher.Start(context.Background()))
	received := subscribeSignalEnriched(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalExit, strat.SideSell, 0.66, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	evs := waitForEvents(t, received, 1)
	got := evs[0].Payload.(domain.SignalEnrichment)
	assert.Equal(t, domain.EnrichmentSkipped, got.Status)
	assert.InDelta(t, sig.Strength, got.Confidence, 0.0000001)
	assert.Equal(t, domain.DirectionCloseLong, got.Direction)
	assert.Equal(t, 0, advisor.calls)
}

func TestSignalDebateEnricher_ExitSignal_WithPnL(t *testing.T) {
	bus := memory.NewBus()
	advisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{Confidence: 0.99}}

	posLookup := func(symbol string) (domain.MonitoredPosition, bool) {
		if symbol == "AAPL" {
			pos, _ := domain.NewMonitoredPosition("AAPL", 95.0, time.Now(), "avwap_v1", domain.AssetClassEquity, nil, "t1", domain.EnvModePaper, 10)
			return pos, true
		}
		return domain.MonitoredPosition{}, false
	}

	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil,
		strategy.WithPositionLookup(posLookup),
	)
	require.NoError(t, enricher.Start(context.Background()))
	received := subscribeSignalEnriched(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalExit, strat.SideSell, 0.80, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	evs := waitForEvents(t, received, 1)
	got := evs[0].Payload.(domain.SignalEnrichment)
	assert.Equal(t, domain.EnrichmentSkipped, got.Status)
	assert.True(t, got.HasPnL)
	assert.InDelta(t, 95.0, got.EntryPrice, 0.001)
	assert.InDelta(t, 0.05263, got.UnrealizedPnLPct, 0.001) // (100-95)/95 ≈ 5.26%
	assert.Equal(t, 0, advisor.calls)
}

func TestSignalDebateEnricher_ExitSignal_NoPnLWithoutLookup(t *testing.T) {
	bus := memory.NewBus()
	advisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{Confidence: 0.99}}

	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil)
	require.NoError(t, enricher.Start(context.Background()))
	received := subscribeSignalEnriched(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalExit, strat.SideSell, 0.66, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	evs := waitForEvents(t, received, 1)
	got := evs[0].Payload.(domain.SignalEnrichment)
	assert.Equal(t, domain.EnrichmentSkipped, got.Status)
	assert.False(t, got.HasPnL)
	assert.InDelta(t, 0.0, got.EntryPrice, 0.001)
	assert.InDelta(t, 0.0, got.UnrealizedPnLPct, 0.001)
}

func TestSignalDebateEnricher_FlatSignal_Ignored(t *testing.T) {
	bus := memory.NewBus()
	advisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{Confidence: 0.99}}

	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil)
	require.NoError(t, enricher.Start(context.Background()))
	received := subscribeSignalEnriched(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalFlat, strat.SideBuy, 0.5, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	noEventsReceived(t, received)
	assert.Equal(t, 0, advisor.calls)
}

func TestSignalDebateEnricher_NonSignalPayload_Ignored(t *testing.T) {
	bus := memory.NewBus()
	advisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{Confidence: 0.99}}

	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil)
	require.NoError(t, enricher.Start(context.Background()))
	received := subscribeSignalEnriched(t, bus)

	ctx := context.Background()
	envMode := mustEnvMode(t)
	ev, err := domain.NewEvent(domain.EventSignalCreated, "t1", envMode, "nonsig-1", "not-a-signal")
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *ev))

	noEventsReceived(t, received)
}

func TestSignalDebateEnricher_PassesSignalContext(t *testing.T) {
	bus := memory.NewBus()
	advisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{Direction: domain.DirectionLong, Confidence: 0.5}}

	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil,
		strategy.WithDebateOptionFactory(func(sig strat.Signal) []ports.DebateOption {
			return []ports.DebateOption{func(any) {}}
		}),
	)
	require.NoError(t, enricher.Start(context.Background()))
	received := subscribeSignalEnriched(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.9, map[string]string{"ref_price": "100", "tag1": "v"})
	publishSignalCreated(t, bus, sig)

	_ = waitForEvents(t, received, 1)
	assert.Equal(t, 1, advisor.calls)
	assert.Greater(t, len(advisor.lastOpts), 0)
}

func TestSignalDebateEnricher_SavesThoughtLogOnAISuccess(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	advisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{
		Direction:      domain.DirectionLong,
		Confidence:     0.91,
		Rationale:      "r",
		BullArgument:   "b",
		BearArgument:   "br",
		JudgeReasoning: "j",
	}}

	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil, strategy.WithRepository(repo))
	require.NoError(t, enricher.Start(context.Background()))

	received := subscribeSignalEnriched(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.8, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	_ = waitForEvents(t, received, 1)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.thoughtLogs, 1)
	got := repo.thoughtLogs[0]
	assert.Equal(t, "SignalEnriched", got.EventType)
	assert.NotEmpty(t, got.BullArgument)
	assert.NotEmpty(t, got.BearArgument)
	assert.NotEmpty(t, got.JudgeReasoning)
}

func TestSignalDebateEnricher_SavesThoughtLogOnFallback(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	advisor := &fakeAIAdvisor{err: errors.New("llm down")}

	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil, strategy.WithRepository(repo), strategy.WithDebateTimeout(200*time.Millisecond))
	require.NoError(t, enricher.Start(context.Background()))

	received := subscribeSignalEnriched(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.77, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	_ = waitForEvents(t, received, 1)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.thoughtLogs, 1)
	got := repo.thoughtLogs[0]
	assert.Equal(t, "SignalEnriched", got.EventType)
	assert.Empty(t, got.BullArgument)
	assert.NotEmpty(t, got.Rationale)
}

func TestSignalDebateEnricher_NoRepoNoError(t *testing.T) {
	bus := memory.NewBus()
	advisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{Direction: domain.DirectionLong, Confidence: 0.5}}

	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil)
	require.NoError(t, enricher.Start(context.Background()))

	received := subscribeSignalEnriched(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.8, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	_ = waitForEvents(t, received, 1)
}

func TestSignalDebateEnricher_WithMarketDataProvider(t *testing.T) {
	bus := memory.NewBus()
	advisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{
		Direction:  domain.DirectionLong,
		Confidence: 0.85,
		Rationale:  "strong momentum",
	}}

	provider := func(symbol string) (domain.IndicatorSnapshot, bool) {
		if symbol == "BTC/USD" {
			return domain.IndicatorSnapshot{
				Symbol:    "BTC/USD",
				Timeframe: "5m",
				RSI:       62.5,
				StochK:    71.0,
				StochD:    68.0,
				EMA9:      85000.0,
				EMA21:     84500.0,
				VWAP:      84800.0,
				AnchorRegimes: map[domain.Timeframe]domain.MarketRegime{
					"5m": {
						Symbol:    "BTC/USD",
						Timeframe: "5m",
						Type:      "bullish",
						Strength:  0.75,
					},
				},
			}, true
		}
		return domain.IndicatorSnapshot{}, false
	}

	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil,
		strategy.WithMarketDataProvider(provider),
	)
	require.NoError(t, enricher.Start(context.Background()))

	received := subscribeSignalEnriched(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:BTC/USD")
	sig, _ := strat.NewSignal(iid, "BTC/USD", strat.SignalEntry, strat.SideBuy, 0.9, map[string]string{"ref_price": "85000"})
	publishSignalCreated(t, bus, sig)

	evs := waitForEvents(t, received, 1)
	got := evs[0].Payload.(domain.SignalEnrichment)

	// AI advisor should have received real market data, not zeros.
	assert.Equal(t, 1, advisor.calls)
	assert.InDelta(t, 62.5, advisor.lastIndicators.RSI, 0.001)
	assert.InDelta(t, 71.0, advisor.lastIndicators.StochK, 0.001)
	assert.InDelta(t, 68.0, advisor.lastIndicators.StochD, 0.001)
	assert.InDelta(t, 85000.0, advisor.lastIndicators.EMA9, 0.001)
	assert.InDelta(t, 84500.0, advisor.lastIndicators.EMA21, 0.001)
	assert.InDelta(t, 84800.0, advisor.lastIndicators.VWAP, 0.001)

	// Regime should come from AnchorRegimes["5m"].
	assert.Equal(t, domain.RegimeType("bullish"), advisor.lastRegime.Type)
	assert.InDelta(t, 0.75, advisor.lastRegime.Strength, 0.001)

	// Enrichment should be OK with AI-supplied values.
	assert.Equal(t, domain.EnrichmentOK, got.Status)
	assert.InDelta(t, 0.85, got.Confidence, 0.001)
	assert.Equal(t, domain.DirectionLong, got.Direction)
}

func TestSignalDebateEnricher_MarketDataProvider_FallbackTo15m(t *testing.T) {
	bus := memory.NewBus()
	advisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{
		Direction:  domain.DirectionShort,
		Confidence: 0.70,
		Rationale:  "weakness",
	}}

	provider := func(symbol string) (domain.IndicatorSnapshot, bool) {
		return domain.IndicatorSnapshot{
			RSI: 35.0,
			AnchorRegimes: map[domain.Timeframe]domain.MarketRegime{
				"15m": {
					Type:     "bearish",
					Strength: 0.60,
				},
			},
		}, true
	}

	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil,
		strategy.WithMarketDataProvider(provider),
	)
	require.NoError(t, enricher.Start(context.Background()))

	received := subscribeSignalEnriched(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:ETH/USD")
	sig, _ := strat.NewSignal(iid, "ETH/USD", strat.SignalEntry, strat.SideSell, 0.8, map[string]string{"ref_price": "2500"})
	publishSignalCreated(t, bus, sig)

	_ = waitForEvents(t, received, 1)

	// Should fall back to 15m regime when 5m is absent.
	assert.Equal(t, domain.RegimeType("bearish"), advisor.lastRegime.Type)
	assert.InDelta(t, 0.60, advisor.lastRegime.Strength, 0.001)
	assert.InDelta(t, 35.0, advisor.lastIndicators.RSI, 0.001)
}

func TestSignalDebateEnricher_MarketDataProvider_SymbolNotFound(t *testing.T) {
	bus := memory.NewBus()
	advisor := &fakeAIAdvisor{decision: &domain.AdvisoryDecision{
		Direction:  domain.DirectionLong,
		Confidence: 0.50,
		Rationale:  "no data",
	}}

	// Provider that never finds any symbol.
	provider := func(symbol string) (domain.IndicatorSnapshot, bool) {
		return domain.IndicatorSnapshot{}, false
	}

	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil,
		strategy.WithMarketDataProvider(provider),
	)
	require.NoError(t, enricher.Start(context.Background()))

	received := subscribeSignalEnriched(t, bus)

	iid, _ := strat.NewInstanceID("avwap_v1:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.7, map[string]string{"ref_price": "150"})
	publishSignalCreated(t, bus, sig)

	_ = waitForEvents(t, received, 1)

	// Should still call AI, but with zero indicators/regime.
	assert.Equal(t, 1, advisor.calls)
	assert.InDelta(t, 0.0, advisor.lastIndicators.RSI, 0.001)
	assert.InDelta(t, 0.0, advisor.lastRegime.Strength, 0.001)
}
