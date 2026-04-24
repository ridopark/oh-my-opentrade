package execution

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// multiFillRepo records every RecordFill call so the test can assert the
// per-leg writes and the final orders state. It also enforces the
// monotonic-update + execution_id UNIQUE semantics of the production repo
// so out-of-order delivery stays correct.
type orderFillState struct {
	filledQty   float64
	filledPrice float64
	filledAt    time.Time
	status      string
}

type multiFillRepo struct {
	mu      sync.Mutex
	fills   []domain.Trade // every trade row written, in order
	execIDs map[string]struct{}
	orders  map[string]orderFillState
}

func newMultiFillRepo() *multiFillRepo {
	return &multiFillRepo{
		execIDs: make(map[string]struct{}),
		orders:  make(map[string]orderFillState),
	}
}

// orderQty is the intent quantity used for status promotion in the mock.
// Set by the test before firing updates.
const multiFillOrderQty = 34.0

// applyMonotonicFill mirrors the production GREATEST/WHERE orders update:
// qty/price advance only when the incoming filledQty exceeds the recorded
// cumulative, and status promotes submitted → partially_filled → filled.
func (r *multiFillRepo) applyMonotonicFill(brokerOrderID string, filledAt time.Time, filledPrice, filledQty float64) {
	cur := r.orders[brokerOrderID]
	if filledQty > cur.filledQty {
		cur.filledQty = filledQty
		cur.filledPrice = filledPrice
	}
	if filledAt.After(cur.filledAt) {
		cur.filledAt = filledAt
	}
	if cur.status != "filled" {
		if filledQty+1e-9 >= multiFillOrderQty {
			cur.status = "filled"
		} else {
			cur.status = "partially_filled"
		}
	}
	r.orders[brokerOrderID] = cur
}

func (r *multiFillRepo) RecordFill(_ context.Context, brokerOrderID string, filledAt time.Time, filledPrice, filledQty float64, trade domain.Trade) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if trade.ExecutionID != "" {
		if _, dup := r.execIDs[trade.ExecutionID]; dup {
			return nil
		}
		r.execIDs[trade.ExecutionID] = struct{}{}
	}
	r.fills = append(r.fills, trade)
	r.applyMonotonicFill(brokerOrderID, filledAt, filledPrice, filledQty)
	return nil
}

func (r *multiFillRepo) UpdateOrderFill(_ context.Context, brokerOrderID string, filledAt time.Time, filledPrice, filledQty float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applyMonotonicFill(brokerOrderID, filledAt, filledPrice, filledQty)
	return nil
}

func (r *multiFillRepo) SaveTrade(context.Context, domain.Trade) error { return nil }
func (r *multiFillRepo) SaveOrder(context.Context, domain.BrokerOrder) error { return nil }

