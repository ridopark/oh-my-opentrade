package positionmonitor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedPartiallyExitedPosition(t *testing.T, svc *Service, symbol string, buyQty, partialSellQty float64) {
	t.Helper()
	svc.processFill(fillMsg{
		Symbol:     domain.Symbol(symbol),
		Side:       "BUY",
		Price:      12.14,
		Quantity:   buyQty,
		FilledAt:   time.Now(),
		Strategy:   "avwap",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})
	svc.processFill(fillMsg{
		Symbol:   domain.Symbol(symbol),
		Side:     "SELL",
		Price:    12.13,
		Quantity: partialSellQty,
		FilledAt: time.Now(),
		Strategy: "avwap",
	})
}

func setPendingExit(svc *Service, symbol, orderID string) {
	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, symbol)
	pos := svc.positions[key]
	pos.ExitPending = true
	pos.ExitOrderID = orderID
	pos.ExitPendingAt = time.Now().Add(-exitPendingTimeout - time.Second)
}

func TestHandleExitTimeout_OrderAlreadyFilled_ReconcilesMissingFill(t *testing.T) {
	broker := &mockBroker{
		cancelErr: fmt.Errorf("alpaca: cancel order failed (status 422): order is already in filled state"),
		orderDetailsResult: ports.OrderDetails{
			FilledQty:      576.58,
			FilledAvgPrice: 12.13,
		},
	}
	repo := &capturingRepo{}
	svc := newTestServiceWithBrokerAndRepo(broker, repo)

	seedPartiallyExitedPosition(t, svc, "F", 576.58, 9.0)
	require.Equal(t, 1, svc.PositionCount())

	setPendingExit(svc, "F", "order-abc")

	svc.tick()

	assert.Equal(t, 0, svc.PositionCount(), "position removed after reconciliation")

	require.Len(t, repo.savedTrades, 1)
	trade := repo.savedTrades[0]
	assert.Equal(t, "SELL", trade.Side)
	assert.Equal(t, domain.Symbol("F"), trade.Symbol)
	assert.InDelta(t, 567.58, trade.Quantity, 0.001)
	assert.Equal(t, 12.13, trade.Price)
	assert.Equal(t, "FILLED", trade.Status)
	assert.Equal(t, "avwap", trade.Strategy)
	assert.Contains(t, trade.Rationale, "fill reconciliation")
	assert.Contains(t, trade.Rationale, "order-abc")
}

func TestHandleExitTimeout_OrderAlreadyFilled_GetDetailsFails_SchedulesRetry(t *testing.T) {
	broker := &mockBroker{
		cancelErr:       fmt.Errorf("alpaca: cancel order failed (status 422): order is already in filled state"),
		orderDetailsErr: fmt.Errorf("network timeout"),
	}
	repo := &capturingRepo{}
	svc := newTestServiceWithBrokerAndRepo(broker, repo)

	seedPartiallyExitedPosition(t, svc, "F", 576.58, 9.0)
	setPendingExit(svc, "F", "order-abc")

	svc.tick()

	assert.Equal(t, 1, svc.PositionCount(), "position retained when GetOrderDetails fails")
	assert.Empty(t, repo.savedTrades, "no DB write when details unavailable")

	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, "F")
	pos := svc.positions[key]
	// Single-ExitPending invariant: ExitPending stays true across the
	// cancel-filled-race, retry bump is the "attempt advanced" signal.
	assert.True(t, pos.ExitPending, "ExitPending stays true for retry")
	assert.Equal(t, 1, pos.ExitRetryCount)
}

func TestHandleExitTimeout_OrderAlreadyFilled_PartialFillOnly_SchedulesRetry(t *testing.T) {
	broker := &mockBroker{
		cancelErr: fmt.Errorf("alpaca: cancel order failed (status 422): order is already in filled state"),
		orderDetailsResult: ports.OrderDetails{
			FilledQty:      100.0,
			FilledAvgPrice: 12.13,
		},
	}
	repo := &capturingRepo{}
	svc := newTestServiceWithBrokerAndRepo(broker, repo)

	seedPartiallyExitedPosition(t, svc, "F", 576.58, 9.0)
	setPendingExit(svc, "F", "order-abc")

	svc.tick()

	assert.Equal(t, 1, svc.PositionCount(), "position retained when broker fill < remaining qty")
	assert.Empty(t, repo.savedTrades)

	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, "F")
	assert.Equal(t, 1, svc.positions[key].ExitRetryCount)
}

