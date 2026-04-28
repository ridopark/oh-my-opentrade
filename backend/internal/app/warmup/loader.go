package warmup

import (
	"context"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// BarRepo is the minimal repository surface the loader needs.
// Both ports.RepositoryPort and the backtest's repo satisfy it.
type BarRepo interface {
	GetMarketBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
}

// calendarLookback returns a generous calendar window large enough to
// contain the required bar count after RTH filtering and weekend/holiday
// gaps. The loader trims the result; the lookback only needs to be a
// safe upper bound.
func calendarLookback(tf domain.Timeframe) time.Duration {
	switch tf {
	case "1m", "5m":
		// RTH = 6.5h/day, so 800 1m bars ~= 2 trading days. 30 calendar
		// days is safe even across long weekends and holiday stretches.
		return 30 * 24 * time.Hour
	case "1h":
		// 200 RTH 1h bars ~= 30 trading days. 90 calendar days covers it.
		return 90 * 24 * time.Hour
	case "1d":
		// 800 daily bars ~= 3.2 calendar years. Use 4 years.
		return 4 * 365 * 24 * time.Hour
	default:
		return 30 * 24 * time.Hour
	}
}

// Load fetches and trims warmup bars for one symbol/timeframe. Returns the
// last `spec.Required[tf]` bars (or fewer if the DB doesn't have enough,
// in which case the caller should log a warning that long-period
// indicators may not be fully converged).
func Load(ctx context.Context, repo BarRepo, spec Spec, sym domain.Symbol, tf domain.Timeframe, now time.Time) ([]domain.MarketBar, error) {
	required, ok := spec.Required[tf]
	if !ok || required <= 0 {
		return nil, fmt.Errorf("warmup: no required bars configured for timeframe %q", tf)
	}

	from := now.Add(-calendarLookback(tf))
	bars, err := repo.GetMarketBars(ctx, sym, tf, from, now)
	if err != nil {
		return nil, fmt.Errorf("warmup: load %s %s: %w", sym, tf, err)
	}

	if spec.RTHFilter && (tf == "1m" || tf == "5m") {
		bars = filterRTH(bars)
	}

	if len(bars) > required {
		bars = bars[len(bars)-required:]
	}
	return bars, nil
}

// filterRTH returns only bars whose timestamp falls within an NYSE
// regular trading session (09:30-16:00 ET, honoring early-close days
// and holidays).
func filterRTH(bars []domain.MarketBar) []domain.MarketBar {
	out := bars[:0]
	for _, b := range bars {
		if isRTH(b.Time) {
			out = append(out, b)
		}
	}
	return out
}

// isRTH reports whether t falls within an NYSE regular trading session.
// Returns false on weekends, holidays, before 09:30 ET, or after the
// session close (16:00 ET on regular days, 13:00 ET on early closes).
func isRTH(t time.Time) bool {
	loc := domain.NYLocation()
	nt := t.In(loc)
	if nt.Weekday() == time.Saturday || nt.Weekday() == time.Sunday {
		return false
	}
	if domain.IsNYSEHoliday(nt) {
		return false
	}
	closeHour, closeMin := domain.NYSECloseTime(nt)
	mins := nt.Hour()*60 + nt.Minute()
	openMins := 9*60 + 30
	closeMins := closeHour*60 + closeMin
	return mins >= openMins && mins < closeMins
}
