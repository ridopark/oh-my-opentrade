package ports_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// 1. MarketDataPort
type mockMarketData struct{}

var _ ports.MarketDataPort = (*mockMarketData)(nil)

func (m *mockMarketData) StreamBars(ctx context.Context, symbols []domain.Symbol, timeframe domain.Timeframe, handler ports.BarHandler) error {
	return nil
}

func (m *mockMarketData) GetHistoricalBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	return []domain.MarketBar{{}}, nil
}

func (m *mockMarketData) Close() error {
	return nil
}

// 2. BrokerPort
type mockBroker struct{}

var _ ports.BrokerPort = (*mockBroker)(nil)

func (m *mockBroker) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	return "order-123", nil
}

func (m *mockBroker) CancelOrder(ctx context.Context, orderID string) error {
	return nil
}

func (m *mockBroker) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	return "FILLED", nil
}

func (m *mockBroker) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	return []domain.Trade{{}}, nil
}

// 3. AIAdvisorPort
type mockAIAdvisor struct{}

var _ ports.AIAdvisorPort = (*mockAIAdvisor)(nil)

func (m *mockAIAdvisor) RequestDebate(ctx context.Context, symbol domain.Symbol, regime domain.MarketRegime, indicators domain.IndicatorSnapshot) (*domain.AdvisoryDecision, error) {
	return &domain.AdvisoryDecision{
		Direction:  domain.Direction("LONG"),
		Confidence: 0.85,
	}, nil
}

// 4. EventBusPort
type mockEventBus struct{}

var _ ports.EventBusPort = (*mockEventBus)(nil)

func (m *mockEventBus) Publish(ctx context.Context, event domain.Event) error {
	return nil
}

func (m *mockEventBus) Subscribe(ctx context.Context, eventType domain.EventType, handler ports.EventHandler) error {
	return nil
}

func (m *mockEventBus) Unsubscribe(ctx context.Context, eventType domain.EventType, handler ports.EventHandler) error {
	return nil
}

// 5. RepositoryPort
type mockRepository struct{}

var _ ports.RepositoryPort = (*mockRepository)(nil)

func (m *mockRepository) SaveMarketBar(ctx context.Context, bar domain.MarketBar) error {
	return nil
}

func (m *mockRepository) GetMarketBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	return []domain.MarketBar{{}}, nil
}

func (m *mockRepository) SaveTrade(ctx context.Context, trade domain.Trade) error {
	return nil
}

func (m *mockRepository) GetTrades(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.Trade, error) {
	return []domain.Trade{{}}, nil
}

func (m *mockRepository) SaveStrategyDNA(ctx context.Context, dna domain.StrategyDNA) error {
	return nil
}

func (m *mockRepository) GetLatestStrategyDNA(ctx context.Context, tenantID string, envMode domain.EnvMode) (*domain.StrategyDNA, error) {
	return &domain.StrategyDNA{}, nil
}

// 6. NotifierPort
type mockNotifier struct{}

var _ ports.NotifierPort = (*mockNotifier)(nil)

func (m *mockNotifier) Notify(ctx context.Context, tenantID string, message string) error {
	return nil
}

//
// Tests
//

func TestMarketDataPort(t *testing.T) {
	var port ports.MarketDataPort = &mockMarketData{}
	ctx := context.Background()

	err := port.StreamBars(ctx, []domain.Symbol{"BTCUSDT"}, "1m", func(ctx context.Context, bar domain.MarketBar) error {
		return nil
	})
	require.NoError(t, err)

	bars, err := port.GetHistoricalBars(ctx, "BTCUSDT", "1m", time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)
	assert.Len(t, bars, 1)

	err = port.Close()
	require.NoError(t, err)
}

func TestBrokerPort(t *testing.T) {
	var port ports.BrokerPort = &mockBroker{}
	ctx := context.Background()

	orderID, err := port.SubmitOrder(ctx, domain.OrderIntent{})
	require.NoError(t, err)
	assert.Equal(t, "order-123", orderID)

	err = port.CancelOrder(ctx, "order-123")
	require.NoError(t, err)

	status, err := port.GetOrderStatus(ctx, "order-123")
	require.NoError(t, err)
	assert.Equal(t, "FILLED", status)

	positions, err := port.GetPositions(ctx, "tenant-1", "paper")
	require.NoError(t, err)
	assert.Len(t, positions, 1)
}

func TestAIAdvisorPort(t *testing.T) {
	var port ports.AIAdvisorPort = &mockAIAdvisor{}
	ctx := context.Background()

	decision, err := port.RequestDebate(ctx, "BTCUSDT", domain.MarketRegime{}, domain.IndicatorSnapshot{})
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, 0.85, decision.Confidence)
}

func TestEventBusPort(t *testing.T) {
	var port ports.EventBusPort = &mockEventBus{}
	ctx := context.Background()

	handler := func(ctx context.Context, event domain.Event) error { return nil }

	err := port.Publish(ctx, domain.Event{})
	require.NoError(t, err)

	err = port.Subscribe(ctx, "trade.created", handler)
	require.NoError(t, err)

	err = port.Unsubscribe(ctx, "trade.created", handler)
	require.NoError(t, err)
}

func TestRepositoryPort(t *testing.T) {
	var port ports.RepositoryPort = &mockRepository{}
	ctx := context.Background()

	err := port.SaveMarketBar(ctx, domain.MarketBar{})
	require.NoError(t, err)

	bars, err := port.GetMarketBars(ctx, "BTCUSDT", "1m", time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)
	assert.Len(t, bars, 1)

	err = port.SaveTrade(ctx, domain.Trade{})
	require.NoError(t, err)

	trades, err := port.GetTrades(ctx, "tenant-1", "paper", time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)
	assert.Len(t, trades, 1)

	err = port.SaveStrategyDNA(ctx, domain.StrategyDNA{})
	require.NoError(t, err)

	dna, err := port.GetLatestStrategyDNA(ctx, "tenant-1", "paper")
	require.NoError(t, err)
	require.NotNil(t, dna)
}

func TestNotifierPort(t *testing.T) {
	var port ports.NotifierPort = &mockNotifier{}
	ctx := context.Background()

	err := port.Notify(ctx, "tenant-1", "Hello World")
	require.NoError(t, err)
}
