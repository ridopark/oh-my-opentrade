package strategy_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouter_Replace_AtomicSwap(t *testing.T) {
	r := strategy.NewRouter()

	oldFS := newFakeStrategy("orb", "1.0.0")
	oldID, _ := strat.NewInstanceID("orb:1.0.0:multi")
	oldInst := strategy.NewInstance(oldID, oldFS, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL", "MSFT"},
		Priority: 100,
	}, strat.LifecyclePaperActive, nil)
	r.Register(oldInst)

	require.Len(t, r.InstancesForSymbol("AAPL"), 1)
	require.Len(t, r.InstancesForSymbol("MSFT"), 1)

	newFS := newFakeStrategy("orb", "2.0.0")
	newID, _ := strat.NewInstanceID("orb:2.0.0:multi")
	newInst := strategy.NewInstance(newID, newFS, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL", "MSFT"},
		Priority: 100,
	}, strat.LifecyclePaperActive, nil)

	returned := r.Replace(oldID, newInst)

	require.NotNil(t, returned)
	assert.Equal(t, oldID, returned.ID())

	aaplInsts := r.InstancesForSymbol("AAPL")
	require.Len(t, aaplInsts, 1)
	assert.Equal(t, newID, aaplInsts[0].ID())

	msftInsts := r.InstancesForSymbol("MSFT")
	require.Len(t, msftInsts, 1)
	assert.Equal(t, newID, msftInsts[0].ID())

	_, found := r.Instance(oldID)
	assert.False(t, found, "old instance should be removed")
}

func TestRouter_Replace_OldNotFound(t *testing.T) {
	r := strategy.NewRouter()

	newFS := newFakeStrategy("orb", "2.0.0")
	newID, _ := strat.NewInstanceID("orb:2.0.0:AAPL")
	newInst := strategy.NewInstance(newID, newFS, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecyclePaperActive, nil)

	bogusID, _ := strat.NewInstanceID("bogus:1.0.0:AAPL")
	returned := r.Replace(bogusID, newInst)

	assert.Nil(t, returned, "should return nil when old not found")
	require.Len(t, r.InstancesForSymbol("AAPL"), 1)
	assert.Equal(t, newID, r.InstancesForSymbol("AAPL")[0].ID())
}

func TestSwapManager_RequestSwap_OldNotFound(t *testing.T) {
	router := strategy.NewRouter()
	sm := strategy.NewSwapManager(router, nil)

	newFS := newFakeStrategy("orb", "2.0.0")
	newID, _ := strat.NewInstanceID("orb:2.0.0:AAPL")
	newInst := strategy.NewInstance(newID, newFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"},
	}, strat.LifecycleDraft, nil)

	bogusID, _ := strat.NewInstanceID("bogus:1.0.0:AAPL")
	err := sm.RequestSwap(bogusID, newInst)
	assert.ErrorIs(t, err, strat.ErrInstanceNotFound)
}

func TestSwapManager_RequestSwap_DuplicateRejected(t *testing.T) {
	router := strategy.NewRouter()
	sm := strategy.NewSwapManager(router, nil)

	oldFS := newFakeStrategy("orb", "1.0.0")
	oldID, _ := strat.NewInstanceID("orb:1.0.0:AAPL")
	oldInst := strategy.NewInstance(oldID, oldFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecyclePaperActive, nil)
	router.Register(oldInst)

	makeNew := func(ver string) *strategy.Instance {
		fs := newFakeStrategy("orb", ver)
		id, _ := strat.NewInstanceID("orb:" + ver + ":AAPL")
		return strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
			Symbols: []string{"AAPL"},
		}, strat.LifecycleDraft, nil)
	}

	require.NoError(t, sm.RequestSwap(oldID, makeNew("2.0.0")))
	err := sm.RequestSwap(oldID, makeNew("3.0.0"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "swap already pending")
}

