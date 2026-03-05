package execution_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- helpers ----------

func makeIntent(t *testing.T, symbol string, dir domain.Direction) domain.OrderIntent {
	t.Helper()
	intent, err := domain.NewOrderIntent(
		uuid.New(), "tenant-1", domain.EnvModePaper,
		domain.Symbol(symbol), dir,
		50000.0, 49000.0, 10, 1.0,
		"strategy-1", "test", 0.8, uuid.NewString(),
	)
	require.NoError(t, err)
	return intent
}

func makeTrade(symbol, side string, qty float64) domain.Trade {
	t, _ := domain.NewTrade(
		time.Now(), "tenant-1", domain.EnvModePaper, uuid.New(),
		domain.Symbol(symbol), side, qty, 50000, 0, "filled",
	)
	return t
}

func newGate(positions []domain.Trade) *execution.PositionGate {
	broker := &mockBroker{
		GetPositionsFunc: func(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
			return positions, nil
		},
	}
	return execution.NewPositionGate(broker, zerolog.Nop())
}

// ---------- Unit tests for PositionGate.Check ----------

func TestPositionGate_Check(t *testing.T) {
	tests := []struct {
		name      string
		positions []domain.Trade
		inflight  bool // whether to pre-mark inflight
		intent    func(t *testing.T) domain.OrderIntent
		wantErr   error
	}{
		// ---- ENTRY SCENARIOS ----
		{
			name:      "entry_LONG_no_position_allows",
			positions: nil,
			intent:    func(t *testing.T) domain.OrderIntent { return makeIntent(t, "BTCUSD", domain.DirectionLong) },
			wantErr:   nil,
		},
		{
			name:      "entry_LONG_already_long_rejects",
			positions: []domain.Trade{makeTrade("BTCUSD", "BUY", 1.0)},
			intent:    func(t *testing.T) domain.OrderIntent { return makeIntent(t, "BTCUSD", domain.DirectionLong) },
			wantErr:   execution.ErrAlreadyInPosition,
		},
		{
			name:      "entry_LONG_already_short_rejects_conflict",
			positions: []domain.Trade{makeTrade("BTCUSD", "SELL", 1.0)},
			intent:    func(t *testing.T) domain.OrderIntent { return makeIntent(t, "BTCUSD", domain.DirectionLong) },
			wantErr:   execution.ErrConflictPosition,
		},
		{
			name:      "entry_LONG_inflight_rejects",
			positions: nil,
			inflight:  true,
			intent:    func(t *testing.T) domain.OrderIntent { return makeIntent(t, "BTCUSD", domain.DirectionLong) },
			wantErr:   execution.ErrInflightEntry,
		},
		{
			name: "entry_LONG_different_symbol_allows",
			positions: []domain.Trade{
				makeTrade("ETHUSD", "BUY", 1.0),
			},
			intent:  func(t *testing.T) domain.OrderIntent { return makeIntent(t, "BTCUSD", domain.DirectionLong) },
			wantErr: nil,
		},

		// ---- EXIT SCENARIOS ----
		{
			name:      "exit_SHORT_with_long_position_allows",
			positions: []domain.Trade{makeTrade("BTCUSD", "BUY", 1.0)},
			intent:    func(t *testing.T) domain.OrderIntent { return makeIntent(t, "BTCUSD", domain.DirectionShort) },
			wantErr:   nil,
		},
		{
			name:      "exit_SHORT_no_position_rejects",
			positions: nil,
			intent:    func(t *testing.T) domain.OrderIntent { return makeIntent(t, "BTCUSD", domain.DirectionShort) },
			wantErr:   execution.ErrNoPositionToExit,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gate := newGate(tc.positions)
			intent := tc.intent(t)

			if tc.inflight {
				gate.MarkInflight(intent.TenantID, intent.EnvMode, intent.Symbol)
			}

			err := gate.Check(context.Background(), intent)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// ---------- Inflight lock tests ----------

func TestPositionGate_InflightLockAndClear(t *testing.T) {
	gate := newGate(nil) // no existing positions
	intent := makeIntent(t, "BTCUSD", domain.DirectionLong)

	// First entry should be allowed.
	require.NoError(t, gate.Check(context.Background(), intent))

	// Mark inflight → second entry should be rejected.
	gate.MarkInflight("tenant-1", domain.EnvModePaper, "BTCUSD")
	err := gate.Check(context.Background(), intent)
	require.ErrorIs(t, err, execution.ErrInflightEntry)

	// Clear inflight → entry should be allowed again.
	gate.ClearInflight("tenant-1", domain.EnvModePaper, "BTCUSD")
	require.NoError(t, gate.Check(context.Background(), intent))
}

// ---------- Broker error handling ----------

func TestPositionGate_BrokerError_RejectsConservatively(t *testing.T) {
	broker := &mockBroker{
		GetPositionsFunc: func(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
			return nil, assert.AnError
		},
	}
	gate := execution.NewPositionGate(broker, zerolog.Nop())
	intent := makeIntent(t, "BTCUSD", domain.DirectionLong)

	err := gate.Check(context.Background(), intent)
	require.Error(t, err, "should reject when broker position query fails")
}
