package tradierimport

import (
	"context"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// ScheduledConfig controls the nightly Tradier chain snapshot.
type ScheduledConfig struct {
	Symbols       []string
	RunAtHourET   int // default 16 (4 PM ET — shortly after US close)
	RunAtMinuteET int // default 30
}

// ScheduledService wraps a Service with a daily ET-scheduled trigger.
// It mirrors optionsimport.ScheduledService to keep the omo-data bootstrap
// homogeneous.
type ScheduledService struct {
	svc      *Service
	cfg      ScheduledConfig
	notifier ports.NotifierPort
	log      zerolog.Logger
	et       *time.Location
}

// NewScheduledService constructs a scheduled Tradier snapshot runner.
func NewScheduledService(cfg ScheduledConfig, svc *Service, log zerolog.Logger) *ScheduledService {
	if cfg.RunAtHourET == 0 && cfg.RunAtMinuteET == 0 {
		cfg.RunAtHourET = 16
		cfg.RunAtMinuteET = 30
	}
	et, _ := time.LoadLocation("America/New_York")
	return &ScheduledService{
		svc: svc,
		cfg: cfg,
		log: log.With().Str("component", "tradier_scheduled").Logger(),
		et:  et,
	}
}

// SetNotifier wires an optional notifier (Discord/Slack) that receives
// per-run summaries.
func (s *ScheduledService) SetNotifier(n ports.NotifierPort) {
	s.notifier = n
}

// Start begins the scheduler loop. First run fires immediately to populate
// today's row; subsequent runs align to cfg.RunAtHourET:MinuteET.
func (s *ScheduledService) Start(ctx context.Context) error {
	go func() {
		s.runOnce(ctx)
		s.loop(ctx)
	}()
	s.log.Info().
		Int("symbols", len(s.cfg.Symbols)).
		Int("run_hour_et", s.cfg.RunAtHourET).
		Int("run_minute_et", s.cfg.RunAtMinuteET).
		Msg("Tradier scheduled service started")
	return nil
}

// RunOnce exposes a manual trigger used by omo-data's one-shot mode.
func (s *ScheduledService) RunOnce(ctx context.Context) {
	s.runOnce(ctx)
}

func (s *ScheduledService) loop(ctx context.Context) {
	for {
		next := s.nextRunTime(time.Now())
		s.log.Info().Time("next_run", next).Msg("Tradier snapshot scheduled")

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
	start := time.Now()
	stats, err := s.svc.Snapshot(ctx, s.cfg.Symbols)
	elapsed := time.Since(start)
	if err != nil {
		s.log.Error().Err(err).Msg("Tradier snapshot failed")
	}
	s.log.Info().
		Int("symbols", stats.Symbols).
		Int("expirations", stats.Expirations).
		Int("rows", stats.ContractRows).
		Int("errors", stats.Errors).
		Dur("elapsed", elapsed).
		Msg("Tradier snapshot complete")

	if s.notifier != nil {
		msg := fmt.Sprintf("📈 **Tradier options chain snapshot**\n• Symbols: %d  |  Expirations: %d  |  Rows: %d  |  Errors: %d  |  Elapsed: %s",
			stats.Symbols, stats.Expirations, stats.ContractRows, stats.Errors, elapsed.Round(time.Second))
		if err := s.notifier.Notify(ctx, "omo-data", msg); err != nil {
			s.log.Warn().Err(err).Msg("notification failed")
		}
	}
}

func (s *ScheduledService) nextRunTime(now time.Time) time.Time {
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