func TestHandleExitTimeout_NormalCancelSuccess_SchedulesRetry(t *testing.T) {
	broker := &mockBroker{}
	repo := &capturingRepo{}
	svc := newTestServiceWithBrokerAndRepo(broker, repo)

	seedPartiallyExitedPosition(t, svc, "F", 576.58, 9.0)
	setPendingExit(svc, "F", "order-abc")

	svc.tick()

	assert.Equal(t, 1, svc.PositionCount(), "position retained after successful cancel")
	assert.Empty(t, repo.savedTrades, "no reconciliation trade on normal cancel")

	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, "F")
	pos := svc.positions[key]
	// Single-ExitPending invariant (2026-04-16): ExitPending stays true
	// across cancel+resubmit; the retry bump and cleared ExitOrderID are
	// the observable "attempt cycled" signals.
	assert.True(t, pos.ExitPending)
	assert.Empty(t, pos.ExitOrderID)
	assert.Equal(t, 1, pos.ExitRetryCount)
}

// ---------------------------------------------------------------------------
// Re-peg + asymmetric timeout tests (live-trading safety change).
// ---------------------------------------------------------------------------

// trackingBroker records every CancelOrder and GetOrderDetails call with
// programmable responses. Used by the re-peg tests to drive the
// cancel-and-await protocol through its branches.
type trackingBroker struct {
	mockBroker
	cancelCalls   []string
	detailsCalls  int
	detailsStatus string
	cancelReturn  error
	detailsResult ports.OrderDetails
	detailsErr    error
}

func (m *trackingBroker) CancelOrder(_ context.Context, orderID string) error {
	m.cancelCalls = append(m.cancelCalls, orderID)
	return m.cancelReturn
}

func (m *trackingBroker) GetOrderDetails(_ context.Context, _ string) (ports.OrderDetails, error) {
	m.detailsCalls++
	if m.detailsStatus != "" {
		return ports.OrderDetails{Status: m.detailsStatus}, nil
	}
	return m.detailsResult, m.detailsErr
}

// seedOptionPendingExit builds an option position with a specific exit-rule
// type, applies it, and wires a pending-exit clock so the tick loop's
// timeout branch fires on the next invocation.
func seedOptionPendingExit(
	t *testing.T,
	svc *Service,
	symbol string,
	ruleType domain.ExitRuleType,
	ageBeyondTimeout time.Duration,
) *domain.MonitoredPosition {
	t.Helper()
	now := svc.nowFunc()
	svc.processFill(fillMsg{
		Symbol:     domain.Symbol(symbol),
		Side:       "BUY",
		Price:      1.23,
		Quantity:   5,
		FilledAt:   now.Add(-10 * time.Minute),
		Strategy:   "test",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{{Type: ruleType, Params: map[string]float64{}}},
	})
	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, symbol)
	pos := svc.positions[key]
	require.NotNil(t, pos)
	pos.InstrumentType = domain.InstrumentTypeOption
	pos.OptionRight = "CALL"
	pos.ExitPending = true
	pos.ExitOrderID = "live-order-1"
	// Force the tick loop to see this as past-timeout using the rule's
	// asymmetric timeout as the baseline.
	baseTimeout := exitTimeoutForPos(pos)
	pos.ExitPendingAt = now.Add(-baseTimeout - ageBeyondTimeout)
	return pos
}

func TestHandleExitTimeout_OptionTarget_RepegsBeforeEscalation(t *testing.T) {
	broker := &trackingBroker{detailsStatus: "canceled"}
	repo := &capturingRepo{}
	svc := newTestServiceWithBrokerAndRepo(&broker.mockBroker, repo)
	svc.broker = broker

	pos := seedOptionPendingExit(t, svc, "AAPL_OPT_C", domain.ExitRulePremiumTarget, time.Second)
	// Pre-set wall start so it's inside the budget.
	pos.ExitWallStartedAt = pos.ExitPendingAt

	svc.tick()

	// Target rule → re-peg path. ExitRetryCount untouched, ExitRepegCount++.
	assert.Equal(t, 0, pos.ExitRetryCount, "re-peg must not bump retry count")
	assert.Equal(t, 1, pos.ExitRepegCount, "re-peg count increments")
	assert.Len(t, broker.cancelCalls, 1, "broker cancel invoked exactly once")
	assert.True(t, pos.ExitPending, "re-peg leaves a new pending exit behind")
}

