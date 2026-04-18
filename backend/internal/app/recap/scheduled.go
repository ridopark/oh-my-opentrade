package recap

import (
	"context"
	"time"

	"github.com/rs/zerolog"
)

// ScheduledConfig controls when the daily recap fires.
type ScheduledConfig struct {
	RunAtHourET   int // default 17 (5 PM ET)
	RunAtMinuteET int // default 15
}

// ScheduledService wraps Service with an ET-time daily tick, weekend skip.
// Mirrors optionsimport.ScheduledService so operators have one mental model.
type ScheduledService struct {
	svc *Service
	cfg ScheduledConfig
	log zerolog.Logger
	et  *time.Location
}

func NewScheduledService(cfg ScheduledConfig, svc *Service, log zerolog.Logger) *ScheduledService {
	if cfg.RunAtHourET == 0 && cfg.RunAtMinuteET == 0 {
		cfg.RunAtHourET = 17
		cfg.RunAtMinuteET = 15
	}
	et, _ := time.LoadLocation("America/New_York")
	return &ScheduledService{
		svc: svc,
		cfg: cfg,
		log: log.With().Str("component", "recap_scheduled").Logger(),
		et:  et,
	}
}

// Start launches the background scheduler. Does NOT fire immediately --
// recap is meant for post-close only, so we wait until the scheduled time.
func (s *ScheduledService) Start(ctx context.Context) error {
	go s.loop(ctx)
	s.log.Info().
		Int("run_hour_et", s.cfg.RunAtHourET).
		Int("run_minute_et", s.cfg.RunAtMinuteET).
		Msg("recap scheduled service started")
	return nil
}

func (s *ScheduledService) loop(ctx context.Context) {
	for {
		next := s.nextRunTime(time.Now())
		s.log.Info().Time("next_run", next).Msg("recap next run scheduled")
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.runOnce(ctx)
		}
	}
}

func (s *ScheduledService) runOnce(ctx context.Context) {
	// "Today" in ET -- recap covers the session that just closed.
	day := time.Now().In(s.et)
	if _, err := s.svc.GenerateDigest(ctx, day); err != nil {
		s.log.Error().Err(err).Msg("recap generation failed")
	}
}

// RunOnce is the run-once entrypoint invoked by omo-data -run-once.
func (s *ScheduledService) RunOnce(ctx context.Context) {
	s.runOnce(ctx)
}

func (s *ScheduledService) nextRunTime(now time.Time) time.Time {
	nowET := now.In(s.et)
	target := time.Date(
		nowET.Year(), nowET.Month(), nowET.Day(),
		s.cfg.RunAtHourET, s.cfg.RunAtMinuteET, 0, 0,
		s.et,
	)
	if !nowET.Before(target) {
		target = target.AddDate(0, 0, 1)
	}
	for target.Weekday() == time.Saturday || target.Weekday() == time.Sunday {
		target = target.AddDate(0, 0, 1)
	}
	return target
}
