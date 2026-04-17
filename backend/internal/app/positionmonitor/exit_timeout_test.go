package positionmonitor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
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
	assert.False(t, pos.ExitPending, "ExitPending cleared for retry")
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
	assert.False(t, pos.ExitPending)
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
	assert.False(t, pos.ExitPending, "ExitPending cleared after terminal confirm")
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
