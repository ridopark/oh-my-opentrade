package gate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeEarningsChecker struct {
	entry *ports.EarningsEntry
	err   error
}

func (f *fakeEarningsChecker) NextEarnings(_ context.Context, _ string) (*ports.EarningsEntry, error) {
	return f.entry, f.err
}

func TestEarningsBlackoutGate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 4, 15, 14, 30, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }

	entryToday := &ports.EarningsEntry{
		Symbol:       "AAPL",
		EarningsDate: time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
		Hour:         "amc",
	}
	entryTomorrow := &ports.EarningsEntry{
		Symbol:       "AAPL",
		EarningsDate: time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC),
		Hour:         "bmo",
	}
	entryFarFuture := &ports.EarningsEntry{
		Symbol:       "AAPL",
		EarningsDate: time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		Hour:         "amc",
	}

	buildIntent := func(dir domain.Direction, strategy string) domain.OrderIntent {
		return domain.OrderIntent{
			Symbol:    "AAPL",
			Direction: dir,
			Strategy:  strategy,
		}
	}

	t.Run("nil checker passes (disabled)", func(t *testing.T) {
		g := &earningsBlackoutGate{nowFn: nowFn}
		gctx := &ExecutionGateContext{Intent: buildIntent(domain.DirectionLong, "avwap")}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("strict inside window rejects entry", func(t *testing.T) {
		g := &earningsBlackoutGate{
			checker: &fakeEarningsChecker{entry: entryToday},
			modes:   map[string]string{"avwap": "strict"},
			nowFn:   nowFn,
		}
		result := g.Check(ctx, &ExecutionGateContext{Intent: buildIntent(domain.DirectionLong, "avwap")})
		require.NotNil(t, result)
		assert.Equal(t, "earnings_blackout_gate", result.GateName)
		assert.Contains(t, result.Reason, "AAPL")
		assert.Contains(t, result.Reason, "mode=strict")
	})

	t.Run("strict tomorrow rejects (±1 day window)", func(t *testing.T) {
		g := &earningsBlackoutGate{
			checker: &fakeEarningsChecker{entry: entryTomorrow},
			modes:   map[string]string{"avwap": "strict"},
			nowFn:   nowFn,
		}
		result := g.Check(ctx, &ExecutionGateContext{Intent: buildIntent(domain.DirectionLong, "avwap")})
		require.NotNil(t, result)
	})

	t.Run("strict outside window passes", func(t *testing.T) {
		g := &earningsBlackoutGate{
			checker: &fakeEarningsChecker{entry: entryFarFuture},
			modes:   map[string]string{"avwap": "strict"},
			nowFn:   nowFn,
		}
		assert.Nil(t, g.Check(ctx, &ExecutionGateContext{Intent: buildIntent(domain.DirectionLong, "avwap")}))
	})

	t.Run("permissive allows day before announcement", func(t *testing.T) {
		g := &earningsBlackoutGate{
			checker: &fakeEarningsChecker{entry: entryTomorrow},
			modes:   map[string]string{"avwap": "permissive"},
			nowFn:   nowFn,
		}
		assert.Nil(t, g.Check(ctx, &ExecutionGateContext{Intent: buildIntent(domain.DirectionLong, "avwap")}))
	})

	t.Run("permissive rejects on announcement day", func(t *testing.T) {
		g := &earningsBlackoutGate{
			checker: &fakeEarningsChecker{entry: entryToday},
			modes:   map[string]string{"avwap": "permissive"},
			nowFn:   nowFn,
		}
		result := g.Check(ctx, &ExecutionGateContext{Intent: buildIntent(domain.DirectionLong, "avwap")})
		require.NotNil(t, result)
		assert.Contains(t, result.Reason, "mode=permissive")
	})

	t.Run("off mode bypassed", func(t *testing.T) {
		g := &earningsBlackoutGate{
			checker: &fakeEarningsChecker{entry: entryToday},
			modes:   map[string]string{"avwap": "off"},
			nowFn:   nowFn,
		}
		assert.Nil(t, g.Check(ctx, &ExecutionGateContext{Intent: buildIntent(domain.DirectionLong, "avwap")}))
	})

	t.Run("unknown strategy defaults to off", func(t *testing.T) {
		g := &earningsBlackoutGate{
			checker: &fakeEarningsChecker{entry: entryToday},
			modes:   map[string]string{"avwap": "strict"},
			nowFn:   nowFn,
		}
		assert.Nil(t, g.Check(ctx, &ExecutionGateContext{Intent: buildIntent(domain.DirectionLong, "other_strategy")}))
	})

	t.Run("exit intents always pass even in strict blackout", func(t *testing.T) {
		g := &earningsBlackoutGate{
			checker: &fakeEarningsChecker{entry: entryToday},
			modes:   map[string]string{"avwap": "strict"},
			nowFn:   nowFn,
		}
		assert.Nil(t, g.Check(ctx, &ExecutionGateContext{Intent: buildIntent(domain.DirectionCloseLong, "avwap")}))
	})

	t.Run("checker error fails open (does not block)", func(t *testing.T) {
		g := &earningsBlackoutGate{
			checker: &fakeEarningsChecker{err: errors.New("db down")},
			modes:   map[string]string{"avwap": "strict"},
			nowFn:   nowFn,
		}
		assert.Nil(t, g.Check(ctx, &ExecutionGateContext{Intent: buildIntent(domain.DirectionLong, "avwap")}))
	})

	t.Run("no entry known passes", func(t *testing.T) {
		g := &earningsBlackoutGate{
			checker: &fakeEarningsChecker{entry: nil},
			modes:   map[string]string{"avwap": "strict"},
			nowFn:   nowFn,
		}
		assert.Nil(t, g.Check(ctx, &ExecutionGateContext{Intent: buildIntent(domain.DirectionLong, "avwap")}))
	})

	t.Run("factory reads deps", func(t *testing.T) {
		deps := &ExecutionGateDeps{
			EarningsBlackoutGuard: &fakeEarningsChecker{},
			EarningsBlackoutModes: map[string]string{"avwap": "strict"},
		}
		g, err := newEarningsBlackoutGate(nil, deps)
		require.NoError(t, err)
		assert.Equal(t, "earnings_blackout_gate", g.Name())
	})
}
