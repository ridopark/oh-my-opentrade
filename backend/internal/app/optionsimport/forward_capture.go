package optionsimport

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// ForwardCaptureConfig drives the daily live-snapshot dump into
// historical_option_chain. The 16:15 ET default mirrors the IV collector's
// existing slot, since both consume the same Alpaca options snapshot endpoint.
type ForwardCaptureConfig struct {
	Symbols       []string
	RunAtHourET   int // default 16
	RunAtMinuteET int // default 15
	Concurrency   int // parallel symbol workers (default 4)
}

// ForwardCaptureService captures today's full option chain (calls + puts,
// DTE 1-60) into historical_option_chain so backtests on recent dates have
// real OPRA-derived strikes rather than synthetic ones. Lifted out of the
// ivcollector — the IV collector's job is the ATM IV snapshot, not chain
// capture.
type ForwardCaptureService struct {
	cfg         ForwardCaptureConfig
	optionsData ports.OptionsMarketDataPort
	repo        ports.HistoricalOptionsPort
	notifier    ports.NotifierPort
	log         zerolog.Logger
	et          *time.Location
}

// NewForwardCaptureService wires a service. Both ports are required —
// without them the daily capture cannot run. Notifier is optional.
func NewForwardCaptureService(
	cfg ForwardCaptureConfig,
	optionsData ports.OptionsMarketDataPort,
	repo ports.HistoricalOptionsPort,
	log zerolog.Logger,
) *ForwardCaptureService {
	if cfg.RunAtHourET == 0 && cfg.RunAtMinuteET == 0 {
		cfg.RunAtHourET = 16
		cfg.RunAtMinuteET = 15
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 4
	}
	et, _ := time.LoadLocation("America/New_York")
	return &ForwardCaptureService{
		cfg:         cfg,
		optionsData: optionsData,
		repo:        repo,
		log:         log.With().Str("component", "forward_capture").Logger(),
		et:          et,
	}
}

// SetNotifier opts the service into Discord/Telegram notification on each
// capture run.
func (s *ForwardCaptureService) SetNotifier(n ports.NotifierPort) {
	s.notifier = n
}

// Start launches the background scheduler: one immediate startup tick to
// catch up if today's slot was missed (omo-data restart, crash mid-fetch),
// then a daily 16:15 ET tick. Idempotent — repo.HasData skips the work
// when today's chain is already captured, so a startup tick after the
// daily one is a no-op rather than a duplicate write.
func (s *ForwardCaptureService) Start(ctx context.Context) error {
	go func() {
		s.runOnce(ctx, time.Now())
		s.loop(ctx)
	}()
	s.log.Info().
		Int("symbols", len(s.cfg.Symbols)).
		Int("run_hour_et", s.cfg.RunAtHourET).
		Int("run_minute_et", s.cfg.RunAtMinuteET).
		Msg("forward capture service started")
	return nil
}

// RunOnce executes a single capture pass for today, used by the
// omo-data --run-once mode.
func (s *ForwardCaptureService) RunOnce(ctx context.Context) {
	s.runOnce(ctx, time.Now())
}

func (s *ForwardCaptureService) loop(ctx context.Context) {
	for {
		next := s.nextRunTime(time.Now())
		s.log.Debug().Time("next_run", next).Msg("forward capture scheduled")
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.runOnce(ctx, time.Now())
		}
	}
}

