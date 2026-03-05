package execution_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// mockQuoteProvider is used for testing slippage guard
type mockQuoteProvider struct {
	Bid float64
	Ask float64
	Err error
}

func (m *mockQuoteProvider) GetQuote(ctx context.Context, symbol domain.Symbol) (float64, float64, error) {
	return m.Bid, m.Ask, m.Err
}

// mockBroker is used for testing order submission
type mockBroker struct {
	SubmitOrderFunc    func(ctx context.Context, intent domain.OrderIntent) (string, error)
	CancelOrderFunc    func(ctx context.Context, orderID string) error
	GetOrderStatusFunc func(ctx context.Context, orderID string) (string, error)
	GetPositionsFunc   func(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error)

	SubmitOrderCalls int
}

func (m *mockBroker) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	m.SubmitOrderCalls++
	if m.SubmitOrderFunc != nil {
		return m.SubmitOrderFunc(ctx, intent)
	}
	return "order-123", nil
}

func (m *mockBroker) CancelOrder(ctx context.Context, orderID string) error {
	if m.CancelOrderFunc != nil {
		return m.CancelOrderFunc(ctx, orderID)
	}
	return nil
}

func (m *mockBroker) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	if m.GetOrderStatusFunc != nil {
		return m.GetOrderStatusFunc(ctx, orderID)
	}
	return "FILLED", nil
}

func (m *mockBroker) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	if m.GetPositionsFunc != nil {
		return m.GetPositionsFunc(ctx, tenantID, envMode)
	}
	return []domain.Trade{}, nil
}

// mockRepository is used for testing
type mockRepository struct {
	SaveTradeFunc            func(ctx context.Context, trade domain.Trade) error
	SaveMarketBarFunc        func(ctx context.Context, bar domain.MarketBar) error
	GetMarketBarsFunc        func(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
	GetTradesFunc            func(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.Trade, error)
	SaveStrategyDNAFunc      func(ctx context.Context, dna domain.StrategyDNA) error
	GetLatestStrategyDNAFunc func(ctx context.Context, tenantID string, envMode domain.EnvMode) (*domain.StrategyDNA, error)
}

func (m *mockRepository) SaveMarketBar(ctx context.Context, bar domain.MarketBar) error {
	if m.SaveMarketBarFunc != nil {
		return m.SaveMarketBarFunc(ctx, bar)
	}
	return nil
}

func (m *mockRepository) GetMarketBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	if m.GetMarketBarsFunc != nil {
		return m.GetMarketBarsFunc(ctx, symbol, timeframe, from, to)
	}
	return nil, nil
}

func (m *mockRepository) SaveTrade(ctx context.Context, trade domain.Trade) error {
	if m.SaveTradeFunc != nil {
		return m.SaveTradeFunc(ctx, trade)
	}
	return nil
}

func (m *mockRepository) GetTrades(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.Trade, error) {
	if m.GetTradesFunc != nil {
		return m.GetTradesFunc(ctx, tenantID, envMode, from, to)
	}
	return nil, nil
}

func (m *mockRepository) SaveStrategyDNA(ctx context.Context, dna domain.StrategyDNA) error {
	if m.SaveStrategyDNAFunc != nil {
		return m.SaveStrategyDNAFunc(ctx, dna)
	}
	return nil
}

func (m *mockRepository) GetLatestStrategyDNA(ctx context.Context, tenantID string, envMode domain.EnvMode) (*domain.StrategyDNA, error) {
	if m.GetLatestStrategyDNAFunc != nil {
		return m.GetLatestStrategyDNAFunc(ctx, tenantID, envMode)
	}
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

// createTestOrderIntent creates a valid domain.OrderIntent for testing.
func createTestOrderIntent(t *testing.T) domain.OrderIntent {
	t.Helper()
	intent, err := domain.NewOrderIntent(
		uuid.New(),
		"tenant-1",
		domain.EnvModePaper,
		"BTCUSD",
		domain.DirectionLong,
		50000.0,
		49000.0,
		10, // MaxSlippageBPS
		1.0,
		"strategy-1",
		"rationale",
		0.8,
		"idem-key",
	)
	if err != nil {
		t.Fatalf("failed to create test order intent: %v", err)
	}
	return intent
}

// createOrderIntentEvent creates a valid EventOrderIntentCreated event for testing.
func createOrderIntentEvent(t *testing.T, dir domain.Direction) domain.Event {
	t.Helper()
	intentID := uuid.New()
	intent, err := domain.NewOrderIntent(
		intentID,
		"tenant-1",
		domain.EnvModePaper,
		"BTCUSD",
		dir,
		50000.0,
		49000.0,
		10,  // MaxSlippageBPS
		1.0, // Quantity
		"strategy-1",
		"RSI_Oversold triggered",
		0.8,
		intentID.String(),
	)
	if err != nil {
		t.Fatalf("failed to create test order intent: %v", err)
	}

	event, err := domain.NewEvent(
		domain.EventOrderIntentCreated,
		"tenant-1",
		domain.EnvModePaper,
		intentID.String(),
		intent,
	)
	if err != nil {
		t.Fatalf("failed to create intent event: %v", err)
	}
	return *event
}

// createExitOrderIntentEvent creates a valid exit EventOrderIntentCreated event for testing.
// The intent will have IsExit=true, simulating a sell-to-close order.
func createExitOrderIntentEvent(t *testing.T, dir domain.Direction) domain.Event {
	t.Helper()
	intentID := uuid.New()
	intent, err := domain.NewOrderIntent(
		intentID,
		"tenant-1",
		domain.EnvModePaper,
		"BTCUSD",
		dir,
		50000.0,
		49000.0,
		10,  // MaxSlippageBPS
		1.0, // Quantity
		"strategy-1",
		"exit signal",
		0.8,
		intentID.String(),
	)
	if err != nil {
		t.Fatalf("failed to create test order intent: %v", err)
	}
	intent.IsExit = true

	event, err := domain.NewEvent(
		domain.EventOrderIntentCreated,
		"tenant-1",
		domain.EnvModePaper,
		intentID.String(),
		intent,
	)
	if err != nil {
		t.Fatalf("failed to create intent event: %v", err)
	}
	return *event
}
