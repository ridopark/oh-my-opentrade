package simbroker

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"golang.org/x/sync/singleflight"
)

// OptionBarVolumeAdapter wraps an OptionsHistoricalBarsPort and serves
// BarVolume(occ, ts, tf) queries from a per-OCC bar cache. On first touch
// for an OCC, the adapter fetches the entire [from, to] series so subsequent
// lookups are O(log n) binary searches against the cached slice. Concurrent
// first-touches for the same OCC are collapsed via singleflight.
//
// Tier 1 ship-1 only supports 1m bars. Other timeframes return (0, nil) which
// the broker helper treats as "data unavailable, no impact applied".
type OptionBarVolumeAdapter struct {
	source ports.OptionsHistoricalBarsPort
	from   time.Time
	to     time.Time
	log    zerolog.Logger

	mu    sync.RWMutex
	cache map[domain.Symbol][]domain.MarketBar // sorted by Time ascending

	sf singleflight.Group
}

// NewOptionBarVolumeAdapter constructs an adapter bound to a backtest's
// [from, to] window. Subsequent BarVolume calls fetch within that window only.
func NewOptionBarVolumeAdapter(source ports.OptionsHistoricalBarsPort, from, to time.Time, log zerolog.Logger) *OptionBarVolumeAdapter {
	return &OptionBarVolumeAdapter{
		source: source,
		from:   from,
		to:     to,
		log:    log.With().Str("component", "option_bar_volume").Logger(),
		cache:  make(map[domain.Symbol][]domain.MarketBar),
	}
}

// BarVolume returns the contract count for the bar containing ts. Worst-case
// fetch (cache miss) is bounded by the caller's ctx deadline. Returns
// (0, nil) when the source has no data for the OCC or the bar is unknown —
// the helper treats this identically to a port error.
func (a *OptionBarVolumeAdapter) BarVolume(ctx context.Context, occ domain.Symbol, ts time.Time, tf domain.Timeframe) (int64, error) {
	if tf != "" && tf != domain.Timeframe("1m") {
		return 0, nil
	}

	a.mu.RLock()
	bars, ok := a.cache[occ]
	a.mu.RUnlock()

	if !ok {
		fetched, err, _ := a.sf.Do(string(occ), func() (any, error) {
			a.mu.RLock()
			if cached, hit := a.cache[occ]; hit {
				a.mu.RUnlock()
				return cached, nil
			}
			a.mu.RUnlock()

			// First-touch fetch loads the full backtest [from, to] series
			// for this OCC — typically a one-shot API call that takes
			// seconds. Use a separate timeout (5s) so a slow Alpaca doesn't
			// stall a SubmitOrder ctx whose parent (the runner) has its own
			// budget. Subsequent calls hit cache and never reach this path.
			fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			result, fetchErr := a.source.GetHistoricalOptionBars(fetchCtx, []domain.Symbol{occ}, a.from, a.to)
			if fetchErr != nil {
				a.log.Debug().Err(fetchErr).Str("occ", string(occ)).Msg("bar volume fetch failed")
				// Cache the empty slice so subsequent calls don't refetch.
				a.mu.Lock()
				a.cache[occ] = nil
				a.mu.Unlock()
				return []domain.MarketBar(nil), nil
			}
			ocBars := result[occ]
			sort.Slice(ocBars, func(i, j int) bool { return ocBars[i].Time.Before(ocBars[j].Time) })

			a.mu.Lock()
			a.cache[occ] = ocBars
			a.mu.Unlock()
			return ocBars, nil
		})
		if err != nil {
			return 0, err
		}
		bars = fetched.([]domain.MarketBar)
	}

	if len(bars) == 0 {
		return 0, nil
	}

	// Locate the bar whose start time covers ts. 1m bars are aligned to
	// minute boundaries, so we round ts down to the nearest minute and
	// binary-search by exact Time match.
	target := ts.Truncate(time.Minute)
	idx := sort.Search(len(bars), func(i int) bool {
		return !bars[i].Time.Before(target)
	})
	if idx < len(bars) && bars[idx].Time.Equal(target) {
		return int64(bars[idx].Volume), nil
	}
	return 0, nil
}
