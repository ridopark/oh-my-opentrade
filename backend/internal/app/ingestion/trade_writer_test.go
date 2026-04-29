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

// --- mock TradeBatchSaver ---

type mockTradeSaver struct {
	mu      sync.Mutex
	batches [][]domain.MarketTrade
	err     error
	calls   atomic.Int32
}

func (m *mockTradeSaver) SaveMarketTrades(_ context.Context, trades []domain.MarketTrade) (int, error) {
	m.calls.Add(1)
	if m.err != nil {
		return 0, m.err
	}
	m.mu.Lock()
	cp := make([]domain.MarketTrade, len(trades))
	copy(cp, trades)
	m.batches = append(m.batches, cp)
	m.mu.Unlock()
	return len(trades), nil
}

func (m *mockTradeSaver) allTrades() []domain.MarketTrade {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.MarketTrade
	for _, b := range m.batches {
		out = append(out, b...)
	}
	return out
}

// --- helpers ---

func testTrade(sym string, price float64) domain.MarketTrade {
	s, _ := domain.NewSymbol(sym)
	return domain.MarketTrade{
		Symbol:   s,
		Time:     time.Now(),
		Price:    price,
		Size:     100,
		Exchange: "D",
	}
}

// --- tests ---

func TestAsyncTradeWriter_NonBlockingEnqueue(t *testing.T) {
	saver := &mockTradeSaver{}
	w := ingestion.NewAsyncTradeWriter(saver, zerolog.Nop(),
		ingestion.WithTradeBatchSize(100),
		ingestion.WithTradeFlushInterval(time.Hour),
	)
	w.Start()
	defer w.Close()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			w.Enqueue(testTrade("AAPL", float64(100+i)))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Enqueue blocked for >1s")
	}
}

func TestAsyncTradeWriter_BatchThresholdFlush(t *testing.T) {
	saver := &mockTradeSaver{}
	w := ingestion.NewAsyncTradeWriter(saver, zerolog.Nop(),
		ingestion.WithTradeBatchSize(5),
		ingestion.WithTradeFlushInterval(time.Hour),
		ingestion.WithTradeChannelSize(100),
	)
	w.Start()

	for i := 0; i < 5; i++ {
		w.Enqueue(testTrade("AAPL", float64(100+i)))
	}

	time.Sleep(100 * time.Millisecond)
	w.Close()

	trades := saver.allTrades()
	require.Len(t, trades, 5)
	for i, tr := range trades {
		assert.InDelta(t, float64(100+i), tr.Price, 0.001)
	}
}

func TestAsyncTradeWriter_TimerFlush(t *testing.T) {
	saver := &mockTradeSaver{}
	w := ingestion.NewAsyncTradeWriter(saver, zerolog.Nop(),
		ingestion.WithTradeBatchSize(1000),
		ingestion.WithTradeFlushInterval(50*time.Millisecond),
		ingestion.WithTradeChannelSize(100),
	)
	w.Start()

	w.Enqueue(testTrade("AAPL", 150))
	w.Enqueue(testTrade("TSLA", 250))

	time.Sleep(200 * time.Millisecond)
	w.Close()

	trades := saver.allTrades()
	require.Len(t, trades, 2)
}

func TestAsyncTradeWriter_GracefulDrainOnClose(t *testing.T) {
	saver := &mockTradeSaver{}
	w := ingestion.NewAsyncTradeWriter(saver, zerolog.Nop(),
		ingestion.WithTradeBatchSize(1000),
		ingestion.WithTradeFlushInterval(time.Hour),
		ingestion.WithTradeChannelSize(100),
	)
	w.Start()

	for i := 0; i < 7; i++ {
		w.Enqueue(testTrade("SPY", float64(400+i)))
	}

	w.Close()

	trades := saver.allTrades()
	assert.Len(t, trades, 7, "all enqueued trades must be flushed on close")
}

func TestAsyncTradeWriter_DropOnFull(t *testing.T) {
	saver := &mockTradeSaver{}
	w := ingestion.NewAsyncTradeWriter(saver, zerolog.Nop(),
		ingestion.WithTradeBatchSize(1000),
		ingestion.WithTradeFlushInterval(time.Hour),
		ingestion.WithTradeChannelSize(2),
	)

	w.Enqueue(testTrade("AAPL", 100))
	w.Enqueue(testTrade("AAPL", 101))
	w.Enqueue(testTrade("AAPL", 102))

	w.Start()
	w.Close()

	trades := saver.allTrades()
	assert.Len(t, trades, 2, "only 2 trades should survive (channel capacity)")
}

func TestAsyncTradeWriter_RetryOnFailure(t *testing.T) {
	failing := &failOnceTradeSaver{}

	w := ingestion.NewAsyncTradeWriter(failing, zerolog.Nop(),
		ingestion.WithTradeBatchSize(3),
		ingestion.WithTradeFlushInterval(time.Hour),
		ingestion.WithTradeChannelSize(100),
	)
	w.Start()

	for i := 0; i < 3; i++ {
		w.Enqueue(testTrade("META", float64(300+i)))
	}

	time.Sleep(100 * time.Millisecond)
	w.Close()

	assert.Equal(t, int32(2), failing.calls.Load(), "should have called SaveMarketTrades twice (fail + retry)")
	trades := failing.allTrades()
	assert.Len(t, trades, 3, "trades should be saved on retry")
}

func TestAsyncTradeWriter_IdempotentClose(t *testing.T) {
	saver := &mockTradeSaver{}
	w := ingestion.NewAsyncTradeWriter(saver, zerolog.Nop())
	w.Start()

	w.Close()
	w.Close()
	w.Close()
}

func TestAsyncTradeWriter_ConcurrentEnqueue(t *testing.T) {
	saver := &mockTradeSaver{}
	w := ingestion.NewAsyncTradeWriter(saver, zerolog.Nop(),
		ingestion.WithTradeBatchSize(10),
		ingestion.WithTradeFlushInterval(50*time.Millisecond),
		ingestion.WithTradeChannelSize(500),
	)
	w.Start()

	var wg sync.WaitGroup
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				w.Enqueue(testTrade("AAPL", float64(offset*100+i)))
			}
		}(g)
	}
	wg.Wait()
	w.Close()

	trades := saver.allTrades()
	assert.Len(t, trades, 100, "all 100 trades from 5 goroutines should be saved")
}

// --- failOnceTradeSaver: fails first SaveMarketTrades call, then delegates ---

type failOnceTradeSaver struct {
	mu      sync.Mutex
	batches [][]domain.MarketTrade
	failed  atomic.Bool
	calls   atomic.Int32
}

func (f *failOnceTradeSaver) SaveMarketTrades(_ context.Context, trades []domain.MarketTrade) (int, error) {
	f.calls.Add(1)
	if !f.failed.Load() {
		f.failed.Store(true)
		return 0, errors.New("transient DB error")
	}
	f.mu.Lock()
	cp := make([]domain.MarketTrade, len(trades))
	copy(cp, trades)
	f.batches = append(f.batches, cp)
	f.mu.Unlock()
	return len(trades), nil
}

func (f *failOnceTradeSaver) allTrades() []domain.MarketTrade {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.MarketTrade
	for _, b := range f.batches {
		out = append(out, b...)
	}
	return out
}
