package ingestion

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// TradeBatchSaver is a narrow interface for batch-saving raw trade ticks.
// The TimescaleDB adapter satisfies this implicitly via SaveMarketTrades.
type TradeBatchSaver interface {
	SaveMarketTrades(ctx context.Context, trades []domain.MarketTrade) (int, error)
}

// AsyncTradeWriter decouples market_trades writes from the live event bus.
// Trade ticks are enqueued non-blocking and flushed in batches by a single
// worker goroutine. Flush triggers: batch threshold reached OR timer fires
// (whichever comes first). On overflow the writer drops with a warning so
// the hot path never blocks.
//
// Sized for the equity SIP feed: ~3-5M trades/day across the active universe.
// Defaults bias toward larger batches and shorter intervals than the bar
// writer because trade volume is two orders of magnitude higher.
type AsyncTradeWriter struct {
	saver  TradeBatchSaver
	ch     chan domain.MarketTrade
	done   chan struct{}
	log    zerolog.Logger
	closed atomic.Bool

	batchSize     int
	flushInterval time.Duration
}

// TradeWriterOption configures an AsyncTradeWriter.
type TradeWriterOption func(*AsyncTradeWriter)

// WithTradeBatchSize sets the flush threshold (default 500).
func WithTradeBatchSize(n int) TradeWriterOption {
	return func(w *AsyncTradeWriter) { w.batchSize = n }
}

// WithTradeFlushInterval sets the periodic flush interval (default 1s).
func WithTradeFlushInterval(d time.Duration) TradeWriterOption {
	return func(w *AsyncTradeWriter) { w.flushInterval = d }
}

// WithTradeChannelSize sets the buffered channel capacity (default 50000).
func WithTradeChannelSize(n int) TradeWriterOption {
	return func(w *AsyncTradeWriter) { w.ch = make(chan domain.MarketTrade, n) }
}

// NewAsyncTradeWriter creates a writer with the given saver and options.
// Call Start() to launch the worker goroutine.
//
// Channel cap 50k absorbs ~5s of buffer at the open-bell peak burst
// (~10k trades/sec across the 34-symbol universe) so a single 2-3s DB
// stall doesn't trip the drop branch. ~5 MB memory cost.
func NewAsyncTradeWriter(saver TradeBatchSaver, log zerolog.Logger, opts ...TradeWriterOption) *AsyncTradeWriter {
	w := &AsyncTradeWriter{
		saver:         saver,
		ch:            make(chan domain.MarketTrade, 50000),
		done:          make(chan struct{}),
		log:           log.With().Str("component", "trade_writer").Logger(),
		batchSize:     500,
		flushInterval: 1 * time.Second,
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Start launches the background worker goroutine.
func (w *AsyncTradeWriter) Start() {
	go w.worker()
	w.log.Info().
		Int("batch_size", w.batchSize).
		Dur("flush_interval", w.flushInterval).
		Int("channel_size", cap(w.ch)).
		Msg("async trade writer started")
}

// Enqueue adds a trade to the write buffer. Non-blocking; drops with warning if full.
func (w *AsyncTradeWriter) Enqueue(t domain.MarketTrade) {
	select {
	case w.ch <- t:
	default:
		w.log.Error().
			Str("symbol", string(t.Symbol)).
			Msg("async trade writer: channel full, dropping trade")
	}
}

// Close signals the worker to drain remaining trades and waits up to 10s.
func (w *AsyncTradeWriter) Close() {
	if !w.closed.CompareAndSwap(false, true) {
		return
	}
	close(w.ch)
	select {
	case <-w.done:
		w.log.Info().Msg("async trade writer shut down cleanly")
	case <-time.After(10 * time.Second):
		w.log.Error().Msg("async trade writer: shutdown timeout after 10s")
	}
}

// worker is the single goroutine that batches and flushes trades.
func (w *AsyncTradeWriter) worker() {
	defer close(w.done)

	batch := make([]domain.MarketTrade, 0, w.batchSize)
	ticker := time.NewTicker(w.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case t, ok := <-w.ch:
			if !ok {
				w.flush(batch)
				return
			}
			batch = append(batch, t)
			if len(batch) >= w.batchSize {
				w.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				w.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

// flush writes a batch to the DB. Retries once on failure.
func (w *AsyncTradeWriter) flush(trades []domain.MarketTrade) {
	if len(trades) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	n, err := w.saver.SaveMarketTrades(ctx, trades)
	if err != nil {
		w.log.Error().Err(err).Int("batch_size", len(trades)).Msg("flush failed, retrying once")

		retryCtx, retryCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer retryCancel()

		n2, err2 := w.saver.SaveMarketTrades(retryCtx, trades)
		if err2 != nil {
			w.log.Error().Err(err2).Int("batch_size", len(trades)).Msg("retry failed, trades lost")
			return
		}
		w.log.Info().Int("saved", n2).Msg("retry succeeded")
		return
	}

	w.log.Debug().Int("saved", n).Msg("batch flushed")
}
