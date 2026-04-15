// Phase-1 read-only gap detector orchestrator and Prometheus gauges.
// No goroutine loop yet — the caller drives RunOnce.
package gapdetect

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog"
)

var (
	missingBarsCount = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "omo_missing_bars_count",
		Help: "Count of missing bars in the most recent gap-detector scan window per (symbol, timeframe).",
	}, []string{"symbol", "timeframe"})

	lastBarAgeSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "omo_last_bar_age_seconds",
		Help: "Seconds since the most recent persisted bar per (symbol, timeframe).",
	}, []string{"symbol", "timeframe"})
)

// LatestBarReader returns the most recent persisted bar timestamp.
type LatestBarReader interface {
	GetLatestMarketBarTime(ctx context.Context, sym domain.Symbol, tf domain.Timeframe) (*time.Time, error)
}

// Service orchestrates per-(symbol, timeframe) gap scans and updates gauges.
// Phase 1 exposes RunOnce only; Phase 2 will add a ticker.
type Service struct {
	detector ports.GapDetector
	reader   LatestBarReader
	now      func() time.Time
	log      zerolog.Logger
}

// NewService builds a gap-detection orchestrator. now defaults to time.Now
// when nil — injected for deterministic tests.
func NewService(detector ports.GapDetector, reader LatestBarReader, log zerolog.Logger, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{detector: detector, reader: reader, now: now, log: log}
}

// scanWindows are the per-tf lookbacks chosen so equity 1m sees ~2 sessions of
// recent intraday data while crypto/daily windows cover meaningful coverage
// without scanning history we do not intend to backfill in Phase 1.
var scanWindows = []struct {
	tf       domain.Timeframe
	lookback time.Duration
}{
	{"1m", 120 * time.Minute},
	{"5m", 8 * time.Hour},
	{"15m", 8 * time.Hour},
	{"1h", 72 * time.Hour},
	{"1d", 250 * 24 * time.Hour},
}

// RunOnce executes a single gap-detection pass over the configured timeframes
// for every symbol, updates gauges, and returns the total number of gap ranges
// seen across all (symbol, tf) combinations.
func (s *Service) RunOnce(ctx context.Context, symbols []domain.Symbol) int {
	now := s.now()
	totalGaps := 0
	for _, sym := range symbols {
		for _, w := range scanWindows {
			from := now.Add(-w.lookback)
			gaps, err := s.detector.FindMissingBars(ctx, sym, w.tf, from, now)
			if err != nil {
				s.log.Warn().Err(err).
					Str("symbol", string(sym)).
					Str("timeframe", string(w.tf)).
					Msg("gap detector scan failed")
				continue
			}
			missing := 0
			for _, g := range gaps {
				missing += g.ExpectedCount - g.ActualCount
			}
			missingBarsCount.WithLabelValues(string(sym), string(w.tf)).Set(float64(missing))
			totalGaps += len(gaps)

			latest, lerr := s.reader.GetLatestMarketBarTime(ctx, sym, w.tf)
			if lerr != nil {
				s.log.Warn().Err(lerr).
					Str("symbol", string(sym)).
					Str("timeframe", string(w.tf)).
					Msg("latest bar lookup failed")
				continue
			}
			if latest != nil {
				lastBarAgeSeconds.WithLabelValues(string(sym), string(w.tf)).Set(now.Sub(*latest).Seconds())
			}
		}
	}
	return totalGaps
}
