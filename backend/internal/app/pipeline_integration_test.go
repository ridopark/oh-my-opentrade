//go:build integration

package pipeline_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/debate"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockAIAdvisor struct {
	Decision *domain.AdvisoryDecision
	Err      error

	RequestDebateCalls int
}

func (m *mockAIAdvisor) RequestDebate(ctx context.Context, symbol domain.Symbol, regime domain.MarketRegime, indicators domain.IndicatorSnapshot, opts ...ports.DebateOption) (*domain.AdvisoryDecision, error) {
	m.RequestDebateCalls++
	if m.Err != nil {
		return nil, m.Err
	}
	if m.Decision == nil {
		return &domain.AdvisoryDecision{Direction: domain.DirectionLong, Confidence: 1.0, Rationale: "default"}, nil
	}
	return m.Decision, nil
}

type mockBroker struct {
	SubmitOrderCalls int
}

func (m *mockBroker) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	m.SubmitOrderCalls++
	return "order-123", nil
}

func (m *mockBroker) CancelOrder(ctx context.Context, orderID string) error { return nil }

func (m *mockBroker) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	return "new", nil
}

func (m *mockBroker) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	return []domain.Trade{}, nil
}
func (m *mockBroker) CancelOpenOrders(_ context.Context, _ domain.Symbol, _ string) (int, error) {
	return 0, nil
}
func (m *mockBroker) GetPosition(_ context.Context, _ domain.Symbol) (float64, error) {
	return 0, nil
}
func (m *mockBroker) ClosePosition(_ context.Context, _ domain.Symbol) (string, error) {
	return "", nil
}
func (m *mockBroker) GetOrderDetails(_ context.Context, _ string) (ports.OrderDetails, error) {
	return ports.OrderDetails{}, nil
}

type mockRepository struct{}

func (m *mockRepository) SaveMarketBar(ctx context.Context, bar domain.MarketBar) error { return nil }

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

func (m *mockRepository) SaveOrder(ctx context.Context, order domain.BrokerOrder) error { return nil }

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
func (m *mockRepository) GetMaxBarHighSince(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _ time.Time) (float64, error) {
	return 0, nil
}
func (m *mockRepository) GetLatestThesisForSymbol(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol) (json.RawMessage, error) {
	return nil, nil
}

type mockQuoteProvider struct{}

func (m *mockQuoteProvider) GetQuote(ctx context.Context, symbol domain.Symbol) (float64, float64, error) {
	return 49950.0, 50050.0, nil
}

func TestIntegration_FullPipeline_SetupToOrder(t *testing.T) {
	ctx := context.Background()
	log := zerolog.Nop()

	bus := memory.NewBus()
	mockAI := &mockAIAdvisor{Decision: &domain.AdvisoryDecision{Direction: domain.DirectionLong, Confidence: 0.9, Rationale: "ok"}}
	mockB := &mockBroker{}
	mockRepo := &mockRepository{}

	debateSvc := debate.NewService(bus, mockAI, nil, 0.5, log)
	riskEngine := execution.NewRiskEngine(0.02)
	slippageGuard := execution.NewSlippageGuard(&mockQuoteProvider{})
	killSwitch := execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, time.Now)
	posGate := execution.NewPositionGate(mockB, log)
	executionSvc := execution.NewService(bus, mockB, mockRepo, riskEngine, slippageGuard, killSwitch, nil, 100000.0, log, execution.WithPositionGate(posGate))

	require.NoError(t, debateSvc.Start(ctx))
	require.NoError(t, executionSvc.Start(ctx))

	submittedCh := make(chan domain.Event, 1)
	require.NoError(t, bus.Subscribe(ctx, domain.EventOrderSubmitted, func(ctx context.Context, event domain.Event) error {
		submittedCh <- event
		return nil
	}))

	sym, err := domain.NewSymbol("BTCUSD")
	require.NoError(t, err)
	tf, err := domain.NewTimeframe("1m")
	require.NoError(t, err)

	snapshot, err := domain.NewIndicatorSnapshot(time.Now().UTC(), sym, tf, 30, 10, 20, 49900, 50000, 50010, 1000, 900)
	require.NoError(t, err)

	rt, err := domain.NewRegimeType(string(domain.RegimeTrend))
	require.NoError(t, err)
	regime, err := domain.NewMarketRegime(sym, tf, rt, time.Now().UTC().Add(-10*time.Minute), 0.7)
	require.NoError(t, err)

	setup := monitor.SetupCondition{
		Symbol:    sym,
		Timeframe: tf,
		Direction: domain.DirectionLong,
		Trigger:   "integration-test",
		Snapshot:  snapshot,
		Regime:    regime,
		BarClose:  50000.0,
	}

	setupEvt, err := domain.NewEvent(domain.EventSetupDetected, "tenant-1", domain.EnvModePaper, "setup-1", setup)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *setupEvt))

	select {
	case <-submittedCh:

	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", domain.EventOrderSubmitted)
	}

	assert.Equal(t, 1, mockB.SubmitOrderCalls)
}

