package positionmonitor

import (
	"fmt"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProcessFill_PartialFill_ClearsExitPendingOnTerminal exercises the
// terminal-fill cleanup when a deliberately-partial exit intent (e.g.
// copytrade STC at frac<1.0) fills its full intent quantity but leaves
// the position non-zero. Pre-fix processFill kept ExitPending=true on
// the partial-else branch under the assumption the broker order was
// still working — wrong when the broker order was sized to match the
// intent qty, not the full position. Symptom: subsequent STC keywords
// hit `prior exit in flight — rejecting` until the escalate-ladder
// closed the position via market.
//
// Single-ExitPending invariant (SOFI-1605, 2026-04-16): when peer exit
// orders are still working on the same position, ExitPending must NOT
// be cleared on the terminal fill of one peer — the tick loop's
// ExitPending guard is the only thing blocking a third order from
// firing in the gap.
func TestProcessFill_PartialFill_ClearsExitPendingOnTerminal(t *testing.T) {
	contract := domain.Symbol("AAPL260425C00190000")

	tests := []struct {
		name              string
		startQty          float64
		exitOrderID       string
		pendingIDs        map[string]struct{}
		fillBrokerOrderID string
		fillQty           float64
		// expectations
		wantPositionDeleted bool
		wantExitPending     bool
		wantExitOrderID     string
		wantPendingIDs      map[string]struct{}
		wantQty             float64
		wantGateCleared     bool
	}{
		{
			name:                "partial_fill_of_partial_intent_no_peer",
			startQty:            50,
			exitOrderID:         "ord-A",
			pendingIDs:          map[string]struct{}{"ord-A": {}},
			fillBrokerOrderID:   "ord-A",
			fillQty:             17,
			wantPositionDeleted: false,
			wantExitPending:     false,
			wantExitOrderID:     "",
			wantPendingIDs:      map[string]struct{}{},
			wantQty:             33,
			wantGateCleared:     true,
		},
		{
			name:                "partial_fill_with_peer_working",
			startQty:            50,
			exitOrderID:         "ord-A",
			pendingIDs:          map[string]struct{}{"ord-A": {}, "ord-B": {}},
			fillBrokerOrderID:   "ord-A",
			fillQty:             17,
			wantPositionDeleted: false,
			wantExitPending:     true,
			wantExitOrderID:     "ord-A",
			wantPendingIDs:      map[string]struct{}{"ord-B": {}},
			wantQty:             33,
			wantGateCleared:     false,
		},
		{
			name:                "peer_order_fill_does_not_touch_tracked_ExitOrderID",
			startQty:            50,
			exitOrderID:         "ord-A",
			pendingIDs:          map[string]struct{}{"ord-A": {}, "ord-B": {}},
			fillBrokerOrderID:   "ord-B",
			fillQty:             17,
			wantPositionDeleted: false,
			wantExitPending:     true,
			wantExitOrderID:     "ord-A",
			wantPendingIDs:      map[string]struct{}{"ord-A": {}},
			wantQty:             33,
			wantGateCleared:     false,
		},
		{
			name:                "full_close_branch_unchanged",
			startQty:            17,
			exitOrderID:         "ord-A",
			pendingIDs:          map[string]struct{}{"ord-A": {}},
			fillBrokerOrderID:   "ord-A",
			fillQty:             17,
			wantPositionDeleted: true,
			// other expectations N/A when position is deleted; full-close
			// already clears the gate via the pre-existing line at :704.
			wantGateCleared: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newCopytradeExitService()
			pos := seedOptionPosition(t, svc, contract, tc.startQty)
			pos.ExitPending = true
			pos.ExitPendingAt = time.Now()
			pos.ExitOrderID = tc.exitOrderID
			pos.PendingExitOrderIDs = make(map[string]struct{}, len(tc.pendingIDs))
			for k, v := range tc.pendingIDs {
				pos.PendingExitOrderIDs[k] = v
			}

			// Mark the gate as inflight so we can observe ClearInflightExit.
			require.True(t, svc.positionGate.TryMarkInflightExit(svc.tenantID, svc.envMode, contract),
				"precondition: gate slot must be free before test")

			svc.processFill(fillMsg{
				Symbol:        contract,
				Side:          "SELL",
				Direction:     string(domain.DirectionCloseLong),
				Price:         1.30,
				Quantity:      tc.fillQty,
				FilledAt:      time.Now(),
				Strategy:      "copytrade_v1",
				BrokerOrderID: tc.fillBrokerOrderID,
			})

			key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, contract)
			_, stillTracked := svc.positions[key]

			if tc.wantPositionDeleted {
				assert.False(t, stillTracked, "position should be removed from monitor on full close")
			} else {
				require.True(t, stillTracked, "partial fill must leave position in monitor")
				assert.Equal(t, tc.wantExitPending, pos.ExitPending, "ExitPending state mismatch")
				assert.Equal(t, tc.wantExitOrderID, pos.ExitOrderID, "ExitOrderID mismatch")
				assert.Equal(t, tc.wantPendingIDs, pos.PendingExitOrderIDs, "PendingExitOrderIDs mismatch")
				assert.InDelta(t, tc.wantQty, pos.Quantity, 1e-9, "remaining qty mismatch")
			}

			// Inverse-of-ClearInflightExit assertion: if the gate is clear,
			// TryMarkInflightExit returns true (and re-acquires the slot,
			// which is fine — the test object is throwaway).
			gateFree := svc.positionGate.TryMarkInflightExit(svc.tenantID, svc.envMode, contract)
			assert.Equal(t, tc.wantGateCleared, gateFree, "gate cleared state mismatch")
		})
	}
}

