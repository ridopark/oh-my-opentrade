package builtin_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMultiStrategy_AllRegisterWithoutConflict verifies all 3 builtin strategies
// can coexist in a single MemRegistry without ID collisions.
func TestMultiStrategy_AllRegisterWithoutConflict(t *testing.T) {
	reg := strategy.NewMemRegistry()

	strategies := []strat.Strategy{
		builtin.NewORBStrategy(),
		builtin.NewAVWAPStrategy(),
		builtin.NewAIScalperStrategy(),
	}

	for _, s := range strategies {
		err := reg.Register(s)
		require.NoError(t, err, "failed to register %s", s.Meta().ID)
	}

	ids := reg.List()
	assert.Len(t, ids, 3, "all 3 strategies should be registered")
}

// TestMultiStrategy_MetaUniqueIDs verifies each strategy has a unique ID.
func TestMultiStrategy_MetaUniqueIDs(t *testing.T) {
	strategies := []strat.Strategy{
		builtin.NewORBStrategy(),
		builtin.NewAVWAPStrategy(),
		builtin.NewAIScalperStrategy(),
	}

	seen := make(map[string]bool)
	for _, s := range strategies {
		id := s.Meta().ID.String()
		assert.False(t, seen[id], "duplicate strategy ID: %s", id)
		seen[id] = true
	}
	assert.Len(t, seen, 3)
}

// TestMultiStrategy_PriorityOrdering verifies that when all 3 strategies are
// registered on the same symbol with ORB=100, AVWAP=80, AIScalper=60,
// the Router returns instances in descending priority order.
func TestMultiStrategy_PriorityOrdering(t *testing.T) {
	router := strategy.NewRouter()

	type stratEntry struct {
		strategy strat.Strategy
		priority int
	}

	entries := []stratEntry{
		{builtin.NewORBStrategy(), 100},
		{builtin.NewAVWAPStrategy(), 80},
		{builtin.NewAIScalperStrategy(), 60},
	}

	symbol := "AAPL"
	ctx := newTestContext(time.Date(2025, 3, 4, 14, 45, 0, 0, time.UTC))

	for _, e := range entries {
		id := fmt.Sprintf("%s:%s:%s", e.strategy.Meta().ID, e.strategy.Meta().Version, symbol)
		instanceID, err := strat.NewInstanceID(id)
		require.NoError(t, err)

		inst := strategy.NewInstance(instanceID, e.strategy, nil, strategy.InstanceAssignment{
			Symbols:  []string{symbol},
			Priority: e.priority,
		}, strat.LifecycleLiveActive, nil)

		require.NoError(t, inst.InitSymbol(ctx, symbol, nil))
		router.Register(inst)
	}

	instances := router.InstancesForSymbol(symbol)
	require.Len(t, instances, 3, "all 3 instances should be registered for symbol")

	// Verify descending priority: ORB (100) > AVWAP (80) > AIScalper (60).
	assert.Equal(t, 100, instances[0].Assignment().Priority, "ORB should be first (priority 100)")
	assert.Equal(t, 80, instances[1].Assignment().Priority, "AVWAP should be second (priority 80)")
	assert.Equal(t, 60, instances[2].Assignment().Priority, "AIScalper should be third (priority 60)")
}

// TestMultiStrategy_AllInitSuccessfully verifies that all 3 strategies can
// Init on the same symbol without errors, even with nil params.
func TestMultiStrategy_AllInitSuccessfully(t *testing.T) {
	strategies := []strat.Strategy{
		builtin.NewORBStrategy(),
		builtin.NewAVWAPStrategy(),
		builtin.NewAIScalperStrategy(),
	}

	symbol := "MSFT"
	ctx := newTestContext(time.Date(2025, 3, 4, 14, 45, 0, 0, time.UTC))

	for _, s := range strategies {
		id := fmt.Sprintf("%s:%s:%s", s.Meta().ID, s.Meta().Version, symbol)
		instanceID, err := strat.NewInstanceID(id)
		require.NoError(t, err)

		inst := strategy.NewInstance(instanceID, s, nil, strategy.InstanceAssignment{
			Symbols:  []string{symbol},
			Priority: 100,
		}, strat.LifecycleLiveActive, nil)

		err = inst.InitSymbol(ctx, symbol, nil)
		assert.NoError(t, err, "Init failed for %s", s.Meta().ID)
	}
}

