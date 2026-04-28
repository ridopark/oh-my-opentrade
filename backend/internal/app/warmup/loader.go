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

// TfDuration returns the bar-interval duration for the given timeframe.
// Used for the boot+1 bar fetch — the bar at warmupEnd-TfDuration mirrors
// the bar live processes in real-time between boot completion and the
// first replay snapshot.
func TfDuration(tf domain.Timeframe) time.Duration {
	switch tf {
	case "1m":
		return time.Minute
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "30m":
		return 30 * time.Minute
	case "1h":
		return time.Hour
	case "1d":
		return 24 * time.Hour
	default:
		return time.Minute
	}
}

// CalendarLookback returns a generous window the loader needs to fetch
// from the repo to satisfy spec.Required[tf] after RTH filter and
// truncation. Exposed so callers (e.g. backtest) can batch-fetch every
// symbol in one DB roundtrip and feed the result through Trim, instead
// of issuing N per-symbol Load calls.
func CalendarLookback(tf domain.Timeframe) time.Duration {
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

// Trim applies the spec's RTH filter (intraday only) and truncates to
// the last spec.Required[tf] bars. Pure transform — safe to call on a
// pre-fetched batch slice. If Required[tf] is unset, returns the bars
// after filtering only (no truncation).
func Trim(spec Spec, tf domain.Timeframe, bars []domain.MarketBar) []domain.MarketBar {
	if spec.RTHFilter && (tf == "1m" || tf == "5m") {
		bars = filterRTH(bars)
	}
	if required := spec.Required[tf]; required > 0 && len(bars) > required {
		bars = bars[len(bars)-required:]
	}
	return bars
}

// Load returns the last spec.Required[tf] bars after RTH filtering, or
// fewer if the DB doesn't have enough. The caller is responsible for
// logging a degradation warning when the result is short — long-period
// indicators may not be fully converged.
func Load(ctx context.Context, repo BarRepo, spec Spec, sym domain.Symbol, tf domain.Timeframe, now time.Time) ([]domain.MarketBar, error) {
	if _, ok := spec.Required[tf]; !ok {
		return nil, fmt.Errorf("warmup: no required bars configured for timeframe %q", tf)
	}
	from := now.Add(-CalendarLookback(tf))
	bars, err := repo.GetMarketBars(ctx, sym, tf, from, now)
	if err != nil {
		return nil, fmt.Errorf("warmup: load %s %s: %w", sym, tf, err)
	}
	return Trim(spec, tf, bars), nil
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