// TestRaceFillBeforeSubmitted_DedupViaRecentlyFilled exercises the
// bus-ordering race fix: in backtest the SimBroker's FillReceived can
// land BEFORE the OrderSubmitted handler set up tracking, so processFill
// must stash the broker_order_id and processExitSubmitted must skip its
// in-flight setup when the id is already marked filled.
func TestRaceFillBeforeSubmitted_DedupViaRecentlyFilled(t *testing.T) {
	contract := domain.Symbol("AMZN260202C00245000")
	svc := newCopytradeExitService()
	pos := seedOptionPosition(t, svc, contract, 5)
	// Fresh position: no ExitPending state, no tracked exit ids.
	pos.ExitPending = false
	pos.ExitOrderID = ""
	pos.PendingExitOrderIDs = nil

	// processFill arrives first for an untracked broker_order_id.
	svc.processFill(fillMsg{
		Symbol:        contract,
		Side:          "SELL",
		Direction:     string(domain.DirectionCloseLong),
		Price:         1.30,
		Quantity:      2,
		FilledAt:      time.Now(),
		Strategy:      "copytrade_v1",
		BrokerOrderID: "ord-A",
	})

	_, marked := svc.recentlyFilledOrders["ord-A"]
	require.True(t, marked, "processFill must stash untracked broker_order_id in recentlyFilledOrders")

	// processExitSubmitted arrives later for the same id.
	svc.processExitSubmitted(exitOrderSubmittedMsg{
		Symbol:        contract,
		BrokerOrderID: "ord-A",
		Direction:     string(domain.DirectionCloseLong),
	})

	assert.False(t, pos.ExitPending, "ExitPending must NOT be stamped on an already-filled order")
	assert.Equal(t, "", pos.ExitOrderID, "ExitOrderID must NOT be stamped on an already-filled order")
	_, stillMarked := svc.recentlyFilledOrders["ord-A"]
	assert.False(t, stillMarked, "processExitSubmitted must clear the recentlyFilled entry")
}

