package execution

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests live in package `execution` (not `execution_test`) so they can
// exercise the unexported sweepDustPosition helpers directly. A full
// intent-to-fill integration test would need to route through handleIntent,
// which depends on the full guard chain; driving sweepDustPosition as a
// focused unit gets us coverage of the pricing decision + fallback logic
// without that setup cost.

const (
	testOCC    domain.Symbol = "AAPL261219C00190000"
	testEquity domain.Symbol = "AAPL"
)

// dustMockBroker is a standalone broker mock with tunable behavior for
// sweep-specific tests (quote-sequence replay, conditional status).
type dustMockBroker struct {
	mu             sync.Mutex
	positionQty    float64
	submitCalls    []domain.OrderIntent
	submitErr      error
	returnOrderID  string
	cancelCalls    int32
	cancelErr      error
	getDetailsFunc func(orderID string) (ports.OrderDetails, error)
	nextOrderIDSeq int
	submitObserver func(intent domain.OrderIntent)
}

func (b *dustMockBroker) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	b.mu.Lock()
	b.submitCalls = append(b.submitCalls, intent)
	b.mu.Unlock()
	if b.submitObserver != nil {
		b.submitObserver(intent)
	}
	if b.submitErr != nil {
		return "", b.submitErr
	}
	if b.returnOrderID != "" {
		return b.returnOrderID, nil
	}
	b.mu.Lock()
	b.nextOrderIDSeq++
	id := "sweep-" + string(rune('A'+b.nextOrderIDSeq-1))
	b.mu.Unlock()
	return id, nil
}

func (b *dustMockBroker) CancelOrder(ctx context.Context, orderID string) error {
	atomic.AddInt32(&b.cancelCalls, 1)
	return b.cancelErr
}

func (b *dustMockBroker) CancelOpenOrders(context.Context, domain.Symbol, string) (int, error) {
	return 0, nil
}
func (b *dustMockBroker) GetOrderStatus(context.Context, string) (string, error) {
	return "", nil
}
func (b *dustMockBroker) GetPositions(context.Context, string, domain.EnvMode) ([]domain.Trade, error) {
	return nil, nil
}
func (b *dustMockBroker) GetPosition(context.Context, domain.Symbol) (float64, error) {
	return b.positionQty, nil
}
func (b *dustMockBroker) CloseAtMarket(context.Context, domain.Symbol) (string, error) {
	return "", nil
}
func (b *dustMockBroker) GetOrderDetails(_ context.Context, orderID string) (ports.OrderDetails, error) {
	if b.getDetailsFunc != nil {
		return b.getDetailsFunc(orderID)
	}
	return ports.OrderDetails{Status: "new"}, nil
}
func (b *dustMockBroker) CancelAllOpenOrders(context.Context) (int, error) { return 0, nil }
func (b *dustMockBroker) GetOpenOrders(context.Context) ([]ports.OpenOrder, error) {
	return nil, nil
}

// dustMockRepo captures the saved trade so tests can assert rationale/strategy.
type dustMockRepo struct {
	mu     sync.Mutex
	trades []domain.Trade
}

