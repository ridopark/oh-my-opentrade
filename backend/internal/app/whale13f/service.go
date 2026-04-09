package whale13f

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/openfigi"
	"github.com/oh-my-opentrade/backend/internal/adapters/sec"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/rs/zerolog"
)

// ScheduledConfig controls the periodic 13F refresh in omo-core.
type ScheduledConfig struct {
	RunAtHourET   int    // default 6 (6 AM ET)
	RunAtMinuteET int    // default 0
	UserAgent     string // SEC requirement
}

// Service runs periodic 13F filing refresh alongside other omo-core services.
type Service struct {
	backfill *BackfillService
	filers   []sec.FilerConfig
	cfg      ScheduledConfig
	log      zerolog.Logger
	et       *time.Location
}

// NewScheduledService creates a scheduled 13F whale accumulation service.
func NewScheduledService(
	cfg ScheduledConfig,
	edgar *sec.EdgarClient,
	figi *openfigi.Client,
	cusipCache *timescaledb.CUSIPCacheRepo,
	whaleRepo *timescaledb.WhaleRepo,
	log zerolog.Logger,
) *Service {
	if cfg.RunAtHourET == 0 && cfg.RunAtMinuteET == 0 {
		cfg.RunAtHourET = 6
	}
	et, _ := time.LoadLocation("America/New_York")
	return &Service{
		backfill: NewBackfillService(edgar, figi, cusipCache, whaleRepo, Config{
			Concurrency: 3,
			BatchSize:   500,
			UserAgent:   cfg.UserAgent,
		}, log),
		filers: sec.DefaultFilers(),
		cfg:    cfg,
		log:    log,
		et:     et,
	}
}

// Start launches the background scheduler goroutine.
func (s *Service) Start(ctx context.Context) error {
	go s.loop(ctx)
	s.log.Info().
		Int("run_hour_et", s.cfg.RunAtHourET).
		Int("run_minute_et", s.cfg.RunAtMinuteET).
		Int("filers", len(s.filers)).
		Msg("whale 13F service started")
	return nil
}

func (s *Service) loop(ctx context.Context) {
	for {
		next := s.nextRunTime(time.Now())
		s.log.Debug().Time("next_run", next).Msg("whale 13F scheduled")

		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if err := s.Refresh(ctx); err != nil {
				s.log.Error().Err(err).Msg("whale 13F refresh failed")
			}
		}
	}
}

func (s *Service) nextRunTime(now time.Time) time.Time {
	nowET := now.In(s.et)
	target := time.Date(
		nowET.Year(), nowET.Month(), nowET.Day(),
		s.cfg.RunAtHourET, s.cfg.RunAtMinuteET, 0, 0,
		s.et,
	)
	if nowET.After(target) {
		target = target.AddDate(0, 0, 1)
	}
	for target.Weekday() == time.Saturday || target.Weekday() == time.Sunday {
		target = target.AddDate(0, 0, 1)
	}
	return target
}

// Refresh fetches the latest quarter's 13F filings and recomputes scores.
// During the filing window (45-90 days after quarter end), new filings appear
// daily. Outside the window this is a fast no-op.
func (s *Service) Refresh(ctx context.Context) error {
	q := CurrentQuarter()
	s.log.Info().Str("quarter", q.String()).Msg("starting whale 13F refresh")

	// Only process the latest quarter — CLI handles multi-quarter backfill.
	if err := s.backfill.RunQuarters(ctx, q, q, s.filers); err != nil {
		return err
	}

	s.log.Info().Str("quarter", q.String()).Msg("whale 13F refresh complete")
	return nil
}
