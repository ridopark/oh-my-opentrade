package execution_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExitIntent_PreservesPartialQuantity is the regression test for the
// 2026-05-11 bug where execution/service.go unconditionally stomped every
// exit intent's quantity to the broker's full position quantity, defeating
// the partial sizing done upstream (tiered_tp / time_partial / copytrade
// STC). Verified previously by inspecting fills.csv showing SELL 50 for a
// 0.33 "partial" STC on a 50-contract position.
func TestExitIntent_PreservesPartialQuantity(t *testing.T) {
	cases := []struct {
		name        string
		intentQty   float64
		brokerQty   float64
		wantSubmit  float64
		wantRejects bool
	}{
		{name: "deliberate_partial_preserved", intentQty: 17, brokerQty: 50, wantSubmit: 17},
		{name: "full_close_passthrough", intentQty: 50, brokerQty: 50, wantSubmit: 50},
		{name: "oversize_clamped_to_broker", intentQty: 51, brokerQty: 50, wantSubmit: 50},
		{name: "drift_down_fits", intentQty: 17, brokerQty: 33, wantSubmit: 17},
		{name: "drift_down_clamps", intentQty: 17, brokerQty: 10, wantSubmit: 10},
		{name: "zero_qty_rejected", intentQty: 0, brokerQty: 50, wantSubmit: 0, wantRejects: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, bus, broker, _ := setupTestService(t)

			var mu sync.Mutex
			var submitted []domain.OrderIntent
			broker.SubmitOrderFunc = func(_ context.Context, intent domain.OrderIntent) (string, error) {
				mu.Lock()
				submitted = append(submitted, intent)
				mu.Unlock()
				return "order-test", nil
			}
			brokerQty := tc.brokerQty
			broker.GetPositionsFunc = func(_ context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
				trade, _ := domain.NewTrade(
					time.Now(), tenantID, envMode, uuid.New(),
					"BTCUSD", "long", brokerQty, 50000, 0, "FILLED", "strategy-1", "test",
				)
				return []domain.Trade{trade}, nil
			}

			require.NoError(t, svc.Start(context.Background(), "test", domain.EnvModePaper))

			var rejected []domain.OrderIntentEventPayload
			err := bus.Subscribe(context.Background(), domain.EventOrderIntentRejected, func(_ context.Context, ev domain.Event) error {
				if p, ok := ev.Payload.(domain.OrderIntentEventPayload); ok {
					mu.Lock()
					rejected = append(rejected, p)
					mu.Unlock()
				}
				return nil
			})
			require.NoError(t, err)

			intentID := uuid.New()
			intent, ierr := domain.NewOrderIntent(
				intentID, "tenant-1", domain.EnvModePaper, "BTCUSD",
				domain.DirectionCloseLong,
				50000.0, 49000.0, 10, tc.intentQty,
				"strategy-1", "partial exit test", 0.8, intentID.String(),
			)
			require.NoError(t, ierr)

			evt, eerr := domain.NewEvent(
				domain.EventOrderIntentCreated, "tenant-1", domain.EnvModePaper,
				intentID.String(), intent,
			)
			require.NoError(t, eerr)

			require.NoError(t, bus.Publish(context.Background(), *evt))
			bus.Flush()

			mu.Lock()
			defer mu.Unlock()

			if tc.wantRejects {
				assert.Empty(t, submitted, "expected no broker submission")
				require.NotEmpty(t, rejected, "expected an OrderIntentRejected event")
				assert.Contains(t, rejected[0].Reason, "partial_qty_invalid")
				return
			}

			require.Len(t, submitted, 1, "expected exactly one broker submission")
			assert.InDelta(t, tc.wantSubmit, submitted[0].Quantity, 1e-9, "broker received wrong qty")
		})
	}
}
