// Package earnings provides a scheduled service that fetches upcoming
// earnings dates from Finnhub and stores them in the earnings_calendar table.
package earnings

import (
	"context"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/finnhub"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// Config controls the earnings calendar refresh schedule.
type Config struct {
	Symbols       []string
	RunAtHourET   int // default 5 (5 AM ET — before market open)
	RunAtMinuteET int
	LookbackDays  int // how far ahead to look (default 90)
}

// Service refreshes earnings dates on a daily schedule.
type Service struct {
	cfg      Config
	finnhub  *finnhub.Client
	repo     ports.EarningsCalendarPort
	notifier ports.NotifierPort
	log      zerolog.Logger
	et       *time.Location
}

// NewService creates a new earnings calendar service.
func NewService(cfg Config, client *finnhub.Client, repo ports.EarningsCalendarPort, log zerolog.Logger) *Service {
	if cfg.RunAtHourET == 0 && cfg.RunAtMinuteET == 0 {
		cfg.RunAtHourET = 5
	}
	if cfg.LookbackDays == 0 {
		cfg.LookbackDays = 90
	}
	et, _ := time.LoadLocation("America/New_York")
	return &Service{
		cfg:     cfg,
		finnhub: client,
		repo:    repo,
		log:     log.With().Str("component", "earnings").Logger(),
		et:      et,
	}
}

// SetNotifier sets an optional notifier for status updates.
func (s *Service) SetNotifier(n ports.NotifierPort) {
	s.notifier = n
}

// Start launches the background scheduler goroutine.
func (s *Service) Start(ctx context.Context) error {
	go s.loop(ctx)
	s.log.Info().
		Int("run_hour_et", s.cfg.RunAtHourET).
		Int("symbols", len(s.cfg.Symbols)).
		Msg("earnings calendar service started")
	return nil
}

func (s *Service) loop(ctx context.Context) {
	for {
		next := s.nextRunTime(time.Now())
		s.log.Debug().Time("next_run", next).Msg("earnings refresh scheduled")

		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if err := s.Refresh(ctx); err != nil {
				s.log.Error().Err(err).Msg("earnings refresh failed")
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

// Refresh fetches earnings dates for all configured symbols and upserts them.
func (s *Service) Refresh(ctx context.Context) error {
	from := time.Now()
	to := from.AddDate(0, 0, s.cfg.LookbackDays)

	s.log.Info().
		Int("symbols", len(s.cfg.Symbols)).
		Str("from", from.Format("2006-01-02")).
		Str("to", to.Format("2006-01-02")).
		Msg("fetching earnings calendar")

	entries, err := s.finnhub.FetchEarningsBatch(ctx, s.cfg.Symbols, from, to)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		s.log.Info().Msg("no upcoming earnings found for configured symbols")
		return nil
	}

	if err := s.repo.UpsertBatch(ctx, entries); err != nil {
		return err
	}

	s.log.Info().Int("entries", len(entries)).Msg("earnings calendar updated")

	if s.notifier != nil {
		msg := fmt.Sprintf("Earnings calendar updated: %d entries for %d symbols", len(entries), len(s.cfg.Symbols))
		_ = s.notifier.Notify(ctx, "earnings", msg)
	}

	return nil
}