func (r *dustMockRepo) SaveTrade(_ context.Context, t domain.Trade) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trades = append(r.trades, t)
	return nil
}
func (r *dustMockRepo) lastTrade() (domain.Trade, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.trades) == 0 {
		return domain.Trade{}, false
	}
	return r.trades[len(r.trades)-1], true
}
func (r *dustMockRepo) SaveMarketBar(context.Context, domain.MarketBar) error { return nil }
func (r *dustMockRepo) SaveMarketBars(context.Context, []domain.MarketBar) (int, error) {
	return 0, nil
}
func (r *dustMockRepo) GetMarketBars(context.Context, domain.Symbol, domain.Timeframe, time.Time, time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}
func (r *dustMockRepo) GetMarketBarsMulti(context.Context, []domain.Symbol, domain.Timeframe, time.Time, time.Time) (map[string][]domain.MarketBar, error) {
	return nil, nil
}
func (r *dustMockRepo) GetTrades(context.Context, string, domain.EnvMode, time.Time, time.Time) ([]domain.Trade, error) {
	return nil, nil
}
func (r *dustMockRepo) SaveStrategyDNA(context.Context, domain.StrategyDNA) error { return nil }
func (r *dustMockRepo) GetLatestStrategyDNA(context.Context, string, domain.EnvMode) (*domain.StrategyDNA, error) {
	return nil, nil
}
func (r *dustMockRepo) SaveOrder(context.Context, domain.BrokerOrder) error { return nil }
func (r *dustMockRepo) UpdateOrderFill(context.Context, string, time.Time, float64, float64) error {
	return nil
}
func (r *dustMockRepo) RecordFill(context.Context, string, time.Time, float64, float64, domain.Trade) error {
	return nil
}
func (r *dustMockRepo) ListTrades(context.Context, ports.TradeQuery) (ports.TradePage, error) {
	return ports.TradePage{}, nil
}
func (r *dustMockRepo) ListOrders(context.Context, ports.OrderQuery) (ports.OrderPage, error) {
	return ports.OrderPage{}, nil
}
func (r *dustMockRepo) SaveThoughtLog(context.Context, domain.ThoughtLog) error { return nil }
func (r *dustMockRepo) GetThoughtLogsByIntentID(context.Context, string) ([]domain.ThoughtLog, error) {
	return nil, nil
}
func (r *dustMockRepo) UpdateTradeThesis(context.Context, string, domain.EnvMode, domain.Symbol, json.RawMessage) error {
	return nil
}
func (r *dustMockRepo) GetMaxBarHighSince(context.Context, domain.Symbol, domain.Timeframe, time.Time) (float64, error) {
	return 0, nil
}
func (r *dustMockRepo) GetLatestThesisForSymbol(context.Context, string, domain.EnvMode, domain.Symbol) (json.RawMessage, error) {
	return nil, nil
}
func (r *dustMockRepo) GetNonTerminalOrders(context.Context, string, domain.EnvMode) ([]domain.BrokerOrder, error) {
	return nil, nil
}
func (r *dustMockRepo) GetRecordedFillQty(context.Context, string, domain.EnvMode, domain.Symbol, string, time.Time) (float64, error) {
	return 0, nil
}
func (r *dustMockRepo) UpdateOrderStatus(context.Context, string, string) error { return nil }
func (r *dustMockRepo) GetNetPositions(context.Context, string, domain.EnvMode) (map[domain.Symbol]float64, error) {
	return nil, nil
}
func (r *dustMockRepo) GetAvgEntryPrice(context.Context, string, domain.EnvMode, domain.Symbol) (float64, error) {
	return 0, nil
}
func (r *dustMockRepo) HasCanceledExitOrder(context.Context, string, domain.EnvMode, domain.Symbol) (bool, error) {
	return false, nil
}
func (r *dustMockRepo) UpdateBarIndicators(context.Context, domain.Symbol, domain.Timeframe, time.Time, float64, float64, float64, float64, map[string]float64) error {
	return nil
}

// dustMockOptionsPrice returns a single canned quote per symbol.
type dustMockOptionsPrice struct {
	quote domain.OptionQuote
	err   error
	calls int32
}

func (m *dustMockOptionsPrice) GetOptionPrices(_ context.Context, syms []domain.Symbol) (map[domain.Symbol]domain.OptionQuote, error) {
	atomic.AddInt32(&m.calls, 1)
	if m.err != nil {
		return nil, m.err
	}
	out := make(map[domain.Symbol]domain.OptionQuote, len(syms))
	for _, s := range syms {
		out[s] = m.quote
	}
	return out, nil
}

// newTestService builds a Service wired with the caller-provided mocks. Only
// the dust-sweep paths run in these tests, so fields unused by sweep stay nil.
func newTestService(broker ports.BrokerPort, repo ports.RepositoryPort, nowFn func() time.Time, opt ports.OptionsPricePort) (*Service, *memory.Bus) {
	bus := memory.NewBus()
	s := &Service{
		eventBus:         bus,
		broker:           broker,
		repo:             repo,
		log:              zerolog.Nop(),
		nowFn:            nowFn,
		optionsPricePort: opt,
	}
	return s, bus
}

