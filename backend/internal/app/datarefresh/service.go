// Package datarefresh provides a scheduled service that fetches daily bars
// (VIX from IBKR, equities from Alpaca) and keeps the monitor's VIX level current.
package datarefresh

import (
	"context"
	"time"

	"fmt"

	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// VIXSetter is satisfied by *monitor.Service.
type VIXSetter interface {
	SetVIXLevel(float64)
}

// BarStore abstracts the repository methods needed by this service.
type BarStore interface {
	SaveMarketBars(ctx context.Context, bars []domain.MarketBar) (int, error)
	GetMarketBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
	UpdateBarIndicators(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, t time.Time, ema9, ema21, ema50, ema200 float64, avwaps map[string]float64) error
}

// Config controls the daily data refresh schedule and scope.
type Config struct {
	VIXSymbol      string   // "VIX"
	IndexSymbols   []string // SPY, QQQ, IWM
	TradingSymbols []string // all trading universe symbols
	RunAtHourET    int      // 16 (4 PM ET)
	RunAtMinuteET  int      // 15
	LookbackDays   int      // 90
}

// Service fetches daily bars on a schedule and updates the monitor's VIX level.
type Service struct {
	cfg        Config
	ibkrData   ports.MarketDataPort
	alpacaData ports.MarketDataPort
	repo       BarStore
	monitor    VIXSetter
	log        zerolog.Logger
	etLocation *time.Location
}

// NewService creates a data refresh service.
func NewService(cfg Config, ibkrData, alpacaData ports.MarketDataPort, repo BarStore, monitor VIXSetter, log zerolog.Logger) *Service {
	if cfg.LookbackDays == 0 {
		cfg.LookbackDays = 90
	}
	if cfg.RunAtHourET == 0 {
		cfg.RunAtHourET = 16
	}
	if cfg.RunAtMinuteET == 0 {
		cfg.RunAtMinuteET = 15
	}
	if cfg.VIXSymbol == "" {
		cfg.VIXSymbol = "VIX"
	}
	et, _ := time.LoadLocation("America/New_York")
	return &Service{
		cfg:        cfg,
		ibkrData:   ibkrData,
		alpacaData: alpacaData,
		repo:       repo,
		monitor:    monitor,
		log:        log.With().Str("component", "datarefresh").Logger(),
		etLocation: et,
	}
}

// Start loads VIX from DB (or fetches if empty), then launches the daily scheduler.
func (s *Service) Start(ctx context.Context) error {
	// Set VIX level fast: DB first (instant), then SPY fallback (~200ms).
	// IBKR VIX is attempted in the background — it has a 10s timeout and often fails.
	vix, err := s.loadVIXFromDB(ctx)
	if err == nil && vix > 0 {
		s.monitor.SetVIXLevel(vix)
		s.log.Info().Float64("vix", vix).Msg("VIX level loaded from DB")
	} else {
		s.fallbackSPYRealizedVol(ctx)
	}

	// Refresh all bar data in background on startup (non-blocking).
	go func() {
		// Try IBKR VIX in background — may update the SPY fallback value.
		if err := s.refreshVIX(ctx); err != nil {
			s.log.Debug().Err(err).Msg("background IBKR VIX fetch failed (SPY fallback already set)")
		}

		allSymbols := s.deduplicatedSymbols()

		// Daily bars from Alpaca.
		saved, failed := s.refreshDailyBars(ctx, allSymbols)
		s.log.Info().Int("symbols_refreshed", saved).Int("symbols_failed", failed).Msg("startup daily bar refresh complete")

		// Backfill 1m bars for today + aggregate into 5m/15m/1h.
		s.backfillIntradayBars(ctx, allSymbols)
	}()

	go s.loop(ctx)
	s.log.Info().
		Str("vix_symbol", s.cfg.VIXSymbol).
		Int("index_symbols", len(s.cfg.IndexSymbols)).
		Int("trading_symbols", len(s.cfg.TradingSymbols)).
		Int("run_hour_et", s.cfg.RunAtHourET).
		Int("run_minute_et", s.cfg.RunAtMinuteET).
		Msg("daily data refresh service started")
	return nil
}

func (s *Service) loop(ctx context.Context) {
	for {
		next := s.nextRunTime(time.Now())
		s.log.Info().Time("next_run", next).Msg("daily refresh scheduled")

		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.RefreshAll(ctx)
		}
	}
}

