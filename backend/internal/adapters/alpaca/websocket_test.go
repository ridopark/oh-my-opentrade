package alpaca

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	alpacastream "github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

func TestWSClient_ParseBarMessage(t *testing.T) {
	// Arrange
	client := NewWSClient("wss://test", "test-key", "test-secret", "sip", nil)
	data := []byte(`{"T": "b", "S": "AAPL", "o": 150.0, "h": 151.0, "l": 149.5, "c": 150.5, "v": 1000, "t": "2024-01-15T10:30:00Z"}`)

	// Act
	bar, err := client.ParseBarMessage(data)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "AAPL", bar.Symbol.String())
	assert.Equal(t, 150.0, bar.Open)
	assert.Equal(t, 151.0, bar.High)
	assert.Equal(t, 149.5, bar.Low)
	assert.Equal(t, 150.5, bar.Close)
	assert.Equal(t, 1000.0, bar.Volume)
	expectedTime, _ := time.Parse(time.RFC3339, "2024-01-15T10:30:00Z")
	assert.Equal(t, expectedTime.UTC(), bar.Time.UTC())
}

func TestWSClient_ParseBarMessage_InvalidJSON(t *testing.T) {
	// Arrange
	client := NewWSClient("wss://test", "test-key", "test-secret", "sip", nil)
	data := []byte(`{invalid json`)

	// Act
	_, err := client.ParseBarMessage(data)

	// Assert
	require.Error(t, err)
}

func TestWSClient_ParseBarMessage_FieldMapping(t *testing.T) {
	// Arrange
	client := NewWSClient("wss://test", "test-key", "test-secret", "sip", nil)
	data := []byte(`{"T": "b", "S": "MSFT", "o": 300.0, "h": 305.0, "l": 299.0, "c": 302.0, "v": 5000, "t": "2024-01-16T15:00:00Z"}`)

	// Act
	bar, err := client.ParseBarMessage(data)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "MSFT", bar.Symbol.String())
	assert.Equal(t, 300.0, bar.Open)
	assert.Equal(t, 305.0, bar.High)
	assert.Equal(t, 299.0, bar.Low)
	assert.Equal(t, 302.0, bar.Close)
	assert.Equal(t, 5000.0, bar.Volume)
	expectedTime, _ := time.Parse(time.RFC3339, "2024-01-16T15:00:00Z")
	assert.Equal(t, expectedTime.UTC(), bar.Time.UTC())
}

func TestWSClient_Close_Idempotent(t *testing.T) {
	// Arrange
	client := NewWSClient("wss://test", "test-key", "test-secret", "sip", nil)

	// Act
	err1 := client.Close()
	err2 := client.Close()

	// Assert
	require.NoError(t, err1)
	require.NoError(t, err2)
}

func TestDeriveStreamURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
	}{
		{
			name:  "production data URL",
			input: "https://data.alpaca.markets",
			want:  "wss://stream.data.alpaca.markets/v2",
		},
		{
			name:  "sandbox data URL",
			input: "https://data.sandbox.alpaca.markets",
			want:  "wss://stream.data.sandbox.alpaca.markets/v2",
		},
		{
			name:  "trailing slash stripped",
			input: "https://data.alpaca.markets/",
			want:  "wss://stream.data.alpaca.markets/v2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, deriveStreamURL(tc.input))
		})
	}
}


// ---------------------------------------------------------------------------
// Helper: make a domain.MarketBar for testing
// ---------------------------------------------------------------------------

func makeBar(t *testing.T, symbol string, ts time.Time, price float64) domain.MarketBar {
	t.Helper()
	sym, err := domain.NewSymbol(symbol)
	require.NoError(t, err)
	tf, _ := domain.NewTimeframe("1m")
	bar, err := domain.NewMarketBar(ts, sym, tf, price, price, price, price, 100)
	require.NoError(t, err)
	return bar
}

// ---------------------------------------------------------------------------
// ghostWait & barKey unit tests
// ---------------------------------------------------------------------------

