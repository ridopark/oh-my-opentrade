package ibkr

import (
	"context"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
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

func TestModifyOrder_ReusesOrderID(t *testing.T) {
	open := makeTrade(4118, ibsync.Submitted, 0)
	open.Order.LmtPrice = 1.595
	open.Contract = &ibsync.Contract{Symbol: "NFLX", SecType: "OPT"}
	mock := &mockIB{connected: true, openTrades: []*ibsync.Trade{open}}
	a := NewAdapterWithClient(mock, zerolog.Nop())

	require.NoError(t, a.ModifyOrder(context.Background(), "4118", 1.560, 0))

	require.Len(t, mock.placedOrders, 1, "modify must forward exactly one PlaceOrder call")
	assert.Equal(t, int64(4118), mock.placedOrders[0].OrderID,
		"modify must reuse the original OrderID")
	assert.InDelta(t, 1.560, mock.placedOrders[0].LmtPrice, 1e-9,
		"modify must apply the new LmtPrice")
}

func TestModifyOrder_DoneTrade_ReturnsUnsupported(t *testing.T) {
	done := makeTrade(4118, ibsync.Filled, 10)
	done.Contract = &ibsync.Contract{Symbol: "NFLX", SecType: "OPT"}
	mock := &mockIB{connected: true, openTrades: []*ibsync.Trade{done}}
	a := NewAdapterWithClient(mock, zerolog.Nop())

	err := a.ModifyOrder(context.Background(), "4118", 1.560, 0)
	require.ErrorIs(t, err, ports.ErrUnsupportedModify)
	assert.Empty(t, mock.placedOrders, "no PlaceOrder must fire on a done trade")
}

func TestModifyOrder_UnknownOrder_ReturnsHardError(t *testing.T) {
	mock := &mockIB{connected: true, openTrades: nil, trades: nil}
	a := NewAdapterWithClient(mock, zerolog.Nop())

	err := a.ModifyOrder(context.Background(), "4118", 1.560, 0)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ports.ErrUnsupportedModify,
		"truly unknown orderIDs MUST be hard errors so callers do not fall through to cancel+place")
}

func TestModifyOrder_NewQty_MutatesTotalQuantity(t *testing.T) {
	open := makeTrade(4118, ibsync.Submitted, 0)
	open.Order.LmtPrice = 1.595
	open.Order.TotalQuantity = ibsync.StringToDecimal("10")
	open.Contract = &ibsync.Contract{Symbol: "NFLX", SecType: "OPT"}
	mock := &mockIB{connected: true, openTrades: []*ibsync.Trade{open}}
	a := NewAdapterWithClient(mock, zerolog.Nop())

	require.NoError(t, a.ModifyOrder(context.Background(), "4118", 1.560, 7))

	require.Len(t, mock.placedOrders, 1)
	assert.InDelta(t, 7.0, mock.placedOrders[0].TotalQuantity.Float(), 1e-9,
		"newQty>0 must overwrite TotalQuantity")
	assert.InDelta(t, 1.560, mock.placedOrders[0].LmtPrice, 1e-9)
}

// Filled-and-gone-from-OpenTrades is the load-bearing soft-fail path: the
// order is no longer in OpenTrades, but GetOrderDetails finds it in Trades()
// with a terminal status. Must return ErrUnsupportedModify so the caller's
// cancel+place fallback can reconcile via cancelAndAwaitTerminal.
func TestModifyOrder_FilledAndGone_ReturnsUnsupported(t *testing.T) {
	filled := makeTrade(4118, ibsync.Filled, 10)
	filled.Order.TotalQuantity = ibsync.StringToDecimal("10")
	filled.Contract = &ibsync.Contract{Symbol: "NFLX", SecType: "OPT"}
	mock := &mockIB{
		connected:  true,
		openTrades: nil,                         // gone from open
		trades:     []*ibsync.Trade{filled},     // still visible historically
	}
	a := NewAdapterWithClient(mock, zerolog.Nop())

	err := a.ModifyOrder(context.Background(), "4118", 1.560, 0)
	require.ErrorIs(t, err, ports.ErrUnsupportedModify,
		"terminal-but-known order must yield soft fail so callers cancel+place safely")
	assert.Empty(t, mock.placedOrders, "no PlaceOrder may fire on a terminal order")
}

