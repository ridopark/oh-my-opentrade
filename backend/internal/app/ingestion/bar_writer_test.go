package ingestion_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- mock BarBatchSaver ---

type mockBatchSaver struct {
	mu      sync.Mutex
	batches [][]domain.MarketBar // each flush call recorded
	err     error
	calls   atomic.Int32
}

func (m *mockBatchSaver) SaveMarketBars(_ context.Context, bars []domain.MarketBar) (int, error) {
	m.calls.Add(1)
	if m.err != nil {
		return 0, m.err
	}
	m.mu.Lock()
	cp := make([]domain.MarketBar, len(bars))
	copy(cp, bars)
	m.batches = append(m.batches, cp)
	m.mu.Unlock()
	return len(bars), nil
}

func (m *mockBatchSaver) allBars() []domain.MarketBar {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.MarketBar
	for _, b := range m.batches {
		out = append(out, b...)
	}
	return out
}

// --- helpers ---

func testBar(sym string, close float64) domain.MarketBar {
	s, _ := domain.NewSymbol(sym)
	return domain.MarketBar{
		Symbol:    s,
		Timeframe: domain.Timeframe("1m"),
		Time:      time.Now(),
		Open:      close - 1,
		High:      close + 1,
		Low:       close - 2,
		Close:     close,
		Volume:    100,
	}
}

// --- tests ---

func TestAsyncBarWriter_NonBlockingEnqueue(t *testing.T) {
	saver := &mockBatchSaver{}
	w := ingestion.NewAsyncBarWriter(saver, zerolog.Nop(),
		ingestion.WithBatchSize(100), // won't trigger batch flush
		ingestion.WithFlushInterval(time.Hour),
	)
	w.Start()
	defer w.Close()

	// Enqueue should return immediately without blocking.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			w.Enqueue(testBar("AAPL", float64(100+i)))
		}
		close(done)
	}()

	select {
	case <-done:
		// OK — non-blocking
	case <-time.After(time.Second):
		t.Fatal("Enqueue blocked for >1s")
	}
}

func TestAsyncBarWriter_BatchThresholdFlush(t *testing.T) {
	saver := &mockBatchSaver{}
	w := ingestion.NewAsyncBarWriter(saver, zerolog.Nop(),
		ingestion.WithBatchSize(5),
		ingestion.WithFlushInterval(time.Hour), // timer won't fire
		ingestion.WithChannelSize(100),
	)
	w.Start()

	for i := 0; i < 5; i++ {
		w.Enqueue(testBar("AAPL", float64(100+i)))
	}

	// Give worker time to process.
	time.Sleep(100 * time.Millisecond)
	w.Close()

	bars := saver.allBars()
	require.Len(t, bars, 5)
	for i, b := range bars {
		assert.InDelta(t, float64(100+i), b.Close, 0.001)
	}
}

func TestAsyncBarWriter_TimerFlush(t *testing.T) {
	saver := &mockBatchSaver{}
	w := ingestion.NewAsyncBarWriter(saver, zerolog.Nop(),
		ingestion.WithBatchSize(1000),                    // won't trigger batch flush
		ingestion.WithFlushInterval(50*time.Millisecond), // fast timer
		ingestion.WithChannelSize(100),
	)
	w.Start()

	w.Enqueue(testBar("AAPL", 150))
	w.Enqueue(testBar("TSLA", 250))

	// Wait for timer to fire.
	time.Sleep(200 * time.Millisecond)
	w.Close()

	bars := saver.allBars()
	require.Len(t, bars, 2)
}

func TestAsyncBarWriter_GracefulDrainOnClose(t *testing.T) {
	saver := &mockBatchSaver{}
	w := ingestion.NewAsyncBarWriter(saver, zerolog.Nop(),
		ingestion.WithBatchSize(1000),
		ingestion.WithFlushInterval(time.Hour),
		ingestion.WithChannelSize(100),
	)
	w.Start()

	for i := 0; i < 7; i++ {
		w.Enqueue(testBar("SPY", float64(400+i)))
	}

	// Close should drain the 7 bars even though neither batch nor timer triggered.
	w.Close()

	bars := saver.allBars()
	assert.Len(t, bars, 7, "all enqueued bars must be flushed on close")
}