func TestHandleExitTimeout_OptionStop_SingleRepegThenEscalate(t *testing.T) {
	broker := &trackingBroker{detailsStatus: "canceled"}
	svc := newTestServiceWithBrokerAndRepo(&broker.mockBroker, &capturingRepo{})
	// Replace the broker pointer on the service — newTestServiceWithBrokerAndRepo
	// takes *mockBroker but we need the tracking subclass for CancelOrder.
	svc.broker = broker

	pos := seedOptionPendingExit(t, svc, "AAPL_OPT_S", domain.ExitRuleTrailingStop, time.Second)
	pos.ExitWallStartedAt = pos.ExitPendingAt

	// First tick: stop-category gets 1 re-peg.
	svc.tick()
	assert.Equal(t, 1, pos.ExitRepegCount)
	assert.Equal(t, 0, pos.ExitRetryCount)

	// Prepare for second tick: the re-peg call to triggerExit set a new
	// ExitPending/ExitPendingAt. Age it past the timeout again.
	pos.ExitOrderID = "live-order-2"
	pos.ExitPendingAt = svc.nowFunc().Add(-exitTimeoutForPos(pos) - time.Second)

	svc.tick()
	// Budget exhausted → escalate. ExitRetryCount++, re-peg count reset.
	assert.Equal(t, 1, pos.ExitRetryCount)
	assert.Equal(t, 0, pos.ExitRepegCount)
	assert.Equal(t, 2, len(broker.cancelCalls))
}

func TestHandleExitTimeout_WallTimeOverride_EscalatesImmediately(t *testing.T) {
	broker := &trackingBroker{detailsStatus: "canceled"}
	svc := newTestServiceWithBrokerAndRepo(&broker.mockBroker, &capturingRepo{})
	svc.broker = broker

	pos := seedOptionPendingExit(t, svc, "AAPL_OPT_W", domain.ExitRulePremiumTarget, time.Second)
	// Pin wall start past the ceiling so even with budget remaining we
	// short-circuit to escalation.
	pos.ExitWallStartedAt = svc.nowFunc().Add(-2 * exitMaxWallTime)

	svc.tick()

	assert.Equal(t, 0, pos.ExitRepegCount, "wall-time override skips re-peg")
	assert.Equal(t, 1, pos.ExitRetryCount, "escalate path bumps retry count")
}

func TestHandleExitTimeout_CancelReturnsFilled_ReconcilesPosition(t *testing.T) {
	broker := &trackingBroker{
		cancelReturn:  fmt.Errorf("cancel rejected: order already in filled state"),
		detailsResult: ports.OrderDetails{FilledQty: 5, FilledAvgPrice: 1.25},
	}
	repo := &capturingRepo{}
	svc := newTestServiceWithBrokerAndRepo(&broker.mockBroker, repo)
	svc.broker = broker

	_ = seedOptionPendingExit(t, svc, "AAPL_OPT_F", domain.ExitRulePremiumTarget, time.Second)

	svc.tick()

	assert.Equal(t, 0, svc.PositionCount(), "filled-race should reconcile and remove pos")
	require.Len(t, repo.savedTrades, 1)
	assert.Equal(t, "FILLED", repo.savedTrades[0].Status)
}

func TestHandleExitTimeout_EquityUnchanged_StillEscalates(t *testing.T) {
	broker := &trackingBroker{detailsStatus: "canceled"}
	svc := newTestServiceWithBrokerAndRepo(&broker.mockBroker, &capturingRepo{})
	svc.broker = broker

	// Equity asset class, stop rule — budget is 0, escalate on first timeout.
	now := svc.nowFunc()
	svc.processFill(fillMsg{
		Symbol:     "F",
		Side:       "BUY",
		Price:      12.14,
		Quantity:   10,
		FilledAt:   now.Add(-10 * time.Minute),
		Strategy:   "test",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{{Type: domain.ExitRuleTrailingStop, Params: map[string]float64{}}},
	})
	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, "F")
	pos := svc.positions[key]
	require.NotNil(t, pos)
	pos.ExitPending = true
	pos.ExitOrderID = "eq-1"
	pos.ExitPendingAt = now.Add(-exitPendingTimeoutEquity - time.Second)

	svc.tick()

	assert.Equal(t, 0, pos.ExitRepegCount, "equity never re-pegs")
	assert.Equal(t, 1, pos.ExitRetryCount, "equity escalates on first timeout")
	// Single-ExitPending invariant (2026-04-16): ExitPending stays true
	// throughout re-peg/escalate so concurrent tick-loop rule evaluation
	// cannot race a parallel CLOSE_LONG. The broker order id was cleared
	// as the observable "old order terminated" signal.
	assert.True(t, pos.ExitPending, "ExitPending stays true under new invariant")
	assert.Empty(t, pos.ExitOrderID, "old broker order id cleared after terminal")
}

