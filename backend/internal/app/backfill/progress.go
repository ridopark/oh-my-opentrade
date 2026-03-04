package backfill

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
)

// Progress tracks backfill progress counters with periodic logging.
type Progress struct {
	totalBars    atomic.Int64
	totalChunks  atomic.Int64
	totalErrors  atomic.Int64
	skippedBars  atomic.Int64
	symbolsDone  atomic.Int64
	totalSymbols int
	log          zerolog.Logger
	startTime    time.Time
	stopOnce     sync.Once
	done         chan struct{}
}

// NewProgress creates a new Progress tracker that logs every interval.
func NewProgress(totalSymbols int, interval time.Duration, log zerolog.Logger) *Progress {
	p := &Progress{
		totalSymbols: totalSymbols,
		log:          log,
		startTime:    time.Now(),
		done:         make(chan struct{}),
	}
	go p.periodicLog(interval)
	return p
}

// AddBars increments the bar counter.
func (p *Progress) AddBars(n int) { p.totalBars.Add(int64(n)) }

// AddChunks increments the chunk counter.
func (p *Progress) AddChunks(n int) { p.totalChunks.Add(int64(n)) }

// AddErrors increments the error counter.
func (p *Progress) AddErrors(n int) { p.totalErrors.Add(int64(n)) }

// AddSkipped increments the skipped bars counter.
func (p *Progress) AddSkipped(n int) { p.skippedBars.Add(int64(n)) }

// SymbolDone increments the completed symbols counter.
func (p *Progress) SymbolDone() { p.symbolsDone.Add(1) }

// Stop halts the periodic logger and logs a final summary.
func (p *Progress) Stop() {
	p.stopOnce.Do(func() {
		close(p.done)
		p.logSummary()
	})
}

// Bars returns the total bar count.
func (p *Progress) Bars() int64 { return p.totalBars.Load() }

// Errors returns the total error count.
func (p *Progress) Errors() int64 { return p.totalErrors.Load() }

func (p *Progress) periodicLog(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			p.logProgress()
		}
	}
}

func (p *Progress) logProgress() {
	elapsed := time.Since(p.startTime).Round(time.Second)
	p.log.Info().
		Int64("bars", p.totalBars.Load()).
		Int64("chunks", p.totalChunks.Load()).
		Int64("errors", p.totalErrors.Load()).
		Int64("skipped", p.skippedBars.Load()).
		Int64("symbols_done", p.symbolsDone.Load()).
		Int("symbols_total", p.totalSymbols).
		Str("elapsed", elapsed.String()).
		Msg("backfill progress")
}

func (p *Progress) logSummary() {
	elapsed := time.Since(p.startTime).Round(time.Second)
	barsPerSec := float64(0)
	if elapsed.Seconds() > 0 {
		barsPerSec = float64(p.totalBars.Load()) / elapsed.Seconds()
	}
	p.log.Info().
		Int64("total_bars", p.totalBars.Load()).
		Int64("total_chunks", p.totalChunks.Load()).
		Int64("total_errors", p.totalErrors.Load()).
		Int64("total_skipped", p.skippedBars.Load()).
		Int("symbols", p.totalSymbols).
		Str("elapsed", elapsed.String()).
		Float64("bars_per_sec", barsPerSec).
		Msg("backfill complete")
}
