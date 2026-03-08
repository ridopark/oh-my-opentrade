package ingestion_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockRepository struct {
	savedBars []domain.MarketBar
	saveErr   error
}

func (m *mockRepository) SaveMarketBar(ctx context.Context, bar domain.MarketBar) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.savedBars = append(m.savedBars, bar)
	return nil
}

func (m *mockRepository) GetMarketBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}
func (m *mockRepository) SaveTrade(ctx context.Context, trade domain.Trade) error { return nil }
func (m *mockRepository) GetTrades(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.Trade, error) {
	return nil, nil
}
func (m *mockRepository) SaveStrategyDNA(ctx context.Context, dna domain.StrategyDNA) error {
	return nil
}
func (m *mockRepository) GetLatestStrategyDNA(ctx context.Context, tenantID string, envMode domain.EnvMode) (*domain.StrategyDNA, error) {
	return nil, nil
}

func (m *mockRepository) SaveOrder(ctx context.Context, order domain.BrokerOrder) error {
	return nil
}

func (m *mockRepository) UpdateOrderFill(ctx context.Context, brokerOrderID string, filledAt time.Time, filledPrice, filledQty float64) error {
	return nil
}

func (m *mockRepository) ListTrades(_ context.Context, _ ports.TradeQuery) (ports.TradePage, error) {
	return ports.TradePage{}, nil
}
func (m *mockRepository) ListOrders(_ context.Context, _ ports.OrderQuery) (ports.OrderPage, error) {
	return ports.OrderPage{}, nil
}
func (m *mockRepository) SaveThoughtLog(_ context.Context, _ domain.ThoughtLog) error { return nil }
func (m *mockRepository) GetThoughtLogsByIntentID(_ context.Context, _ string) ([]domain.ThoughtLog, error) {
	return nil, nil
}
func (m *mockRepository) UpdateTradeThesis(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol, _ json.RawMessage) error {
	return nil
}

func createTestEvent(t *testing.T, payload any) domain.Event {
	ev, err := domain.NewEvent(
		domain.EventMarketBarReceived,
		"tenant123",
		domain.EnvModePaper,
		"idempotency123",
		payload,
	)
	require.NoError(t, err)
	return *ev
}

func TestService_StartSubscribes(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	filter := ingestion.NewZScoreFilter(5, 4.0)
	svc := ingestion.NewService(bus, repo, filter, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	// Now publish an event and see if handler picks it up. We can verify by sending invalid payload and expecting error in Publish.
	err = bus.Publish(context.Background(), createTestEvent(t, "invalid payload"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "payload is not a MarketBar")
}

func TestService_SanitizesCleanBar(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	filter := ingestion.NewZScoreFilter(5, 4.0) // needs 5 for active filter
	svc := ingestion.NewService(bus, repo, filter, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	sym, _ := domain.NewSymbol("BTC/USD")
	bar := createBar(t, sym, 100.0, 10.0)

	// Subscribe to MarketBarSanitized to verify emission
	var emitted domain.Event
	bus.Subscribe(context.Background(), domain.EventMarketBarSanitized, func(ctx context.Context, ev domain.Event) error {
		emitted = ev
		return nil
	})

	err = bus.Publish(context.Background(), createTestEvent(t, bar))
	require.NoError(t, err)

	// Should be saved
	assert.Len(t, repo.savedBars, 1)
	assert.Equal(t, bar.Close, repo.savedBars[0].Close)

	// Should emit EventMarketBarSanitized
	assert.Equal(t, domain.EventMarketBarSanitized, emitted.Type)
	assert.Equal(t, "tenant123", emitted.TenantID)
	assert.Equal(t, domain.EnvModePaper, emitted.EnvMode)
	assert.Equal(t, "idempotency123-sanitized", emitted.IdempotencyKey)

	emittedBar, ok := emitted.Payload.(domain.MarketBar)
	require.True(t, ok)
	assert.Equal(t, bar.Close, emittedBar.Close)
	assert.False(t, emittedBar.Suspect)
}

func TestService_RejectsAnomalousBar(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	filter := ingestion.NewZScoreFilter(5, 4.0)
	svc := ingestion.NewService(bus, repo, filter, zerolog.Nop())
	err := svc.Start(context.Background())
	require.NoError(t, err)

	sym, _ := domain.NewSymbol("BTC/USD")

	// Fill window to enable filter
	for i := 0; i < 5; i++ {
		b := createBar(t, sym, 100.0, 10.0)
		err = bus.Publish(context.Background(), createTestEvent(t, b))
		require.NoError(t, err)
	}

	// We expect 5 saved bars
	assert.Len(t, repo.savedBars, 5)

	// Subscribe to MarketBarRejected
	var emitted domain.Event
	bus.Subscribe(context.Background(), domain.EventMarketBarRejected, func(ctx context.Context, ev domain.Event) error {
		emitted = ev
		return nil
	})

	// Now send anomaly
	anomalousBar := createBar(t, sym, 200.0, 10.0)
	err = bus.Publish(context.Background(), createTestEvent(t, anomalousBar))
	require.NoError(t, err)

	// Should NOT be saved
	assert.Len(t, repo.savedBars, 5, "Anomalous bar should not be persisted")

	// Should emit EventMarketBarRejected
	assert.Equal(t, domain.EventMarketBarRejected, emitted.Type)
	assert.Equal(t, "tenant123", emitted.TenantID)
	assert.Equal(t, domain.EnvModePaper, emitted.EnvMode)
	assert.Equal(t, "idempotency123-rejected", emitted.IdempotencyKey)

	emittedBar, ok := emitted.Payload.(domain.MarketBar)
	require.True(t, ok)
	assert.Equal(t, anomalousBar.Close, emittedBar.Close)
	assert.True(t, emittedBar.Suspect, "Rejected bar must be flagged as suspect")
}

func TestService_InvalidPayload(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	filter := ingestion.NewZScoreFilter(5, 4.0)
	svc := ingestion.NewService(bus, repo, filter, zerolog.Nop())

	err := svc.HandleMarketBar(context.Background(), createTestEvent(t, "not a bar"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "payload is not a MarketBar")
}

func TestService_RepositoryErrorPropagates(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{
		saveErr: errors.New("db error"),
	}
	filter := ingestion.NewZScoreFilter(5, 4.0)
	svc := ingestion.NewService(bus, repo, filter, zerolog.Nop())

	sym, _ := domain.NewSymbol("BTC/USD")
	bar := createBar(t, sym, 100.0, 10.0)

	err := svc.HandleMarketBar(context.Background(), createTestEvent(t, bar))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "db error")
}
