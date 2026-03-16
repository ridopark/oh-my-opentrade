package ibkr

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/scmhub/ibsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockAlpacaProvider struct {
	getHistoricalBarsCalled bool
	getSnapshotsCalled      bool
	getOptionChainCalled    bool
	listTradeableCalled     bool
	closeCalled             bool
}

func (m *mockAlpacaProvider) GetHistoricalBars(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _, _ time.Time) ([]domain.MarketBar, error) {
	m.getHistoricalBarsCalled = true
	return nil, nil
}
func (m *mockAlpacaProvider) GetSnapshots(_ context.Context, _ []string, _ time.Time) (map[string]ports.Snapshot, error) {
	m.getSnapshotsCalled = true
	return nil, nil
}
func (m *mockAlpacaProvider) GetOptionChain(_ context.Context, _ domain.Symbol, _ time.Time, _ domain.OptionRight) ([]domain.OptionContractSnapshot, error) {
	m.getOptionChainCalled = true
	return nil, nil
}
func (m *mockAlpacaProvider) GetOptionPrices(_ context.Context, _ []domain.Symbol) (map[domain.Symbol]domain.OptionQuote, error) {
	return nil, nil
}
func (m *mockAlpacaProvider) ListTradeable(_ context.Context, _ domain.AssetClass) ([]ports.Asset, error) {
	m.listTradeableCalled = true
	return nil, nil
}
func (m *mockAlpacaProvider) Close() error { m.closeCalled = true; return nil }

func makeTestComposite(t *testing.T, mock *mockIB) (*CompositeAdapter, *mockAlpacaProvider) {
	t.Helper()
	a := NewAdapterWithClient(mock, zerolog.Nop())
	alpMock := &mockAlpacaProvider{}
	c := NewCompositeAdapter(a, alpMock, zerolog.Nop())
	return c, alpMock
}

func TestComposite_SubmitOrder_RoutesToIBKR(t *testing.T) {
	mock := &mockIB{connected: true}
	c, alpMock := makeTestComposite(t, mock)

	orderID, err := c.SubmitOrder(context.Background(), domain.OrderIntent{
		Symbol: "AAPL", Direction: domain.DirectionLong,
		Quantity: 1, OrderType: "market",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, orderID)
	assert.Len(t, mock.placedOrders, 1)
	_ = alpMock
}

func TestComposite_GetHistoricalBars_RoutesToAlpaca(t *testing.T) {
	mock := &mockIB{connected: true}
	c, alpMock := makeTestComposite(t, mock)

	_, _ = c.GetHistoricalBars(context.Background(), "AAPL", "1m", time.Now().Add(-time.Hour), time.Now())
	assert.True(t, alpMock.getHistoricalBarsCalled)
	assert.Empty(t, mock.placedOrders)
}

func TestComposite_GetSnapshots_RoutesToAlpaca(t *testing.T) {
	mock := &mockIB{connected: true}
	c, alpMock := makeTestComposite(t, mock)

	_, _ = c.GetSnapshots(context.Background(), []string{"AAPL"}, time.Now())
	assert.True(t, alpMock.getSnapshotsCalled)
}

func TestComposite_GetOptionChain_RoutesToAlpaca(t *testing.T) {
	mock := &mockIB{connected: true}
	c, alpMock := makeTestComposite(t, mock)

	_, _ = c.GetOptionChain(context.Background(), "AAPL", time.Now(), domain.OptionRight("C"))
	assert.True(t, alpMock.getOptionChainCalled)
}

func TestComposite_ListTradeable_RoutesToAlpaca(t *testing.T) {
	mock := &mockIB{connected: true}
	c, alpMock := makeTestComposite(t, mock)

	_, _ = c.ListTradeable(context.Background(), domain.AssetClass("us_equity"))
	assert.True(t, alpMock.listTradeableCalled)
}

func TestComposite_Close_ClosesBoth(t *testing.T) {
	mock := &mockIB{connected: true}
	c, alpMock := makeTestComposite(t, mock)

	err := c.Close()
	assert.NoError(t, err)
	assert.True(t, alpMock.closeCalled)
}

func TestComposite_SubscribeOrderUpdates_RoutesToIBKR(t *testing.T) {
	mock := &mockIB{connected: true}
	c, _ := makeTestComposite(t, mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.SubscribeOrderUpdates(ctx)
	require.NoError(t, err)
	assert.NotNil(t, ch)
}

func TestComposite_StreamBars_RoutesToIBKR(t *testing.T) {
	mock := &mockIB{connected: true}
	c, _ := makeTestComposite(t, mock)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	noop := func(_ context.Context, _ domain.MarketBar) error { return nil }
	_ = c.StreamBars(ctx, []domain.Symbol{"AAPL"}, "1m", noop)
	_ = ibsync.RealTimeBar{}
}
