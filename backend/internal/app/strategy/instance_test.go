package strategy_test

import (
	"encoding/json"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstance_Snapshot_ReturnsStateForInitializedSymbol(t *testing.T) {
	fs := newFakeStrategy("test_strat", "1.0.0")
	id, _ := strat.NewInstanceID("test_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	ctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(ctx, "AAPL", nil))

	snap, ok := inst.Snapshot("AAPL")
	require.True(t, ok, "snapshot should exist for initialized symbol")
	assert.Equal(t, "test_strat", snap.Strategy)
	assert.Equal(t, "AAPL", snap.Symbol)
	assert.Equal(t, "test_strat", snap.Kind)
	assert.False(t, snap.AsOf.IsZero(), "AsOf should be set")

	// Verify payload is the marshaled state ("init" from fakeState).
	assert.Equal(t, json.RawMessage("init"), snap.Payload)
}

func TestInstance_Snapshot_ReturnsFalseForUninitializedSymbol(t *testing.T) {
	fs := newFakeStrategy("test_strat", "1.0.0")
	id, _ := strat.NewInstanceID("test_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	_, ok := inst.Snapshot("AAPL")
	assert.False(t, ok, "snapshot should not exist for uninitialized symbol")
}

func TestInstance_AllSnapshots_ReturnsAllInitializedSymbols(t *testing.T) {
	fs := newFakeStrategy("multi_strat", "1.0.0")
	id, _ := strat.NewInstanceID("multi_strat:1.0.0:multi")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL", "MSFT", "GOOGL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	ctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(ctx, "AAPL", nil))
	require.NoError(t, inst.InitSymbol(ctx, "MSFT", nil))
	// GOOGL not initialized.

	snaps := inst.AllSnapshots()
	require.Len(t, snaps, 2)

	symbols := map[string]bool{}
	for _, s := range snaps {
		symbols[s.Symbol] = true
		assert.Equal(t, "multi_strat", s.Strategy)
		assert.Equal(t, "multi_strat", s.Kind)
		assert.Equal(t, json.RawMessage("init"), s.Payload)
	}
	assert.True(t, symbols["AAPL"])
	assert.True(t, symbols["MSFT"])
}

func TestInstance_AllSnapshots_EmptyWhenNoSymbolsInitialized(t *testing.T) {
	fs := newFakeStrategy("empty_strat", "1.0.0")
	id, _ := strat.NewInstanceID("empty_strat:1.0.0:multi")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	snaps := inst.AllSnapshots()
	assert.Empty(t, snaps)
}
