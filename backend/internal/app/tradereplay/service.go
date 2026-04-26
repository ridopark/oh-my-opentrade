// Package tradereplay replays persisted market_trades rows back into a
// per-trade callback (Sink). It is the boot-time recovery path for stateful
// tick consumers — primarily the live DP aggregator (Phase 4 of the
// backtest/live parity plan), which loses its in-memory 5m bucket on every
// omo-core restart.
//
// The replayer reads trades per symbol from a Reader implementation
// (Repository.GetMarketTrades satisfies this implicitly) and feeds them to
// the Sink in chronological order within each symbol. Cross-symbol order is
// not preserved — DP aggregation is per-symbol so it does not need to be.
//
// Failure model: best-effort. A reader error for one symbol logs and skips
// to the next; a sink error for one symbol stops feeding that symbol but
// still attempts the rest. Both kinds of error are joined into the returned
// error so the caller can decide whether to retry or proceed cold.
package tradereplay

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// Reader is the narrow read port the replayer needs. The TimescaleDB
// Repository satisfies it implicitly via GetMarketTrades.
type Reader interface {
	GetMarketTrades(ctx context.Context, symbol domain.Symbol, from, to time.Time) ([]domain.MarketTrade, error)
}

// Sink is a per-trade callback. Implementations should be cheap and not
// block on I/O — typical sinks update in-memory aggregator state.
// Returning an error stops the replay for the current symbol; the replayer
// continues with the next symbol.
type Sink func(ctx context.Context, trade domain.MarketTrade) error

// Stats summarizes a Replay run. Returned even when err != nil so the caller
// can log partial coverage.
type Stats struct {
	// Symbols counts symbols the replayer processed (had a successful read).
	Symbols int
	// SymbolsFailed counts symbols whose read or sink returned an error.
	SymbolsFailed int
	// Trades is the total number of trades dispatched to the sink across
	// all symbols.
	Trades int
	// PerSymbol holds per-symbol detail keyed by symbol string. Populated
	// only for symbols the replayer attempted (either successful or failed).
	PerSymbol map[string]SymbolStats
}

// SymbolStats describes a single symbol's replay coverage.
type SymbolStats struct {
	Trades    int
	First     time.Time
	Last      time.Time
	ReadError bool
	SinkError bool
}

// Service orchestrates per-symbol replay. Constructed once at boot and
// invoked on demand.
type Service struct {
	reader Reader
	log    zerolog.Logger
}

// New builds a Service. The logger is used for per-symbol progress lines so
// boot logs reflect the replay's coverage; pass a no-op logger to disable.
func New(reader Reader, log zerolog.Logger) *Service {
	return &Service{
		reader: reader,
		log:    log.With().Str("component", "tradereplay").Logger(),
	}
}

// Replay reads trades for each symbol in [since, now] and feeds them to the
// sink in chronological order per symbol. The "now" boundary is captured
// once at entry so a long-running replay sees a stable window.
//
// Returns Stats summarizing coverage. The returned error is errors.Join of
// every per-symbol read or sink error encountered; nil means clean success.
// Stats is always populated, even on error, so the caller can log partial
// coverage and decide whether to fall back to a REST replay.
func (s *Service) Replay(ctx context.Context, since time.Time, symbols []domain.Symbol, sink Sink) (Stats, error) {
	stats := Stats{PerSymbol: make(map[string]SymbolStats, len(symbols))}
	if sink == nil {
		return stats, errors.New("tradereplay: sink is nil")
	}
	if since.IsZero() {
		return stats, errors.New("tradereplay: since is zero")
	}
	to := time.Now()
	if !since.Before(to) {
		// Empty window — nothing to do, not an error. A boot before
		// session_open or with a clock skew lands here.
		return stats, nil
	}

	var errs []error
	for _, sym := range symbols {
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("tradereplay: canceled before %s: %w", sym, err))
			break
		}

		ss := SymbolStats{}
		trades, err := s.reader.GetMarketTrades(ctx, sym, since, to)
		if err != nil {
			ss.ReadError = true
			stats.SymbolsFailed++
			stats.PerSymbol[string(sym)] = ss
			s.log.Warn().Err(err).Str("symbol", string(sym)).Msg("trade read failed; skipping symbol")
			errs = append(errs, fmt.Errorf("tradereplay: read %s: %w", sym, err))
			continue
		}

		for i := range trades {
			if err := sink(ctx, trades[i]); err != nil {
				ss.SinkError = true
				stats.SymbolsFailed++
				s.log.Warn().Err(err).
					Str("symbol", string(sym)).
					Int("dispatched", ss.Trades).
					Int("remaining", len(trades)-i).
					Msg("sink error; stopping symbol")
				errs = append(errs, fmt.Errorf("tradereplay: sink %s: %w", sym, err))
				break
			}
			if ss.Trades == 0 {
				ss.First = trades[i].Time
			}
			ss.Last = trades[i].Time
			ss.Trades++
		}

		if !ss.SinkError {
			stats.Symbols++
		}
		stats.Trades += ss.Trades
		stats.PerSymbol[string(sym)] = ss

		s.log.Debug().
			Str("symbol", string(sym)).
			Int("trades", ss.Trades).
			Time("first", ss.First).
			Time("last", ss.Last).
			Msg("symbol replay complete")
	}

	if len(errs) > 0 {
		return stats, errors.Join(errs...)
	}
	return stats, nil
}

// LoggingSink returns a Sink that does nothing per trade. Use it as a
// telemetry-only hook before Phase 4 wires the real DP aggregator: the
// Stats returned by Replay still report counts and time ranges, validating
// that the read path works end-to-end on production data.
func LoggingSink() Sink {
	return func(_ context.Context, _ domain.MarketTrade) error { return nil }
}
