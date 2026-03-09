package ingestion

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// BarBatchSaver is a narrow interface for batch-saving market bars.
// The TimescaleDB adapter satisfies this implicitly via its SaveMarketBars method.
type BarBatchSaver interface {
	SaveMarketBars(ctx context.Context, bars []domain.MarketBar) (int, error)
}

// AsyncBarWriter decouples DB writes from the bar processing pipeline.
// Bars are enqueued non-blocking and flushed in batches by a single worker goroutine.
// Flush triggers: batch threshold reached OR timer fires (whichever comes first).
type AsyncBarWriter struct {
	saver  BarBatchSaver
	ch     chan domain.MarketBar
	done   chan struct{}
	log    zerolog.Logger
	closed atomic.Bool

	// Configurable parameters.
	batchSize     int
	flushInterval time.Duration
}

// WriterOption configures an AsyncBarWriter.
type WriterOption func(*AsyncBarWriter)

// WithBatchSize sets the flush threshold (default 50).
func WithBatchSize(n int) WriterOption {
	return func(w *AsyncBarWriter) { w.batchSize = n }
}

// WithFlushInterval sets the periodic flush interval (default 5s).
func WithFlushInterval(d time.Duration) WriterOption {
	return func(w *AsyncBarWriter) { w.flushInterval = d }
}

// WithChannelSize sets the buffered channel capacity (default 1000).
func WithChannelSize(n int) WriterOption {
	return func(w *AsyncBarWriter) { w.ch = make(chan domain.MarketBar, n) }
}

// NewAsyncBarWriter creates a writer with the given saver and options.
// Call Start() to launch the worker goroutine.
func NewAsyncBarWriter(saver BarBatchSaver, log zerolog.Logger, opts ...WriterOption) *AsyncBarWriter {
	w := &AsyncBarWriter{
		saver:         saver,
		ch:            make(chan domain.MarketBar, 1000),
		done:          make(chan struct{}),
		log:           log.With().Str("component", "bar_writer").Logger(),
		batchSize:     50,
		flushInterval: 5 * time.Second,
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Start launches the background worker goroutine.
func (w *AsyncBarWriter) Start() {
	go w.worker()
	w.log.Info().
		Int("batch_size", w.batchSize).
		Dur("flush_interval", w.flushInterval).
		Int("channel_size", cap(w.ch)).
		Msg("async bar writer started")
}

// Enqueue adds a bar to the write buffer. Non-blocking; drops with warning if full.
func (w *AsyncBarWriter) Enqueue(bar domain.MarketBar) {
	select {
	case w.ch <- bar:
	default:
		w.log.Error().
			Str("symbol", string(bar.Symbol)).
			Str("timeframe", string(bar.Timeframe)).
			Msg("async bar writer: channel full, dropping bar")
	}
}

// Close signals the worker to drain remaining bars and waits up to 10s.
func (w *AsyncBarWriter) Close() {
	if !w.closed.CompareAndSwap(false, true) {
		return // already closed
	}
	close(w.ch)
	select {
	case <-w.done:
		w.log.Info().Msg("async bar writer shut down cleanly")
	case <-time.After(10 * time.Second):
		w.log.Error().Msg("async bar writer: shutdown timeout after 10s")
	}
}

// worker is the single goroutine that batches and flushes bars.
func (w *AsyncBarWriter) worker() {
	defer close(w.done)

	batch := make([]domain.MarketBar, 0, w.batchSize)
	ticker := time.NewTicker(w.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case bar, ok := <-w.ch:
			if !ok {
				// Channel closed — flush remaining and exit.
				w.flush(batch)
				return
			}
			batch = append(batch, bar)
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
func (w *AsyncBarWriter) flush(bars []domain.MarketBar) {
	if len(bars) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	n, err := w.saver.SaveMarketBars(ctx, bars)
	if err != nil {
		w.log.Error().Err(err).Int("batch_size", len(bars)).Msg("flush failed, retrying once")

		retryCtx, retryCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer retryCancel()

		n2, err2 := w.saver.SaveMarketBars(retryCtx, bars)
		if err2 != nil {
			w.log.Error().Err(err2).Int("batch_size", len(bars)).Msg("retry failed, bars lost")
			return
		}
		w.log.Info().Int("saved", n2).Msg("retry succeeded")
		return
	}

	w.log.Debug().Int("saved", n).Msg("batch flushed")
}
