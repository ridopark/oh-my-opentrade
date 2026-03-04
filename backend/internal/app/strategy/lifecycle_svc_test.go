package strategy_test

import (
	"log/slog"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockStrategy struct{}

func (m *mockStrategy) Meta() strat.Meta {
	return strat.Meta{ID: strat.StrategyID("test"), Name: "test"}
}
func (m *mockStrategy) Init(_ strat.Context, _ string, _ map[string]any, _ strat.State) (strat.State, error) {
	return nil, nil
}
func (m *mockStrategy) OnBar(_ strat.Context, _ string, _ strat.Bar, _ strat.State) (strat.State, []strat.Signal, error) {
	return nil, nil, nil
}
func (m *mockStrategy) OnEvent(_ strat.Context, _ string, _ any, _ strat.State) (strat.State, []strat.Signal, error) {
	return nil, nil, nil
}
func (m *mockStrategy) WarmupBars() int { return 0 }

func newTestInstance(id string, lifecycle strat.LifecycleState, symbols []string) *strategy.Instance {
	iid, _ := strat.NewInstanceID(id)
	return strategy.NewInstance(iid, &mockStrategy{}, nil, strategy.InstanceAssignment{
		Symbols: symbols,
	}, lifecycle, nil)
}

func TestLifecycleService_Promote_Valid(t *testing.T) {
	router := strategy.NewRouter()
	inst := newTestInstance("s:1:AAPL", strat.LifecyclePaperActive, []string{"AAPL"})
	router.Register(inst)

	svc := strategy.NewLifecycleService(router, slog.Default())
	err := svc.Promote(inst.ID(), strat.LifecycleLiveActive)
	require.NoError(t, err)
	assert.Equal(t, strat.LifecycleLiveActive, inst.Lifecycle())
}

func TestLifecycleService_Promote_InvalidTransition(t *testing.T) {
	router := strategy.NewRouter()
	inst := newTestInstance("s:1:AAPL", strat.LifecycleDraft, []string{"AAPL"})
	router.Register(inst)

	svc := strategy.NewLifecycleService(router, slog.Default())
	err := svc.Promote(inst.ID(), strat.LifecycleLiveActive)
	require.Error(t, err)
	assert.ErrorIs(t, err, strat.ErrInvalidTransition)
}

func TestLifecycleService_Promote_NotFound(t *testing.T) {
	router := strategy.NewRouter()
	svc := strategy.NewLifecycleService(router, slog.Default())

	iid, _ := strat.NewInstanceID("missing")
	err := svc.Promote(iid, strat.LifecyclePaperActive)
	require.Error(t, err)
	assert.ErrorIs(t, err, strat.ErrInstanceNotFound)
}

func TestLifecycleService_Deactivate(t *testing.T) {
	router := strategy.NewRouter()
	inst := newTestInstance("s:1:AAPL", strat.LifecyclePaperActive, []string{"AAPL"})
	router.Register(inst)

	svc := strategy.NewLifecycleService(router, slog.Default())
	err := svc.Deactivate(inst.ID())
	require.NoError(t, err)
	assert.Equal(t, strat.LifecycleDeactivated, inst.Lifecycle())
}

func TestLifecycleService_Deactivate_AlreadyDeactivated(t *testing.T) {
	router := strategy.NewRouter()
	inst := newTestInstance("s:1:AAPL", strat.LifecycleDeactivated, []string{"AAPL"})
	router.Register(inst)

	svc := strategy.NewLifecycleService(router, slog.Default())
	err := svc.Deactivate(inst.ID())
	require.Error(t, err)
	assert.ErrorIs(t, err, strat.ErrAlreadyInState)
}

func TestLifecycleService_Archive(t *testing.T) {
	router := strategy.NewRouter()
	inst := newTestInstance("s:1:AAPL", strat.LifecycleDeactivated, []string{"AAPL"})
	router.Register(inst)

	svc := strategy.NewLifecycleService(router, slog.Default())
	err := svc.Archive(inst.ID())
	require.NoError(t, err)
	assert.Equal(t, strat.LifecycleArchived, inst.Lifecycle())
}

func TestLifecycleService_Archive_NotDeactivated(t *testing.T) {
	router := strategy.NewRouter()
	inst := newTestInstance("s:1:AAPL", strat.LifecyclePaperActive, []string{"AAPL"})
	router.Register(inst)

	svc := strategy.NewLifecycleService(router, slog.Default())
	err := svc.Archive(inst.ID())
	require.Error(t, err)
	assert.ErrorIs(t, err, strat.ErrInvalidTransition)
}

func TestLifecycleService_ListInstances(t *testing.T) {
	router := strategy.NewRouter()
	inst1 := newTestInstance("s:1:AAPL", strat.LifecyclePaperActive, []string{"AAPL", "MSFT"})
	inst2 := newTestInstance("s:1:TSLA", strat.LifecycleDeactivated, []string{"TSLA"})
	router.Register(inst1)
	router.Register(inst2)

	svc := strategy.NewLifecycleService(router, slog.Default())
	got := svc.ListInstances()
	require.Len(t, got, 2)

	var aapl strategy.InstanceInfo
	var tsla strategy.InstanceInfo
	for _, ii := range got {
		switch ii.ID {
		case inst1.ID().String():
			aapl = ii
		case inst2.ID().String():
			tsla = ii
		}
	}

	assert.Equal(t, "test", aapl.StrategyName)
	assert.Equal(t, strat.LifecyclePaperActive.String(), aapl.Lifecycle)
	assert.True(t, aapl.IsActive)
	assert.Equal(t, []string{"AAPL", "MSFT"}, aapl.Symbols)
	assert.Contains(t, aapl.AllowedTransitions, strat.LifecycleDeactivated.String())
	assert.Contains(t, aapl.AllowedTransitions, strat.LifecycleLiveActive.String())

	assert.Equal(t, "test", tsla.StrategyName)
	assert.Equal(t, strat.LifecycleDeactivated.String(), tsla.Lifecycle)
	assert.False(t, tsla.IsActive)
	assert.Equal(t, []string{"TSLA"}, tsla.Symbols)
	assert.Contains(t, tsla.AllowedTransitions, strat.LifecycleArchived.String())
	assert.Contains(t, tsla.AllowedTransitions, strat.LifecyclePaperActive.String())
}