func captureFillEvents(t *testing.T, bus *memory.Bus) *[]map[string]any {
	t.Helper()
	var payloads []map[string]any
	var mu sync.Mutex
	err := bus.Subscribe(context.Background(), domain.EventFillReceived, func(ctx context.Context, e domain.Event) error {
		if p, ok := e.Payload.(map[string]any); ok {
			mu.Lock()
			payloads = append(payloads, p)
			mu.Unlock()
		}
		return nil
	})
	require.NoError(t, err)
	return &payloads
}

// A weekday 11:00 ET in UTC — safely outside the near-close window and well
// after market open. Used as the default clock for most tests.
func clockWeekdayMorning() func() time.Time {
	loc, _ := time.LoadLocation("America/New_York")
	return func() time.Time {
		return time.Date(2026, 4, 15, 11, 0, 0, 0, loc)
	}
}

func TestSweepDust_HealthyQuote_MarketableLimitFills(t *testing.T) {
	now := clockWeekdayMorning()
	broker := &dustMockBroker{positionQty: 1.0, returnOrderID: "sweep-1"}
	filled := false
	broker.getDetailsFunc = func(id string) (ports.OrderDetails, error) {
		// First poll returns pending, second returns filled. Forces the poll
		// loop to iterate at least once.
		if !filled {
			filled = true
			return ports.OrderDetails{Status: "accepted"}, nil
		}
		return ports.OrderDetails{
			Status: "filled", FilledQty: 1.0, FilledAvgPrice: 1.73, FilledAt: now(),
		}, nil
	}
	repo := &dustMockRepo{}
	oprice := &dustMockOptionsPrice{
		quote: domain.OptionQuote{
			Bid: 1.75, Ask: 1.85, BidSize: 10, AskSize: 10, Timestamp: now(),
		},
	}

	svc, bus := newTestService(broker, repo, now, oprice)
	fills := captureFillEvents(t, bus)

	svc.sweepDustPosition("t", domain.EnvModePaper, testOCC, "parent-1", "macd_only_v1")
	bus.Flush()

	require.Len(t, broker.submitCalls, 1, "should submit exactly one order (limit only)")
	assert.Equal(t, "limit", broker.submitCalls[0].OrderType)
	// Limit should be priced one tick below the bid (floored by bps cap).
	// bid=1.75, maxAdverseBps=max(150, 571/2=285)=285 -> bid*(1-0.0285)=1.70, floor=bid-0.01=1.74 -> max=1.74
	assert.InDelta(t, 1.74, broker.submitCalls[0].LimitPrice, 0.001)
	assert.Equal(t, "day", broker.submitCalls[0].TimeInForce)
	assert.Zero(t, atomic.LoadInt32(&broker.cancelCalls), "no cancel on successful fill")

	trade, ok := repo.lastTrade()
	require.True(t, ok)
	assert.Equal(t, "dust_sweep", trade.Strategy)
	assert.Contains(t, trade.Rationale, "origin=macd_only_v1")

	require.Len(t, *fills, 1)
	assert.Equal(t, "dust_sweep", (*fills)[0]["strategy"])
	assert.Equal(t, "macd_only_v1", (*fills)[0]["origin_strategy"])
}

func TestSweepDust_StaleQuote_GoesStraightToMarket(t *testing.T) {
	now := clockWeekdayMorning()
	broker := &dustMockBroker{positionQty: 1.0, returnOrderID: "mkt-1"}
	broker.getDetailsFunc = func(string) (ports.OrderDetails, error) {
		return ports.OrderDetails{Status: "filled", FilledQty: 1.0, FilledAvgPrice: 1.70, FilledAt: now()}, nil
	}
	repo := &dustMockRepo{}
	stale := now().Add(-10 * time.Second)
	oprice := &dustMockOptionsPrice{
		quote: domain.OptionQuote{Bid: 1.75, Ask: 1.85, BidSize: 10, Timestamp: stale},
	}

	svc, _ := newTestService(broker, repo, now, oprice)
	svc.sweepDustPosition("t", domain.EnvModePaper, testOCC, "parent-1", "macd_only_v1")

	require.Len(t, broker.submitCalls, 1)
	assert.Equal(t, "market", broker.submitCalls[0].OrderType)
}