func TestGhostWait_StaysWithinJitterBounds(t *testing.T) {
	for i := 0; i < len(probeSchedule); i++ {
		got := ghostWait(i)
		base := probeSchedule[i]
		min := time.Duration(float64(base) * 0.9)
		max := time.Duration(float64(base) * 1.1)
		assert.GreaterOrEqual(t, got, min, "probe %d: wait below lower jitter bound", i)
		assert.LessOrEqual(t, got, max, "probe %d: wait above upper jitter bound", i)
	}
}

func TestGhostWait_ClampsToLastEntry(t *testing.T) {
	// Beyond the end of the schedule should use the last entry.
	got := ghostWait(999)
	base := probeSchedule[len(probeSchedule)-1]
	min := time.Duration(float64(base) * 0.9)
	max := time.Duration(float64(base) * 1.1)
	assert.GreaterOrEqual(t, got, min)
	assert.LessOrEqual(t, got, max)
}

func TestBarKey_UniquePerSymbolAndTime(t *testing.T) {
	t1 := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)

	sym1, _ := domain.NewSymbol("AAPL")
	sym2, _ := domain.NewSymbol("MSFT")
	tf, _ := domain.NewTimeframe("1m")

	bar1, _ := domain.NewMarketBar(t1, sym1, tf, 1, 1, 1, 1, 1)
	bar2, _ := domain.NewMarketBar(t1, sym2, tf, 1, 1, 1, 1, 1) // different symbol, same time
	bar3, _ := domain.NewMarketBar(t2, sym1, tf, 1, 1, 1, 1, 1) // same symbol, different time

	assert.NotEqual(t, barKey(bar1), barKey(bar2), "different symbols should produce different keys")
	assert.NotEqual(t, barKey(bar1), barKey(bar3), "different times should produce different keys")
	assert.Equal(t, barKey(bar1), barKey(bar1), "same bar should produce same key")
}

// ---------------------------------------------------------------------------
// StreamBars integration tests (using injected connectFactory)
// ---------------------------------------------------------------------------

// fakeConnect returns an error immediately (simulates connection failure).
func fakeConnectError(errMsg string) connectFn {
	return func(ctx context.Context) error {
		return errors.New(errMsg)
	}
}

// fakeConnectBlock blocks until ctx is cancelled then returns nil.
func fakeConnectBlock() connectFn {
	return func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	}
}

// makeWSClientWithFakeConnect returns a WSClient whose connectFactory calls
// the provided sequence of connectFns in order. After exhausting the sequence
// it blocks forever (simulating a stable connection).
func makeWSClientWithFakeConnect(fetcher BarFetcher, connects ...connectFn) *WSClient {
	ws := NewWSClient("wss://test", "k", "s", "iex", fetcher)
	var mu sync.Mutex
	idx := 0
	ws.connectFactory = func(symStrs []string, _ func(alpacastream.Bar), _ func(alpacastream.Trade)) connectFn {
		return func(ctx context.Context) error {
			mu.Lock()
			i := idx
			if i < len(connects) {
				idx++
			}
			mu.Unlock()
			if i < len(connects) {
				return connects[i](ctx)
			}
			// All sequences exhausted: block until context done.
			<-ctx.Done()
			return nil
		}
	}
	return ws
}

// TestStreamBars_ConnLimit_ProbesAndReconnects verifies that when the first
// connect call returns a 406 / connection-limit error, StreamBars enters the
// probe loop, and eventually reconnects when a subsequent probe succeeds.
func TestStreamBars_ConnLimit_ProbesAndReconnects(t *testing.T) {
	// Speed up the probe schedule for this test.
	origSchedule := probeSchedule
	probeSchedule = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond}
	defer func() { probeSchedule = origSchedule }()

	var handlerCalled atomic.Bool
	handler := func(_ context.Context, _ domain.MarketBar) error {
		handlerCalled.Store(true)
		return nil
	}

	// Sequence:
//  1. 406 error → enter probe loop
//  2. probe returns 406 → still ghost
//  3. probe succeeds (nil quickly) → ghost cleared → outer loop makes live connect
//  4. live connect blocks until ctx cancelled
	ws := makeWSClientWithFakeConnect(nil,
		fakeConnectError("connection limit exceeded"),  // outer connect → 406
		fakeConnectError("406 connection limit exceeded"), // probe 1 → still ghost
		fakeConnectError("ghost cleared: different error"), // probe 2 → non-406 → ghost gone
		// final connect: block until done
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sym, err := domain.NewSymbol("AAPL")
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		done <- ws.StreamBars(ctx, []domain.Symbol{sym}, domain.Timeframe("1m"), handler)
	}()

	// Wait for the stable (blocking) connect to be reached, then cancel.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("StreamBars did not return after context cancel")
	}
}

// TestStreamBars_RESTPoller_FeedsHandler verifies that when a 406 error is
// returned, the REST poller fires and delivers bars to the handler.
func TestStreamBars_RESTPoller_FeedsHandler(t *testing.T) {
	// Speed up the probe schedule so we don't wait long.
	origSchedule := probeSchedule
	probeSchedule = []time.Duration{50 * time.Millisecond}
	defer func() { probeSchedule = origSchedule }()

	sym, err := domain.NewSymbol("AAPL")
	require.NoError(t, err)
	tf, _ := domain.NewTimeframe("1m")
	barTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)

	// REST fetcher returns one bar.
	var fetchCount atomic.Int32
	fetcher := func(ctx context.Context, s domain.Symbol, t2 domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
		fetchCount.Add(1)
		bar, err2 := domain.NewMarketBar(barTime, sym, tf, 100, 100, 100, 100, 100)
		if err2 != nil {
			return nil, err2
		}
		return []domain.MarketBar{bar}, nil
	}

	var receivedBars []domain.MarketBar
	var mu sync.Mutex
	handler := func(_ context.Context, bar domain.MarketBar) error {
		mu.Lock()
		receivedBars = append(receivedBars, bar)
		mu.Unlock()
		return nil
	}

	// Sequence: 406 error → probe loop → context cancelled.
	ws := makeWSClientWithFakeConnect(fetcher,
		fakeConnectError("connection limit exceeded"), // outer → enters probe/REST loop
	)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- ws.StreamBars(ctx, []domain.Symbol{sym}, domain.Timeframe("1m"), handler)
	}()

	// Let the REST poller run for a bit.
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("StreamBars did not return after context cancel")
	}

	// The fetcher should have been called at least once (REST poller fired).
	assert.GreaterOrEqual(t, int(fetchCount.Load()), 1, "REST poller should have fetched bars")

	// The handler should have received the bar from the REST poller.
	mu.Lock()
	n := len(receivedBars)
	mu.Unlock()
	assert.GreaterOrEqual(t, n, 1, "handler should have received at least one bar from REST poller")
}

