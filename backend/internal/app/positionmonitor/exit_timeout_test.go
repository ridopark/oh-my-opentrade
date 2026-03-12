package positionmonitor

import (
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
		Strategy:   "avwap_v1",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})
	svc.processFill(fillMsg{
		Symbol:   domain.Symbol(symbol),
		Side:     "SELL",
		Price:    12.13,
		Quantity: partialSellQty,
		FilledAt: time.Now(),
		Strategy: "avwap_v1",
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
	assert.Equal(t, "avwap_v1", trade.Strategy)
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