func TestHandleExitTimeout_TimeoutAsymmetric(t *testing.T) {
	cases := []struct {
		name    string
		inst    domain.InstrumentType
		rule    domain.ExitRuleType
		wantDur time.Duration
	}{
		{"equity stop", "", domain.ExitRuleTrailingStop, exitPendingTimeoutEquity},
		{"equity target", "", domain.ExitRuleProfitTarget, exitPendingTimeoutEquity},
		{"option stop", domain.InstrumentTypeOption, domain.ExitRuleTrailingStop, exitPendingTimeoutOptionStop},
		{"option target", domain.InstrumentTypeOption, domain.ExitRulePremiumTarget, exitPendingTimeoutOptionTarget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pos := &domain.MonitoredPosition{
				InstrumentType: tc.inst,
				ExitRules: []domain.ExitRule{
					{Type: tc.rule, Params: map[string]float64{}},
				},
			}
			assert.Equal(t, tc.wantDur, exitTimeoutForPos(pos))
		})
	}
}

func TestHandleExitTimeout_RepegBudget(t *testing.T) {
	cases := []struct {
		name string
		inst domain.InstrumentType
		rule domain.ExitRuleType
		want int
	}{
		{"equity", "", domain.ExitRuleTrailingStop, 0},
		{"option stop", domain.InstrumentTypeOption, domain.ExitRuleTrailingStop, exitMaxRepegsStop},
		{"option target", domain.InstrumentTypeOption, domain.ExitRulePremiumTarget, exitMaxRepegsTarget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pos := &domain.MonitoredPosition{
				InstrumentType: tc.inst,
				ExitRules: []domain.ExitRule{
					{Type: tc.rule, Params: map[string]float64{}},
				},
			}
			assert.Equal(t, tc.want, exitRepegBudget(pos))
		})
	}
}

// ---------------------------------------------------------------------------
// SOFI phantom-short regression guards (2026-04-16).
// ---------------------------------------------------------------------------

// recordingRepegNotifier captures the broker order id handed to
// MarkRepegCancel so tests can assert ordering: cancel MUST be preceded by
// a MarkRepegCancel for the same order id.
type recordingRepegNotifier struct {
	calls []string
	// cancelHook fires when the test's fake broker receives the cancel,
	// so we can assert MarkRepegCancel happened BEFORE the cancel.
	orderSeen map[string]int // id → call ordinal
}

func (n *recordingRepegNotifier) MarkRepegCancel(brokerOrderID string) bool {
	n.calls = append(n.calls, brokerOrderID)
	if n.orderSeen == nil {
		n.orderSeen = make(map[string]int)
	}
	n.orderSeen[brokerOrderID] = len(n.calls)
	return true
}

// orderedCancelBroker remembers the ordinal at which each cancel arrived.
// Pairing with recordingRepegNotifier lets us assert that MarkRepegCancel
// occurred BEFORE CancelOrder for the same broker order id.
type orderedCancelBroker struct {
	trackingBroker
	notifier *recordingRepegNotifier
	// cancelOrdinal[id] = index at which CancelOrder for this id was
	// called relative to notifier.calls length at that moment.
	cancelOrdinal map[string]int
}

func (b *orderedCancelBroker) CancelOrder(ctx context.Context, orderID string) error {
	if b.cancelOrdinal == nil {
		b.cancelOrdinal = make(map[string]int)
	}
	// Record how many MarkRepegCancel calls the notifier had seen when
	// the cancel arrived. Expected: >= 1 for the cancel's order id.
	if b.notifier != nil {
		b.cancelOrdinal[orderID] = len(b.notifier.calls)
	}
	return b.trackingBroker.CancelOrder(ctx, orderID)
}

