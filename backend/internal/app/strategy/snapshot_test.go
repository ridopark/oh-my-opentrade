package strategy_test

import (
	"encoding/json"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunner_ListStrategies(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	fs1 := newFakeStrategy("alpha_strat", "1.0.0")
	fs2 := newFakeStrategy("beta_strat", "2.0.0")

	id1, _ := strat.NewInstanceID("alpha_strat:1.0.0:AAPL")
	inst1 := strategy.NewInstance(id1, fs1, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL", "MSFT"},
		Priority: 200,
	}, strat.LifecycleLiveActive, nil)

	id2, _ := strat.NewInstanceID("beta_strat:2.0.0:TSLA")
	inst2 := strategy.NewInstance(id2, fs2, nil, strategy.InstanceAssignment{
		Symbols:  []string{"TSLA"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	router.Register(inst1)
	router.Register(inst2)

	infos := runner.ListStrategies()
	require.Len(t, infos, 2)

	infoMap := map[string]strategy.StrategyInfo{}
	for _, info := range infos {
		infoMap[info.ID] = info
	}

	alpha := infoMap["alpha_strat"]
	assert.Equal(t, "Fake alpha_strat", alpha.Name)
	assert.Equal(t, "1.0.0", alpha.Version)
	assert.Equal(t, []string{"AAPL", "MSFT"}, alpha.Symbols)
	assert.Equal(t, 200, alpha.Priority)
	assert.True(t, alpha.Active)

	beta := infoMap["beta_strat"]
	assert.Equal(t, "Fake beta_strat", beta.Name)
	assert.Equal(t, "2.0.0", beta.Version)
	assert.Equal(t, []string{"TSLA"}, beta.Symbols)
	assert.Equal(t, 100, beta.Priority)
	assert.True(t, beta.Active)
}

func TestRunner_StrategySnapshots(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	fs := newFakeStrategy("orb_strat", "1.0.0")
	id, _ := strat.NewInstanceID("orb_strat:1.0.0:multi")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL", "MSFT"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	ctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(ctx, "AAPL", nil))
	require.NoError(t, inst.InitSymbol(ctx, "MSFT", nil))
	router.Register(inst)

	snaps := runner.StrategySnapshots("orb_strat")
	require.Len(t, snaps, 2)

	symbols := map[string]bool{}
	for _, s := range snaps {
		symbols[s.Symbol] = true
		assert.Equal(t, "orb_strat", s.Strategy)
		assert.Equal(t, json.RawMessage("init"), s.Payload)
	}
	assert.True(t, symbols["AAPL"])
	assert.True(t, symbols["MSFT"])
}

func TestRunner_StrategySnapshots_NoMatchReturnsNil(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	snaps := runner.StrategySnapshots("nonexistent")
	assert.Nil(t, snaps)
}

func TestRunner_StrategySnapshot_SingleSymbol(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	fs := newFakeStrategy("avwap_strat", "1.0.0")
	id, _ := strat.NewInstanceID("avwap_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	ctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(ctx, "AAPL", nil))
	router.Register(inst)

	snap, ok := runner.StrategySnapshot("avwap_strat", "AAPL")
	require.True(t, ok)
	assert.Equal(t, "avwap_strat", snap.Strategy)
	assert.Equal(t, "AAPL", snap.Symbol)
	assert.Equal(t, json.RawMessage("init"), snap.Payload)
}

func TestRunner_StrategySnapshot_NotFoundReturnsFalse(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	_, ok := runner.StrategySnapshot("nonexistent", "AAPL")
	assert.False(t, ok)
}