// RepositoryPort stubs.
func (r *multiFillRepo) SaveMarketBar(context.Context, domain.MarketBar) error { return nil }
func (r *multiFillRepo) SaveMarketBars(context.Context, []domain.MarketBar) (int, error) {
	return 0, nil
}
func (r *multiFillRepo) GetMarketBars(context.Context, domain.Symbol, domain.Timeframe, time.Time, time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}
func (r *multiFillRepo) GetMarketBarsMulti(context.Context, []domain.Symbol, domain.Timeframe, time.Time, time.Time) (map[string][]domain.MarketBar, error) {
	return nil, nil
}
func (r *multiFillRepo) GetTrades(context.Context, string, domain.EnvMode, time.Time, time.Time) ([]domain.Trade, error) {
	return nil, nil
}
func (r *multiFillRepo) SaveStrategyDNA(context.Context, domain.StrategyDNA) error { return nil }
func (r *multiFillRepo) GetLatestStrategyDNA(context.Context, string, domain.EnvMode) (*domain.StrategyDNA, error) {
	return nil, nil
}
func (r *multiFillRepo) ListTrades(context.Context, ports.TradeQuery) (ports.TradePage, error) {
	return ports.TradePage{}, nil
}
func (r *multiFillRepo) ListOrders(context.Context, ports.OrderQuery) (ports.OrderPage, error) {
	return ports.OrderPage{}, nil
}
func (r *multiFillRepo) SaveThoughtLog(context.Context, domain.ThoughtLog) error { return nil }
func (r *multiFillRepo) GetThoughtLogsByIntentID(context.Context, string) ([]domain.ThoughtLog, error) {
	return nil, nil
}
func (r *multiFillRepo) UpdateTradeThesis(context.Context, string, domain.EnvMode, domain.Symbol, json.RawMessage) error {
	return nil
}
func (r *multiFillRepo) GetMaxBarHighSince(context.Context, domain.Symbol, domain.Timeframe, time.Time) (float64, error) {
	return 0, nil
}
func (r *multiFillRepo) GetLatestThesisForSymbol(context.Context, string, domain.EnvMode, domain.Symbol) (json.RawMessage, error) {
	return nil, nil
}
func (r *multiFillRepo) GetNonTerminalOrders(context.Context, string, domain.EnvMode) ([]domain.BrokerOrder, error) {
	return nil, nil
}
func (r *multiFillRepo) GetOrderByBrokerOrderID(context.Context, string) (*domain.BrokerOrder, error) {
	return nil, nil
}
func (r *multiFillRepo) GetRecordedFillQty(context.Context, string, domain.EnvMode, domain.Symbol, string, time.Time) (float64, error) {
	return 0, nil
}
func (r *multiFillRepo) UpdateOrderStatus(context.Context, string, string) error { return nil }
func (r *multiFillRepo) GetRecordedExecutionIDs(context.Context, string, domain.EnvMode, time.Time) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (r *multiFillRepo) GetNetPositions(context.Context, string, domain.EnvMode) (map[domain.Symbol]float64, error) {
	return nil, nil
}
func (r *multiFillRepo) GetAvgEntryPrice(context.Context, string, domain.EnvMode, domain.Symbol) (float64, error) {
	return 0, nil
}
func (r *multiFillRepo) HasCanceledExitOrder(context.Context, string, domain.EnvMode, domain.Symbol) (bool, error) {
	return false, nil
}
func (r *multiFillRepo) UpdateBarIndicators(context.Context, domain.Symbol, domain.Timeframe, time.Time, float64, float64, float64, float64, map[string]float64) error {
	return nil
}

// newMultiFillService wires a Service ready to receive stream fills for
// broker order "3222" representing a 34-lot BUY.
func newMultiFillService(repo *multiFillRepo) *Service {
	bus := memory.NewBus()
	return &Service{
		eventBus: bus,
		repo:     repo,
		tenantID: "tenant-1",
		envMode:  domain.EnvModePaper,
		log:      zerolog.Nop(),
		nowFn:    func() time.Time { return time.Date(2026, 4, 24, 15, 32, 40, 0, time.UTC) },
	}
}

// queuePendingBuy seeds s.pendingOrders with a multi-fill buy intent.
func queuePendingBuy(s *Service, brokerOrderID string, qty float64) *pendingOrder {
	intent := domain.OrderIntent{
		ID:        uuid.New(),
		Symbol:    domain.Symbol("SPY260424C00712000"),
		Direction: domain.DirectionLong,
		Quantity:  qty,
		LimitPrice: 1.05,
		Strategy:   "copytrade_v1",
		Rationale:  "multi-fill test",
	}
	po := &pendingOrder{
		intent:      intent,
		tenantID:    "tenant-1",
		envMode:     domain.EnvModePaper,
		submitStart: time.Date(2026, 4, 24, 15, 32, 30, 0, time.UTC),
	}
	s.pendingOrders.Store(brokerOrderID, po)
	return po
}

// fillLeg is one exec event in a multi-fill sequence.
type fillLeg struct {
	execID string
	legQty float64
	price  float64
	ts     time.Time
}

