package ingest

import (
	"context"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// Pipeline receives 1m bars, applies spike filtering, persists them,
// and aggregates into higher timeframes (5m, 15m, 1h, 1d).
type Pipeline struct {
	filter *ingestion.AdaptiveFilter
	writer *ingestion.AsyncBarWriter
	log    zerolog.Logger

	mu          sync.Mutex
	aggregators map[domain.Symbol][]*domain.BarAggregator
}

// htfTimeframes lists the target timeframes produced by aggregation.
var htfTimeframes = []domain.Timeframe{"5m", "15m", "1h", "1d"}

// NewPipeline creates a pipeline with per-symbol HTF aggregators.
// equitySymbols use session-aligned buckets (anchored at sessionOpen).
// cryptoSymbols use UTC clock-aligned buckets.
func NewPipeline(
	filter *ingestion.AdaptiveFilter,
	writer *ingestion.AsyncBarWriter,
	equitySymbols []domain.Symbol,
	cryptoSymbols []domain.Symbol,
	sessionOpen time.Time,
	log zerolog.Logger,
) *Pipeline {
	p := &Pipeline{
		filter:      filter,
		writer:      writer,
		log:         log.With().Str("component", "pipeline").Logger(),
		aggregators: make(map[domain.Symbol][]*domain.BarAggregator),
	}
	p.initAggregators(equitySymbols, cryptoSymbols, sessionOpen)
	return p
}

func (p *Pipeline) initAggregators(equitySymbols, cryptoSymbols []domain.Symbol, sessionOpen time.Time) {
	for _, sym := range equitySymbols {
		var aggs []*domain.BarAggregator
		for _, tf := range htfTimeframes {
			agg, err := domain.NewBarAggregator(sym, tf, sessionOpen)
			if err != nil {
				p.log.Error().Err(err).Str("symbol", string(sym)).Str("tf", string(tf)).Msg("failed to create aggregator")
				continue
			}
			aggs = append(aggs, agg)
		}
		p.aggregators[sym] = aggs
	}
	for _, sym := range cryptoSymbols {
		var aggs []*domain.BarAggregator
		for _, tf := range htfTimeframes {
			agg, err := domain.NewClockAlignedAggregator(sym, tf)
			if err != nil {
				p.log.Error().Err(err).Str("symbol", string(sym)).Str("tf", string(tf)).Msg("failed to create aggregator")
				continue
			}
			aggs = append(aggs, agg)
		}
		p.aggregators[sym] = aggs
	}
}

// HandleBar is the ports.BarHandler callback for streaming.
// It filters, persists the 1m bar, and feeds HTF aggregators.
func (p *Pipeline) HandleBar(_ context.Context, bar domain.MarketBar) error {
	// Filter is stateful — lock during processing.
	p.mu.Lock()
	result := p.filter.Process(bar)
	p.mu.Unlock()

	switch result.Status {
	case ingestion.FilterRejected:
		p.log.Debug().
			Str("symbol", string(bar.Symbol)).
			Str("gate", string(result.Gate)).
			Msg("bar rejected")
		return nil
	case ingestion.FilterRepaired:
		p.log.Info().
			Str("symbol", string(bar.Symbol)).
			Str("gate", string(result.Gate)).
			Msg("bar repaired")
	}

	// Enqueue the (possibly repaired) 1m bar.
	cleanBar := result.Bar
	p.writer.Enqueue(cleanBar)

	// Feed HTF aggregators.
	p.mu.Lock()
	aggs := p.aggregators[cleanBar.Symbol]
	p.mu.Unlock()

	for _, agg := range aggs {
		if closed, ok := agg.Push(cleanBar); ok {
			p.writer.Enqueue(closed)
		}
	}

	return nil
}

// ResetSession reinitializes equity aggregators for a new trading session.
func (p *Pipeline) ResetSession(equitySymbols []domain.Symbol, sessionOpen time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, sym := range equitySymbols {
		aggs := p.aggregators[sym]
		for _, agg := range aggs {
			agg.Reset(sessionOpen)
		}
	}
	p.log.Info().Time("session_open", sessionOpen).Msg("equity aggregators reset for new session")
}
