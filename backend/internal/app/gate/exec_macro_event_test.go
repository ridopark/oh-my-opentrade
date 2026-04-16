package gate

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMacroChecker struct {
	events []ports.MacroEvent
	err    error
}

func (f *fakeMacroChecker) EventsInWindow(_ context.Context, _ time.Time, _ int) ([]ports.MacroEvent, error) {
	return f.events, f.err
}

func TestMacroEventGate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }

	buildGate := func(events []ports.MacroEvent, blocked []string) *macroEventGate {
		set := make(map[string]struct{}, len(blocked))
		for _, s := range blocked {
			set[s] = struct{}{}
		}
		return &macroEventGate{
			checker:        &fakeMacroChecker{events: events},
			windowMinutes:  30,
			blockedImpacts: set,
			nowFn:          nowFn,
		}
	}

	entryIntent := domain.OrderIntent{Symbol: "AAPL", Direction: domain.DirectionLong, Strategy: "avwap"}
	exitIntent := domain.OrderIntent{Symbol: "AAPL", Direction: domain.DirectionCloseLong, Strategy: "avwap"}

	t.Run("high-impact event at now+20min rejects entry", func(t *testing.T) {
		g := buildGate([]ports.MacroEvent{{
			ID:          "fomc",
			Name:        "FOMC Rate Decision",
			ScheduledAt: now.Add(20 * time.Minute),
			Impact:      "high",
		}}, []string{"high"})
		result := g.Check(ctx, &ExecutionGateContext{Intent: entryIntent})
		require.NotNil(t, result)
		assert.Equal(t, "macro_event_gate", result.GateName)
		assert.Contains(t, result.Reason, "FOMC Rate Decision")
		assert.Contains(t, result.Reason, "high")
	})

	t.Run("event at ±45min is outside the window and allowed", func(t *testing.T) {
		// The checker is responsible for window filtering. Simulate by
		// returning no events (the repo query wouldn't have matched).
		g := buildGate(nil, []string{"high"})
		assert.Nil(t, g.Check(ctx, &ExecutionGateContext{Intent: entryIntent}))
	})

	t.Run("low-impact event filtered out", func(t *testing.T) {
		g := buildGate([]ports.MacroEvent{{
			ID:          "retail_sales",
			Name:        "Retail Sales",
			ScheduledAt: now.Add(10 * time.Minute),
			Impact:      "low",
		}}, []string{"high"})
		assert.Nil(t, g.Check(ctx, &ExecutionGateContext{Intent: entryIntent}))
	})

	t.Run("medium event also blocked when configured", func(t *testing.T) {
		g := buildGate([]ports.MacroEvent{{
			ID:          "pmi",
			Name:        "PMI",
			ScheduledAt: now.Add(5 * time.Minute),
			Impact:      "medium",
		}}, []string{"high", "medium"})
		result := g.Check(ctx, &ExecutionGateContext{Intent: entryIntent})
		require.NotNil(t, result)
	})

	t.Run("exit intent always allowed even during high-impact event", func(t *testing.T) {
		g := buildGate([]ports.MacroEvent{{
			ID:          "fomc",
			Name:        "FOMC Rate Decision",
			ScheduledAt: now.Add(5 * time.Minute),
			Impact:      "high",
		}}, []string{"high"})
		assert.Nil(t, g.Check(ctx, &ExecutionGateContext{Intent: exitIntent}))
	})

	t.Run("nil checker passes (disabled)", func(t *testing.T) {
		g := &macroEventGate{windowMinutes: 30, nowFn: nowFn}
		assert.Nil(t, g.Check(ctx, &ExecutionGateContext{Intent: entryIntent}))
	})

	t.Run("empty impact is treated as medium", func(t *testing.T) {
		g := buildGate([]ports.MacroEvent{{
			ID:          "x",
			Name:        "Unknown",
			ScheduledAt: now.Add(5 * time.Minute),
			Impact:      "",
		}}, []string{"medium"})
		result := g.Check(ctx, &ExecutionGateContext{Intent: entryIntent})
		require.NotNil(t, result)
	})

	t.Run("factory applies defaults", func(t *testing.T) {
		deps := &ExecutionGateDeps{MacroEventGuard: &fakeMacroChecker{}}
		g, err := newMacroEventGate(nil, deps)
		require.NoError(t, err)
		assert.Equal(t, "macro_event_gate", g.Name())
		gg := g.(*macroEventGate)
		assert.Equal(t, 30, gg.windowMinutes)
		_, hasHigh := gg.blockedImpacts["high"]
		assert.True(t, hasHigh)
	})
}