func TestSwapManager_SwapAfterWarmup(t *testing.T) {
	router := strategy.NewRouter()
	sm := strategy.NewSwapManager(router, nil)

	oldFS := newFakeStrategy("orb", "1.0.0")
	oldID, _ := strat.NewInstanceID("orb:1.0.0:AAPL")
	oldInst := strategy.NewInstance(oldID, oldFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecyclePaperActive, nil)
	ctx := newTestCtx()
	require.NoError(t, oldInst.InitSymbol(ctx, "AAPL", nil))
	router.Register(oldInst)

	newFS := newFakeStrategy("orb", "2.0.0")
	newFS.warmup = 3
	newID, _ := strat.NewInstanceID("orb:2.0.0:AAPL")
	newInst := strategy.NewInstance(newID, newFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecycleDraft, nil)

	require.NoError(t, sm.RequestSwap(oldID, newInst))
	assert.Equal(t, 1, sm.PendingSwaps())
	assert.True(t, sm.HasPendingSwap(oldID))

	bar := strat.Bar{Time: time.Now(), Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}
	ind := strat.IndicatorData{}

	for i := 0; i < 2; i++ {
		completed := sm.OnBarProcessed(ctx, "AAPL", bar, ind)
		assert.Empty(t, completed, "swap should not trigger during warmup (bar %d)", i)
		assert.Equal(t, 1, sm.PendingSwaps())
	}

	completed := sm.OnBarProcessed(ctx, "AAPL", bar, ind)
	require.Len(t, completed, 1)
	assert.Equal(t, oldID, completed[0].OldInstanceID)
	assert.Equal(t, newID, completed[0].NewInstanceID)
	assert.Equal(t, strat.LifecyclePaperActive, completed[0].OldLifecycle)
	assert.Equal(t, 0, sm.PendingSwaps())

	aaplInsts := router.InstancesForSymbol("AAPL")
	require.Len(t, aaplInsts, 1)
	assert.Equal(t, newID, aaplInsts[0].ID())
	assert.Equal(t, strat.LifecyclePaperActive, aaplInsts[0].Lifecycle())
}

func TestSwapManager_MultiSymbolWarmup(t *testing.T) {
	router := strategy.NewRouter()
	sm := strategy.NewSwapManager(router, nil)

	oldFS := newFakeStrategy("orb", "1.0.0")
	oldID, _ := strat.NewInstanceID("orb:1.0.0:multi")
	oldInst := strategy.NewInstance(oldID, oldFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL", "MSFT"}, Priority: 100,
	}, strat.LifecycleLiveActive, nil)
	ctx := newTestCtx()
	require.NoError(t, oldInst.InitSymbol(ctx, "AAPL", nil))
	require.NoError(t, oldInst.InitSymbol(ctx, "MSFT", nil))
	router.Register(oldInst)

	newFS := newFakeStrategy("orb", "2.0.0")
	newFS.warmup = 2
	newID, _ := strat.NewInstanceID("orb:2.0.0:multi")
	newInst := strategy.NewInstance(newID, newFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL", "MSFT"}, Priority: 100,
	}, strat.LifecycleDraft, nil)

	require.NoError(t, sm.RequestSwap(oldID, newInst))

	bar := strat.Bar{Time: time.Now(), Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}
	ind := strat.IndicatorData{}

	completed := sm.OnBarProcessed(ctx, "AAPL", bar, ind)
	assert.Empty(t, completed, "AAPL still warming, MSFT not started")

	completed = sm.OnBarProcessed(ctx, "AAPL", bar, ind)
	assert.Empty(t, completed, "AAPL done but MSFT still needs warmup")

	completed = sm.OnBarProcessed(ctx, "MSFT", bar, ind)
	assert.Empty(t, completed, "MSFT needs one more bar")

	completed = sm.OnBarProcessed(ctx, "MSFT", bar, ind)
	require.Len(t, completed, 1, "all symbols warmed up, swap should trigger")
	assert.Equal(t, strat.LifecycleLiveActive, completed[0].OldLifecycle)
}

func TestSwapManager_StateHandoff(t *testing.T) {
	router := strategy.NewRouter()
	sm := strategy.NewSwapManager(router, nil)

	var receivedPrior strat.State
	oldFS := newFakeStrategy("orb", "1.0.0")
	oldID, _ := strat.NewInstanceID("orb:1.0.0:AAPL")
	oldInst := strategy.NewInstance(oldID, oldFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecyclePaperActive, nil)
	ctx := newTestCtx()
	require.NoError(t, oldInst.InitSymbol(ctx, "AAPL", nil))

	bar := strat.Bar{Time: time.Now(), Open: 150, High: 155, Low: 148, Close: 152, Volume: 500}
	_, err := oldInst.OnBar(ctx, "AAPL", bar, strat.IndicatorData{})
	require.NoError(t, err)
	router.Register(oldInst)

	newFS := newFakeStrategy("orb", "2.0.0")
	newFS.warmup = 0
	captureInit := newFS
	origInit := captureInit.Meta()
	_ = origInit
	newFS = &fakeStrategy{
		meta:   newFS.meta,
		warmup: 0,
		onBarFunc: func(_ strat.Context, _ string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
			return st, nil, nil
		},
	}
	// Override Init to capture prior state
	type initCapture struct {
		*fakeStrategy
	}
	_ = initCapture{}

	capturedFS := &fakeStrategyWithInitCapture{
		fakeStrategy:  newFakeStrategy("orb", "2.0.0"),
		capturedPrior: &receivedPrior,
	}
	capturedFS.warmup = 0

	newID, _ := strat.NewInstanceID("orb:2.0.0:AAPL")
	newInst := strategy.NewInstance(newID, capturedFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecycleDraft, nil)

	require.NoError(t, sm.RequestSwap(oldID, newInst))

	assert.NotNil(t, receivedPrior, "new strategy should receive prior state from old instance")
}

