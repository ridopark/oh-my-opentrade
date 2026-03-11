package activation_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/activation"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/domain/screener"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDataProvider struct {
	mu   sync.Mutex
	bars map[string][]domain.MarketBar
}

func newMockDataProvider() *mockDataProvider {
	return &mockDataProvider{bars: make(map[string][]domain.MarketBar)}
}

func (m *mockDataProvider) SetBars(symbol string, tf string, bars []domain.MarketBar) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bars[symbol+":"+tf] = bars
}

func (m *mockDataProvider) GetHistoricalBars(_ context.Context, symbol domain.Symbol, timeframe domain.Timeframe, _, _ time.Time) ([]domain.MarketBar, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := string(symbol) + ":" + string(timeframe)
	if bars, ok := m.bars[key]; ok {
		return bars, nil
	}
	return nil, nil
}

type mockSubscriber struct {
	mu         sync.Mutex
	subscribed []domain.Symbol
}

func (m *mockSubscriber) SubscribeSymbols(_ context.Context, symbols []domain.Symbol) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subscribed = append(m.subscribed, symbols...)
	return nil
}

func (m *mockSubscriber) Subscribed() []domain.Symbol {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Symbol, len(m.subscribed))
	copy(out, m.subscribed)
	return out
}

type mockSpikeFilter struct {
	mu      sync.Mutex
	configs map[string]float64
	seeded  map[string]int
}

func newMockSpikeFilter() *mockSpikeFilter {
	return &mockSpikeFilter{
		configs: make(map[string]float64),
		seeded:  make(map[string]int),
	}
}

func (m *mockSpikeFilter) SetMaxDeviation(symbol domain.Symbol, maxDev float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs[string(symbol)] = maxDev
}

func (m *mockSpikeFilter) Seed(sym domain.Symbol, bars []domain.MarketBar) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seeded[string(sym)] = len(bars)
	return len(bars)
}

type mockRepo struct{}

func (m *mockRepo) SaveMarketBar(_ context.Context, _ domain.MarketBar) error { return nil }
func (m *mockRepo) GetMarketBars(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _, _ time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}
func (m *mockRepo) SaveTrade(_ context.Context, _ domain.Trade) error { return nil }
func (m *mockRepo) GetTrades(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) ([]domain.Trade, error) {
	return nil, nil
}
func (m *mockRepo) UpdateTradeThesis(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol, _ json.RawMessage) error {
	return nil
}
func (m *mockRepo) SaveStrategyDNA(_ context.Context, _ domain.StrategyDNA) error { return nil }
func (m *mockRepo) GetLatestStrategyDNA(_ context.Context, _ string, _ domain.EnvMode) (*domain.StrategyDNA, error) {
	return nil, nil
}
func (m *mockRepo) SaveOrder(_ context.Context, _ domain.BrokerOrder) error { return nil }
func (m *mockRepo) UpdateOrderFill(_ context.Context, _ string, _ time.Time, _, _ float64) error {
	return nil
}
func (m *mockRepo) ListTrades(_ context.Context, _ ports.TradeQuery) (ports.TradePage, error) {
	return ports.TradePage{}, nil
}
func (m *mockRepo) ListOrders(_ context.Context, _ ports.OrderQuery) (ports.OrderPage, error) {
	return ports.OrderPage{}, nil
}
func (m *mockRepo) GetMaxBarHighSince(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _ time.Time) (float64, error) {
	return 0, nil
}
func (m *mockRepo) GetLatestThesisForSymbol(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol) (json.RawMessage, error) {
	return nil, nil
}
func (m *mockRepo) SaveThoughtLog(_ context.Context, _ domain.ThoughtLog) error { return nil }
func (m *mockRepo) GetThoughtLogsByIntentID(_ context.Context, _ string) ([]domain.ThoughtLog, error) {
	return nil, nil
}
func (m *mockRepo) GetNonTerminalOrders(_ context.Context, _ string, _ domain.EnvMode) ([]domain.BrokerOrder, error) {
	return nil, nil
}
func (m *mockRepo) GetRecordedFillQty(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol, _ string, _ time.Time) (float64, error) {
	return 0, nil
}
func (m *mockRepo) UpdateOrderStatus(_ context.Context, _ string, _ string) error { return nil }
func (m *mockRepo) GetNetPositions(_ context.Context, _ string, _ domain.EnvMode) (map[domain.Symbol]float64, error) {
	return nil, nil
}

func makeDailyBars(symbol string, n int) []domain.MarketBar {
	bars := make([]domain.MarketBar, n)
	sym := domain.Symbol(symbol)
	for i := 0; i < n; i++ {
		t := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i)
		bar, _ := domain.NewMarketBar(t, sym, "1d", 100+float64(i)*0.1, 101+float64(i)*0.1, 99+float64(i)*0.1, 100+float64(i)*0.1, 1000)
		bars[i] = bar
	}
	return bars
}

