package gapdetect

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingBackfiller captures every Backfill call and lets tests inject
// per-(symbol,tf) outcomes.
type recordingBackfiller struct {
	mu     sync.Mutex
	calls  []backfillCall
	saved  map[string]int   // key = "symbol:tf" → bars to report saved
	errs   map[string]error // key = "symbol:tf" → error to return
}

type backfillCall struct {
	symbol    domain.Symbol
	timeframe domain.Timeframe
	from      time.Time
	to        time.Time
}

func newRecordingBackfiller() *recordingBackfiller {
	return &recordingBackfiller{
		saved: make(map[string]int),
		errs:  make(map[string]error),
	}
}

func (r *recordingBackfiller) Backfill(_ context.Context, sym domain.Symbol, tf domain.Timeframe, from, to time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, backfillCall{symbol: sym, timeframe: tf, from: from, to: to})
	key := string(sym) + ":" + string(tf)
	return r.saved[key], r.errs[key]
}

// detectorWithGaps emits the same gap list (pre-populated with sym/tf) on
// every call so RunOnce sees a non-empty fill list per (symbol, tf) pair.
type detectorWithGaps struct {
	mu          sync.Mutex
	calls       int
	gapsBySymTF map[string][]ports.GapRange
	defaultGaps []ports.GapRange
}

func (d *detectorWithGaps) FindMissingBars(_ context.Context, sym domain.Symbol, tf domain.Timeframe, _, _ time.Time) ([]ports.GapRange, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	key := string(sym) + ":" + string(tf)
	if specific, ok := d.gapsBySymTF[key]; ok {
		return specific, nil
	}
	// Default: stamp the requested sym/tf onto each defaultGaps row.
	out := make([]ports.GapRange, len(d.defaultGaps))
	for i, g := range d.defaultGaps {
		g.Symbol = sym
		g.Timeframe = tf
		out[i] = g
	}
	return out, nil
}

func TestService_RunOnce_NoBackfiller_DetectionOnly(t *testing.T) {
	det := &detectorWithGaps{defaultGaps: []ports.GapRange{
		{ExpectedCount: 60, ActualCount: 55, Start: time.Now().Add(-time.Hour), End: time.Now()},
	}}
	bf := newRecordingBackfiller()
	svc := NewService(det, &fakeReader{}, zerolog.Nop(), func() time.Time { return time.Now() })
	// Backfiller not attached.

	total := svc.RunOnce(context.Background(), []domain.Symbol{"AAPL"})
	assert.Equal(t, len(scanWindows), total)
	assert.Empty(t, bf.calls, "no backfill calls when SetBackfiller is not used")
}

func TestService_RunOnce_WithBackfiller_FillsEachDetectedGap(t *testing.T) {
	now := time.Date(2026, 4, 25, 18, 0, 0, 0, time.UTC)
	gap1 := ports.GapRange{ExpectedCount: 60, ActualCount: 55, Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour)}
	gap2 := ports.GapRange{ExpectedCount: 60, ActualCount: 55, Start: now.Add(-30 * time.Minute), End: now.Add(-15 * time.Minute)}

	det := &detectorWithGaps{defaultGaps: []ports.GapRange{gap1, gap2}}
	bf := newRecordingBackfiller()
	bf.saved["AAPL:1m"] = 60
	bf.saved["AAPL:5m"] = 12

	svc := NewService(det, &fakeReader{}, zerolog.Nop(), func() time.Time { return now })
	svc.SetBackfiller(bf)

	total := svc.RunOnce(context.Background(), []domain.Symbol{"AAPL"})
	assert.Equal(t, 2*len(scanWindows), total)

	// Backfiller should be invoked twice per scanWindow that is NOT 1d
	// (since 1d is skipped). scanWindows has {1m, 5m, 15m, 1h, 1d} → 4
	// fillable timeframes × 2 gaps = 8 calls.
	assert.Len(t, bf.calls, 8)

	// Confirm each scanWindow's tf was passed through (excluding 1d).
	tfsCalled := map[domain.Timeframe]int{}
	for _, c := range bf.calls {
		tfsCalled[c.timeframe]++
	}
	assert.Equal(t, 2, tfsCalled["1m"])
	assert.Equal(t, 2, tfsCalled["5m"])
	assert.Equal(t, 2, tfsCalled["15m"])
	assert.Equal(t, 2, tfsCalled["1h"])
	assert.Equal(t, 0, tfsCalled["1d"], "1d is owned by datarefresh, not intraday backfill")
}

func TestService_RunOnce_WithBackfiller_FetchErrorContinues(t *testing.T) {
	det := &detectorWithGaps{defaultGaps: []ports.GapRange{
		{ExpectedCount: 60, ActualCount: 55, Start: time.Now().Add(-time.Hour), End: time.Now()},
	}}
	boom := errors.New("alpaca 503")
	bf := newRecordingBackfiller()
	// Inject error for every (AAPL, tf) combination — service must continue
	// onto MSFT despite AAPL's repeated failures.
	for _, tf := range []domain.Timeframe{"1m", "5m", "15m", "1h"} {
		bf.errs["AAPL:"+string(tf)] = boom
	}

	svc := NewService(det, &fakeReader{}, zerolog.Nop(), func() time.Time { return time.Now() })
	svc.SetBackfiller(bf)

	_ = svc.RunOnce(context.Background(), []domain.Symbol{"AAPL", "MSFT"})

	// AAPL: 4 fillable tfs × 1 gap = 4 attempts.
	// MSFT: same = 4 attempts.
	// 1d is skipped before reaching backfiller.
	assert.Len(t, bf.calls, 8, "MSFT still attempted after AAPL fails")
}

