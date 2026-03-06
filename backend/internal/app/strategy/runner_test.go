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
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Fake strategy for testing ---

type fakeStrategy struct {
	meta      strat.Meta
	warmup    int
	onBarFunc func(ctx strat.Context, symbol string, bar strat.Bar, st strat.State) (strat.State, []strat.Signal, error)
	onEventFn func(ctx strat.Context, symbol string, evt any, st strat.State) (strat.State, []strat.Signal, error)
}

// fakeReplayableStrategy extends fakeStrategy with ReplayOnBar
type fakeReplayableStrategy struct {
	fakeStrategy
	replayCount int
}

func (f *fakeReplayableStrategy) ReplayOnBar(_ strat.Context, _ string, _ strat.Bar, st strat.State, _ strat.IndicatorData) (strat.State, error) {
	f.replayCount++
	return st, nil
}

func newFakeStrategy(id, version string) *fakeStrategy {
	sid, _ := strat.NewStrategyID(id)
	ver, _ := strat.NewVersion(version)
	return &fakeStrategy{
		meta: strat.Meta{
			ID:      sid,
			Version: ver,
			Name:    "Fake " + id,
			Author:  "test",
		},
		warmup: 0,
	}
}

func (f *fakeStrategy) Meta() strat.Meta { return f.meta }
func (f *fakeStrategy) WarmupBars() int  { return f.warmup }

func (f *fakeStrategy) Init(_ strat.Context, _ string, _ map[string]any, _ strat.State) (strat.State, error) {
	return &fakeState{data: "init"}, nil
}

func (f *fakeStrategy) OnBar(ctx strat.Context, symbol string, bar strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
	if f.onBarFunc != nil {
		return f.onBarFunc(ctx, symbol, bar, st)
	}
	return st, nil, nil
}

func (f *fakeStrategy) OnEvent(ctx strat.Context, symbol string, evt any, st strat.State) (strat.State, []strat.Signal, error) {
	if f.onEventFn != nil {
		return f.onEventFn(ctx, symbol, evt, st)
	}
	return st, nil, nil
}

// fakeState implements strat.State
type fakeState struct {
	data string
}

func (s *fakeState) Marshal() ([]byte, error) { return []byte(s.data), nil }
func (s *fakeState) Unmarshal(d []byte) error { s.data = string(d); return nil }

// --- Test context ---

type testCtx struct {
	now    time.Time
	logger *slog.Logger
}

func (c *testCtx) Now() time.Time              { return c.now }
func (c *testCtx) Logger() *slog.Logger        { return c.logger }
func (c *testCtx) EmitDomainEvent(_ any) error { return nil }

func newTestCtx() *testCtx {
	return &testCtx{
		now:    time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC),
		logger: slog.Default(),
	}
}

// --- Router tests ---

func TestRouter_RegisterAndLookup(t *testing.T) {
	r := strategy.NewRouter()
	fs := newFakeStrategy("test_strat", "1.0.0")
	id, _ := strat.NewInstanceID("test_strat:1.0.0:AAPL")

	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL", "MSFT"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	r.Register(inst)

	got := r.InstancesForSymbol("AAPL")
	require.Len(t, got, 1)
	assert.Equal(t, id, got[0].ID())

	got = r.InstancesForSymbol("MSFT")
	require.Len(t, got, 1)
	assert.Equal(t, id, got[0].ID())

	got = r.InstancesForSymbol("TSLA")
	assert.Empty(t, got)
}

func TestRouter_PriorityOrdering(t *testing.T) {
	r := strategy.NewRouter()

	lowID, _ := strat.NewInstanceID("low:1.0.0:AAPL")
	lowInst := strategy.NewInstance(lowID, newFakeStrategy("low_strat", "1.0.0"), nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 50,
	}, strat.LifecycleLiveActive, nil)

	highID, _ := strat.NewInstanceID("high:1.0.0:AAPL")
	highInst := strategy.NewInstance(highID, newFakeStrategy("high_strat", "1.0.0"), nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 200,
	}, strat.LifecycleLiveActive, nil)

	r.Register(lowInst)
	r.Register(highInst)

	got := r.InstancesForSymbol("AAPL")
	require.Len(t, got, 2)
	assert.Equal(t, highID, got[0].ID(), "higher priority should come first")
	assert.Equal(t, lowID, got[1].ID())
}