type fakeStrategyWithInitCapture struct {
	*fakeStrategy
	capturedPrior *strat.State
}

func (f *fakeStrategyWithInitCapture) Init(ctx strat.Context, symbol string, params map[string]any, prior strat.State) (strat.State, error) {
	*f.capturedPrior = prior
	return f.fakeStrategy.Init(ctx, symbol, params, prior)
}

func TestSwapManager_StateHandoff_IncompatibleFallback(t *testing.T) {
	router := strategy.NewRouter()
	sm := strategy.NewSwapManager(router, slog.Default())

	oldFS := newFakeStrategy("orb", "1.0.0")
	oldID, _ := strat.NewInstanceID("orb:1.0.0:AAPL")
	oldInst := strategy.NewInstance(oldID, oldFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecyclePaperActive, nil)
	ctx := newTestCtx()
	require.NoError(t, oldInst.InitSymbol(ctx, "AAPL", nil))
	router.Register(oldInst)

	initCallCount := 0
	failingFS := &fakeStrategyConditionalInit{
		fakeStrategy:  newFakeStrategy("orb", "2.0.0"),
		initCallCount: &initCallCount,
	}
	failingFS.warmup = 0

	newID, _ := strat.NewInstanceID("orb:2.0.0:AAPL")
	newInst := strategy.NewInstance(newID, failingFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecycleDraft, nil)

	err := sm.RequestSwap(oldID, newInst)
	require.NoError(t, err, "should succeed by falling back to nil prior")
	assert.Equal(t, 2, initCallCount, "should have tried with prior then without")
}

type fakeStrategyConditionalInit struct {
	*fakeStrategy
	initCallCount *int
}

func (f *fakeStrategyConditionalInit) Init(ctx strat.Context, symbol string, params map[string]any, prior strat.State) (strat.State, error) {
	*f.initCallCount++
	if prior != nil {
		return nil, strat.ErrStateCorrupted
	}
	return f.fakeStrategy.Init(ctx, symbol, params, prior)
}

func TestSwapManager_CancelSwap(t *testing.T) {
	router := strategy.NewRouter()
	sm := strategy.NewSwapManager(router, nil)

	oldFS := newFakeStrategy("orb", "1.0.0")
	oldID, _ := strat.NewInstanceID("orb:1.0.0:AAPL")
	oldInst := strategy.NewInstance(oldID, oldFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecyclePaperActive, nil)
	ctx := newTestCtx()
	require.NoError(t, oldInst.InitSymbol(ctx, "AAPL", nil))
	router.Register(oldInst)

	newFS := newFakeStrategy("orb", "2.0.0")
	newFS.warmup = 5
	newID, _ := strat.NewInstanceID("orb:2.0.0:AAPL")
	newInst := strategy.NewInstance(newID, newFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"},
	}, strat.LifecycleDraft, nil)

	require.NoError(t, sm.RequestSwap(oldID, newInst))
	assert.True(t, sm.HasPendingSwap(oldID))

	cancelled := sm.CancelSwap(oldID)
	assert.True(t, cancelled)
	assert.False(t, sm.HasPendingSwap(oldID))
	assert.Equal(t, 0, sm.PendingSwaps())

	aaplInsts := router.InstancesForSymbol("AAPL")
	require.Len(t, aaplInsts, 1)
	assert.Equal(t, oldID, aaplInsts[0].ID(), "old instance should remain after cancel")
}

