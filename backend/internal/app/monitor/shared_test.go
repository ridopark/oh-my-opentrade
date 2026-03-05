package monitor_test

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/stretchr/testify/require"
)

func createBar(t *testing.T, symbol domain.Symbol, closePrice, volume float64) domain.MarketBar {
	bar, err := domain.NewMarketBar(
		time.Now(),
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
		time.Now(),
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
