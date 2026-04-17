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
	cancelCalls    []string
	detailsCalls   int
	detailsStatus  string
	cancelReturn   error
	detailsResult  ports.OrderDetails
	detailsErr     error
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