func TestService_RunOnce_WithBackfiller_NoMissingBars_SkipsFill(t *testing.T) {
	// detector returns no gaps → missing count stays 0 → backfiller untouched.
	det := &detectorWithGaps{defaultGaps: nil}
	bf := newRecordingBackfiller()
	svc := NewService(det, &fakeReader{}, zerolog.Nop(), func() time.Time { return time.Now() })
	svc.SetBackfiller(bf)

	_ = svc.RunOnce(context.Background(), []domain.Symbol{"AAPL"})
	assert.Empty(t, bf.calls)
}

// fakeFetcher records every GetHistoricalBars call and returns canned bars.
type fakeFetcher struct {
	bars []domain.MarketBar
	err  error
}

func (f *fakeFetcher) GetHistoricalBars(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _, _ time.Time) ([]domain.MarketBar, error) {
	return f.bars, f.err
}

// fakeSaver counts SaveMarketBars calls; can fail with an injected error.
type fakeSaver struct {
	saved      [][]domain.MarketBar
	saveCount  int
	saveErr    error
	saveErrAt  int // 0-indexed call number that fails; -1 = never
	failedOnce bool
}

func newFakeSaver() *fakeSaver { return &fakeSaver{saveErrAt: -1} }

func (f *fakeSaver) SaveMarketBars(_ context.Context, bars []domain.MarketBar) (int, error) {
	idx := f.saveCount
	f.saveCount++
	f.saved = append(f.saved, bars)
	if f.saveErrAt == idx && !f.failedOnce {
		f.failedOnce = true
		// Report partial save (e.g. half) to exercise the partial-count path.
		return len(bars) / 2, f.saveErr
	}
	return len(bars), nil
}

func TestIntradayBarBackfiller_Backfill_HappyPath(t *testing.T) {
	bar := func(t time.Time) domain.MarketBar {
		return domain.MarketBar{Time: t, Symbol: "AAPL", Timeframe: "1m", Open: 100, High: 101, Low: 99, Close: 100.5, Volume: 1000}
	}
	t0 := time.Date(2026, 4, 25, 18, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{bars: []domain.MarketBar{bar(t0), bar(t0.Add(time.Minute)), bar(t0.Add(2 * time.Minute))}}
	saver := newFakeSaver()
	bf := NewIntradayBarBackfiller(fetcher, saver)

	saved, err := bf.Backfill(context.Background(), "AAPL", "1m", t0, t0.Add(3*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, 3, saved)
	require.Len(t, saver.saved, 1)
	assert.Len(t, saver.saved[0], 3)
}

func TestIntradayBarBackfiller_Backfill_EmptyResponse(t *testing.T) {
	bf := NewIntradayBarBackfiller(&fakeFetcher{bars: nil}, newFakeSaver())
	t0 := time.Now().UTC()
	saved, err := bf.Backfill(context.Background(), "AAPL", "1m", t0.Add(-time.Hour), t0)
	require.NoError(t, err, "empty broker response is not an error — asset may not be tradable yet")
	assert.Equal(t, 0, saved)
}

func TestIntradayBarBackfiller_Backfill_FetchError(t *testing.T) {
	boom := errors.New("alpaca 502 bad gateway")
	bf := NewIntradayBarBackfiller(&fakeFetcher{err: boom}, newFakeSaver())
	t0 := time.Now().UTC()
	saved, err := bf.Backfill(context.Background(), "AAPL", "1m", t0.Add(-time.Hour), t0)
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 0, saved)
}

func TestIntradayBarBackfiller_Backfill_SaveError_ReturnsPartialCount(t *testing.T) {
	t0 := time.Date(2026, 4, 25, 18, 0, 0, 0, time.UTC)
	bar := func(t time.Time) domain.MarketBar {
		return domain.MarketBar{Time: t, Symbol: "AAPL", Timeframe: "1m", Open: 1, High: 1, Low: 1, Close: 1, Volume: 1}
	}
	fetcher := &fakeFetcher{bars: []domain.MarketBar{bar(t0), bar(t0.Add(time.Minute)), bar(t0.Add(2 * time.Minute)), bar(t0.Add(3 * time.Minute))}}
	saver := newFakeSaver()
	saver.saveErr = errors.New("connection reset")
	saver.saveErrAt = 0

	bf := NewIntradayBarBackfiller(fetcher, saver)
	saved, err := bf.Backfill(context.Background(), "AAPL", "1m", t0, t0.Add(4*time.Minute))
	require.Error(t, err)
	assert.Equal(t, 2, saved, "partial save count surfaces so callers can update gauges accurately")
}

func TestIntradayBarBackfiller_Backfill_RejectsEmptyWindow(t *testing.T) {
	bf := NewIntradayBarBackfiller(&fakeFetcher{}, newFakeSaver())
	t0 := time.Now().UTC()
	_, err := bf.Backfill(context.Background(), "AAPL", "1m", t0, t0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-empty")
}

func TestNewIntradayBarBackfiller_PanicsOnNilDeps(t *testing.T) {
	assert.Panics(t, func() { _ = NewIntradayBarBackfiller(nil, newFakeSaver()) })
	assert.Panics(t, func() { _ = NewIntradayBarBackfiller(&fakeFetcher{}, nil) })
}

func TestFillRange_SkipsDailyTimeframe(t *testing.T) {
	bf := newRecordingBackfiller()
	gap := ports.GapRange{Symbol: "AAPL", Timeframe: "1d", Start: time.Now().Add(-24 * time.Hour), End: time.Now()}
	saved, err := fillRange(context.Background(), bf, gap)
	assert.Equal(t, 0, saved)
	assert.ErrorIs(t, err, errBackfillSkippedDailyTF)
	assert.Empty(t, bf.calls, "1d gaps short-circuit before the backfiller")
}