func TestHandleExitTimeout_CallsMarkRepegCancelBeforeCancel(t *testing.T) {
	notifier := &recordingRepegNotifier{}
	broker := &orderedCancelBroker{
		trackingBroker: trackingBroker{detailsStatus: "canceled"},
		notifier:       notifier,
	}
	svc := newTestServiceWithBrokerAndRepo(&broker.trackingBroker.mockBroker, &capturingRepo{})
	svc.broker = broker
	svc.SetRepegNotifier(notifier)

	pos := seedOptionPendingExit(t, svc, "AAPL_OPT_MR", domain.ExitRulePremiumTarget, time.Second)
	pos.ExitWallStartedAt = pos.ExitPendingAt

	svc.tick()

	// MarkRepegCancel must have been called with the exact broker order id
	// that subsequently got canceled.
	require.Contains(t, notifier.calls, "live-order-1")
	// And it must have been called BEFORE the cancel arrived — i.e.,
	// the notifier had recorded the call by the time the broker saw it.
	assert.GreaterOrEqual(t, broker.cancelOrdinal["live-order-1"], 1,
		"MarkRepegCancel must precede CancelOrder for the same broker order id")
}

// TestHandleExitTimeout_SingleExitPendingInvariant verifies ExitPending
// stays true across the cancel+resubmit cycle. This is the load-bearing
// guarantee that stops the tick loop from evaluating other exit rules
// (e.g. STAGNATION_EXIT) and emitting a parallel CLOSE_LONG — which was
// the SOFI 1605 bug on 2026-04-16.
func TestHandleExitTimeout_SingleExitPendingInvariant(t *testing.T) {
	broker := &trackingBroker{detailsStatus: "canceled"}
	notifier := &recordingRepegNotifier{}
	svc := newTestServiceWithBrokerAndRepo(&broker.mockBroker, &capturingRepo{})
	svc.broker = broker
	svc.SetRepegNotifier(notifier)

	pos := seedOptionPendingExit(t, svc, "AAPL_OPT_INV", domain.ExitRulePremiumTarget, time.Second)
	pos.ExitWallStartedAt = pos.ExitPendingAt

	svc.tick()

	// After a re-peg cycle: ExitPending must still be true. If it were ever
	// flipped to false between cancel-terminal and the re-emitted trigger,
	// the tick loop could fire a parallel exit rule in the gap.
	assert.True(t, pos.ExitPending,
		"ExitPending must stay true across re-peg cycle (single-invariant, SOFI fix)")
	// The old broker order id was cleared because we canceled it; the new
	// one will be populated by processExitSubmitted when the re-emitted
	// intent is acked. In this test no event bus is wired to that flow,
	// so we assert the empty-id intermediate state.
	assert.Empty(t, pos.ExitOrderID)
}

// TestHandleExitTimeout_ExitRuleEvalSkippedWhilePending verifies that the
// tick loop's price/time exit-rule evaluation does NOT fire while a
// pending exit is in progress. This is the tick-loop side of the
// single-ExitPending invariant — the complementary guarantee to the
// handleExitTimeout side. If a STAGNATION_EXIT rule would normally
// trigger on this position, the tick must short-circuit on ExitPending
// BEFORE evaluating rules, emitting no outbox message.
func TestHandleExitTimeout_ExitRuleEvalSkippedWhilePending(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	now := time.Date(2026, 4, 16, 17, 10, 0, 0, time.UTC)
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return now }))

	// Seed a position with a rule that WOULD fire in normal conditions
	// (max_loss at -0.01%). Then mark it ExitPending so the tick's
	// ExitPending guard should short-circuit BEFORE rule evaluation.
	svc.processFill(fillMsg{
		Symbol:     "SOFI",
		Side:       "BUY",
		Price:      20.0,
		Quantity:   19,
		FilledAt:   now.Add(-1 * time.Hour),
		Strategy:   "macd",
		AssetClass: domain.AssetClassEquity,
		ExitRules: []domain.ExitRule{
			{Type: domain.ExitRuleMaxLoss, Params: map[string]float64{"pct": 0.0001}},
		},
	})
	pos := svc.positions["tenant-1:Paper:SOFI"]
	require.NotNil(t, pos)
	pos.ExitPending = true
	pos.ExitOrderID = "sofi-limit-1603"
	// ExitPendingAt set to NOW so timeout does NOT fire — simulating the
	// ordinary "exit in progress" tick where the concurrent rule would
	// have raced the cancel+resubmit under the old broken flow.
	pos.ExitPendingAt = now

	// Push a bar that would blow through MAX_LOSS if evaluated.
	svc.priceCache.UpdatePrice("SOFI", 1.0, now) // -95% move

	beforePublished := bus.totalPublished()
	svc.tick()
	afterPublished := bus.totalPublished()

	// No new intent must have been published — the ExitPending guard
	// prevents MAX_LOSS evaluation while the exit is already in flight.
	assert.Equal(t, beforePublished, afterPublished,
		"tick loop must not publish a parallel exit intent while ExitPending is true")
}