func (s *ForwardCaptureService) nextRunTime(now time.Time) time.Time {
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

func (s *ForwardCaptureService) runOnce(ctx context.Context, now time.Time) {
	if len(s.cfg.Symbols) == 0 {
		return
	}
	day := now
	s.log.Info().Int("symbols", len(s.cfg.Symbols)).Str("date", day.Format("2006-01-02")).
		Msg("forward capture run starting")

	sem := make(chan struct{}, s.cfg.Concurrency)
	var wg sync.WaitGroup
	var captured, skipped, failed atomic.Int32

	for _, sym := range s.cfg.Symbols {
		wg.Add(1)
		go func(sym string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			result, err := s.CaptureDay(ctx, sym, day)
			switch {
			case err != nil:
				s.log.Warn().Err(err).Str("symbol", sym).Msg("forward capture failed")
				failed.Add(1)
			case result == CaptureSkippedExisting:
				skipped.Add(1)
			case result == CaptureWroteRows:
				captured.Add(1)
			}
		}(sym)
	}
	wg.Wait()

	s.log.Info().
		Int32("captured", captured.Load()).
		Int32("skipped_existing", skipped.Load()).
		Int32("failed", failed.Load()).
		Int("total", len(s.cfg.Symbols)).
		Msg("forward capture run complete")

	if s.notifier != nil {
		msg := fmt.Sprintf("📈 **Forward option chain capture**\n• Date: %s\n• Captured: %d, skipped (already had data): %d, failed: %d",
			day.Format("2006-01-02"), captured.Load(), skipped.Load(), failed.Load())
		if err := s.notifier.Notify(ctx, "omo-data", msg); err != nil {
			s.log.Warn().Err(err).Msg("notification failed")
		}
	}
}

type CaptureResult int

const (
	CaptureWroteRows CaptureResult = iota
	CaptureSkippedExisting
	CaptureNoSnapshots
)

// CaptureDay fetches the full live option chain (calls + puts, wide DTE
// window 1-60) for the given underlying via Alpaca, converts each snapshot
// to a HistoricalOptionChainRow stamped at `day`, and persists the batch.
// Idempotent — when repo.HasData reports the date already covered, returns
// CaptureSkippedExisting without re-fetching.
func (s *ForwardCaptureService) CaptureDay(ctx context.Context, symbol string, day time.Time) (CaptureResult, error) {
	today := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)

	has, err := s.repo.HasData(ctx, domain.Symbol(symbol), today)
	if err != nil {
		return CaptureNoSnapshots, fmt.Errorf("forward_capture: HasData %s %s: %w",
			symbol, today.Format("2006-01-02"), err)
	}
	if has {
		s.log.Debug().Str("symbol", symbol).Str("date", today.Format("2006-01-02")).
			Msg("historical option chain already captured for date")
		return CaptureSkippedExisting, nil
	}

	var allRows []domain.HistoricalOptionChainRow
	// Target expiry ~30d out with a wide DTE window (1-60) so the wider
	// chain lands rather than just the ATM bracket the IV collector
	// uses. Live adapters ignore the range hint when their native window
	// is wider; Alpaca observes it.
	targetExpiry := day.AddDate(0, 0, 30)
	for _, right := range []domain.OptionRight{domain.OptionRightCall, domain.OptionRightPut} {
		snaps, err := s.optionsData.GetOptionChain(ctx, domain.Symbol(symbol), targetExpiry, right, 1, 60)
		if err != nil {
			s.log.Warn().Err(err).Str("symbol", symbol).Str("right", string(right)).
				Msg("failed to fetch option chain for forward capture")
			continue
		}
		for _, snap := range snaps {
			if snap.Bid <= 0 && snap.Ask <= 0 {
				continue
			}
			allRows = append(allRows, domain.HistoricalOptionChainRow{
				Date:       today,
				Symbol:     domain.Symbol(symbol),
				Expiration: snap.Expiry,
				Strike:     snap.Strike,
				Right:      snap.Right,
				Bid:        snap.Bid,
				Ask:        snap.Ask,
				IV:         snap.IV,
				Delta:      snap.Delta,
				Gamma:      snap.Gamma,
				Theta:      snap.Theta,
				Vega:       snap.Vega,
				Rho:        snap.Rho,
			})
		}
	}

	if len(allRows) == 0 {
		s.log.Warn().Str("symbol", symbol).Msg("no option contracts captured for forward chain")
		return CaptureNoSnapshots, nil
	}

	if err := s.repo.SaveBatch(ctx, allRows); err != nil {
		return CaptureNoSnapshots, fmt.Errorf("forward_capture: SaveBatch %s: %w", symbol, err)
	}

	s.log.Info().Str("symbol", symbol).Int("contracts", len(allRows)).
		Msg("forward option chain captured")
	return CaptureWroteRows, nil
}