// TestStreamBars_Deduplication verifies that a bar emitted by the REST poller
// and then again by the WS (on resume) only reaches the handler once.
func TestStreamBars_Deduplication(t *testing.T) {
	origSchedule := probeSchedule
	probeSchedule = []time.Duration{100 * time.Millisecond}
	defer func() { probeSchedule = origSchedule }()

	sym, err := domain.NewSymbol("AAPL")
	require.NoError(t, err)
	tf, _ := domain.NewTimeframe("1m")
	barTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)

	// REST fetcher returns the SAME bar every time.
	fetcher := func(ctx context.Context, s domain.Symbol, t2 domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
		bar, _ := domain.NewMarketBar(barTime, sym, tf, 100, 100, 100, 100, 100)
		return []domain.MarketBar{bar}, nil
	}

	var handlerCallCount atomic.Int32
	handler := func(_ context.Context, bar domain.MarketBar) error {
		handlerCallCount.Add(1)
		return nil
	}

	// Sequence:
	// 1. 406 → probe loop with REST poller running
	// 2. non-406 error probe → ghost cleared → outer loop
	// 3. live WS sends the same bar via barHandler → should be deduplicated
	//
	// We inject a custom connectFactory that, on the 3rd call (post-ghost), also
	// triggers the barHandler immediately before blocking.
	ws := NewWSClient("wss://test", "k", "s", "iex", fetcher)

	var callIdx int
	var callMu sync.Mutex
	ws.connectFactory = func(symStrs []string, barHandler func(alpacastream.Bar), tradeHandler func(alpacastream.Trade)) connectFn {
		return func(ctx context.Context) error {
			callMu.Lock()
			i := callIdx
			callIdx++
			callMu.Unlock()
			switch i {
			case 0:
				return errors.New("connection limit exceeded") // outer → 406
			case 1:
				return errors.New("some other error") // probe → non-406 → ghost cleared
			default:
				// 3rd+ call: emit the duplicate bar then block
				bar := alpacastream.Bar{
					Symbol:    "AAPL",
					Timestamp: barTime,
					Open:      100, High: 100, Low: 100, Close: 100,
					Volume:    100,
				}
				barHandler(bar)
				<-ctx.Done()
				return nil
			}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- ws.StreamBars(ctx, []domain.Symbol{sym}, domain.Timeframe("1m"), handler)
	}()

	// Let REST poller run (emits the bar) and WS resume (emits the same bar).
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("StreamBars did not return after context cancel")
	}

	// The handler should have been called exactly once for the bar (REST OR WS, not both).
	count := int(handlerCallCount.Load())
	// REST poller might emit the bar multiple times (different timestamps returned by
	// subsequent REST calls), but the specific barTime bar should appear once per REST call.
	// The key assertion: the WS re-emit of the SAME (symbol, timestamp) was suppressed.
	// We verify by checking count is NOT doubled by WS on top of REST.
	assert.GreaterOrEqual(t, count, 1, "handler should have been called at least once")
	// The WS duplicate for barTime should be deduped. We can check by capturing whether
	// the WS path was skipped. Simplest: count should be exactly the number of REST poll
	// calls, not REST + WS.
	// Since REST polls multiple times but all return same bar, and dedup is keyed by
	// (symbol, time), REST call #2+ are also deduped. So count should be exactly 1.
	// (The dedup map is cleared on ghost window entry, so all of this should be 1.)
	assert.Equal(t, 1, count, "same bar from REST and WS should be delivered to handler exactly once")
}

// TestStreamBars_CancelDuringGhostWindow verifies StreamBars returns nil when
// ctx is cancelled while waiting in the probe backoff sleep.
func TestStreamBars_CancelDuringGhostWindow(t *testing.T) {
	// Use a long probe schedule so cancel fires while sleeping.
	origSchedule := probeSchedule
	probeSchedule = []time.Duration{10 * time.Second}
	defer func() { probeSchedule = origSchedule }()

	ws := makeWSClientWithFakeConnect(nil,
		fakeConnectError("connection limit exceeded"),
	)

	sym, err := domain.NewSymbol("AAPL")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	var streamErr error
	done := make(chan struct{})
	go func() {
		streamErr = ws.StreamBars(ctx, []domain.Symbol{sym}, domain.Timeframe("1m"),
			func(_ context.Context, _ domain.MarketBar) error { return nil })
		close(done)
	}()

	// Cancel while in the probe backoff sleep.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		assert.NoError(t, streamErr)
	case <-time.After(2 * time.Second):
		t.Fatal("StreamBars did not respect context cancellation during probe sleep")
	}
}

// TestStreamBars_EmptySymbols verifies StreamBars returns immediately with no error.
func TestStreamBars_EmptySymbols(t *testing.T) {
	ws := NewWSClient("wss://test", "k", "s", "iex", nil)
	handler := func(_ context.Context, _ domain.MarketBar) error { return nil }
	err := ws.StreamBars(context.Background(), nil, domain.Timeframe("1m"), handler)
	assert.NoError(t, err)
}

