package simbroker_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/adapters/simbroker"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the FilledAt-source contract introduced when SimBroker
// stopped sourcing fill timestamps from the cached underlying bar time
// (which lags actual order-submission sim time under sharded execution)
// and started preferring intent.DecidedAt. Fallback path retained for
// callers that do not yet stamp the field.

func makeOptionIntentFor(sym string, underlying domain.Symbol, dir domain.Direction, decidedAt time.Time) domain.OrderIntent {
	inst := &domain.Instrument{
		Type:             domain.InstrumentTypeOption,
		Symbol:           domain.Symbol(sym),
		UnderlyingSymbol: underlying,
	}
	return domain.OrderIntent{
		ID:             uuid.New(),
		TenantID:       "tenant-1",
		EnvMode:        domain.EnvModePaper,
		Symbol:         domain.Symbol(sym),
		Direction:      dir,
		Quantity:       1,
		LimitPrice:     5.00,
		IdempotencyKey: "idem-" + uuid.NewString(),
		Instrument:     inst,
		DecidedAt:      decidedAt,
		Meta: map[string]string{
			"strike":           "150.0",
			"expiry":           "2026-04-10",
			"option_right":     "CALL",
			"underlying":       "AAPL",
			"premium":          "5.00",
			"entry_underlying": "150.0",
			"entry_date":       "2026-02-13",
			"iv_at_entry":      "0.30",
			"delta_at_entry":   "0.55",
		},
	}
}

func TestSubmitOrder_FilledAt_UsesIntentDecidedAt_Entry(t *testing.T) {
	b := simbroker.New(simbroker.Config{SlippageBPS: 0, DisableFillChan: true}, zerolog.Nop())

	underlying := domain.Symbol("AAPL")
	t0 := time.Date(2026, 2, 13, 19, 3, 0, 0, time.UTC)  // stale cached underlying bar
	t1 := time.Date(2026, 2, 26, 20, 48, 31, 0, time.UTC) // orchestrator-decided time
	b.UpdatePrice(underlying, 150.0, t0)

	intent := makeOptionIntentFor("AAPL260410C00150000", underlying, domain.DirectionLong, t1)

	orderID, err := b.SubmitOrder(context.Background(), intent)
	require.NoError(t, err)
	require.NotEmpty(t, orderID)

	details, err := b.GetOrderDetails(context.Background(), orderID)
	require.NoError(t, err)
	assert.Equal(t, t1, details.FilledAt,
		"entry fill timestamp must come from intent.DecidedAt, not the cached underlying bar")
}

func TestSubmitOrder_FilledAt_UsesIntentDecidedAt_Exit(t *testing.T) {
	b := simbroker.New(simbroker.Config{SlippageBPS: 0, DisableFillChan: false}, zerolog.Nop())

	underlying := domain.Symbol("AAPL")
	t0 := time.Date(2026, 2, 13, 19, 3, 0, 0, time.UTC)
	t1 := time.Date(2026, 2, 26, 20, 48, 31, 0, time.UTC)
	b.UpdatePrice(underlying, 150.0, t0)

	intent := makeOptionIntentFor("AAPL260410C00150000", underlying, domain.DirectionCloseLong, t1)
	// Exit needs no LimitPrice; computeOptionExitPrice runs off meta + underlying.
	intent.LimitPrice = 0

	fillCh, err := b.SubscribeOrderUpdates(context.Background())
	require.NoError(t, err)

	orderID, err := b.SubmitOrder(context.Background(), intent)
	require.NoError(t, err)
	require.NotEmpty(t, orderID)

	details, err := b.GetOrderDetails(context.Background(), orderID)
	require.NoError(t, err)
	assert.Equal(t, t1, details.FilledAt,
		"exit fill timestamp (simOrder.filledAt) must come from intent.DecidedAt")

	// Drain one OrderUpdate to pin the second write site (broker.go:748).
	select {
	case upd := <-fillCh:
		assert.Equal(t, t1, upd.FilledAt,
			"OrderUpdate.FilledAt must also come from intent.DecidedAt")
	case <-time.After(time.Second):
		t.Fatal("expected an OrderUpdate fill event")
	}
}

func TestSubmitOrder_FilledAt_FallbackToBarTime_Logs(t *testing.T) {
	var buf bytes.Buffer
	log := zerolog.New(&buf).Level(zerolog.DebugLevel)
	b := simbroker.New(simbroker.Config{SlippageBPS: 0, DisableFillChan: true}, log)

	sym := domain.Symbol("AAPL")
	t0 := time.Date(2026, 2, 13, 19, 3, 0, 0, time.UTC)
	b.UpdatePrice(sym, 150.0, t0)

	// Equity intent with no DecidedAt — exercises the fallback in the
	// equity branch as well as the option branch since the two share the
	// same FilledAt write sites.
	intent := domain.OrderIntent{
		ID:             uuid.New(),
		TenantID:       "tenant-1",
		EnvMode:        domain.EnvModePaper,
		Symbol:         sym,
		Direction:      domain.DirectionLong,
		Quantity:       1,
		IdempotencyKey: "idem-" + uuid.NewString(),
		// DecidedAt: zero value
	}

	orderID, err := b.SubmitOrder(context.Background(), intent)
	require.NoError(t, err)

	details, err := b.GetOrderDetails(context.Background(), orderID)
	require.NoError(t, err)
	assert.Equal(t, t0, details.FilledAt,
		"with zero DecidedAt, FilledAt must fall back to the cached bar time")
	assert.True(t, strings.Contains(buf.String(), "DecidedAt fallback to barTime"),
		"fallback path must emit a debug log so operators can count it; got log=%q", buf.String())
}