// TestRaceFillBeforeSubmitted_ClearsExitPending exercises iter-3 of the
// race fix. triggerExit sets pos.ExitPending=true at exit_eval.go:848
// before submitting the broker order. In backtest, SimBroker's
// FillReceived can land before the OrderSubmitted handler wires
// ExitOrderID/PendingExitOrderIDs, so processFill hits the "unmatched"
// branch with ExitPending=true && ExitOrderID="". Pre-iter-3 left that
// state intact — handleExitTimeout then looped on ExitPending=true with
// empty ExitOrderID and emitted "exit cancel never reached terminal"
// every bar. Iter-3 clears ExitPending and the position gate on the
// unmatched branch so the position is free for the next exit attempt.
func TestRaceFillBeforeSubmitted_ClearsExitPending(t *testing.T) {
	contract := domain.Symbol("AMZN260202C00245000")
	svc := newCopytradeExitService()
	pos := seedOptionPosition(t, svc, contract, 5)
	// Post-triggerExit state: ExitPending=true but OrderSubmitted hasn't
	// run yet so ExitOrderID is empty and PendingExitOrderIDs is nil.
	pos.ExitPending = true
	pos.ExitPendingAt = time.Now()
	pos.ExitOrderID = ""
	pos.PendingExitOrderIDs = nil

	// Mark the gate as inflight so we can observe ClearInflightExit.
	require.True(t, svc.positionGate.TryMarkInflightExit(svc.tenantID, svc.envMode, contract),
		"precondition: gate slot must be free before test")

	// FillReceived arrives before OrderSubmitted. Partial-close path
	// (qty=2 < startQty=5).
	svc.processFill(fillMsg{
		Symbol:        contract,
		Side:          "SELL",
		Direction:     string(domain.DirectionCloseLong),
		Price:         1.30,
		Quantity:      2,
		FilledAt:      time.Now(),
		Strategy:      "copytrade_v1",
		BrokerOrderID: "ord-X",
	})

	assert.False(t, pos.ExitPending, "iter-3 must clear ExitPending on unmatched-fill race")
	assert.Equal(t, "", pos.ExitOrderID, "ExitOrderID must remain empty")
	_, marked := svc.recentlyFilledOrders["ord-X"]
	assert.True(t, marked, "unmatched broker_order_id must be stashed for processExitSubmitted dedup")

	gateFree := svc.positionGate.TryMarkInflightExit(svc.tenantID, svc.envMode, contract)
	assert.True(t, gateFree, "position gate must be cleared so next exit attempt can fire")

	// processExitSubmitted arrives later for the same id and must consume
	// the dedup entry (existing iter-2 behavior).
	svc.processExitSubmitted(exitOrderSubmittedMsg{
		Symbol:        contract,
		BrokerOrderID: "ord-X",
		Direction:     string(domain.DirectionCloseLong),
	})
	_, stillMarked := svc.recentlyFilledOrders["ord-X"]
	assert.False(t, stillMarked, "processExitSubmitted must consume the recentlyFilled entry")
}

// TestNormalOrderSubmitThenFill_NoSetEntry verifies the normal lifecycle
// path leaves recentlyFilledOrders untouched: when OrderSubmitted runs
// FIRST (the natural live-broker ordering), the fill finds its id
// already tracked in PendingExitOrderIDs and takes the existing iter-1
// cleanup branch — never marking the order as recently-filled.
func TestNormalOrderSubmitThenFill_NoSetEntry(t *testing.T) {
	contract := domain.Symbol("AMZN260202C00245000")
	svc := newCopytradeExitService()
	pos := seedOptionPosition(t, svc, contract, 5)
	pos.ExitPending = false
	pos.ExitOrderID = ""
	pos.PendingExitOrderIDs = nil

	// processExitSubmitted runs first (natural live ordering).
	svc.processExitSubmitted(exitOrderSubmittedMsg{
		Symbol:        contract,
		BrokerOrderID: "ord-A",
		Direction:     string(domain.DirectionCloseLong),
	})
	require.True(t, pos.ExitPending, "processExitSubmitted should set ExitPending")
	require.Equal(t, "ord-A", pos.ExitOrderID, "processExitSubmitted should track ExitOrderID")
	require.Contains(t, pos.PendingExitOrderIDs, "ord-A", "processExitSubmitted should track the id")

	// Then the fill arrives.
	svc.processFill(fillMsg{
		Symbol:        contract,
		Side:          "SELL",
		Direction:     string(domain.DirectionCloseLong),
		Price:         1.30,
		Quantity:      2,
		FilledAt:      time.Now(),
		Strategy:      "copytrade_v1",
		BrokerOrderID: "ord-A",
	})

	// Iter-1 cleanup: tracked id is dropped, ExitPending cleared because
	// no peers remain.
	assert.False(t, pos.ExitPending, "iter-1 cleanup should clear ExitPending")
	assert.Equal(t, "", pos.ExitOrderID, "iter-1 cleanup should clear ExitOrderID")
	assert.Empty(t, svc.recentlyFilledOrders, "normal ordering must NOT add an entry to recentlyFilledOrders")
}
