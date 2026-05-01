package optionsimport

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// ScheduledConfig controls the daily DoltHub options chain refresh.
type ScheduledConfig struct {
	Symbols       []string
	LookbackDays  int // how many days back to check for gaps (default 7)
	RunAtHourET   int // default 7 (7 AM ET — after DoltHub's overnight refresh)
	RunAtMinuteET int // default 0
	Concurrency   int // parallel symbol workers (default 4)
}

// ScheduledService runs daily DoltHub option chain imports.
type ScheduledService struct {
	svc      *DoltHubService
	cfg      ScheduledConfig
	notifier ports.NotifierPort
	log      zerolog.Logger
	et       *time.Location
}

// SetNotifier enables notifications after each refresh.
func (s *ScheduledService) SetNotifier(n ports.NotifierPort) {
	s.notifier = n
}

// NewScheduledService creates a scheduled DoltHub options import service.
func NewScheduledService(cfg ScheduledConfig, svc *DoltHubService, log zerolog.Logger) *ScheduledService {
	if cfg.LookbackDays == 0 {
		cfg.LookbackDays = 7
	}
	if cfg.RunAtHourET == 0 && cfg.RunAtMinuteET == 0 {
		cfg.RunAtHourET = 7
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 4
	}
	et, _ := time.LoadLocation("America/New_York")
	return &ScheduledService{
		svc: svc,
		cfg: cfg,
		log: log.With().Str("component", "dolthub_scheduled").Logger(),
		et:  et,
	}
}

// Start launches the background scheduler.
func (s *ScheduledService) Start(ctx context.Context) error {
	// Run once on startup to fill any gaps.
	go func() {
		s.refresh(ctx)
		s.loop(ctx)
	}()
	s.log.Info().
		Int("symbols", len(s.cfg.Symbols)).
		Int("lookback_days", s.cfg.LookbackDays).
		Int("run_hour_et", s.cfg.RunAtHourET).
		Int("run_minute_et", s.cfg.RunAtMinuteET).
		Msg("DoltHub options scheduled service started")
	return nil
}

func (s *ScheduledService) loop(ctx context.Context) {
	for {
		next := s.nextRunTime(time.Now())
		s.log.Info().Time("next_run", next).Msg("DoltHub options refresh scheduled")

		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.refresh(ctx)
		}
	}
}

func (s *ScheduledService) refresh(ctx context.Context) {
	from := time.Now().AddDate(0, 0, -s.cfg.LookbackDays)
	to := time.Now().AddDate(0, 0, -1) // yesterday (DoltHub is next-day)

	s.log.Info().
		Str("from", from.Format("2006-01-02")).
		Str("to", to.Format("2006-01-02")).
		Msg("starting DoltHub options refresh")

	sem := make(chan struct{}, s.cfg.Concurrency)
	var wg sync.WaitGroup
	var imported, failed atomic.Int32

	for _, sym := range s.cfg.Symbols {
		wg.Add(1)
		go func(sym string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := s.svc.EnsureData(ctx, sym, from, to); err != nil {
				s.log.Warn().Err(err).Str("symbol", sym).Msg("DoltHub import failed")
				failed.Add(1)
			} else {
				imported.Add(1)
			}
		}(sym)
	}
	wg.Wait()

	s.log.Info().Msg("DoltHub options refresh complete")

	if s.notifier != nil {
		msg := fmt.Sprintf("📋 **DoltHub options chain refresh**\n• Range: %s → %s\n• Symbols: %d OK, %d failed",
			from.Format("2006-01-02"), to.Format("2006-01-02"),
			imported.Load(), failed.Load())
		if err := s.notifier.Notify(ctx, "omo-data", msg); err != nil {
			s.log.Warn().Err(err).Msg("notification failed")
		}
	}
}

// RunOnce executes a single refresh pass (for run-once mode).
func (s *ScheduledService) RunOnce(ctx context.Context) {
	s.refresh(ctx)
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