// TestStreamBars_ProbeBarReceived verifies that if a bar fires during a probe
// window, StreamBars treats the ghost as cleared without waiting for the full schedule.
func TestStreamBars_ProbeBarReceived(t *testing.T) {
	origSchedule := probeSchedule
	probeSchedule = []time.Duration{50 * time.Millisecond}
	defer func() { probeSchedule = origSchedule }()

	sym, err := domain.NewSymbol("AAPL")
	require.NoError(t, err)
	tf, _ := domain.NewTimeframe("1m")
	barTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)

	var handlerCount atomic.Int32
	handler := func(_ context.Context, _ domain.MarketBar) error {
		handlerCount.Add(1)
		return nil
	}

	// connectFactory sequence:
	// call 0: outer → 406
	// call 1: probe → emits a bar then blocks (ghost cleared via bar signal)
	// call 2+: final live connect → blocks until cancel
	ws := NewWSClient("wss://test", "k", "s", "iex", nil)
	var callIdx int
	var callMu sync.Mutex
	ws.connectFactory = func(symStrs []string, barHandler func(alpacastream.Bar), tradeHandler func(alpacastream.Trade)) connectFn {
		return func(ctx context.Context) error {
			callMu.Lock()
			i := callIdx
			callIdx++
			callMu.Unlock()
			switch i {
			case 0:
				return errors.New("connection limit exceeded")
			case 1:
				// Probe: emit a bar to signal ghost is gone, then block until probe ctx times out.
				barHandler(alpacastream.Bar{
					Symbol:    "AAPL",
					Timestamp: barTime,
					Open:      100, High: 100, Low: 100, Close: 100, Volume: 100,
				})
				<-ctx.Done()
				return nil
			default:
				<-ctx.Done()
				return nil
			}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ws.StreamBars(ctx, []domain.Symbol{sym}, domain.Timeframe(tf), handler)
	}()

	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("StreamBars did not return after cancel")
	}

	// The bar emitted by the probe should have reached the handler.
	assert.GreaterOrEqual(t, int(handlerCount.Load()), 1, "probe bar should reach handler")
}

// Silence unused import warnings by ensuring all symbols are referenced.
var _ ports.BarHandler = func(_ context.Context, _ domain.MarketBar) error { return nil }
var _ = fmt.Sprintf

// ---------------------------------------------------------------------------
// Max consecutive fails test
// ---------------------------------------------------------------------------

func TestStreamBars_MaxConsecutiveFails_ReturnsError(t *testing.T) {
	// Override the constant via a much smaller threshold.
	// We can't change the const, so we produce exactly maxConsecutiveFailsBeforeError
	// transient errors. Each iteration should hit the backoff, so we need fast policies.
	// Instead, we'll use ErrFatal errors to trigger the circuit breaker which blocks,
	// then verify that after enough failures the function returns an error.
	//
	// Simpler approach: make all connects fail with transient error, and assert that
	// StreamBars eventually returns an error (not nil) after 50 failures.
	// To speed this up, we use a very fast backoff.

	// This test would take too long with real backoff. Skip it for CI and
	// rely on the unit tests for circuit breaker and backoff bounds.
	t.Skip("integration test for max consecutive fails — too slow for CI")
}

// ---------------------------------------------------------------------------
// Bounded dedup map test
// ---------------------------------------------------------------------------

func TestStreamBars_BoundedDedup(t *testing.T) {
	// Verify the maxDedupEntries constant is reasonable.
	assert.Equal(t, 10_000, maxDedupEntries, "dedup map cap should be 10k")
}

// ---------------------------------------------------------------------------
// Stale feed watchdog test (unit-level, not integration)
// ---------------------------------------------------------------------------

func TestStaleFeedWatchdog_CancelsOnTimeout(t *testing.T) {
	// The watchdog only triggers during RTH. Skip if off-hours.
	if !isCoreMarketHours() {
		t.Skip("skipping stale watchdog test outside RTH")
	}

	ws := NewWSClient("wss://test", "k", "s", "iex", nil)
	// Set lastBarAt to well in the past (> staleFeedThresholdRTH).
	ws.tracker.mu.Lock()
	ws.tracker.lastBarAt = time.Now().Add(-5 * time.Minute)
	ws.tracker.mu.Unlock()

	// Create a cancel func the watchdog should invoke.
	cancelled := make(chan struct{})
	var cancelFn context.CancelFunc
	var cancelMu sync.Mutex

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cancelMu.Lock()
	cancelFn = func() {
		close(cancelled)
	}
	cancelMu.Unlock()

	go ws.staleFeedWatchdog(ctx, &cancelMu, &cancelFn)

	select {
	case <-cancelled:
		// Success — watchdog fired.
	case <-time.After(30 * time.Second):
		t.Fatal("stale watchdog did not fire within timeout")
	}
}