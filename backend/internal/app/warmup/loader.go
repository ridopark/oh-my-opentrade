package warmup

import (
	"context"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// BarRepo is the minimal repository surface the loader needs. Declared
// locally so callers can pass any narrow type without depending on the
// 30-method ports.RepositoryPort.
type BarRepo interface {
	GetMarketBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
}

func calendarLookback(tf domain.Timeframe) time.Duration {
	switch tf {
	case "1m", "5m":
		return 30 * 24 * time.Hour
	case "1h":
		return 90 * 24 * time.Hour
	case "1d":
		return 4 * 365 * 24 * time.Hour
	default:
		return 30 * 24 * time.Hour
	}
}

// Load returns the last `spec.Required[tf]` bars after RTH filtering, or
// fewer if the DB doesn't have enough. The caller is responsible for
// logging a degradation warning when the result is short — long-period
// indicators may not be fully converged.
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

func filterRTH(bars []domain.MarketBar) []domain.MarketBar {
	out := bars[:0]
	for _, b := range bars {
		if isRTH(b.Time) {
			out = append(out, b)
		}
	}
	return out
}

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