// TestMultiStrategy_AllProcessBarWithoutPanic verifies all 3 strategies can
// process the same bar without errors or panics. Each gets its own instance.
func TestMultiStrategy_AllProcessBarWithoutPanic(t *testing.T) {
	strategies := []strat.Strategy{
		builtin.NewORBStrategy(),
		builtin.NewAVWAPStrategy(),
		builtin.NewAIScalperStrategy(),
	}

	symbol := "GOOGL"
	now := time.Date(2025, 3, 4, 14, 45, 0, 0, time.UTC)
	ctx := newTestContext(now)

	bar := strat.Bar{
		Time:   now,
		Open:   175.00,
		High:   175.50,
		Low:    174.50,
		Close:  175.25,
		Volume: 50000,
	}
	indicators := strat.IndicatorData{
		RSI:       45.0,
		StochK:    35.0,
		StochD:    38.0,
		EMA9:      174.80,
		EMA21:     174.50,
		VWAP:      175.00,
		Volume:    50000,
		VolumeSMA: 40000,
		AnchorRegimes: map[string]strat.AnchorRegime{
			"5m": {Type: "BALANCE", Strength: 0.6},
		},
	}

	for _, s := range strategies {
		t.Run(s.Meta().ID.String(), func(t *testing.T) {
			id := fmt.Sprintf("%s:%s:%s", s.Meta().ID, s.Meta().Version, symbol)
			instanceID, err := strat.NewInstanceID(id)
			require.NoError(t, err)

			inst := strategy.NewInstance(instanceID, s, nil, strategy.InstanceAssignment{
				Symbols:  []string{symbol},
				Priority: 100,
			}, strat.LifecycleLiveActive, nil)

			require.NoError(t, inst.InitSymbol(ctx, symbol, nil))

			// Process a warmup bar to avoid warmup suppression — strategies need 30 bars.
			// We just verify no panic or error, not that signals are produced.
			_, err = inst.OnBar(ctx, symbol, bar, indicators)
			assert.NoError(t, err, "OnBar failed for %s", s.Meta().ID)
		})
	}
}

// TestMultiStrategy_WarmupBarsSpec verifies the expected warmup counts per Oracle design.
func TestMultiStrategy_WarmupBarsSpec(t *testing.T) {
	tests := []struct {
		strategy strat.Strategy
		expected int
	}{
		{builtin.NewORBStrategy(), 30},
		{builtin.NewAVWAPStrategy(), 30},
		{builtin.NewAIScalperStrategy(), 30},
	}
	for _, tc := range tests {
		t.Run(tc.strategy.Meta().ID.String(), func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.strategy.WarmupBars())
		})
	}
}

// TestMultiStrategy_RouterSymbolDedup verifies that registering 3 strategies on
// overlapping symbols correctly reports all unique symbols.
func TestMultiStrategy_RouterSymbolDedup(t *testing.T) {
	router := strategy.NewRouter()
	ctx := newTestContext(time.Date(2025, 3, 4, 14, 45, 0, 0, time.UTC))

	type entry struct {
		strategy strat.Strategy
		symbols  []string
		priority int
	}
	entries := []entry{
		{builtin.NewORBStrategy(), []string{"AAPL", "MSFT", "GOOGL"}, 100},
		{builtin.NewAVWAPStrategy(), []string{"AAPL", "TSLA", "GOOGL"}, 80},
		{builtin.NewAIScalperStrategy(), []string{"MSFT", "TSLA", "SPY"}, 60},
	}

	for _, e := range entries {
		for _, sym := range e.symbols {
			id := fmt.Sprintf("%s:%s:%s", e.strategy.Meta().ID, e.strategy.Meta().Version, sym)
			instanceID, err := strat.NewInstanceID(id)
			require.NoError(t, err)

			inst := strategy.NewInstance(instanceID, e.strategy, nil, strategy.InstanceAssignment{
				Symbols:  []string{sym},
				Priority: e.priority,
			}, strat.LifecycleLiveActive, nil)

			require.NoError(t, inst.InitSymbol(ctx, sym, nil))
			router.Register(inst)
		}
	}

	allSymbols := router.Symbols()
	// AAPL, GOOGL, MSFT, SPY, TSLA — 5 unique symbols sorted.
	assert.Equal(t, []string{"AAPL", "GOOGL", "MSFT", "SPY", "TSLA"}, allSymbols)

	// AAPL should have 2 instances (ORB + AVWAP).
	assert.Len(t, router.InstancesForSymbol("AAPL"), 2)
	// MSFT should have 2 instances (ORB + AIScalper).
	assert.Len(t, router.InstancesForSymbol("MSFT"), 2)
	// TSLA should have 2 instances (AVWAP + AIScalper).
	assert.Len(t, router.InstancesForSymbol("TSLA"), 2)
	// GOOGL should have 2 instances (ORB + AVWAP).
	assert.Len(t, router.InstancesForSymbol("GOOGL"), 2)
	// SPY should have 1 instance (AIScalper only).
	assert.Len(t, router.InstancesForSymbol("SPY"), 1)
}