func TestAsyncBarWriter_DropOnFull(t *testing.T) {
	saver := &mockBatchSaver{}
	w := ingestion.NewAsyncBarWriter(saver, zerolog.Nop(),
		ingestion.WithBatchSize(1000),
		ingestion.WithFlushInterval(time.Hour),
		ingestion.WithChannelSize(2), // tiny channel
	)
	// Don't start worker — channel will fill up.

	w.Enqueue(testBar("AAPL", 100))
	w.Enqueue(testBar("AAPL", 101))
	// Third should be dropped silently (logged).
	w.Enqueue(testBar("AAPL", 102))

	// Start and close to drain what's in the channel.
	w.Start()
	w.Close()

	bars := saver.allBars()
	assert.Len(t, bars, 2, "only 2 bars should survive (channel capacity)")
}

func TestAsyncBarWriter_RetryOnFailure(t *testing.T) {
	failingSaver := &failOnceSaver{}

	w := ingestion.NewAsyncBarWriter(failingSaver, zerolog.Nop(),
		ingestion.WithBatchSize(3),
		ingestion.WithFlushInterval(time.Hour),
		ingestion.WithChannelSize(100),
	)
	w.Start()

	for i := 0; i < 3; i++ {
		w.Enqueue(testBar("META", float64(300+i)))
	}

	time.Sleep(100 * time.Millisecond)
	w.Close()

	// Retry should have saved the bars.
	assert.Equal(t, int32(2), failingSaver.calls.Load(), "should have called SaveMarketBars twice (fail + retry)")
	bars := failingSaver.allBars()
	assert.Len(t, bars, 3, "bars should be saved on retry")
}

func TestAsyncBarWriter_IdempotentClose(t *testing.T) {
	saver := &mockBatchSaver{}
	w := ingestion.NewAsyncBarWriter(saver, zerolog.Nop())
	w.Start()

	// Multiple closes should not panic.
	w.Close()
	w.Close()
	w.Close()
}

func TestAsyncBarWriter_ConcurrentEnqueue(t *testing.T) {
	saver := &mockBatchSaver{}
	w := ingestion.NewAsyncBarWriter(saver, zerolog.Nop(),
		ingestion.WithBatchSize(10),
		ingestion.WithFlushInterval(50*time.Millisecond),
		ingestion.WithChannelSize(500),
	)
	w.Start()

	var wg sync.WaitGroup
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				w.Enqueue(testBar("AAPL", float64(offset*100+i)))
			}
		}(g)
	}
	wg.Wait()
	w.Close()

	bars := saver.allBars()
	assert.Len(t, bars, 100, "all 100 bars from 5 goroutines should be saved")
}

// --- failOnceSaver: fails first SaveMarketBars call, then delegates ---

type failOnceSaver struct {
	mu      sync.Mutex
	batches [][]domain.MarketBar
	failed  atomic.Bool
	calls   atomic.Int32
}

func (f *failOnceSaver) SaveMarketBars(ctx context.Context, bars []domain.MarketBar) (int, error) {
	f.calls.Add(1)
	if !f.failed.Load() {
		f.failed.Store(true)
		return 0, errors.New("transient DB error")
	}
	f.mu.Lock()
	cp := make([]domain.MarketBar, len(bars))
	copy(cp, bars)
	f.batches = append(f.batches, cp)
	f.mu.Unlock()
	return len(bars), nil
}

func (f *failOnceSaver) allBars() []domain.MarketBar {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.MarketBar
	for _, b := range f.batches {
		out = append(out, b...)
	}
	return out
}