func (s *Service) nextRunTime(now time.Time) time.Time {
	nowET := now.In(s.etLocation)
	target := time.Date(
		nowET.Year(), nowET.Month(), nowET.Day(),
		s.cfg.RunAtHourET, s.cfg.RunAtMinuteET, 0, 0,
		s.etLocation,
	)
	if nowET.After(target) {
		target = target.AddDate(0, 0, 1)
	}
	for target.Weekday() == time.Saturday || target.Weekday() == time.Sunday {
		target = target.AddDate(0, 0, 1)
	}
	return target
}

// RefreshAll fetches VIX + all daily + intraday bars and saves to DB.
func (s *Service) RefreshAll(ctx context.Context) {
	s.log.Info().Msg("daily refresh starting")

	if err := s.refreshVIX(ctx); err != nil {
		s.log.Error().Err(err).Msg("VIX refresh failed")
	}

	allSymbols := s.deduplicatedSymbols()

	saved, failed := s.refreshDailyBars(ctx, allSymbols)
	s.log.Info().
		Int("symbols_refreshed", saved).
		Int("symbols_failed", failed).
		Int("symbols_total", len(allSymbols)).
		Msg("daily refresh complete")
}

func (s *Service) refreshVIX(ctx context.Context) error {
	if s.ibkrData == nil {
		return fmt.Errorf("IBKR adapter not configured — VIX fetch skipped")
	}
	sym, err := domain.NewSymbol(s.cfg.VIXSymbol)
	if err != nil {
		return err
	}
	from := time.Now().AddDate(0, 0, -s.cfg.LookbackDays)
	bars, err := s.ibkrData.GetHistoricalBars(ctx, sym, domain.Timeframe("1d"), from, time.Now())
	if err != nil {
		return err
	}
	if len(bars) == 0 {
		s.log.Warn().Msg("IBKR returned 0 VIX bars")
		return fmt.Errorf("IBKR returned 0 VIX bars")
	}

	n, saveErr := s.repo.SaveMarketBars(ctx, bars)
	if saveErr != nil {
		s.log.Warn().Err(saveErr).Msg("failed to save VIX bars to DB")
	} else {
		s.log.Info().Int("bars_saved", n).Msg("VIX bars saved to DB")
	}

	latest := bars[len(bars)-1].Close
	s.monitor.SetVIXLevel(latest)
	s.log.Info().Float64("vix", latest).Int("bars", len(bars)).Msg("VIX level updated from IBKR")
	return nil
}

func (s *Service) refreshDailyBars(ctx context.Context, symbols []string) (saved, failed int) {
	from := time.Now().AddDate(0, 0, -s.cfg.LookbackDays)
	to := time.Now()

	for _, symStr := range symbols {
		sym, err := domain.NewSymbol(symStr)
		if err != nil {
			s.log.Warn().Str("symbol", symStr).Err(err).Msg("invalid symbol, skipping")
			failed++
			continue
		}
		bars, err := s.alpacaData.GetHistoricalBars(ctx, sym, domain.Timeframe("1d"), from, to)
		if err != nil {
			s.log.Warn().Str("symbol", symStr).Err(err).Msg("failed to fetch daily bars")
			failed++
			continue
		}
		if len(bars) == 0 {
			continue
		}
		if _, err := s.repo.SaveMarketBars(ctx, bars); err != nil {
			s.log.Warn().Str("symbol", symStr).Err(err).Msg("failed to save daily bars")
			failed++
			continue
		}

		// Compute EMA200 from full DB history (not just the fetched window).
		emaFrom := time.Now().AddDate(-2, 0, 0) // 2 years of daily bars
		allDaily, dbErr := s.repo.GetMarketBars(ctx, sym, domain.Timeframe("1d"), emaFrom, to)
		if dbErr == nil && len(allDaily) >= 200 {
			closes := make([]float64, len(allDaily))
			for i, b := range allDaily {
				closes[i] = b.Close
			}
			ema200 := monitor.ComputeStaticEMA(closes, 200)
			if ema200 > 0 {
				latest := allDaily[len(allDaily)-1]
				_ = s.repo.UpdateBarIndicators(ctx, sym, domain.Timeframe("1d"), latest.Time, 0, 0, 0, ema200, nil)
			}
		}

		saved++
	}
	return saved, failed
}

func (s *Service) loadVIXFromDB(ctx context.Context) (float64, error) {
	sym, err := domain.NewSymbol(s.cfg.VIXSymbol)
	if err != nil {
		return 0, err
	}
	from := time.Now().AddDate(0, 0, -7)
	bars, err := s.repo.GetMarketBars(ctx, sym, domain.Timeframe("1d"), from, time.Now())
	if err != nil {
		return 0, err
	}
	if len(bars) == 0 {
		return 0, nil
	}
	return bars[len(bars)-1].Close, nil
}

