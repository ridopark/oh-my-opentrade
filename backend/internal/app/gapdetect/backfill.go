package gapdetect

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// BarBackfiller fills a single (symbol, timeframe) gap range from an
// upstream broker and persists the resulting bars. Returns the number of
// rows actually saved (zero is valid when the broker returns nothing for
// the window — e.g. an asset that wasn't tradable yet).
type BarBackfiller interface {
	Backfill(ctx context.Context, sym domain.Symbol, tf domain.Timeframe, from, to time.Time) (int, error)
}

// BarFetcher reads bars from an upstream broker. The omo-data RoutingFetcher
// satisfies this implicitly; the narrow interface keeps tests off the broker.
type BarFetcher interface {
	GetHistoricalBars(ctx context.Context, sym domain.Symbol, tf domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
}

// BarSaver persists a batch of bars. Repository.SaveMarketBars satisfies it.
type BarSaver interface {
	SaveMarketBars(ctx context.Context, bars []domain.MarketBar) (int, error)
}

// IntradayBarBackfiller fills detected gap ranges by fetching bars directly
// from the upstream broker at the requested timeframe and saving them.
//
// Phase 5 of the parity plan: the only writer for intraday market_bars
// during RTH is omo-core's WS pipeline. If omo-core is down for any portion
// of a session, those minutes are silently missing — every backtest that
// reads the day from market_bars then disagrees with what live actually
// saw. Nightly gap-fill closes the hole. Designed to be idempotent: the
// SaveMarketBars upsert handles re-runs over the same window safely.
type IntradayBarBackfiller struct {
	fetcher BarFetcher
	saver   BarSaver
}

// NewIntradayBarBackfiller wires a fetcher (e.g. backfill.RoutingFetcher) to
// a saver (Repository). Both must be non-nil; the constructor panics
// otherwise so a misconfigured wire doesn't fail silently at fill time.
func NewIntradayBarBackfiller(fetcher BarFetcher, saver BarSaver) *IntradayBarBackfiller {
	if fetcher == nil || saver == nil {
		panic("gapdetect: IntradayBarBackfiller requires non-nil fetcher and saver")
	}
	return &IntradayBarBackfiller{fetcher: fetcher, saver: saver}
}

// Backfill fetches and persists bars for the given range. Empty broker
// responses return (0, nil) — that's distinct from a fetch error and lets
// the caller continue without alarm.
func (b *IntradayBarBackfiller) Backfill(ctx context.Context, sym domain.Symbol, tf domain.Timeframe, from, to time.Time) (int, error) {
	if !from.Before(to) {
		return 0, fmt.Errorf("gapdetect: backfill window must be non-empty: from=%s to=%s", from, to)
	}
	bars, err := b.fetcher.GetHistoricalBars(ctx, sym, tf, from, to)
	if err != nil {
		return 0, fmt.Errorf("gapdetect: fetch %s %s: %w", sym, tf, err)
	}
	if len(bars) == 0 {
		return 0, nil
	}
	saved, err := b.saver.SaveMarketBars(ctx, bars)
	if err != nil {
		return saved, fmt.Errorf("gapdetect: save %s %s: %w", sym, tf, err)
	}
	return saved, nil
}

// errBackfillSkippedDailyTF reports an attempt to backfill 1d ranges through
// the intraday path. Daily bars are owned by datarefresh.refreshDailyBars;
// the fill loop short-circuits before reaching the broker so we don't double-
// schedule the daily refresh.
var errBackfillSkippedDailyTF = errors.New("gapdetect: 1d timeframe is owned by datarefresh, not intraday backfill")

var (
	backfillAttemptsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "omo_gapfill_attempts_total",
		Help: "Number of gap-range backfill attempts per (symbol, timeframe, outcome). Outcome ∈ {ok, fetch_error, save_error, empty}.",
	}, []string{"symbol", "timeframe", "outcome"})

	backfillBarsSavedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "omo_gapfill_bars_saved_total",
		Help: "Number of bars persisted by gap backfill per (symbol, timeframe).",
	}, []string{"symbol", "timeframe"})
)

// fillRange dispatches one detected gap to the backfiller and updates the
// outcome counters. Returns the number of bars persisted; logs are emitted
// by the caller (Service.RunOnce) so it can include surrounding context.
func fillRange(ctx context.Context, b BarBackfiller, gap ports.GapRange) (int, error) {
	if gap.Timeframe == "1d" {
		return 0, errBackfillSkippedDailyTF
	}
	saved, err := b.Backfill(ctx, gap.Symbol, gap.Timeframe, gap.Start, gap.End)
	switch {
	case err != nil && saved == 0:
		backfillAttemptsTotal.WithLabelValues(string(gap.Symbol), string(gap.Timeframe), "fetch_error").Inc()
	case err != nil:
		backfillAttemptsTotal.WithLabelValues(string(gap.Symbol), string(gap.Timeframe), "save_error").Inc()
		backfillBarsSavedTotal.WithLabelValues(string(gap.Symbol), string(gap.Timeframe)).Add(float64(saved))
	case saved == 0:
		backfillAttemptsTotal.WithLabelValues(string(gap.Symbol), string(gap.Timeframe), "empty").Inc()
	default:
		backfillAttemptsTotal.WithLabelValues(string(gap.Symbol), string(gap.Timeframe), "ok").Inc()
		backfillBarsSavedTotal.WithLabelValues(string(gap.Symbol), string(gap.Timeframe)).Add(float64(saved))
	}
	return saved, err
}
