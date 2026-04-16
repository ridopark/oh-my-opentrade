package ingestion

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// FundingLive subscribes to real-time funding rate updates and persists them.
// When the source does not support streaming (returns ErrStreamNotSupported or
// similar), it falls back to a polling loop using Latest().
type FundingLive struct {
	source ports.FundingRatesPort
	repo   *timescaledb.FundingRepo
	log    zerolog.Logger
}

// NewFundingLive creates a live funding rate ingestion job.
func NewFundingLive(source ports.FundingRatesPort, repo *timescaledb.FundingRepo, log zerolog.Logger) *FundingLive {
	return &FundingLive{
		source: source,
		repo:   repo,
		log:    log.With().Str("component", "funding_live").Logger(),
	}
}

// Run starts live ingestion for the given symbols. It first attempts to
// subscribe via Stream(); if the source returns an error, it falls back to
// polling Latest() at pollInterval. Blocks until ctx is canceled, even if
// all stream channels close and individual goroutines degrade to polling —
// the goroutines are self-healing and the parent only needs to cancel ctx.
func (l *FundingLive) Run(ctx context.Context, venue domain.Venue, symbols []domain.Symbol, pollInterval time.Duration) error {
	if pollInterval <= 0 {
		pollInterval = 5 * time.Minute
	}

	// Try streaming first for each symbol.
	streamOK := false
	for _, sym := range symbols {
		ch, err := l.source.Stream(ctx, venue, sym)
		if err != nil {
			l.log.Info().
				Str("venue", string(venue)).
				Str("symbol", string(sym)).
				Err(err).
				Msg("stream not available, will fall back to polling")
			break
		}
		streamOK = true
		go l.consumeStream(ctx, ch, venue, sym)
	}

	if streamOK {
		// Block until context is canceled; streams run in goroutines.
		<-ctx.Done()
		return ctx.Err()
	}

	// Fallback: poll Latest() for all symbols.
	return l.pollLoop(ctx, venue, symbols, pollInterval)
}

// consumeStream reads from a funding rate stream channel and persists each update.
func (l *FundingLive) consumeStream(ctx context.Context, ch <-chan ports.FundingRate, venue domain.Venue, sym domain.Symbol) {
	for {
		select {
		case <-ctx.Done():
			return
		case fr, ok := <-ch:
			if !ok {
				l.log.Warn().
					Str("venue", string(venue)).
					Str("symbol", string(sym)).
					Msg("funding stream channel closed, falling back to polling")
				// Stream died without context cancel — fall back to polling
				// so funding collection continues until the parent shuts down.
				l.pollLoop(ctx, venue, []domain.Symbol{sym}, 5*time.Minute) //nolint:errcheck
				return
			}
			if err := l.repo.Insert(ctx, []ports.FundingRate{fr}); err != nil {
				l.log.Error().Err(err).
					Str("venue", string(venue)).
					Str("symbol", string(sym)).
					Msg("failed to persist streamed funding rate")
			}
		}
	}
}

// pollLoop fetches Latest() for each symbol at the given interval and
// persists new rates. It deduplicates by tracking the last-seen timestamp
// per symbol.
func (l *FundingLive) pollLoop(ctx context.Context, venue domain.Venue, symbols []domain.Symbol, interval time.Duration) error {
	lastSeen := make(map[domain.Symbol]time.Time, len(symbols))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Do an immediate first poll before waiting.
	l.pollOnce(ctx, venue, symbols, lastSeen)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			l.pollOnce(ctx, venue, symbols, lastSeen)
		}
	}
}

// pollOnce fetches Latest() for each symbol and persists new (not yet seen) rates.
func (l *FundingLive) pollOnce(ctx context.Context, venue domain.Venue, symbols []domain.Symbol, lastSeen map[domain.Symbol]time.Time) {
	for _, sym := range symbols {
		fr, err := l.source.Latest(ctx, venue, sym)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				l.log.Warn().Err(err).
					Str("venue", string(venue)).
					Str("symbol", string(sym)).
					Msg("failed to fetch latest funding rate")
			}
			continue
		}

		// Skip if we already persisted this exact timestamp.
		if prev, ok := lastSeen[sym]; ok && !fr.Timestamp.After(prev) {
			continue
		}

		if err := l.repo.Insert(ctx, []ports.FundingRate{fr}); err != nil {
			l.log.Error().Err(err).
				Str("venue", string(venue)).
				Str("symbol", string(sym)).
				Msg("failed to persist polled funding rate")
			continue
		}

		lastSeen[sym] = fr.Timestamp
		l.log.Debug().
			Str("venue", string(venue)).
			Str("symbol", string(sym)).
			Float64("rate", fr.Rate).
			Msg("persisted funding rate from poll")
	}
}

// ErrStreamFallback indicates the adapter does not support streaming and
// the caller should use polling. Exported for use in error checks.
var ErrStreamFallback = fmt.Errorf("funding_live: stream not supported, using poll fallback")