func TestIntegration_LowConfidence_NoOrder(t *testing.T) {
	ctx := context.Background()
	log := zerolog.Nop()

	bus := memory.NewBus()
	mockAI := &mockAIAdvisor{Decision: &domain.AdvisoryDecision{Direction: domain.DirectionLong, Confidence: 0.3, Rationale: "low"}}
	mockB := &mockBroker{}
	mockRepo := &mockRepository{}

	debateSvc := debate.NewService(bus, mockAI, nil, 0.5, log)
	riskEngine := execution.NewRiskEngine(0.02)
	slippageGuard := execution.NewSlippageGuard(&mockQuoteProvider{})
	killSwitch := execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, time.Now)
	posGate2 := execution.NewPositionGate(mockB, log)
	executionSvc := execution.NewService(bus, mockB, mockRepo, riskEngine, slippageGuard, killSwitch, nil, 100000.0, log, execution.WithPositionGate(posGate2))

	require.NoError(t, debateSvc.Start(ctx))
	require.NoError(t, executionSvc.Start(ctx))

	debateCompletedCh := make(chan domain.Event, 1)
	intentCreatedCh := make(chan domain.Event, 1)

	require.NoError(t, bus.Subscribe(ctx, domain.EventDebateCompleted, func(ctx context.Context, event domain.Event) error {
		debateCompletedCh <- event
		return nil
	}))
	require.NoError(t, bus.Subscribe(ctx, domain.EventOrderIntentCreated, func(ctx context.Context, event domain.Event) error {
		intentCreatedCh <- event
		return nil
	}))

	sym, err := domain.NewSymbol("BTCUSD")
	require.NoError(t, err)
	tf, err := domain.NewTimeframe("1m")
	require.NoError(t, err)

	snapshot, err := domain.NewIndicatorSnapshot(time.Now().UTC(), sym, tf, 30, 10, 20, 49900, 50000, 50010, 1000, 900)
	require.NoError(t, err)

	rt, err := domain.NewRegimeType(string(domain.RegimeTrend))
	require.NoError(t, err)
	regime, err := domain.NewMarketRegime(sym, tf, rt, time.Now().UTC().Add(-10*time.Minute), 0.7)
	require.NoError(t, err)

	setup := monitor.SetupCondition{
		Symbol:    sym,
		Timeframe: tf,
		Direction: domain.DirectionLong,
		Trigger:   "integration-test",
		Snapshot:  snapshot,
		Regime:    regime,
		BarClose:  50000.0,
	}

	setupEvt, err := domain.NewEvent(domain.EventSetupDetected, "tenant-1", domain.EnvModePaper, "setup-low-confidence-1", setup)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *setupEvt))

	select {
	case <-debateCompletedCh:

	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", domain.EventDebateCompleted)
	}

	select {
	case <-intentCreatedCh:
		t.Fatalf("unexpected %s event", domain.EventOrderIntentCreated)
	case <-time.After(2 * time.Second):

	}

	assert.Equal(t, 0, mockB.SubmitOrderCalls)
}