// ---------------------------------------------------------------------------
// Parallel-working-exits guards (2026-04-21 HIMS/XOM regression).
// ---------------------------------------------------------------------------

// TestEscalateToMarket_CancelsAllWorkingExits verifies that the escalate
// branch of handleExitTimeout cancels every broker-working exit order
// recorded in PendingExitOrderIDs, not just the one tracked in
// ExitOrderID. The HIMS/XOM bug was caused by EOD_FLATTEN + escalate
// submitting parallel SELL orders because the single-slot ExitOrderID
// was stale when escalate fired.
func TestEscalateToMarket_CancelsAllWorkingExits(t *testing.T) {
	broker := &trackingBroker{detailsStatus: "canceled"}
	svc := newTestServiceWithBrokerAndRepo(&broker.mockBroker, &capturingRepo{})
	svc.broker = broker

	// Equity stop → budget 0 → first timeout escalates (no re-peg).
	now := svc.nowFunc()
	svc.processFill(fillMsg{
		Symbol:     "HIMS",
		Side:       "BUY",
		Price:      30.0,
		Quantity:   13,
		FilledAt:   now.Add(-10 * time.Minute),
		Strategy:   "macd",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{{Type: domain.ExitRuleTrailingStop, Params: map[string]float64{}}},
	})
	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, "HIMS")
	pos := svc.positions[key]
	require.NotNil(t, pos)
	// Two parallel working exits: the primary (tracked) and a peer
	// (e.g. an EOD_FLATTEN submitted in the gap after an unsolicited
	// broker cancel of the primary limit).
	pos.ExitPending = true
	pos.ExitOrderID = "primary-limit"
	pos.PendingExitOrderIDs = map[string]struct{}{
		"primary-limit":  {},
		"peer-eod-order": {},
	}
	pos.ExitPendingAt = now.Add(-exitPendingTimeoutEquity - time.Second)

	svc.tick()

	// Both ids must have been canceled at the broker. The primary-limit
	// may appear twice (once from handleExitTimeout's primary cancel, once
	// from the peer sweep since no event bus is wired here to drain the
	// set between). That's acceptable — idempotent cancel-then-resubmit is
	// the whole point. What matters: the peer was not left working.
	assert.Contains(t, broker.cancelCalls, "primary-limit",
		"primary limit must be canceled")
	assert.Contains(t, broker.cancelCalls, "peer-eod-order",
		"escalate must cancel every working exit — not just the tracked one")
}

// TestEscalateToMarket_PeerCancelledWhenPrimaryAlreadyTerminal pins the
// exact 2026-04-21 HIMS/XOM scenario: ExitOrderID points to a stale
// (already-terminal) primary limit and the authoritative working order
// is only tracked in PendingExitOrderIDs. The new sweep path is the only
// thing that can catch the live peer — cancelAndAwaitTerminal on the
// stale primary is a no-op. Guards against a regression that would make
// cancelAllPendingExits skip ids already handled via the tracked slot.
func TestEscalateToMarket_PeerCancelledWhenPrimaryAlreadyTerminal(t *testing.T) {
	broker := &trackingBroker{detailsStatus: "canceled"}
	svc := newTestServiceWithBrokerAndRepo(&broker.mockBroker, &capturingRepo{})
	svc.broker = broker

	now := svc.nowFunc()
	svc.processFill(fillMsg{
		Symbol:     "HIMS",
		Side:       "BUY",
		Price:      30.0,
		Quantity:   13,
		FilledAt:   now.Add(-10 * time.Minute),
		Strategy:   "macd",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{{Type: domain.ExitRuleTrailingStop, Params: map[string]float64{}}},
	})
	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, "HIMS")
	pos := svc.positions[key]
	require.NotNil(t, pos)
	// Incident-exact: ExitOrderID points to a terminal primary (its entry
	// was drained from PendingExitOrderIDs when processExitTerminal saw it),
	// and the EOD_FLATTEN peer is the only broker-working exit.
	pos.ExitPending = true
	pos.ExitOrderID = "stale-terminal-primary"
	pos.PendingExitOrderIDs = map[string]struct{}{
		"working-eod-peer": {},
	}
	pos.ExitPendingAt = now.Add(-exitPendingTimeoutEquity - time.Second)

	svc.tick()

	assert.Contains(t, broker.cancelCalls, "working-eod-peer",
		"sweep must cancel peers even when ExitOrderID points to a terminal primary")
}

