package monitor_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/stretchr/testify/require"
)

// rthTestTime is the base RTH-ET timestamp (10:00 ET on 2026-04-29, a
// Wednesday — non-holiday) used by createBar / createBarDetailed so 1m
// equity bars in tests pass IndicatorCalculator's RTH gate. Using
// time.Now() is non-deterministic and can fall outside RTH depending on
// when CI runs.
//
// rthTestSeq advances monotonically across calls so successive bars from
// createBar / createBarDetailed have strictly increasing bar.Time. Required
// because IndicatorCalculator.Update dedupes on bar.Time — feeding bars
// with the same timestamp twice would short-circuit on the second call.
// 1-second increments give 6 hours of headroom (RTH spans 6.5h) before
// the global counter would push bars past 16:00 ET, which is plenty for
// every existing test in this package.
var (
	rthTestTime = time.Date(2026, 4, 29, 14, 0, 0, 0, time.UTC)
	rthTestSeq  uint64
)

func nextRthBarTime() time.Time {
	n := atomic.AddUint64(&rthTestSeq, 1)
	return rthTestTime.Add(time.Duration(n-1) * time.Second)
}

func createBar(t *testing.T, symbol domain.Symbol, closePrice, volume float64) domain.MarketBar {
	bar, err := domain.NewMarketBar(
		nextRthBarTime(),
		symbol,
		"1m",
		closePrice, closePrice, closePrice, closePrice,
		volume,
	)
	require.NoError(t, err)
	return bar
}

func createBarDetailed(t *testing.T, symbol domain.Symbol, o, h, l, c, v float64) domain.MarketBar {
	bar, err := domain.NewMarketBar(
		nextRthBarTime(),
		symbol,
		"1m",
		o, h, l, c,
		v,
	)
	require.NoError(t, err)
	return bar
}

func createBarAtTime(t *testing.T, symbol domain.Symbol, barTime time.Time, o, h, l, c, v float64) domain.MarketBar {
	bar, err := domain.NewMarketBar(barTime, symbol, "1m", o, h, l, c, v)
	require.NoError(t, err)
	return bar
}

func createTestEvent(t *testing.T, payload any) domain.Event {
	ev, err := domain.NewEvent(
		domain.EventMarketBarSanitized,
		"tenant123",
		domain.EnvModePaper,
		"idempotency123",
		payload,
	)
	require.NoError(t, err)
	return *ev
}

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
func (m *mockRepository) SaveMarketBars(_ context.Context, _ []domain.MarketBar) (int, error) {
	return 0, nil
}
func (m *mockRepository) GetMarketBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}

func (m *mockRepository) GetMarketBarsMulti(_ context.Context, _ []domain.Symbol, _ domain.Timeframe, _, _ time.Time) (map[string][]domain.MarketBar, error) {
	return map[string][]domain.MarketBar{}, nil
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

func (m *mockRepository) RecordFillPerExec(context.Context, string, time.Time, float64, float64, domain.Trade) error {
	return nil
}

func (m *mockRepository) RecordFillAggregate(context.Context, string, time.Time, float64, float64, domain.Trade) error {
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
func (m *mockRepository) GetNonTerminalOrders(_ context.Context, _ string, _ domain.EnvMode) ([]domain.BrokerOrder, error) {
	return nil, nil
}
func (m *mockRepository) GetOrderByBrokerOrderID(_ context.Context, _ string) (*domain.BrokerOrder, error) {
	return nil, nil
}
func (m *mockRepository) GetRecordedFillQty(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol, _ string, _ time.Time) (float64, error) {
	return 0, nil
}
func (m *mockRepository) UpdateOrderStatus(_ context.Context, _ string, _ string) error {
	return nil
}
func (m *mockRepository) GetRecordedExecutionIDs(_ context.Context, _ string, _ domain.EnvMode, _ time.Time) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

func (m *mockRepository) GetReconciledOrderIDs(_ context.Context, _ string, _ domain.EnvMode, _ time.Time) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (m *mockRepository) GetNetPositions(_ context.Context, _ string, _ domain.EnvMode) (map[domain.Symbol]float64, error) {
	return nil, nil
}
func (m *mockRepository) GetAvgEntryPrice(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol) (float64, error) {
	return 0, nil
}
func (m *mockRepository) HasCanceledExitOrder(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol) (bool, error) {
	return false, nil
}

func (m *mockRepository) HasTradeForBrokerOrderID(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (m *mockRepository) UpdateBarIndicators(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _ time.Time, _, _, _, _ float64, _ map[string]float64) error {
	return nil
}

// mockDNAGate implements monitor.DNAGateChecker for tests.
type mockDNAGate struct {
	approved bool
	err      error
	calls    int
}

func (m *mockDNAGate) IsDNAApproved(_ context.Context, _ string) (bool, error) {
	m.calls++
	return m.approved, m.err
}
