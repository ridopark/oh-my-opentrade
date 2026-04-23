package positionmonitor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCopytradeExitService() *Service {
	broker := &mockBroker{}
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(broker, zerolog.Nop())
	return NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithBroker(broker),
	)
}

// seedOptionPosition injects a long-call option position that the copytrade
// exit handler will attempt to close.
func seedOptionPosition(t *testing.T, svc *Service, contract domain.Symbol, qty float64) *domain.MonitoredPosition {
	t.Helper()
	pos := &domain.MonitoredPosition{
		TenantID:       svc.tenantID,
		EnvMode:        svc.envMode,
		Symbol:         contract,
		InstrumentType: domain.InstrumentTypeOption,
		OptionRight:    "CALL",
		Strategy:       "copytrade_v1",
		Quantity:       qty,
		EntryPrice:     1.20,
		EntryTime:      time.Now().Add(-5 * time.Minute),
		Side:           "BUY",
		ExitRules:      []domain.ExitRule{},
		CustomState:    map[string]float64{},
	}
	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, contract)
	svc.positions[key] = pos
	return pos
}

func TestHandleCopytradeExitRequest_Partial_TriggersExitAndMarksState(t *testing.T) {
	svc := newCopytradeExitService()
	contract := domain.Symbol("AAPL260425C00190000")
	pos := seedOptionPosition(t, svc, contract, 10)

	payload := domain.CopytradeExitRequestPayload{
		TenantID:       svc.tenantID,
		EnvMode:        string(svc.envMode),
		Strategy:       "copytrade_v1",
		Symbol:         "AAPL",
		ContractSymbol: string(contract),
		Fraction:       0.5,
		Reason:         "half out",
	}
	evt := domain.Event{
		Type:    domain.EventCopytradeExitRequest,
		Payload: payload,
	}

	require.NoError(t, svc.handleCopytradeExitRequest(context.Background(), evt))

	// triggerExit pushes the intent onto s.outbox for the outbox goroutine
	// to publish; in a unit test without that goroutine, ExitPending + a
	// queued outbox entry are the observable signal.
	assert.True(t, pos.ExitPending, "triggerExit should have flipped ExitPending")
	assert.GreaterOrEqual(t, len(svc.outbox), 1, "partial exit should queue an intent in the outbox")
}

func TestHandleCopytradeExitRequest_FullClose_FractionOneAccepted(t *testing.T) {
	svc := newCopytradeExitService()
	contract := domain.Symbol("TSLA260425P00250000")
	pos := seedOptionPosition(t, svc, contract, 8)

	payload := domain.CopytradeExitRequestPayload{
		TenantID:       svc.tenantID,
		EnvMode:        string(svc.envMode),
		Strategy:       "copytrade_v1",
		Symbol:         "TSLA",
		ContractSymbol: string(contract),
		Fraction:       1.0,
		Reason:         "all out",
	}
	evt := domain.Event{
		Type:    domain.EventCopytradeExitRequest,
		Payload: payload,
	}

	require.NoError(t, svc.handleCopytradeExitRequest(context.Background(), evt))
	assert.True(t, pos.ExitPending)
	// Fraction=1.0 should be deleted by the fracKey loop (it only retains
	// strictly-partial fractions). Either way the CustomState entry should
	// not leave a stale partial value for downstream rules.
	_, hasFrac := pos.CustomState["copytrade_exit_qty_frac"]
	assert.False(t, hasFrac, "1.0 fraction should be consumed/deleted by triggerExit")
}

func TestHandleCopytradeExitRequest_NoMatchingPosition_NoOp(t *testing.T) {
	svc := newCopytradeExitService()
	// No positions seeded.
	payload := domain.CopytradeExitRequestPayload{
		TenantID:       svc.tenantID,
		EnvMode:        string(svc.envMode),
		Strategy:       "copytrade_v1",
		Symbol:         "AAPL",
		ContractSymbol: "AAPL260425C00190000",
		Fraction:       0.5,
		Reason:         "half out",
	}
	err := svc.handleCopytradeExitRequest(context.Background(), domain.Event{
		Type:    domain.EventCopytradeExitRequest,
		Payload: payload,
	})
	require.NoError(t, err)
	assert.Empty(t, svc.outbox, "missing position path must not queue any intents")
}

func TestHandleCopytradeExitRequest_InvalidFractionRejected(t *testing.T) {
	svc := newCopytradeExitService()
	contract := domain.Symbol("AAPL260425C00190000")
	pos := seedOptionPosition(t, svc, contract, 10)

	for _, frac := range []float64{0, -0.1, 1.5, 2.0} {
		t.Run(fmt.Sprintf("frac=%g", frac), func(t *testing.T) {
			payload := domain.CopytradeExitRequestPayload{
				TenantID:       svc.tenantID,
				EnvMode:        string(svc.envMode),
				Strategy:       "copytrade_v1",
				Symbol:         "AAPL",
				ContractSymbol: string(contract),
				Fraction:       frac,
				Reason:         "bad",
			}
			err := svc.handleCopytradeExitRequest(context.Background(), domain.Event{
				Type:    domain.EventCopytradeExitRequest,
				Payload: payload,
			})
			require.NoError(t, err)
			assert.False(t, pos.ExitPending, "out-of-range fraction must not trigger exit")
		})
		// Reset for next iteration.
		pos.ExitPending = false
	}
}

func TestHandleCopytradeExitRequest_TenantMismatch_SkipsPosition(t *testing.T) {
	svc := newCopytradeExitService()
	contract := domain.Symbol("AAPL260425C00190000")
	pos := seedOptionPosition(t, svc, contract, 10)

	payload := domain.CopytradeExitRequestPayload{
		TenantID:       "other-tenant",
		EnvMode:        string(svc.envMode),
		Strategy:       "copytrade_v1",
		Symbol:         "AAPL",
		ContractSymbol: string(contract),
		Fraction:       0.5,
		Reason:         "half out",
	}
	err := svc.handleCopytradeExitRequest(context.Background(), domain.Event{
		Type:    domain.EventCopytradeExitRequest,
		Payload: payload,
	})
	require.NoError(t, err)
	assert.False(t, pos.ExitPending, "tenant mismatch must not trigger exit on this position")
}
