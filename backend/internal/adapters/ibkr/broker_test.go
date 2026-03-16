package ibkr

import (
	"context"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/scmhub/ibsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubmitOrder_NotConnected(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	conn := &connection{ib: nil, cancel: cancel}
	a := &Adapter{conn: conn, log: zerolog.Nop(), streaming: make(map[domain.Symbol]struct{})}
	_, err := a.SubmitOrder(context.Background(), domain.OrderIntent{
		Symbol: "AAPL", Quantity: 1, Direction: domain.DirectionLong, OrderType: "market",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ibkr: not connected")
}

func TestSubmitOrder_ZeroQuantity_ReturnsError(t *testing.T) {
	mock := &mockIB{connected: true}
	a := NewAdapterWithClient(mock, zerolog.Nop())
	_, err := a.SubmitOrder(context.Background(), domain.OrderIntent{Symbol: "AAPL", Quantity: 0})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quantity must be positive")
}

func TestSubmitOrder_Success_ReturnsOrderID(t *testing.T) {
	mock := &mockIB{connected: true}
	a := NewAdapterWithClient(mock, zerolog.Nop())
	orderID, err := a.SubmitOrder(context.Background(), domain.OrderIntent{
		Symbol: "AAPL", Quantity: 10, Direction: domain.DirectionLong, OrderType: "market",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, orderID)
	assert.Len(t, mock.placedOrders, 1)
}

func TestCancelOrder_NotFound_ReturnsError(t *testing.T) {
	mock := &mockIB{connected: true, openTrades: []*ibsync.Trade{}}
	a := NewAdapterWithClient(mock, zerolog.Nop())
	err := a.CancelOrder(context.Background(), "999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "999")
}

func TestGetPositions_FiltersAccountID(t *testing.T) {
	mock := &mockIB{
		connected: true,
		positions: []ibsync.Position{
			{Account: "DU111111", Contract: &ibsync.Contract{Symbol: "AAPL"}, Position: ibsync.StringToDecimal("10")},
			{Account: "DU999999", Contract: &ibsync.Contract{Symbol: "MSFT"}, Position: ibsync.StringToDecimal("5")},
		},
	}
	a := NewAdapterWithClientAndCfg(mock, config.IBKRConfig{AccountID: "DU111111"}, zerolog.Nop())
	trades, err := a.GetPositions(context.Background(), "tenant", domain.EnvModePaper)
	require.NoError(t, err)
	require.Len(t, trades, 1)
	assert.Equal(t, domain.Symbol("AAPL"), trades[0].Symbol)
}

func TestGetPositions_EmptyAccountID_ReturnsAll(t *testing.T) {
	mock := &mockIB{
		connected: true,
		positions: []ibsync.Position{
			{Account: "DU111111", Contract: &ibsync.Contract{Symbol: "AAPL"}, Position: ibsync.StringToDecimal("10")},
			{Account: "DU999999", Contract: &ibsync.Contract{Symbol: "MSFT"}, Position: ibsync.StringToDecimal("5")},
		},
	}
	a := NewAdapterWithClientAndCfg(mock, config.IBKRConfig{AccountID: ""}, zerolog.Nop())
	trades, err := a.GetPositions(context.Background(), "tenant", domain.EnvModePaper)
	require.NoError(t, err)
	assert.Len(t, trades, 2)
}

func TestGetPosition_ZeroWhenMissing(t *testing.T) {
	mock := &mockIB{connected: true, positions: []ibsync.Position{}}
	a := NewAdapterWithClient(mock, zerolog.Nop())
	qty, err := a.GetPosition(context.Background(), "UNKNOWN")
	require.NoError(t, err)
	assert.Equal(t, float64(0), qty)
}