func TestSweepDust_HaltedQuote_BidSizeZero_GoesStraightToMarket(t *testing.T) {
	now := clockWeekdayMorning()
	broker := &dustMockBroker{positionQty: 1.0, returnOrderID: "mkt-1"}
	broker.getDetailsFunc = func(string) (ports.OrderDetails, error) {
		return ports.OrderDetails{Status: "filled", FilledQty: 1.0, FilledAvgPrice: 1.70, FilledAt: now()}, nil
	}
	repo := &dustMockRepo{}
	oprice := &dustMockOptionsPrice{
		quote: domain.OptionQuote{Bid: 1.75, Ask: 1.85, BidSize: 0, Timestamp: now()},
	}

	svc, _ := newTestService(broker, repo, now, oprice)
	svc.sweepDustPosition("t", domain.EnvModePaper, testOCC, "parent-1", "macd_only_v1")

	require.Len(t, broker.submitCalls, 1)
	assert.Equal(t, "market", broker.submitCalls[0].OrderType)
}

func TestSweepDust_HaltedQuote_BidZero_GoesStraightToMarket(t *testing.T) {
	now := clockWeekdayMorning()
	broker := &dustMockBroker{positionQty: 1.0, returnOrderID: "mkt-1"}
	broker.getDetailsFunc = func(string) (ports.OrderDetails, error) {
		return ports.OrderDetails{Status: "filled", FilledQty: 1.0, FilledAvgPrice: 1.70, FilledAt: now()}, nil
	}
	repo := &dustMockRepo{}
	oprice := &dustMockOptionsPrice{
		quote: domain.OptionQuote{Bid: 0, Ask: 1.85, BidSize: 10, Timestamp: now()},
	}

	svc, _ := newTestService(broker, repo, now, oprice)
	svc.sweepDustPosition("t", domain.EnvModePaper, testOCC, "parent-1", "macd_only_v1")

	require.Len(t, broker.submitCalls, 1)
	assert.Equal(t, "market", broker.submitCalls[0].OrderType)
}

func TestSweepDust_BlownSpread_GoesStraightToMarket(t *testing.T) {
	now := clockWeekdayMorning()
	broker := &dustMockBroker{positionQty: 1.0, returnOrderID: "mkt-1"}
	broker.getDetailsFunc = func(string) (ports.OrderDetails, error) {
		return ports.OrderDetails{Status: "filled", FilledQty: 1.0, FilledAvgPrice: 1.00, FilledAt: now()}, nil
	}
	repo := &dustMockRepo{}
	// spread/mid = 1.0/1.5 = 0.67, way past 0.25 cap
	oprice := &dustMockOptionsPrice{
		quote: domain.OptionQuote{Bid: 1.0, Ask: 2.0, BidSize: 10, Timestamp: now()},
	}

	svc, _ := newTestService(broker, repo, now, oprice)
	svc.sweepDustPosition("t", domain.EnvModePaper, testOCC, "parent-1", "macd_only_v1")

	require.Len(t, broker.submitCalls, 1)
	assert.Equal(t, "market", broker.submitCalls[0].OrderType)
}