// fallbackSPYRealizedVol computes VIX from SPY daily bars via Alpaca (the old approach).
func (s *Service) fallbackSPYRealizedVol(ctx context.Context) {
	spy, err := domain.NewSymbol("SPY")
	if err != nil {
		return
	}
	from := time.Now().AddDate(0, 0, -60)
	bars, err := s.alpacaData.GetHistoricalBars(ctx, spy, domain.Timeframe("1d"), from, time.Now())
	if err != nil || len(bars) < 22 {
		s.log.Warn().Err(err).Int("bars", len(bars)).Msg("SPY realized vol fallback failed")
		return
	}
	rv := monitor.ComputeRealizedVol(bars, 20)
	s.monitor.SetVIXLevel(rv)
	s.log.Info().Float64("realized_vol", rv).Int("daily_bars", len(bars)).Msg("VIX level set from SPY realized vol (fallback)")
}

func (s *Service) deduplicatedSymbols() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, sym := range s.cfg.IndexSymbols {
		if _, ok := seen[sym]; !ok {
			seen[sym] = struct{}{}
			out = append(out, sym)
		}
	}
	for _, sym := range s.cfg.TradingSymbols {
		if _, ok := seen[sym]; !ok {
			seen[sym] = struct{}{}
			out = append(out, sym)
		}
	}
	return out
}

// backfillIntradayBars fetches 1m bars for today from Alpaca and aggregates
// them into 5m, 15m, and 1h bars. This fills gaps caused by omo-core restarts.
func (s *Service) backfillIntradayBars(ctx context.Context, symbols []string) {
	et, _ := time.LoadLocation("America/New_York")
	nowET := time.Now().In(et)
	// Session open: today 09:30 ET (or yesterday if before market open)
	sessionOpen := time.Date(nowET.Year(), nowET.Month(), nowET.Day(), 9, 30, 0, 0, et)
	if nowET.Before(sessionOpen) {
		sessionOpen = sessionOpen.AddDate(0, 0, -1)
	}
	// Fetch from session open (or start of day for pre-market)
	from := time.Date(nowET.Year(), nowET.Month(), nowET.Day(), 4, 0, 0, 0, et)
	if nowET.Before(from) {
		from = from.AddDate(0, 0, -1)
	}

	htfTimeframes := []domain.Timeframe{"5m", "15m", "1h"}
	totalBars := 0
	totalHTF := 0

	for _, symStr := range symbols {
		sym, err := domain.NewSymbol(symStr)
		if err != nil {
			continue
		}
		bars, err := s.alpacaData.GetHistoricalBars(ctx, sym, domain.Timeframe("1m"), from, time.Now())
		if err != nil {
			s.log.Warn().Str("symbol", symStr).Err(err).Msg("failed to fetch 1m bars for backfill")
			continue
		}
		if len(bars) == 0 {
			continue
		}

		// Save 1m bars.
		if n, err := s.repo.SaveMarketBars(ctx, bars); err == nil {
			totalBars += n
		}

		// Aggregate into HTF bars.
		for _, tf := range htfTimeframes {
			agg, err := domain.NewBarAggregator(sym, tf, sessionOpen)
			if err != nil {
				continue
			}
			var htfBars []domain.MarketBar
			for _, bar := range bars {
				if closed, ok := agg.Push(bar); ok {
					htfBars = append(htfBars, closed)
				}
			}
			if len(htfBars) > 0 {
				if n, err := s.repo.SaveMarketBars(ctx, htfBars); err == nil {
					totalHTF += n
				}

				// Persist EMA50 for the latest 1h bar (used for HTF warmup).
				if tf == domain.Timeframe("1h") && len(htfBars) >= 50 {
					closes := make([]float64, len(htfBars))
					for i, b := range htfBars {
						closes[i] = b.Close
					}
					ema50 := monitor.ComputeStaticEMA(closes, 50)
					if ema50 > 0 {
						_ = s.repo.UpdateBarIndicators(ctx, sym, domain.Timeframe("1h"), htfBars[len(htfBars)-1].Time, 0, 0, ema50, 0, nil)
					}
				}
			}
		}
	}

	s.log.Info().
		Int("symbols", len(symbols)).
		Int("bars_1m", totalBars).
		Int("bars_htf", totalHTF).
		Msg("intraday bar backfill complete")
}