// TestProcessExitTerminal_WithRemainingPeers_KeepsExitPending verifies
// that when a terminal event arrives for one of several working exits,
// ExitPending stays true and the remaining peers are canceled. Without
// this, an unsolicited broker cancel for order A would clear
// ExitPending while order B is still working — leaving the tick loop
// free to fire a third rule.
func TestProcessExitTerminal_WithRemainingPeers_KeepsExitPending(t *testing.T) {
	broker := &trackingBroker{detailsStatus: "canceled"}
	svc := newTestServiceWithBrokerAndRepo(&broker.mockBroker, &capturingRepo{})
	svc.broker = broker

	svc.processFill(fillMsg{
		Symbol:     "XOM",
		Side:       "BUY",
		Price:      148.0,
		Quantity:   8,
		FilledAt:   time.Now().Add(-10 * time.Minute),
		Strategy:   "macd",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})
	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, "XOM")
	pos := svc.positions[key]
	require.NotNil(t, pos)
	pos.ExitPending = true
	pos.ExitOrderID = "order-A"
	pos.PendingExitOrderIDs = map[string]struct{}{
		"order-A": {},
		"order-B": {},
	}

	svc.processExitTerminal(exitOrderTerminalMsg{
		Symbol:        "XOM",
		BrokerOrderID: "order-A",
	})

	assert.True(t, pos.ExitPending, "ExitPending must stay true while peer exit still working")
	assert.NotContains(t, pos.PendingExitOrderIDs, "order-A", "terminal order drained from set")
	assert.Contains(t, pos.PendingExitOrderIDs, "order-B", "peer retained until its own terminal")
	assert.Contains(t, broker.cancelCalls, "order-B",
		"peer must be canceled when terminal arrives for its sibling")
}

// TestProcessExitTerminal_LastPeer_ClearsExitPending verifies the normal
// single-exit path still clears state: when the last entry in
// PendingExitOrderIDs goes terminal, ExitPending/ExitOrderID reset so
// the tick loop can re-evaluate exits on the next pass.
func TestProcessExitTerminal_LastPeer_ClearsExitPending(t *testing.T) {
	broker := &trackingBroker{detailsStatus: "canceled"}
	svc := newTestServiceWithBrokerAndRepo(&broker.mockBroker, &capturingRepo{})
	svc.broker = broker

	svc.processFill(fillMsg{
		Symbol:     "XOM",
		Side:       "BUY",
		Price:      148.0,
		Quantity:   8,
		FilledAt:   time.Now().Add(-10 * time.Minute),
		Strategy:   "macd",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})
	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, "XOM")
	pos := svc.positions[key]
	require.NotNil(t, pos)
	pos.ExitPending = true
	pos.ExitOrderID = "order-A"
	pos.PendingExitOrderIDs = map[string]struct{}{
		"order-A": {},
		"order-B": {},
	}

	// First terminal: peer remains, ExitPending stays true.
	svc.processExitTerminal(exitOrderTerminalMsg{
		Symbol:        "XOM",
		BrokerOrderID: "order-A",
	})
	require.True(t, pos.ExitPending)

	// With the peer canceled via the cancelAllPendingExits path above,
	// processExitTerminal will be re-invoked by the broker for order-B.
	// Simulate that terminal arriving. ExitOrderID still tracks "order-A"
	// because handleExitTimeout never ran; adjust ExitOrderID to match
	// the last working order so the terminal matches the tracked slot.
	pos.ExitOrderID = "order-B"

	svc.processExitTerminal(exitOrderTerminalMsg{
		Symbol:        "XOM",
		BrokerOrderID: "order-B",
	})

	assert.False(t, pos.ExitPending, "ExitPending cleared when last peer goes terminal")
	assert.Empty(t, pos.ExitOrderID)
	assert.Empty(t, pos.PendingExitOrderIDs)
}

