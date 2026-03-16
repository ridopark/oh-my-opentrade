package ibkr

import (
	"fmt"
	"sync"

	"github.com/scmhub/ibsync"
)

// mockIB implements ibClient for unit testing.
type mockIB struct {
	mu              sync.Mutex
	connected       bool
	trades          []*ibsync.Trade
	openTrades      []*ibsync.Trade
	positions       []ibsync.Position
	accountSummary  ibsync.AccountSummary
	accountErr      error
	snapTicker      *ibsync.Ticker
	snapErr         error
	placedOrders    []*ibsync.Order
	cancelledOrders []int64
	globalCancelled bool
	rtBarChans      map[string]chan ibsync.RealTimeBar
}

func (m *mockIB) IsConnected() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.connected }
func (m *mockIB) Disconnect() error { return nil }
func (m *mockIB) PlaceOrder(_ *ibsync.Contract, order *ibsync.Order) *ibsync.Trade {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.placedOrders = append(m.placedOrders, order)
	t := &ibsync.Trade{Order: order}
	m.trades = append(m.trades, t)
	return t
}
func (m *mockIB) CancelOrder(order *ibsync.Order, _ ibsync.OrderCancel) *ibsync.Trade {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelledOrders = append(m.cancelledOrders, order.OrderID)
	return nil
}
func (m *mockIB) ReqGlobalCancel() { m.mu.Lock(); m.globalCancelled = true; m.mu.Unlock() }
func (m *mockIB) OpenTrades() []*ibsync.Trade {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.openTrades
}
func (m *mockIB) Trades() []*ibsync.Trade {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.trades
}
func (m *mockIB) Positions(_ ...string) []ibsync.Position {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.positions
}
func (m *mockIB) ReqAccountSummary(_ string, _ string) (ibsync.AccountSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.accountSummary, m.accountErr
}
func (m *mockIB) Snapshot(_ *ibsync.Contract, _ ...bool) (*ibsync.Ticker, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapTicker, m.snapErr
}
func (m *mockIB) ReqRealTimeBars(contract *ibsync.Contract, _ int, _ string, _ bool, _ ...ibsync.TagValue) (chan ibsync.RealTimeBar, ibsync.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rtBarChans == nil {
		m.rtBarChans = make(map[string]chan ibsync.RealTimeBar)
	}
	ch := make(chan ibsync.RealTimeBar, 16)
	m.rtBarChans[contract.Symbol] = ch
	return ch, func() { close(ch) }
}
func (m *mockIB) ReqHistoricalData(_ *ibsync.Contract, _, _, _, _ string, _ bool, _ int, _ ...ibsync.TagValue) (chan ibsync.Bar, ibsync.CancelFunc) {
	ch := make(chan ibsync.Bar)
	close(ch)
	return ch, func() {}
}

// makeTrade creates a *ibsync.Trade with given orderID and status for tests.
func makeTrade(orderID int64, status ibsync.Status, filled float64) *ibsync.Trade {
	order := &ibsync.Order{}
	order.OrderID = orderID
	t := &ibsync.Trade{Order: order}
	t.OrderStatus.Status = status
	t.OrderStatus.Filled = ibsync.StringToDecimal(fmt.Sprintf("%.6f", filled))
	return t
}

// Compile-time assertion: mockIB satisfies ibClient.
var _ ibClient = (*mockIB)(nil)