// cumulative computes the running cumQty / VWAP at each leg for building
// OrderUpdate payloads the way a broker would deliver them.
func cumulative(legs []fillLeg) []ports.OrderUpdate {
	out := make([]ports.OrderUpdate, len(legs))
	var notional, cumQty float64
	for i, leg := range legs {
		notional += leg.price * leg.legQty
		cumQty += leg.legQty
		out[i] = ports.OrderUpdate{
			BrokerOrderID:  "3222",
			ExecutionID:    leg.execID,
			Event:          "partial_fill",
			Qty:            leg.legQty,
			Price:          leg.price,
			FilledQty:      cumQty,
			FilledAvgPrice: notional / cumQty,
			FilledAt:       leg.ts,
		}
	}
	// Last leg arrives as terminal "fill".
	out[len(out)-1].Event = "fill"
	return out
}

// 2026-04-24 SPY260424C00712000 multi-fill incident: 4 partial fills,
// cumulative 34 contracts. The bug recorded only the first leg.
var incidentLegs = []fillLeg{
	{execID: "exec-4a09", legQty: 1, price: 1.00, ts: time.Date(2026, 4, 24, 15, 32, 32, 0, time.UTC)},
	{execID: "exec-4a0b", legQty: 5, price: 1.00, ts: time.Date(2026, 4, 24, 15, 32, 32, 1, time.UTC)},
	{execID: "exec-4a08", legQty: 25, price: 1.00, ts: time.Date(2026, 4, 24, 15, 32, 41, 0, time.UTC)},
	{execID: "exec-4a1a", legQty: 3, price: 0.99, ts: time.Date(2026, 4, 24, 15, 32, 43, 0, time.UTC)},
}

func TestHandleStreamFill_MultiFillPerLeg(t *testing.T) {
	repo := newMultiFillRepo()
	svc := newMultiFillService(repo)
	queuePendingBuy(svc, "3222", multiFillOrderQty)

	updates := cumulative(incidentLegs)
	for _, u := range updates {
		svc.handleStreamFill(u, zerolog.Nop())
	}

	require.Len(t, repo.fills, 4, "expected one trade row per execution")

	// Assert the persisted legs match the incident sequence exactly.
	for i, leg := range incidentLegs {
		got := repo.fills[i]
		assert.Equal(t, leg.execID, got.ExecutionID, "leg %d exec id", i)
		assert.InDelta(t, leg.legQty, got.Quantity, 1e-9, "leg %d qty", i)
		assert.InDelta(t, leg.price, got.Price, 1e-9, "leg %d price", i)
		assert.Equal(t, "BUY", got.Side, "leg %d side", i)
	}

	// Orders-row monotonic state after all legs.
	final := repo.orders["3222"]
	assert.InDelta(t, 34.0, final.filledQty, 1e-9, "final cumulative filled_qty")
	expectedVWAP := (1*1.00 + 5*1.00 + 25*1.00 + 3*0.99) / 34
	assert.InDelta(t, expectedVWAP, final.filledPrice, 1e-6, "final VWAP filled_price")
	assert.Equal(t, "filled", final.status, "final orders.status")

	// Final filled_at is the last leg's timestamp.
	assert.Equal(t, incidentLegs[len(incidentLegs)-1].ts, final.filledAt)

	// Pending map cleared after terminal fill.
	_, stillPending := svc.pendingOrders.Load("3222")
	assert.False(t, stillPending, "pending order should be cleared after terminal fill")
}