func drainOutbox(svc *Service) {
	for len(svc.outbox) > 0 {
		<-svc.outbox
	}
}

// TestTriggerExit_SuppressedWhilePriorInFlight verifies cross-reason exit
// arbitration: a rule-driven triggerExit that fires while a prior exit is
// still working at the broker (ExitPending=true AND PendingExitOrderIDs
// non-empty) must be dropped without emitting a new intent. The motivating
// case is EOD_FLATTEN firing after an unsolicited broker cancel stamped
// ExitPending=false only transiently — the original limit may still be
// live at the broker, so emitting the EOD market order produces a double
// exit (duplicate SELL).
func TestTriggerExit_SuppressedWhilePriorInFlight(t *testing.T) {
	broker := &mockBroker{}
	svc := newTestServiceWithBrokerAndRepo(broker, &capturingRepo{})

	now := svc.nowFunc()
	svc.processFill(fillMsg{
		Symbol:     "XOM",
		Side:       "BUY",
		Price:      148.0,
		Quantity:   8,
		FilledAt:   now.Add(-10 * time.Minute),
		Strategy:   "macd",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})
	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, "XOM")
	pos := svc.positions[key]
	require.NotNil(t, pos)

	// Simulate a prior PREMIUM_TRAIL exit working at the broker.
	pos.ExitPending = true
	pos.ExitOrderID = "premium-trail-order"
	pos.PendingExitOrderIDs = map[string]struct{}{
		"premium-trail-order": {},
	}

	drainOutbox(svc)

	// A new rule-driven trigger (EOD_FLATTEN) while the prior is in flight.
	rule := mustExitRule(t, domain.ExitRuleEODFlatten, map[string]float64{"minutes_before_close": 5})
	svc.triggerExit(pos, rule, "EOD flatten: 5 min before session close", 149.0, now)

	assert.Equal(t, 0, len(svc.outbox),
		"rule-driven trigger must be suppressed while a prior exit is in flight")
	assert.Equal(t, "premium-trail-order", pos.ExitOrderID,
		"ExitOrderID must not be mutated by a suppressed trigger")
}

// TestTriggerExit_RetryReasonsAllowed verifies the retry allowlist: re-peg
// and market-escalate triggers — which are owned by handleExitTimeout and
// always paired with cancelAndAwaitTerminal on the prior order — must NOT
// be suppressed even when ExitPending=true and PendingExitOrderIDs is
// non-empty (there is still a stale id between cancel and resubmit).
func TestTriggerExit_RetryReasonsAllowed(t *testing.T) {
	broker := &mockBroker{}
	svc := newTestServiceWithBrokerAndRepo(broker, &capturingRepo{})

	now := svc.nowFunc()
	svc.processFill(fillMsg{
		Symbol:     "XOM",
		Side:       "BUY",
		Price:      148.0,
		Quantity:   8,
		FilledAt:   now.Add(-10 * time.Minute),
		Strategy:   "macd",
		AssetClass: domain.AssetClassEquity,
		ExitRules: []domain.ExitRule{
			mustExitRule(t, domain.ExitRuleTrailingStop, map[string]float64{}),
		},
	})
	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, "XOM")
	pos := svc.positions[key]
	require.NotNil(t, pos)
	pos.ExitPending = true
	pos.PendingExitOrderIDs = map[string]struct{}{"stale-limit": {}}

	drainOutbox(svc)

	rule := pos.ExitRules[0]
	svc.triggerExit(pos, rule, "escalate-to-market", 148.0, now)
	assert.Equal(t, 1, len(svc.outbox), "escalate-to-market retry must emit an intent")

	drainOutbox(svc)
	svc.triggerExit(pos, rule, "repeg 1/3", 148.0, now)
	assert.Equal(t, 1, len(svc.outbox), "repeg retry must emit an intent")
}
