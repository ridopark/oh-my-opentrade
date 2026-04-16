package strategy_test

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock CrossSectionalStrategy ---

type mockXSecStrategy struct {
	fakeStrategy
	universe []string

	mu    sync.Mutex
	calls []xsecCall
	// returnSignals is the set of signals OnCrossSectionalBar will return.
	returnSignals []strat.Signal
	returnErr     error
	// returnState, if non-nil, is returned instead of the input state.
	returnState strat.State
}

type xsecCall struct {
	Ts   time.Time
	Bars map[string]strat.Bar
}

func (m *mockXSecStrategy) OnCrossSectionalBar(_ strat.Context, ts time.Time, bars map[string]strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Deep copy bars so test assertions aren't affected by buffer reuse.
	copied := make(map[string]strat.Bar, len(bars))
	for k, v := range bars {
		copied[k] = v
	}
	m.calls = append(m.calls, xsecCall{Ts: ts, Bars: copied})

	if m.returnErr != nil {
		return st, nil, m.returnErr
	}
	nextState := st
	if m.returnState != nil {
		nextState = m.returnState
	}
	return nextState, m.returnSignals, nil
}

func (m *mockXSecStrategy) Universe() []string {
	return m.universe
}

func (m *mockXSecStrategy) getCalls() []xsecCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]xsecCall, len(m.calls))
	copy(out, m.calls)
	return out
}

func newMockXSecStrategy(universe []string) *mockXSecStrategy {
	fs := newFakeStrategy("xsec_test", "1.0.0")
	return &mockXSecStrategy{
		fakeStrategy: *fs,
		universe:     universe,
	}
}

// --- Tests ---

func TestXSecRunner_CompleteCrossSection(t *testing.T) {
	t.Run("3 symbols all arrive at same timestamp", func(t *testing.T) {
		mock := newMockXSecStrategy([]string{"BTC", "ETH", "SOL"})
		sid, _ := strat.NewInstanceID("xsec:1.0.0:universe")
		sig, _ := strat.NewSignal(sid, "BTC", strat.SignalEntry, strat.SideBuy, 0.8, nil)
		mock.returnSignals = []strat.Signal{sig}

		runner := strategy.NewXSecRunner(mock, strategy.XSecRunnerConfig{}, nil)
		runner.SetState(&fakeState{data: "initial"})

		ctx := newTestCtx()
		ts := time.Date(2026, 4, 15, 14, 30, 0, 0, time.UTC)

		// First two symbols: no dispatch yet.
		sigs, err := runner.OnBar(ctx, "BTC", strat.Bar{Time: ts, Close: 60000})
		require.NoError(t, err)
		assert.Empty(t, sigs)

		sigs, err = runner.OnBar(ctx, "ETH", strat.Bar{Time: ts, Close: 3000})
		require.NoError(t, err)
		assert.Empty(t, sigs)

		// Third symbol completes the cross-section.
		sigs, err = runner.OnBar(ctx, "SOL", strat.Bar{Time: ts, Close: 150})
		require.NoError(t, err)
		require.Len(t, sigs, 1)
		assert.Equal(t, "BTC", sigs[0].Symbol)

		calls := mock.getCalls()
		require.Len(t, calls, 1)
		assert.Len(t, calls[0].Bars, 3)
		assert.Equal(t, ts.UTC().Truncate(time.Minute), calls[0].Ts)
	})
}

func TestXSecRunner_PartialArrival(t *testing.T) {
	t.Run("2 of 3 symbols arrive, no dispatch in backtest mode", func(t *testing.T) {
		mock := newMockXSecStrategy([]string{"BTC", "ETH", "SOL"})
		runner := strategy.NewXSecRunner(mock, strategy.XSecRunnerConfig{}, nil)
		runner.SetState(&fakeState{data: "init"})
		ctx := newTestCtx()
		ts := time.Date(2026, 4, 15, 14, 30, 0, 0, time.UTC)

		sigs, err := runner.OnBar(ctx, "BTC", strat.Bar{Time: ts, Close: 60000})
		require.NoError(t, err)
		assert.Empty(t, sigs)

		sigs, err = runner.OnBar(ctx, "ETH", strat.Bar{Time: ts, Close: 3000})
		require.NoError(t, err)
		assert.Empty(t, sigs)

		// No third bar → no dispatch.
		assert.Equal(t, 1, runner.BufferedTimestamps())
		assert.Empty(t, mock.getCalls())
	})
}