func TestHandleStreamFill_DedupOnReplay(t *testing.T) {
	repo := newMultiFillRepo()
	svc := newMultiFillService(repo)
	queuePendingBuy(svc, "3222", multiFillOrderQty)

	updates := cumulative(incidentLegs)
	for _, u := range updates {
		svc.handleStreamFill(u, zerolog.Nop())
	}
	// Replay the same sequence — execution_id UNIQUE should drop every dupe.
	// Note: the pending order is already cleared, so the replay will log the
	// "unknown order" warning and skip; re-queue to exercise the repo-level
	// dedup path directly.
	queuePendingBuy(svc, "3222", multiFillOrderQty)
	for _, u := range updates {
		svc.handleStreamFill(u, zerolog.Nop())
	}

	assert.Len(t, repo.fills, 4, "replay should not insert duplicate trade rows")
	assert.InDelta(t, 34.0, repo.orders["3222"].filledQty, 1e-9, "cumulative unchanged on replay")
}

// TestHandleStreamFill_CumulativeFallbackClosesOrder exercises the defensive
// fallback: if a broker fails to label the final leg as Event="fill" and
// only sends "partial_fill", the cumulative-match (cumQty >= intent.Quantity)
// still finalizes the order so pending doesn't leak intraday.
func TestHandleStreamFill_CumulativeFallbackClosesOrder(t *testing.T) {
	repo := newMultiFillRepo()
	svc := newMultiFillService(repo)
	queuePendingBuy(svc, "3222", multiFillOrderQty)

	updates := cumulative(incidentLegs)
	for i := range updates {
		updates[i].Event = "partial_fill" // broker forgot to label terminal
	}
	for _, u := range updates {
		svc.handleStreamFill(u, zerolog.Nop())
	}

	assert.Len(t, repo.fills, 4, "all four legs persisted even without terminal label")
	assert.InDelta(t, 34.0, repo.orders["3222"].filledQty, 1e-9)
	_, stillPending := svc.pendingOrders.Load("3222")
	assert.False(t, stillPending, "cumulative match should finalize when broker omits terminal label")
}

// TestHandleStreamFill_SingleFillStillWorks exercises the common case —
// simbroker and alpaca-style all-at-once fills — to guard against
// regressions in the per-leg pathway.
func TestHandleStreamFill_SingleFillStillWorks(t *testing.T) {
	repo := newMultiFillRepo()
	svc := newMultiFillService(repo)
	queuePendingBuy(svc, "3222", multiFillOrderQty)

	svc.handleStreamFill(ports.OrderUpdate{
		BrokerOrderID:  "3222",
		ExecutionID:    "exec-single",
		Event:          "fill",
		Qty:            34,
		Price:          1.00,
		FilledQty:      34,
		FilledAvgPrice: 1.00,
		FilledAt:       time.Date(2026, 4, 24, 15, 32, 32, 0, time.UTC),
	}, zerolog.Nop())

	require.Len(t, repo.fills, 1)
	assert.Equal(t, "exec-single", repo.fills[0].ExecutionID)
	assert.InDelta(t, 34.0, repo.fills[0].Quantity, 1e-9)
	final := repo.orders["3222"]
	assert.InDelta(t, 34.0, final.filledQty, 1e-9)
	assert.Equal(t, "filled", final.status)
}

// Ensure the helper stays consistent even if someone changes floats.
func TestCumulativeHelper(t *testing.T) {
	u := cumulative(incidentLegs)
	require.Len(t, u, 4)
	assert.InDelta(t, 34.0, u[3].FilledQty, 1e-9)
	expected := (1*1.00 + 5*1.00 + 25*1.00 + 3*0.99) / 34
	assert.InDelta(t, expected, u[3].FilledAvgPrice, 1e-9)
	assert.Equal(t, "fill", u[3].Event)
	assert.Equal(t, "partial_fill", u[0].Event)
	// Invariant: cumQty is monotonic non-decreasing.
	for i := 1; i < len(u); i++ {
		assert.True(t, u[i].FilledQty+1e-9 >= u[i-1].FilledQty, "cumQty non-decreasing at leg %d", i)
	}
	// Invariant: VWAP stays within leg-price range.
	for _, up := range u {
		assert.True(t, up.FilledAvgPrice <= math.Max(1.00, 1.05)+1e-9)
		assert.True(t, up.FilledAvgPrice >= 0.99-1e-9)
	}
}