func TestGetPositions_FiltersAccountID(t *testing.T) {
	mock := &mockIB{connected: true}
	a := NewAdapterWithClientAndCfg(mock, config.IBKRConfig{AccountID: "DU111111"}, zerolog.Nop())
	// Seed livePos directly (GetPositions reads from livePos, not ib.Positions()).
	a.livePos[1] = ibsync.Position{Account: "DU111111", Contract: &ibsync.Contract{ConID: 1, Symbol: "AAPL"}, Position: ibsync.StringToDecimal("10")}
	a.livePos[2] = ibsync.Position{Account: "DU999999", Contract: &ibsync.Contract{ConID: 2, Symbol: "MSFT"}, Position: ibsync.StringToDecimal("5")}
	trades, err := a.GetPositions(context.Background(), "tenant", domain.EnvModePaper)
	require.NoError(t, err)
	require.Len(t, trades, 1)
	assert.Equal(t, domain.Symbol("AAPL"), trades[0].Symbol)
}

func TestGetPositions_EmptyAccountID_ReturnsAll(t *testing.T) {
	mock := &mockIB{connected: true}
	a := NewAdapterWithClientAndCfg(mock, config.IBKRConfig{AccountID: ""}, zerolog.Nop())
	a.livePos[1] = ibsync.Position{Account: "DU111111", Contract: &ibsync.Contract{ConID: 1, Symbol: "AAPL"}, Position: ibsync.StringToDecimal("10")}
	a.livePos[2] = ibsync.Position{Account: "DU999999", Contract: &ibsync.Contract{ConID: 2, Symbol: "MSFT"}, Position: ibsync.StringToDecimal("5")}
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

// Regression: IBKR's TWS API reports option AvgCost as per-contract
// (premium × multiplier). Before the fix broker.go returned Price = AvgCost
// for options, so a $6.10 premium position showed up as Price = $610 per
// share; the ledger writer then bootstrapped avgEntry=610 and any later SELL
// computed a catastrophically wrong fill P&L. The fix divides by the
// contract multiplier so Price is per-share for the rest of the system.
func TestOptionPerShareFromAvgCost(t *testing.T) {
	cases := []struct {
		name       string
		avgCost    float64
		multiplier string
		want       float64
	}{
		{"standard equity option, explicit 100", 610.0, "100", 6.10},
		{"standard equity option, empty multiplier falls back to 100", 610.0, "", 6.10},
		{"standard equity option, unparseable falls back to 100", 610.0, "NaN", 6.10},
		{"zero multiplier string falls back to 100 (no divide-by-zero)", 610.0, "0", 6.10},
		{"mini-index option with 50x multiplier honored", 305.0, "50", 6.10},
		{"zero avg cost stays zero", 0.0, "100", 0.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := optionPerShareFromAvgCost(tc.avgCost, tc.multiplier)
			assert.InDelta(t, tc.want, got, 1e-9)
		})
	}
}

// Regression for the full GetPositions path: constructing a trade from a
// livePos entry for an option must return Price as per-share, not the
// raw per-contract AvgCost that IBKR hands over.
func TestGetPositions_OptionPriceIsPerShare(t *testing.T) {
	mock := &mockIB{connected: true}
	a := NewAdapterWithClientAndCfg(mock, config.IBKRConfig{AccountID: "DU111111"}, zerolog.Nop())
	// RBLX 62-put, 12 contracts, per-share premium 6.10 → AvgCost=610 per contract.
	a.livePos[1] = ibsync.Position{
		Account: "DU111111",
		Contract: &ibsync.Contract{
			ConID: 1, Symbol: "RBLX",
			SecType: "OPT", Right: "P", Strike: 62.0,
			LastTradeDateOrContractMonth: "20260501",
			Multiplier:                   "100",
		},
		Position: ibsync.StringToDecimal("12"),
		AvgCost:  610.42,
	}
	trades, err := a.GetPositions(context.Background(), "tenant", domain.EnvModePaper)
	require.NoError(t, err)
	require.Len(t, trades, 1)
	tr := trades[0]
	assert.Equal(t, domain.InstrumentTypeOption, tr.InstrumentType)
	assert.InDelta(t, 6.1042, tr.Price, 1e-9,
		"option Price must be per-share; AvgCost=610.42 / multiplier=100 = 6.1042")
	// Sanity: contract metadata still populates as before.
	assert.Equal(t, "RBLX", tr.Underlying)
	assert.Equal(t, 62.0, tr.Strike)
	assert.Equal(t, "P", tr.OptionRight)
}