func TestXSecRunner_OutOfOrderArrival(t *testing.T) {
	t.Run("bars arrive in different order but still dispatch correctly", func(t *testing.T) {
		mock := newMockXSecStrategy([]string{"A", "B", "C"})
		runner := strategy.NewXSecRunner(mock, strategy.XSecRunnerConfig{}, nil)
		runner.SetState(&fakeState{data: "init"})
		ctx := newTestCtx()
		ts := time.Date(2026, 4, 15, 14, 30, 0, 0, time.UTC)

		// Arrive: C, A, B (out of alphabetical order).
		_, _ = runner.OnBar(ctx, "C", strat.Bar{Time: ts, Close: 30})
		_, _ = runner.OnBar(ctx, "A", strat.Bar{Time: ts, Close: 10})
		sigs, err := runner.OnBar(ctx, "B", strat.Bar{Time: ts, Close: 20})
		require.NoError(t, err)
		assert.Empty(t, sigs) // mock returns nil signals by default

		calls := mock.getCalls()
		require.Len(t, calls, 1)
		assert.Contains(t, calls[0].Bars, "A")
		assert.Contains(t, calls[0].Bars, "B")
		assert.Contains(t, calls[0].Bars, "C")
	})
}

func TestXSecRunner_MultipleTimestamps(t *testing.T) {
	t.Run("bars for ts1 and ts2 interleaved, each dispatched separately", func(t *testing.T) {
		mock := newMockXSecStrategy([]string{"X", "Y"})
		runner := strategy.NewXSecRunner(mock, strategy.XSecRunnerConfig{}, nil)
		runner.SetState(&fakeState{data: "init"})
		ctx := newTestCtx()

		ts1 := time.Date(2026, 4, 15, 14, 30, 0, 0, time.UTC)
		ts2 := time.Date(2026, 4, 15, 14, 31, 0, 0, time.UTC)

		// Interleave: X@ts1, X@ts2, Y@ts1, Y@ts2
		_, _ = runner.OnBar(ctx, "X", strat.Bar{Time: ts1, Close: 100})
		_, _ = runner.OnBar(ctx, "X", strat.Bar{Time: ts2, Close: 101})

		// Y@ts1 completes ts1 cross-section.
		sigs, err := runner.OnBar(ctx, "Y", strat.Bar{Time: ts1, Close: 200})
		require.NoError(t, err)
		_ = sigs

		// Y@ts2 completes ts2 cross-section.
		sigs, err = runner.OnBar(ctx, "Y", strat.Bar{Time: ts2, Close: 201})
		require.NoError(t, err)
		_ = sigs

		calls := mock.getCalls()
		require.Len(t, calls, 2)

		// First dispatch is ts1.
		assert.Equal(t, ts1.UTC().Truncate(time.Minute), calls[0].Ts)
		assert.InDelta(t, 100.0, calls[0].Bars["X"].Close, 0.01)

		// Second dispatch is ts2.
		assert.Equal(t, ts2.UTC().Truncate(time.Minute), calls[1].Ts)
		assert.InDelta(t, 101.0, calls[1].Bars["X"].Close, 0.01)
	})
}

func TestXSecRunner_GraceWindow(t *testing.T) {
	t.Run("timer fires before all bars arrive, dispatches partial", func(t *testing.T) {
		mock := newMockXSecStrategy([]string{"BTC", "ETH", "SOL"})
		runner := strategy.NewXSecRunner(mock, strategy.XSecRunnerConfig{
			GraceWindow: 50 * time.Millisecond,
		}, slog.Default())
		runner.SetState(&fakeState{data: "init"})
		ctx := newTestCtx()
		ts := time.Date(2026, 4, 15, 14, 30, 0, 0, time.UTC)

		// Only 2 of 3 arrive.
		_, err := runner.OnBar(ctx, "BTC", strat.Bar{Time: ts, Close: 60000})
		require.NoError(t, err)
		_, err = runner.OnBar(ctx, "ETH", strat.Bar{Time: ts, Close: 3000})
		require.NoError(t, err)

		// Wait for grace window to fire.
		time.Sleep(150 * time.Millisecond)

		calls := mock.getCalls()
		require.Len(t, calls, 1, "grace timer should have flushed partial cross-section")
		assert.Len(t, calls[0].Bars, 2)
		assert.Contains(t, calls[0].Bars, "BTC")
		assert.Contains(t, calls[0].Bars, "ETH")

		// Buffer should be cleaned up.
		assert.Equal(t, 0, runner.BufferedTimestamps())
	})
}