func TestSwapManager_NoDoubleSignals_DuringSwap(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	sm := strategy.NewSwapManager(router, nil)
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)
	runner.SetSwapManager(sm)

	signalCount := 0
	oldFS := newFakeStrategy("orb", "1.0.0")
	oldFS.onBarFunc = func(_ strat.Context, symbol string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		signalCount++
		iid, _ := strat.NewInstanceID("orb:1.0.0:" + symbol)
		sig, _ := strat.NewSignal(iid, symbol, strat.SignalEntry, strat.SideBuy, 0.8, nil)
		return st, []strat.Signal{sig}, nil
	}
	oldID, _ := strat.NewInstanceID("orb:1.0.0:AAPL")
	oldInst := strategy.NewInstance(oldID, oldFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecyclePaperActive, nil)
	ctx := newTestCtx()
	require.NoError(t, oldInst.InitSymbol(ctx, "AAPL", nil))
	router.Register(oldInst)

	newFS := newFakeStrategy("orb", "2.0.0")
	newFS.warmup = 1
	newFS.onBarFunc = func(_ strat.Context, symbol string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		iid, _ := strat.NewInstanceID("orb:2.0.0:" + symbol)
		sig, _ := strat.NewSignal(iid, symbol, strat.SignalEntry, strat.SideBuy, 0.9, nil)
		return st, []strat.Signal{sig}, nil
	}
	newID, _ := strat.NewInstanceID("orb:2.0.0:AAPL")
	newInst := strategy.NewInstance(newID, newFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecycleDraft, nil)

	require.NoError(t, sm.RequestSwap(oldID, newInst))

	bar := strat.Bar{Time: time.Now(), Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}

	signals, err := runner.ProcessBar(context.Background(), "AAPL", bar, strat.IndicatorData{Volume: 10})
	require.NoError(t, err)
	assert.Len(t, signals, 1, "only old instance should produce signals during warmup")
	assert.Equal(t, oldID, signals[0].StrategyInstanceID)

	signals, err = runner.ProcessBar(context.Background(), "AAPL", bar, strat.IndicatorData{Volume: 10})
	require.NoError(t, err)
	assert.Len(t, signals, 1, "should still only get one signal — swap just completed, new instance takes over")
}

func TestSwapManager_OldInstanceArchived_AfterSwap(t *testing.T) {
	router := strategy.NewRouter()
	sm := strategy.NewSwapManager(router, nil)

	oldFS := newFakeStrategy("orb", "1.0.0")
	oldID, _ := strat.NewInstanceID("orb:1.0.0:AAPL")
	oldInst := strategy.NewInstance(oldID, oldFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecyclePaperActive, nil)
	ctx := newTestCtx()
	require.NoError(t, oldInst.InitSymbol(ctx, "AAPL", nil))
	router.Register(oldInst)

	newFS := newFakeStrategy("orb", "2.0.0")
	newFS.warmup = 0
	newID, _ := strat.NewInstanceID("orb:2.0.0:AAPL")
	newInst := strategy.NewInstance(newID, newFS, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecycleDraft, nil)

	require.NoError(t, sm.RequestSwap(oldID, newInst))

	bar := strat.Bar{Time: time.Now(), Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}
	completed := sm.OnBarProcessed(ctx, "AAPL", bar, strat.IndicatorData{})
	require.Len(t, completed, 1)

	assert.Equal(t, strat.LifecycleArchived, oldInst.Lifecycle(), "old instance should be archived")
	assert.Equal(t, strat.LifecyclePaperActive, newInst.Lifecycle(), "new instance should inherit PaperActive")
}

func TestInstance_WarmupOnBar(t *testing.T) {
	barCount := 0
	fs := newFakeStrategy("test_strat", "1.0.0")
	fs.warmup = 2
	fs.onBarFunc = func(_ strat.Context, _ string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		barCount++
		return st, nil, nil
	}

	id, _ := strat.NewInstanceID("test:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecycleDraft, nil)

	ctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(ctx, "AAPL", nil))

	bar := strat.Bar{Time: time.Now(), Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}

	assert.False(t, inst.IsWarmedUp("AAPL"))

	require.NoError(t, inst.WarmupOnBar(ctx, "AAPL", bar, strat.IndicatorData{}))
	assert.False(t, inst.IsWarmedUp("AAPL"))

	require.NoError(t, inst.WarmupOnBar(ctx, "AAPL", bar, strat.IndicatorData{}))
	assert.True(t, inst.IsWarmedUp("AAPL"))
	assert.Equal(t, 2, barCount, "strategy OnBar should be called for each warmup bar")
}
