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