func TestXSecRunner_GraceWindow_CancelledOnComplete(t *testing.T) {
	t.Run("grace timer cancelled when cross-section completes normally", func(t *testing.T) {
		mock := newMockXSecStrategy([]string{"A", "B"})
		runner := strategy.NewXSecRunner(mock, strategy.XSecRunnerConfig{
			GraceWindow: 200 * time.Millisecond,
		}, nil)
		runner.SetState(&fakeState{data: "init"})
		ctx := newTestCtx()
		ts := time.Date(2026, 4, 15, 14, 30, 0, 0, time.UTC)

		_, _ = runner.OnBar(ctx, "A", strat.Bar{Time: ts, Close: 10})
		_, _ = runner.OnBar(ctx, "B", strat.Bar{Time: ts, Close: 20})

		// Complete arrival → dispatched synchronously, timer cancelled.
		calls := mock.getCalls()
		require.Len(t, calls, 1)

		// Wait past grace window — should NOT double-dispatch.
		time.Sleep(300 * time.Millisecond)
		calls = mock.getCalls()
		assert.Len(t, calls, 1, "should not dispatch twice")
	})
}

func TestXSecRunner_EmptyUniverse(t *testing.T) {
	t.Run("empty universe means no symbols expected", func(t *testing.T) {
		mock := newMockXSecStrategy([]string{})
		runner := strategy.NewXSecRunner(mock, strategy.XSecRunnerConfig{}, nil)
		assert.Equal(t, 0, runner.UniverseSize())
	})
}

func TestXSecRunner_UnknownSymbol(t *testing.T) {
	t.Run("bar for symbol not in universe returns error", func(t *testing.T) {
		mock := newMockXSecStrategy([]string{"BTC"})
		runner := strategy.NewXSecRunner(mock, strategy.XSecRunnerConfig{}, nil)
		runner.SetState(&fakeState{data: "init"})
		ctx := newTestCtx()
		ts := time.Date(2026, 4, 15, 14, 30, 0, 0, time.UTC)

		_, err := runner.OnBar(ctx, "DOGE", strat.Bar{Time: ts, Close: 0.1})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not in universe")
	})
}

func TestXSecRunner_StatePersistence(t *testing.T) {
	t.Run("state returned from OnCrossSectionalBar is passed to next call", func(t *testing.T) {
		mock := newMockXSecStrategy([]string{"A"})
		secondState := &fakeState{data: "updated"}
		mock.returnState = secondState

		runner := strategy.NewXSecRunner(mock, strategy.XSecRunnerConfig{}, nil)
		initialState := &fakeState{data: "initial"}
		runner.SetState(initialState)
		ctx := newTestCtx()

		ts1 := time.Date(2026, 4, 15, 14, 30, 0, 0, time.UTC)
		ts2 := time.Date(2026, 4, 15, 14, 31, 0, 0, time.UTC)

		// First dispatch: returns secondState.
		_, err := runner.OnBar(ctx, "A", strat.Bar{Time: ts1, Close: 100})
		require.NoError(t, err)

		// Runner's state should now be secondState.
		assert.Equal(t, secondState, runner.State())

		// Change what mock returns for the second call.
		thirdState := &fakeState{data: "third"}
		mock.returnState = thirdState

		_, err = runner.OnBar(ctx, "A", strat.Bar{Time: ts2, Close: 101})
		require.NoError(t, err)

		assert.Equal(t, thirdState, runner.State())
	})
}

func TestXSecRunner_DrainPendingSignals(t *testing.T) {
	t.Run("pending signals from grace flushes are drainable", func(t *testing.T) {
		sid, _ := strat.NewInstanceID("xsec:1.0.0:universe")
		sig, _ := strat.NewSignal(sid, "BTC", strat.SignalEntry, strat.SideBuy, 0.9, nil)

		mock := newMockXSecStrategy([]string{"BTC", "ETH"})
		mock.returnSignals = []strat.Signal{sig}

		runner := strategy.NewXSecRunner(mock, strategy.XSecRunnerConfig{
			GraceWindow: 50 * time.Millisecond,
		}, nil)
		runner.SetState(&fakeState{data: "init"})
		ctx := newTestCtx()
		ts := time.Date(2026, 4, 15, 14, 30, 0, 0, time.UTC)

		// Only 1 of 2 arrives → grace timer will fire.
		_, err := runner.OnBar(ctx, "BTC", strat.Bar{Time: ts, Close: 60000})
		require.NoError(t, err)

		// Wait for grace flush.
		time.Sleep(150 * time.Millisecond)

		pending := runner.DrainPendingSignals()
		require.Len(t, pending, 1)
		assert.Equal(t, "BTC", pending[0].Symbol)

		// Second drain should be empty.
		assert.Empty(t, runner.DrainPendingSignals())
	})
}

func TestIsXSec(t *testing.T) {
	t.Run("returns true for CrossSectionalStrategy", func(t *testing.T) {
		mock := newMockXSecStrategy([]string{"BTC"})
		assert.True(t, strategy.IsXSec(mock))
	})

	t.Run("returns false for plain Strategy", func(t *testing.T) {
		plain := newFakeStrategy("plain", "1.0.0")
		assert.False(t, strategy.IsXSec(plain))
	})
}