func TestSweepDust_LimitTimesOut_MarketFallback(t *testing.T) {
	now := clockWeekdayMorning()
	// sequencedBroker issues "LIMIT-1" on the first submit, "MKT-1" on the
	// second. Limit order never fills (status stays "accepted") until we
	// observe a cancel. After the timeout fires, cancel arrives, status
	// flips to "canceled", the market fallback submits and fills.
	var (
		mu            sync.Mutex
		limitCanceled bool
		submitSeq     int
	)
	broker := &sequencedBroker{
		dustMockBroker: &dustMockBroker{positionQty: 1.0},
		onSubmit: func() string {
			mu.Lock()
			defer mu.Unlock()
			submitSeq++
			if submitSeq == 1 {
				return "LIMIT-1"
			}
			return "MKT-1"
		},
		onCancel: func() {
			mu.Lock()
			limitCanceled = true
			mu.Unlock()
		},
	}
	broker.dustMockBroker.getDetailsFunc = func(orderID string) (ports.OrderDetails, error) {
		if strings.HasPrefix(orderID, "LIMIT") {
			mu.Lock()
			canceled := limitCanceled
			mu.Unlock()
			if canceled {
				return ports.OrderDetails{Status: "canceled"}, nil
			}
			return ports.OrderDetails{Status: "accepted"}, nil
		}
		return ports.OrderDetails{Status: "filled", FilledQty: 1.0, FilledAvgPrice: 1.70, FilledAt: now()}, nil
	}
	repo := &dustMockRepo{}
	oprice := &dustMockOptionsPrice{
		quote: domain.OptionQuote{Bid: 1.75, Ask: 1.85, BidSize: 10, Timestamp: now()},
	}

	svc, bus := newTestService(broker, repo, now, oprice)
	// Shrink the limit window so the test finishes in ~3s instead of 15s.
	// Must be >1s so at least one poll tick (1s cadence) runs before timeout
	// — otherwise we cannot observe a "still accepted" status before cancel.
	svc.dustSweepLimitWindowOverride = 1500 * time.Millisecond
	fills := captureFillEvents(t, bus)

	done := make(chan struct{})
	go func() {
		svc.sweepDustPosition("t", domain.EnvModePaper, testOCC, "parent-1", "macd_only_v1")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sweep did not complete within 10s")
	}
	bus.Flush()

	require.GreaterOrEqual(t, len(broker.dustMockBroker.submitCalls), 2, "should submit limit then market")
	assert.Equal(t, "limit", broker.dustMockBroker.submitCalls[0].OrderType)
	assert.Equal(t, "market", broker.dustMockBroker.submitCalls[1].OrderType)
	assert.GreaterOrEqual(t, int(atomic.LoadInt32(&broker.dustMockBroker.cancelCalls)), 1)

	trade, ok := repo.lastTrade()
	require.True(t, ok)
	assert.Equal(t, "dust_sweep", trade.Strategy)
	assert.Contains(t, trade.Rationale, "origin=macd_only_v1")

	require.NotEmpty(t, *fills)
	last := (*fills)[len(*fills)-1]
	assert.Equal(t, "macd_only_v1", last["origin_strategy"])
}

// sequencedBroker wraps dustMockBroker to return a deterministic sequence of
// order IDs so the status callback can branch on which submission each
// poll is observing.
type sequencedBroker struct {
	*dustMockBroker
	onSubmit func() string
	onCancel func()
}

func (b *sequencedBroker) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	b.mu.Lock()
	b.submitCalls = append(b.submitCalls, intent)
	b.mu.Unlock()
	if b.submitErr != nil {
		return "", b.submitErr
	}
	return b.onSubmit(), nil
}

func (b *sequencedBroker) CancelOrder(ctx context.Context, orderID string) error {
	atomic.AddInt32(&b.cancelCalls, 1)
	if b.onCancel != nil {
		b.onCancel()
	}
	return nil
}

func TestSweepDust_NearClose_GoesStraightToMarket(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	now := func() time.Time {
		return time.Date(2026, 4, 15, 15, 50, 0, 0, loc) // 15:50 ET
	}
	broker := &dustMockBroker{positionQty: 1.0, returnOrderID: "mkt-1"}
	broker.getDetailsFunc = func(string) (ports.OrderDetails, error) {
		return ports.OrderDetails{Status: "filled", FilledQty: 1.0, FilledAvgPrice: 1.70, FilledAt: now()}, nil
	}
	repo := &dustMockRepo{}
	oprice := &dustMockOptionsPrice{
		quote: domain.OptionQuote{Bid: 1.75, Ask: 1.85, BidSize: 10, Timestamp: now()},
	}

	svc, _ := newTestService(broker, repo, now, oprice)
	svc.sweepDustPosition("t", domain.EnvModePaper, testOCC, "parent-1", "macd_only_v1")

	require.Len(t, broker.submitCalls, 1)
	assert.Equal(t, "market", broker.submitCalls[0].OrderType)
	assert.Zero(t, atomic.LoadInt32(&oprice.calls), "near-close should skip quote fetch")
}

