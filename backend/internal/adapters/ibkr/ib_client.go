package ibkr

import "github.com/scmhub/ibsync"

// ibClient is a subset of *ibsync.IB methods used by this adapter.
// Defined as an interface to enable unit testing with mock implementations.
type ibClient interface {
	IsConnected() bool
	Disconnect() error
	PlaceOrder(contract *ibsync.Contract, order *ibsync.Order) *ibsync.Trade
	CancelOrder(order *ibsync.Order, orderCancel ibsync.OrderCancel) *ibsync.Trade
	ReqGlobalCancel()
	OpenTrades() []*ibsync.Trade
	Trades() []*ibsync.Trade
	Positions(account ...string) []ibsync.Position
	ReqAccountSummary(groupName string, tags string) (ibsync.AccountSummary, error)
	Snapshot(contract *ibsync.Contract, regulatorySnapshot ...bool) (*ibsync.Ticker, error)
	ReqRealTimeBars(contract *ibsync.Contract, barSize int, whatToShow string, useRTH bool, realTimeBarsOptions ...ibsync.TagValue) (chan ibsync.RealTimeBar, ibsync.CancelFunc)
	ReqHistoricalData(contract *ibsync.Contract, endDateTime string, duration string, barSize string, whatToShow string, useRTH bool, formatDate int, chartOptions ...ibsync.TagValue) (chan ibsync.Bar, ibsync.CancelFunc)
}

// Compile-time assertion: *ibsync.IB satisfies ibClient.
var _ ibClient = (*ibsync.IB)(nil)