func makeHourlyBars(symbol string, n int) []domain.MarketBar {
	bars := make([]domain.MarketBar, n)
	sym := domain.Symbol(symbol)
	for i := 0; i < n; i++ {
		t := time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
		bar, _ := domain.NewMarketBar(t, sym, "1h", 150, 151, 149, 150, 500)
		bars[i] = bar
	}
	return bars
}

func makeMinuteBars(symbol string, n int) []domain.MarketBar {
	bars := make([]domain.MarketBar, n)
	sym := domain.Symbol(symbol)
	for i := 0; i < n; i++ {
		t := time.Date(2025, 6, 1, 14, 30, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute)
		bar, _ := domain.NewMarketBar(t, sym, "1m", 150, 151, 149, 150, 100)
		bars[i] = bar
	}
	return bars
}

func TestService_ActivatesNewSymbols(t *testing.T) {
	bus := memory.NewBus()
	mon := monitor.NewService(bus, &mockRepo{}, zerolog.Nop())
	mon.SetBaseSymbols([]string{"AAPL"})
	data := newMockDataProvider()
	sub := &mockSubscriber{}
	sf := newMockSpikeFilter()

	data.SetBars("NVDA", "1d", makeDailyBars("NVDA", 200))
	data.SetBars("NVDA", "1h", makeHourlyBars("NVDA", 50))
	data.SetBars("NVDA", "1m", makeMinuteBars("NVDA", 30))

	svc := activation.NewService(
		zerolog.Nop(), bus, mon, data, sub, sf, nil, "1m",
	)
	svc.MarkWarmed("AAPL")

	err := svc.Start(context.Background())
	require.NoError(t, err)

	payload := screener.EffectiveSymbolsUpdatedPayload{
		StrategyKey: "orb_break_retest",
		RunID:       "test-run",
		AsOf:        time.Now(),
		Mode:        "union",
		Source:      "union",
		Symbols:     []string{"AAPL", "NVDA"},
	}
	evt, err := domain.NewEvent(domain.EventEffectiveSymbolsUpdated, "tenant", domain.EnvModePaper, "test-eff", payload)
	require.NoError(t, err)
	err = bus.Publish(context.Background(), *evt)
	require.NoError(t, err)

	assert.True(t, mon.IsReady("NVDA"), "NVDA should be marked ready after activation")
	assert.Contains(t, sub.Subscribed(), domain.Symbol("NVDA"), "NVDA should be subscribed via WebSocket")
	assert.Contains(t, sf.configs, "NVDA", "NVDA should have spike filter configured")
}

func TestService_SkipsAlreadyWarmedSymbols(t *testing.T) {
	bus := memory.NewBus()
	mon := monitor.NewService(bus, &mockRepo{}, zerolog.Nop())
	data := newMockDataProvider()

	svc := activation.NewService(
		zerolog.Nop(), bus, mon, data, nil, nil, nil, "1m",
	)
	svc.MarkWarmed("AAPL", "MSFT")

	err := svc.Start(context.Background())
	require.NoError(t, err)

	payload := screener.EffectiveSymbolsUpdatedPayload{
		StrategyKey: "orb_break_retest",
		RunID:       "test-run",
		AsOf:        time.Now(),
		Mode:        "intersection",
		Source:      "intersection",
		Symbols:     []string{"AAPL", "MSFT"},
	}
	evt, err := domain.NewEvent(domain.EventEffectiveSymbolsUpdated, "tenant", domain.EnvModePaper, "test-eff", payload)
	require.NoError(t, err)
	err = bus.Publish(context.Background(), *evt)
	require.NoError(t, err)

	assert.False(t, mon.IsReady("AAPL"), "AAPL should NOT be re-marked ready by activation (startup handles it)")
}

func TestService_HTFDataAvailableAfterActivation(t *testing.T) {
	bus := memory.NewBus()
	mon := monitor.NewService(bus, &mockRepo{}, zerolog.Nop())
	data := newMockDataProvider()

	data.SetBars("GOOG", "1d", makeDailyBars("GOOG", 200))
	data.SetBars("GOOG", "1h", makeHourlyBars("GOOG", 50))
	data.SetBars("GOOG", "1m", makeMinuteBars("GOOG", 30))

	svc := activation.NewService(
		zerolog.Nop(), bus, mon, data, nil, nil, nil, "1m",
	)

	err := svc.Start(context.Background())
	require.NoError(t, err)

	payload := screener.EffectiveSymbolsUpdatedPayload{
		StrategyKey: "orb_break_retest",
		RunID:       "test-run",
		AsOf:        time.Now(),
		Mode:        "replace",
		Source:      "screener",
		Symbols:     []string{"GOOG"},
	}
	evt, err := domain.NewEvent(domain.EventEffectiveSymbolsUpdated, "tenant", domain.EnvModePaper, "test-eff", payload)
	require.NoError(t, err)
	err = bus.Publish(context.Background(), *evt)
	require.NoError(t, err)

	assert.True(t, mon.IsReady("GOOG"), "GOOG should be ready")

	snap, ok := mon.GetLastSnapshot("GOOG")
	assert.True(t, ok, "GOOG should have indicator snapshot after 1m warmup")
	assert.Greater(t, snap.Volume, 0.0, "volume should be populated")
}