func TestSweepDust_Equity_GoesStraightToMarket(t *testing.T) {
	now := clockWeekdayMorning()
	broker := &dustMockBroker{positionQty: 10.0, returnOrderID: "mkt-1"}
	broker.getDetailsFunc = func(string) (ports.OrderDetails, error) {
		return ports.OrderDetails{Status: "filled", FilledQty: 10.0, FilledAvgPrice: 150.0, FilledAt: now()}, nil
	}
	repo := &dustMockRepo{}
	oprice := &dustMockOptionsPrice{
		quote: domain.OptionQuote{Bid: 149.0, Ask: 151.0, BidSize: 100, Timestamp: now()},
	}

	svc, _ := newTestService(broker, repo, now, oprice)
	svc.sweepDustPosition("t", domain.EnvModePaper, testEquity, "parent-1", "macd_only_v1")

	require.Len(t, broker.submitCalls, 1)
	assert.Equal(t, "market", broker.submitCalls[0].OrderType)
	assert.Zero(t, atomic.LoadInt32(&oprice.calls), "equity should skip quote fetch")
}

func TestSweepDust_OriginStrategy_FlowsToRationaleAndEventPayload(t *testing.T) {
	now := clockWeekdayMorning()
	broker := &dustMockBroker{positionQty: 1.0, returnOrderID: "sweep-1"}
	broker.getDetailsFunc = func(string) (ports.OrderDetails, error) {
		return ports.OrderDetails{Status: "filled", FilledQty: 1.0, FilledAvgPrice: 1.74, FilledAt: now()}, nil
	}
	repo := &dustMockRepo{}
	oprice := &dustMockOptionsPrice{
		quote: domain.OptionQuote{Bid: 1.75, Ask: 1.85, BidSize: 10, Timestamp: now()},
	}
	svc, bus := newTestService(broker, repo, now, oprice)
	fills := captureFillEvents(t, bus)

	svc.sweepDustPosition("t", domain.EnvModePaper, testOCC, "parent-xyz", "avwap_v2")
	bus.Flush()

	trade, ok := repo.lastTrade()
	require.True(t, ok)
	assert.Equal(t, "dust_sweep", trade.Strategy, "raw Strategy stays dust_sweep for audit immutability")
	assert.Contains(t, trade.Rationale, "origin=avwap_v2")

	require.Len(t, *fills, 1)
	payload := (*fills)[0]
	assert.Equal(t, "dust_sweep", payload["strategy"])
	assert.Equal(t, "avwap_v2", payload["origin_strategy"])
}

func TestDustSweepLimitPrice_PricingFloor(t *testing.T) {
	now := time.Now()
	// Healthy quote — price one tick below bid.
	q := domain.OptionQuote{Bid: 1.75, Ask: 1.85, BidSize: 10, Timestamp: now}
	p, ok := dustSweepLimitPrice(q, now)
	require.True(t, ok)
	// maxAdverseBps = max(150, half of spread_bps). spread_bps = (0.10/1.80)*10000 = 555.5, half = 277.8
	// capped = 1.75 * (1 - 0.02778) = 1.7014
	// floor = 1.74
	// max = 1.74
	assert.InDelta(t, 1.74, p, 0.001)
}

func TestDustSweepLimitPrice_RejectsStale(t *testing.T) {
	now := time.Now()
	q := domain.OptionQuote{Bid: 1.75, Ask: 1.85, BidSize: 10, Timestamp: now.Add(-10 * time.Second)}
	_, ok := dustSweepLimitPrice(q, now)
	assert.False(t, ok)
}

func TestIsNearClose(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"11am ET weekday", time.Date(2026, 4, 15, 11, 0, 0, 0, loc), false},
		{"15:50 ET weekday", time.Date(2026, 4, 15, 15, 50, 0, 0, loc), true},
		{"15:44 ET weekday (just outside)", time.Date(2026, 4, 15, 15, 44, 0, 0, loc), false},
		{"15:46 ET weekday (just inside)", time.Date(2026, 4, 15, 15, 46, 0, 0, loc), true},
		{"16:00 ET weekday (at close)", time.Date(2026, 4, 15, 16, 0, 0, 0, loc), false},
		{"15:50 ET saturday", time.Date(2026, 4, 18, 15, 50, 0, 0, loc), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isNearClose(tc.t))
		})
	}
}