func TestRouter_Unregister(t *testing.T) {
	r := strategy.NewRouter()
	id, _ := strat.NewInstanceID("test:1.0.0:AAPL")
	inst := strategy.NewInstance(id, newFakeStrategy("test_strat", "1.0.0"), nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	r.Register(inst)
	require.Len(t, r.InstancesForSymbol("AAPL"), 1)

	r.Unregister(id)
	assert.Empty(t, r.InstancesForSymbol("AAPL"))
}

func TestRouter_InactiveInstancesFiltered(t *testing.T) {
	r := strategy.NewRouter()
	id, _ := strat.NewInstanceID("test:1.0.0:AAPL")
	inst := strategy.NewInstance(id, newFakeStrategy("test_strat", "1.0.0"), nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleDeactivated, nil) // Not active

	r.Register(inst)
	got := r.InstancesForSymbol("AAPL")
	assert.Empty(t, got, "deactivated instance should not be returned")
}

func TestRouter_Symbols(t *testing.T) {
	r := strategy.NewRouter()
	id, _ := strat.NewInstanceID("test:1.0.0:multi")
	inst := strategy.NewInstance(id, newFakeStrategy("test_strat", "1.0.0"), nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL", "MSFT", "GOOGL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	r.Register(inst)
	syms := r.Symbols()
	assert.Equal(t, []string{"AAPL", "GOOGL", "MSFT"}, syms) // sorted
}

// --- Instance tests ---

func TestInstance_InitAndOnBar(t *testing.T) {
	barCount := 0
	fs := newFakeStrategy("test_strat", "1.0.0")
	fs.onBarFunc = func(_ strat.Context, symbol string, bar strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		barCount++
		if barCount >= 3 {
			iid, _ := strat.NewInstanceID("test:1.0.0:" + symbol)
			sig, _ := strat.NewSignal(iid, symbol, strat.SignalEntry, strat.SideBuy, 0.8, nil)
			return st, []strat.Signal{sig}, nil
		}
		return st, nil, nil
	}

	id, _ := strat.NewInstanceID("test:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	ctx := newTestCtx()
	err := inst.InitSymbol(ctx, "AAPL", nil)
	require.NoError(t, err)

	bar := strat.Bar{Time: time.Now(), Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}
	indicators := strat.IndicatorData{Volume: 10}

	// First two bars: no signal
	for i := 0; i < 2; i++ {
		signals, err := inst.OnBar(ctx, "AAPL", bar, indicators)
		require.NoError(t, err)
		assert.Empty(t, signals)
	}

	// Third bar: signal
	signals, err := inst.OnBar(ctx, "AAPL", bar, indicators)
	require.NoError(t, err)
	require.Len(t, signals, 1)
	assert.Equal(t, strat.SignalEntry, signals[0].Type)
}

func TestInstance_WarmupSuppressesSignals(t *testing.T) {
	fs := newFakeStrategy("test_strat", "1.0.0")
	fs.warmup = 3 // Need 3 bars before signals
	fs.onBarFunc = func(_ strat.Context, symbol string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		// Always emit a signal.
		iid, _ := strat.NewInstanceID("test:1.0.0:" + symbol)
		sig, _ := strat.NewSignal(iid, symbol, strat.SignalEntry, strat.SideBuy, 0.8, nil)
		return st, []strat.Signal{sig}, nil
	}

	id, _ := strat.NewInstanceID("test:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	ctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(ctx, "AAPL", nil))

	bar := strat.Bar{Time: time.Now(), Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}
	ind := strat.IndicatorData{}

	// First 3 bars: suppressed during warmup
	for i := 0; i < 3; i++ {
		signals, err := inst.OnBar(ctx, "AAPL", bar, ind)
		require.NoError(t, err)
		assert.Empty(t, signals, "bar %d should be suppressed during warmup", i)
	}

	// Fourth bar: signal should come through
	signals, err := inst.OnBar(ctx, "AAPL", bar, ind)
	require.NoError(t, err)
	require.Len(t, signals, 1, "signal should pass after warmup")
}

func TestInstance_InactiveProducesNoSignals(t *testing.T) {
	fs := newFakeStrategy("test_strat", "1.0.0")
	fs.onBarFunc = func(_ strat.Context, symbol string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		iid, _ := strat.NewInstanceID("test:1.0.0:" + symbol)
		sig, _ := strat.NewSignal(iid, symbol, strat.SignalEntry, strat.SideBuy, 0.8, nil)
		return st, []strat.Signal{sig}, nil
	}

	id, _ := strat.NewInstanceID("test:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleDeactivated, nil) // Not active

	ctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(ctx, "AAPL", nil))

	bar := strat.Bar{Time: time.Now(), Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}
	signals, err := inst.OnBar(ctx, "AAPL", bar, strat.IndicatorData{})
	require.NoError(t, err)
	assert.Empty(t, signals, "deactivated instance should not produce signals")
}

func TestInstance_UninitializedSymbolErrors(t *testing.T) {
	fs := newFakeStrategy("test_strat", "1.0.0")
	id, _ := strat.NewInstanceID("test:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	bar := strat.Bar{Time: time.Now(), Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}
	_, err := inst.OnBar(newTestCtx(), "AAPL", bar, strat.IndicatorData{})
	assert.Error(t, err, "should error on uninitialized symbol")
}

func TestInstance_LifecycleAccessors(t *testing.T) {
	fs := newFakeStrategy("test_strat", "1.0.0")
	id, _ := strat.NewInstanceID("test:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecyclePaperActive, nil)

	assert.True(t, inst.IsActive())
	assert.Equal(t, strat.LifecyclePaperActive, inst.Lifecycle())

	inst.SetLifecycle(strat.LifecycleDeactivated)
	assert.False(t, inst.IsActive())
	assert.Equal(t, strat.LifecycleDeactivated, inst.Lifecycle())
}

// --- MemRegistry tests ---

func TestMemRegistry_RegisterAndGet(t *testing.T) {
	reg := strategy.NewMemRegistry()
	fs := newFakeStrategy("test_strat", "1.0.0")

	err := reg.Register(fs)
	require.NoError(t, err)

	got, err := reg.Get("test_strat")
	require.NoError(t, err)
	assert.Equal(t, fs.Meta().ID, got.Meta().ID)
}

func TestMemRegistry_DuplicateRegistrationFails(t *testing.T) {
	reg := strategy.NewMemRegistry()
	fs := newFakeStrategy("test_strat", "1.0.0")

	require.NoError(t, reg.Register(fs))
	err := reg.Register(fs)
	assert.ErrorIs(t, err, strat.ErrStrategyExists)
}

func TestMemRegistry_GetNotFound(t *testing.T) {
	reg := strategy.NewMemRegistry()
	_, err := reg.Get("nonexistent")
	assert.ErrorIs(t, err, strat.ErrStrategyNotFound)
}

func TestMemRegistry_List(t *testing.T) {
	reg := strategy.NewMemRegistry()
	fs1 := newFakeStrategy("alpha_strat", "1.0.0")
	fs2 := newFakeStrategy("beta_strat", "1.0.0")

	require.NoError(t, reg.Register(fs1))
	require.NoError(t, reg.Register(fs2))

	ids := reg.List()
	assert.Len(t, ids, 2)
}

// --- Runner tests ---

func TestRunner_ProcessBar_NoInstances(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	ctx := context.Background()
	signals, err := runner.ProcessBar(ctx, "AAPL", strat.Bar{
		Time: time.Now(), Open: 100, High: 101, Low: 99, Close: 100, Volume: 10,
	}, strat.IndicatorData{})

	require.NoError(t, err)
	assert.Empty(t, signals)
}

func TestRunner_ProcessBar_SingleInstance_Signal(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	fs := newFakeStrategy("test_strat", "1.0.0")
	fs.onBarFunc = func(_ strat.Context, symbol string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		iid, _ := strat.NewInstanceID("test_strat:1.0.0:" + symbol)
		sig, _ := strat.NewSignal(iid, symbol, strat.SignalEntry, strat.SideBuy, 0.85, map[string]string{"trigger": "test"})
		return st, []strat.Signal{sig}, nil
	}

	id, _ := strat.NewInstanceID("test_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "AAPL", nil))
	router.Register(inst)

	ctx := context.Background()
	signals, err := runner.ProcessBar(ctx, "AAPL", strat.Bar{
		Time: time.Now(), Open: 100, High: 101, Low: 99, Close: 100, Volume: 10,
	}, strat.IndicatorData{Volume: 10})

	require.NoError(t, err)
	require.Len(t, signals, 1)
	assert.Equal(t, strat.SignalEntry, signals[0].Type)
	assert.Equal(t, strat.SideBuy, signals[0].Side)
	assert.Equal(t, "AAPL", signals[0].Symbol)
	assert.Equal(t, 0.85, signals[0].Strength)
}

func TestRunner_ProcessBar_DynamicMetricLabels(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	m := metrics.New("test", "test", "test", false)
	runner.SetMetrics(m)

	fs := newFakeStrategy("alpha_strat", "1.0.0")
	fs.onBarFunc = func(_ strat.Context, symbol string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		iid, _ := strat.NewInstanceID("alpha_strat:1.0.0:" + symbol)
		sig, _ := strat.NewSignal(iid, symbol, strat.SignalEntry, strat.SideBuy, 0.8, nil)
		return st, []strat.Signal{sig}, nil
	}

	id, _ := strat.NewInstanceID("alpha_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "AAPL", nil))
	router.Register(inst)

	ctx := context.Background()
	require.NoError(t, runner.Start(ctx))

	sym, _ := domain.NewSymbol("AAPL")
	bar, _ := domain.NewMarketBar(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC), sym, "1m", 100, 101, 99, 100, 10)
	ev, _ := domain.NewEvent(domain.EventMarketBarSanitized, "test-tenant", envMode, "bar-1", bar)
	require.NoError(t, bus.Publish(ctx, *ev))

	count := counterValue(t, m.Reg, "omo_strategy_signals_total", map[string]string{"strategy": "alpha_strat", "signal": "entry", "direction": "buy"})
	assert.Equal(t, float64(1), count)

	obs := histogramSampleCount(t, m.Reg, "omo_strategy_loop_duration_seconds", map[string]string{"strategy": "all", "phase": "handle_bar"})
	require.Equal(t, uint64(1), obs)
}

func TestRunner_ProcessBar_MultipleInstances(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	makeSignalingStrategy := func(id, side string) *fakeStrategy {
		fs := newFakeStrategy(id, "1.0.0")
		s := strat.SideBuy
		if side == "sell" {
			s = strat.SideSell
		}
		fs.onBarFunc = func(_ strat.Context, symbol string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
			iid, _ := strat.NewInstanceID(id + ":1.0.0:" + symbol)
			sig, _ := strat.NewSignal(iid, symbol, strat.SignalEntry, s, 0.7, nil)
			return st, []strat.Signal{sig}, nil
		}
		return fs
	}

	// Two strategies on the same symbol.
	fs1 := makeSignalingStrategy("strat_alpha", "buy")
	fs2 := makeSignalingStrategy("strat_beta", "sell")

	id1, _ := strat.NewInstanceID("strat_alpha:1.0.0:AAPL")
	inst1 := strategy.NewInstance(id1, fs1, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 200,
	}, strat.LifecycleLiveActive, nil)

	id2, _ := strat.NewInstanceID("strat_beta:1.0.0:AAPL")
	inst2 := strategy.NewInstance(id2, fs2, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst1.InitSymbol(tctx, "AAPL", nil))
	require.NoError(t, inst2.InitSymbol(tctx, "AAPL", nil))
	router.Register(inst1)
	router.Register(inst2)

	ctx := context.Background()
	signals, err := runner.ProcessBar(ctx, "AAPL", strat.Bar{
		Time: time.Now(), Open: 100, High: 101, Low: 99, Close: 100, Volume: 10,
	}, strat.IndicatorData{Volume: 10})

	require.NoError(t, err)
	require.Len(t, signals, 2, "both instances should produce signals")

	// Verify both sides present.
	sides := map[strat.Side]bool{}
	for _, sig := range signals {
		sides[sig.Side] = true
	}
	assert.True(t, sides[strat.SideBuy])
	assert.True(t, sides[strat.SideSell])
}

func TestRunner_HandleBar_SuppressesEquitySignalOutsideRTH(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	fs := newFakeStrategy("test_strat", "1.0.0")
	fs.onBarFunc = func(_ strat.Context, symbol string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		iid, _ := strat.NewInstanceID("test_strat:1.0.0:" + symbol)
		sig, _ := strat.NewSignal(iid, symbol, strat.SignalEntry, strat.SideBuy, 0.9, nil)
		return st, []strat.Signal{sig}, nil
	}

	id, _ := strat.NewInstanceID("test_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "AAPL", nil))
	router.Register(inst)

	// Subscribe to SignalCreated events.
	var received []domain.Event
	ctx := context.Background()
	require.NoError(t, bus.Subscribe(ctx, domain.EventSignalCreated, func(_ context.Context, ev domain.Event) error {
		received = append(received, ev)
		return nil
	}))

	require.NoError(t, runner.Start(ctx))

	// Publish a MarketBarSanitized event with pre-market time (7:00 AM ET = 12:00 UTC)
	preMarketTime := time.Date(2025, 3, 4, 12, 0, 0, 0, time.UTC)
	sym, _ := domain.NewSymbol("AAPL")
	bar, _ := domain.NewMarketBar(preMarketTime, sym, "1m", 100, 101, 99, 100, 10)
	ev, _ := domain.NewEvent(domain.EventMarketBarSanitized, "test-tenant", envMode, "bar-1", bar)
	require.NoError(t, bus.Publish(ctx, *ev))

	// Verify SignalCreated was NOT emitted (RTH gate suppressed it)
	assert.Empty(t, received, "equity signal outside RTH should be suppressed")
}

func TestRunner_HandleBar_AllowsCryptoSignalOutsideRTH(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	fs := newFakeStrategy("test_strat", "1.0.0")
	fs.onBarFunc = func(_ strat.Context, symbol string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		iid, _ := strat.NewInstanceID("test_strat:1.0.0:" + symbol)
		sig, _ := strat.NewSignal(iid, symbol, strat.SignalEntry, strat.SideBuy, 0.9, nil)
		return st, []strat.Signal{sig}, nil
	}

	id, _ := strat.NewInstanceID("test_strat:1.0.0:BTC/USD")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"BTC/USD"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "BTC/USD", nil))
	router.Register(inst)

	// Subscribe to SignalCreated events.
	var received []domain.Event
	ctx := context.Background()
	require.NoError(t, bus.Subscribe(ctx, domain.EventSignalCreated, func(_ context.Context, ev domain.Event) error {
		received = append(received, ev)
		return nil
	}))

	require.NoError(t, runner.Start(ctx))

	// Publish a MarketBarSanitized event with pre-market time (7:00 AM ET = 12:00 UTC)
	preMarketTime := time.Date(2025, 3, 4, 12, 0, 0, 0, time.UTC)
	sym, _ := domain.NewSymbol("BTC/USD")
	bar, _ := domain.NewMarketBar(preMarketTime, sym, "1m", 100, 101, 99, 100, 10)
	ev, _ := domain.NewEvent(domain.EventMarketBarSanitized, "test-tenant", envMode, "bar-1", bar)
	require.NoError(t, bus.Publish(ctx, *ev))

	// Verify SignalCreated WAS emitted (crypto not gated by RTH)
	require.Len(t, received, 1, "crypto signal outside RTH should be allowed")
	assert.Equal(t, domain.EventSignalCreated, received[0].Type)
}
func TestRunner_HandleBar_EmitsSignalCreated(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	fs := newFakeStrategy("test_strat", "1.0.0")
	fs.onBarFunc = func(_ strat.Context, symbol string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		iid, _ := strat.NewInstanceID("test_strat:1.0.0:" + symbol)
		sig, _ := strat.NewSignal(iid, symbol, strat.SignalEntry, strat.SideBuy, 0.9, nil)
		return st, []strat.Signal{sig}, nil
	}

	id, _ := strat.NewInstanceID("test_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "AAPL", nil))
	router.Register(inst)

	// Subscribe to SignalCreated events.
	var received []domain.Event
	ctx := context.Background()
	require.NoError(t, bus.Subscribe(ctx, domain.EventSignalCreated, func(_ context.Context, ev domain.Event) error {
		received = append(received, ev)
		return nil
	}))

	// Subscribe runner to MarketBarSanitized.
	require.NoError(t, runner.Start(ctx))

	// Publish a MarketBarSanitized event.
	sym, _ := domain.NewSymbol("AAPL")
	bar, _ := domain.NewMarketBar(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC), sym, "1m", 100, 101, 99, 100, 10)
	ev, _ := domain.NewEvent(domain.EventMarketBarSanitized, "test-tenant", envMode, "bar-1", bar)
	require.NoError(t, bus.Publish(ctx, *ev))

	// Verify SignalCreated was emitted.
	require.Len(t, received, 1)
	assert.Equal(t, domain.EventSignalCreated, received[0].Type)

	sig, ok := received[0].Payload.(strat.Signal)
	require.True(t, ok)
	assert.Equal(t, "AAPL", sig.Symbol)
	assert.Equal(t, strat.SignalEntry, sig.Type)
	assert.Equal(t, strat.SideBuy, sig.Side)
}

func TestRunner_HandleBar_NoSignalForUnassignedSymbol(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	// Register instance for AAPL only.
	fs := newFakeStrategy("test_strat", "1.0.0")
	fs.onBarFunc = func(_ strat.Context, symbol string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		iid, _ := strat.NewInstanceID("test_strat:1.0.0:" + symbol)
		sig, _ := strat.NewSignal(iid, symbol, strat.SignalEntry, strat.SideBuy, 0.9, nil)
		return st, []strat.Signal{sig}, nil
	}
	id, _ := strat.NewInstanceID("test_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)
	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "AAPL", nil))
	router.Register(inst)

	var received []domain.Event
	ctx := context.Background()
	require.NoError(t, bus.Subscribe(ctx, domain.EventSignalCreated, func(_ context.Context, ev domain.Event) error {
		received = append(received, ev)
		return nil
	}))
	require.NoError(t, runner.Start(ctx))

	// Publish bar for TSLA (not assigned).
	sym, _ := domain.NewSymbol("TSLA")
	bar, _ := domain.NewMarketBar(time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC), sym, "1m", 200, 202, 199, 201, 20)
	ev, _ := domain.NewEvent(domain.EventMarketBarSanitized, "test-tenant", envMode, "bar-tsla-1", bar)
	require.NoError(t, bus.Publish(ctx, *ev))

	assert.Empty(t, received, "no signal for unassigned symbol")
}

func TestRunner_WarmUp_ReplaysBarsAndWarmsInstance(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	fs := newFakeStrategy("test_strat", "1.0.0")
	fs.warmup = 3
	callCount := 0
	fs.onBarFunc = func(_ strat.Context, symbol string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		callCount++
		iid, _ := strat.NewInstanceID("test_strat:1.0.0:" + symbol)
		sig, _ := strat.NewSignal(iid, symbol, strat.SignalEntry, strat.SideBuy, 0.8, nil)
		return st, []strat.Signal{sig}, nil
	}

	id, _ := strat.NewInstanceID("test_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{Symbols: []string{"AAPL"}, Priority: 100}, strat.LifecycleLiveActive, nil)
	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "AAPL", nil))
	router.Register(inst)

	sym, _ := domain.NewSymbol("AAPL")
	b1, _ := domain.NewMarketBar(time.Now().Add(-3*time.Minute), sym, "1m", 100, 101, 99, 100, 10)
	b2, _ := domain.NewMarketBar(time.Now().Add(-2*time.Minute), sym, "1m", 101, 102, 100, 101, 11)
	b3, _ := domain.NewMarketBar(time.Now().Add(-1*time.Minute), sym, "1m", 102, 103, 101, 102, 12)
	bars := []domain.MarketBar{b1, b2, b3}

	snapCalls := 0
	snapshotFn := func(bar domain.MarketBar) strat.IndicatorData {
		snapCalls++
		return strat.IndicatorData{RSI: 50, Volume: bar.Volume}
	}

	n := runner.WarmUp("AAPL", bars, snapshotFn)
	assert.Equal(t, 3, n)
	assert.Equal(t, 3, snapCalls)
	assert.Equal(t, 3, callCount)
	assert.True(t, inst.IsWarmedUp("AAPL"))

	ctx := context.Background()
	sigs, err := runner.ProcessBar(ctx, "AAPL", strat.Bar{Time: time.Now(), Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}, strat.IndicatorData{Volume: 10})
	require.NoError(t, err)
	require.Len(t, sigs, 1)
}

func TestRunner_WarmUp_UsesReplayOnBarForReplayableStrategy(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	fs := &fakeReplayableStrategy{}
	fs.meta = func() strat.Meta {
		sid, _ := strat.NewStrategyID("replay_strat")
		ver, _ := strat.NewVersion("1.0.0")
		return strat.Meta{ID: sid, Version: ver, Name: "Replay Test", Author: "test"}
	}()
	fs.warmup = 3

	id, _ := strat.NewInstanceID("replay_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{Symbols: []string{"AAPL"}, Priority: 100}, strat.LifecycleLiveActive, nil)
	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "AAPL", nil))
	router.Register(inst)

	sym, _ := domain.NewSymbol("AAPL")
	b1, _ := domain.NewMarketBar(time.Now().Add(-3*time.Minute), sym, "1m", 100, 101, 99, 100, 10)
	b2, _ := domain.NewMarketBar(time.Now().Add(-2*time.Minute), sym, "1m", 101, 102, 100, 101, 11)
	b3, _ := domain.NewMarketBar(time.Now().Add(-1*time.Minute), sym, "1m", 102, 103, 101, 102, 12)
	bars := []domain.MarketBar{b1, b2, b3}

	snapshotFn := func(bar domain.MarketBar) strat.IndicatorData {
		return strat.IndicatorData{RSI: 50, Volume: bar.Volume}
	}

	n := runner.WarmUp("AAPL", bars, snapshotFn)
	assert.Equal(t, 3, n)
	assert.Equal(t, 3, fs.replayCount)
	assert.True(t, inst.IsWarmedUp("AAPL"))
}

func TestRunner_WarmUp_NoMatchingInstances(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	sym, _ := domain.NewSymbol("AAPL")
	bar, _ := domain.NewMarketBar(time.Now(), sym, "1m", 100, 101, 99, 100, 10)

	called := false
	snapshotFn := func(_ domain.MarketBar) strat.IndicatorData {
		called = true
		return strat.IndicatorData{}
	}

	n := runner.WarmUp("AAPL", []domain.MarketBar{bar}, snapshotFn)
	assert.Equal(t, 0, n)
	assert.False(t, called)
}

func histogramSampleCount(t *testing.T, reg *prometheus.Registry, metricName string, wantLabels map[string]string) uint64 {
	t.Helper()

	mfs, err := reg.Gather()
	require.NoError(t, err)

	for _, mf := range mfs {
		if mf.GetName() != metricName {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelsMatch(m.GetLabel(), wantLabels) {
				h := m.GetHistogram()
				require.NotNil(t, h)
				return h.GetSampleCount()
			}
		}
	}

	require.FailNow(t, "histogram metric not found", "%s labels=%v", metricName, wantLabels)
	return 0
}

func counterValue(t *testing.T, reg *prometheus.Registry, metricName string, wantLabels map[string]string) float64 {
	t.Helper()

	mfs, err := reg.Gather()
	require.NoError(t, err)

	for _, mf := range mfs {
		if mf.GetName() != metricName {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelsMatch(m.GetLabel(), wantLabels) {
				c := m.GetCounter()
				require.NotNil(t, c)
				return c.GetValue()
			}
		}
	}

	require.FailNow(t, "counter metric not found", "%s labels=%v", metricName, wantLabels)
	return 0
}

func labelsMatch(got []*dto.LabelPair, want map[string]string) bool {
	if len(want) == 0 {
		return true
	}

	gotMap := make(map[string]string, len(got))
	for _, lp := range got {
		gotMap[lp.GetName()] = lp.GetValue()
	}
	for k, v := range want {
		if gotMap[k] != v {
			return false
		}
	}
	return true
}

// --- handleFill / handleRejection routing tests ---

func TestRunner_HandleFill_RoutesToMatchingInstance(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	var gotEvt any
	var gotSymbol string
	fs := newFakeStrategy("alpha_strat", "1.0.0")
	fs.onEventFn = func(_ strat.Context, symbol string, evt any, st strat.State) (strat.State, []strat.Signal, error) {
		gotEvt = evt
		gotSymbol = symbol
		return st, nil, nil
	}

	id, _ := strat.NewInstanceID("alpha_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "AAPL", nil))
	router.Register(inst)

	ctx := context.Background()
	require.NoError(t, runner.Start(ctx))

	// Publish a FillReceived event with map payload (as execution service does).
	fillPayload := map[string]any{
		"broker_order_id": "ord-123",
		"intent_id":       "intent-1",
		"symbol":          "AAPL",
		"side":            "BUY",
		"quantity":        float64(10),
		"price":           float64(150.5),
		"filled_at":       time.Now(),
		"strategy":        "alpha_strat",
	}
	ev, _ := domain.NewEvent(domain.EventFillReceived, "test-tenant", envMode, "fill-1", fillPayload)
	require.NoError(t, bus.Publish(ctx, *ev))

	// Verify the strategy instance received a FillConfirmation.
	require.NotNil(t, gotEvt, "OnEvent should have been called")
	fill, ok := gotEvt.(strat.FillConfirmation)
	require.True(t, ok, "event should be FillConfirmation, got %T", gotEvt)
	assert.Equal(t, "AAPL", fill.Symbol)
	assert.Equal(t, strat.SideBuy, fill.Side)
	assert.Equal(t, float64(10), fill.Quantity)
	assert.Equal(t, float64(150.5), fill.Price)
	assert.Equal(t, "AAPL", gotSymbol)
}

func TestRunner_HandleFill_IgnoresUnknownSymbol(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	called := false
	fs := newFakeStrategy("alpha_strat", "1.0.0")
	fs.onEventFn = func(_ strat.Context, _ string, _ any, st strat.State) (strat.State, []strat.Signal, error) {
		called = true
		return st, nil, nil
	}

	id, _ := strat.NewInstanceID("alpha_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "AAPL", nil))
	router.Register(inst)

	ctx := context.Background()
	require.NoError(t, runner.Start(ctx))

	// Publish fill for a symbol no instance handles.
	fillPayload := map[string]any{
		"symbol":   "TSLA",
		"side":     "BUY",
		"quantity": float64(5),
		"price":    float64(200),
		"strategy": "alpha_strat",
	}
	ev, _ := domain.NewEvent(domain.EventFillReceived, "test-tenant", envMode, "fill-2", fillPayload)
	require.NoError(t, bus.Publish(ctx, *ev))

	assert.False(t, called, "OnEvent should not be called for unknown symbol")
}

func TestRunner_HandleFill_SellSide(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	var gotEvt any
	fs := newFakeStrategy("alpha_strat", "1.0.0")
	fs.onEventFn = func(_ strat.Context, _ string, evt any, st strat.State) (strat.State, []strat.Signal, error) {
		gotEvt = evt
		return st, nil, nil
	}

	id, _ := strat.NewInstanceID("alpha_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "AAPL", nil))
	router.Register(inst)

	ctx := context.Background()
	require.NoError(t, runner.Start(ctx))

	fillPayload := map[string]any{
		"symbol":   "AAPL",
		"side":     "SELL",
		"quantity": float64(5),
		"price":    float64(155),
		"strategy": "alpha_strat",
	}
	ev, _ := domain.NewEvent(domain.EventFillReceived, "test-tenant", envMode, "fill-3", fillPayload)
	require.NoError(t, bus.Publish(ctx, *ev))

	require.NotNil(t, gotEvt)
	fill, ok := gotEvt.(strat.FillConfirmation)
	require.True(t, ok)
	assert.Equal(t, strat.SideSell, fill.Side)
}

func TestRunner_HandleFill_IgnoresUnknownSide(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	called := false
	fs := newFakeStrategy("alpha_strat", "1.0.0")
	fs.onEventFn = func(_ strat.Context, _ string, _ any, st strat.State) (strat.State, []strat.Signal, error) {
		called = true
		return st, nil, nil
	}

	id, _ := strat.NewInstanceID("alpha_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "AAPL", nil))
	router.Register(inst)

	ctx := context.Background()
	require.NoError(t, runner.Start(ctx))

	fillPayload := map[string]any{
		"symbol":   "AAPL",
		"side":     "UNKNOWN_SIDE",
		"quantity": float64(5),
		"price":    float64(155),
		"strategy": "alpha_strat",
	}
	ev, _ := domain.NewEvent(domain.EventFillReceived, "test-tenant", envMode, "fill-4", fillPayload)
	require.NoError(t, bus.Publish(ctx, *ev))

	assert.False(t, called, "OnEvent should not be called for unknown side")
}

func TestRunner_HandleRejection_RoutesToMatchingInstance(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	var gotEvt any
	var gotSymbol string
	fs := newFakeStrategy("beta_strat", "1.0.0")
	fs.onEventFn = func(_ strat.Context, symbol string, evt any, st strat.State) (strat.State, []strat.Signal, error) {
		gotEvt = evt
		gotSymbol = symbol
		return st, nil, nil
	}

	id, _ := strat.NewInstanceID("beta_strat:1.0.0:MSFT")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols: []string{"MSFT"}, Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "MSFT", nil))
	router.Register(inst)

	ctx := context.Background()
	require.NoError(t, runner.Start(ctx))

	// Publish an OrderIntentRejected event with LONG direction (entry rejection).
	rejPayload := domain.OrderIntentEventPayload{
		ID:        "intent-1",
		Symbol:    "MSFT",
		Direction: string(domain.DirectionLong),
		Strategy:  "beta_strat",
		Reason:    "DTBP exhausted",
	}
	ev, _ := domain.NewEvent(domain.EventOrderIntentRejected, "test-tenant", envMode, "rej-1", rejPayload)
	require.NoError(t, bus.Publish(ctx, *ev))

	// Verify the strategy instance received an EntryRejection.
	require.NotNil(t, gotEvt, "OnEvent should have been called")
	rej, ok := gotEvt.(strat.EntryRejection)
	require.True(t, ok, "event should be EntryRejection, got %T", gotEvt)
	assert.Equal(t, "MSFT", rej.Symbol)
	assert.Equal(t, strat.SideBuy, rej.Side)
	assert.Equal(t, "DTBP exhausted", rej.Reason)
	assert.Equal(t, "MSFT", gotSymbol)
}

func TestRunner_HandleRejection_ShortDirection(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	var gotEvt any
	fs := newFakeStrategy("beta_strat", "1.0.0")
	fs.onEventFn = func(_ strat.Context, _ string, evt any, st strat.State) (strat.State, []strat.Signal, error) {
		gotEvt = evt
		return st, nil, nil
	}

	id, _ := strat.NewInstanceID("beta_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "AAPL", nil))
	router.Register(inst)

	ctx := context.Background()
	require.NoError(t, runner.Start(ctx))

	rejPayload := domain.OrderIntentEventPayload{
		ID:        "intent-2",
		Symbol:    "AAPL",
		Direction: string(domain.DirectionShort),
		Strategy:  "beta_strat",
		Reason:    "risk limit",
	}
	ev, _ := domain.NewEvent(domain.EventOrderIntentRejected, "test-tenant", envMode, "rej-2", rejPayload)
	require.NoError(t, bus.Publish(ctx, *ev))

	require.NotNil(t, gotEvt)
	rej, ok := gotEvt.(strat.EntryRejection)
	require.True(t, ok)
	assert.Equal(t, strat.SideSell, rej.Side)
	assert.Equal(t, "risk limit", rej.Reason)
}

func TestRunner_HandleRejection_IgnoresExitRejection(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	called := false
	fs := newFakeStrategy("beta_strat", "1.0.0")
	fs.onEventFn = func(_ strat.Context, _ string, _ any, st strat.State) (strat.State, []strat.Signal, error) {
		called = true
		return st, nil, nil
	}

	id, _ := strat.NewInstanceID("beta_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "AAPL", nil))
	router.Register(inst)

	ctx := context.Background()
	require.NoError(t, runner.Start(ctx))

	// CLOSE_LONG is an exit rejection — should be ignored.
	rejPayload := domain.OrderIntentEventPayload{
		ID:        "intent-3",
		Symbol:    "AAPL",
		Direction: string(domain.DirectionCloseLong),
		Strategy:  "beta_strat",
		Reason:    "slippage",
	}
	ev, _ := domain.NewEvent(domain.EventOrderIntentRejected, "test-tenant", envMode, "rej-3", rejPayload)
	require.NoError(t, bus.Publish(ctx, *ev))

	assert.False(t, called, "OnEvent should not be called for exit rejections")
}

func TestRunner_HandleRejection_IgnoresUnknownSymbol(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	called := false
	fs := newFakeStrategy("beta_strat", "1.0.0")
	fs.onEventFn = func(_ strat.Context, _ string, _ any, st strat.State) (strat.State, []strat.Signal, error) {
		called = true
		return st, nil, nil
	}

	id, _ := strat.NewInstanceID("beta_strat:1.0.0:AAPL")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "AAPL", nil))
	router.Register(inst)

	ctx := context.Background()
	require.NoError(t, runner.Start(ctx))

	rejPayload := domain.OrderIntentEventPayload{
		ID:        "intent-4",
		Symbol:    "TSLA",
		Direction: string(domain.DirectionLong),
		Strategy:  "beta_strat",
		Reason:    "DTBP exhausted",
	}
	ev, _ := domain.NewEvent(domain.EventOrderIntentRejected, "test-tenant", envMode, "rej-4", rejPayload)
	require.NoError(t, bus.Publish(ctx, *ev))

	assert.False(t, called, "OnEvent should not be called for unknown symbol")
}
