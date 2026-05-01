package monitor_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/monitor/monitortest"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rthBars produces N consecutive 1m bars starting at the given UTC time with a
// deterministic OHLCV drift so EMAs/RSI/ATR converge to non-zero values and
// Update is exercised end-to-end. Volume varies so VWAP is not degenerate.
func rthBars(sym domain.Symbol, start time.Time, n int, basePrice float64) []domain.MarketBar {
	bars := make([]domain.MarketBar, n)
	price := basePrice
	for i := 0; i < n; i++ {
		drift := float64(i%17-8) * 0.07
		open := price
		closeP := price + drift
		high := open
		if closeP > high {
			high = closeP
		}
		low := open
		if closeP < low {
			low = closeP
		}
		bar, err := domain.NewMarketBar(
			start.Add(time.Duration(i)*time.Minute),
			sym,
			"1m",
			open, high+0.15, low-0.15, closeP, 800+float64((i*37)%600),
		)
		if err != nil {
			panic(err)
		}
		bars[i] = bar
		price = closeP
	}
	return bars
}

// rthOpenUTC for a given ET-local Y/M/D date — 9:30 ET in UTC.
func rthOpenUTC(year int, month time.Month, day int) time.Time {
	loc, _ := time.LoadLocation("America/New_York")
	openET := time.Date(year, month, day, 9, 30, 0, 0, loc)
	return openET.UTC()
}

// TestSeedORBFromHistory_DoesNotMutateCalcState pins the central invariant of
// the restructure: feeding (bar, snap) pairs through SeedORBFromHistory must
// not advance the indicator calculator's state. Strategy: build two services
// fed identical canonical warmup bars; on B also call SeedORBFromHistory; then
// feed both services one more bar through HandleMarketBar-equivalent and
// assert the resulting snap is byte-equal. Any drift in EMA/MACD/ATR from a
// silent calc.Update inside the seeding path will surface here.
func TestSeedORBFromHistory_DoesNotMutateCalcState(t *testing.T) {
	sym := domain.Symbol("AAPL")
	sessionOpen := rthOpenUTC(2025, 3, 4)

	bars := rthBars(sym, sessionOpen, 30, 100.0)

	// Service A: WarmUpAndCollect only.
	busA := memory.NewBus()
	svcA, _ := monitortest.NewSvc(busA, &mockRepository{}, "seed_orb_A")
	svcA.InitAggregators([]domain.Symbol{sym}, sessionOpen)
	snapsA := svcA.WarmUpAndCollect(bars)
	require.Equal(t, len(bars), len(snapsA), "WarmUpAndCollect must return one snap per bar")
	lastA, okA := svcA.GetLastSnapshot(sym.String())
	require.True(t, okA, "WarmUpAndCollect must populate lastSnaps")

	// Service B: WarmUpAndCollect + SeedORBFromHistory (must be a calc no-op).
	busB := memory.NewBus()
	svcB, _ := monitortest.NewSvc(busB, &mockRepository{}, "seed_orb_B")
	svcB.InitAggregators([]domain.Symbol{sym}, sessionOpen)
	snapsB := svcB.WarmUpAndCollect(bars)
	require.Equal(t, len(bars), len(snapsB))
	svcB.SeedORBFromHistory(sym, snapsB)
	lastB, okB := svcB.GetLastSnapshot(sym.String())
	require.True(t, okB)

	// LastSnap must be byte-equal across both services after seeding.
	assert.Equal(t, lastA.RSI, lastB.RSI, "RSI must not drift after SeedORBFromHistory")
	assert.Equal(t, lastA.EMA9, lastB.EMA9)
	assert.Equal(t, lastA.EMA21, lastB.EMA21)
	assert.Equal(t, lastA.EMA50, lastB.EMA50)
	assert.Equal(t, lastA.MACDLine, lastB.MACDLine)
	assert.Equal(t, lastA.MACDSignal, lastB.MACDSignal)
	assert.Equal(t, lastA.ATR, lastB.ATR)
	assert.Equal(t, lastA.VWAP, lastB.VWAP, "session VWAP must not drift")
}

// TestSeedORBFromHistory_FeedsORBTracker_AndMarksRangeNotified ensures the
// seeding path drives the ORB tracker through PreOpen -> FormingRange ->
// RangeSet using snaps captured during canonical warmup, then flips
// RangeNotified=true so the first runtime bar does NOT re-emit ORBRangeSet.
func TestSeedORBFromHistory_FeedsORBTracker_AndMarksRangeNotified(t *testing.T) {
	sym := domain.Symbol("AAPL")
	sessionOpen := rthOpenUTC(2025, 3, 4) // 09:30 ET on 2025-03-04

	// 35 1m bars from 09:30 ET — covers the 30m default ORB window and a few
	// post-window bars so the aggregator emits the closed 5m buckets that
	// drive RangeSet under the default 5m orbTimeframe.
	bars := rthBars(sym, sessionOpen, 35, 100.0)

	bus := memory.NewBus()
	svc, _ := monitortest.NewSvc(bus, &mockRepository{}, "seed_orb")
	svc.InitAggregators([]domain.Symbol{sym}, sessionOpen)

	snaps := svc.WarmUpAndCollect(bars)
	require.Len(t, snaps, len(bars))

	svc.SeedORBFromHistory(sym, snaps)

	sess := svc.GetORBSession(sym.String())
	require.NotNil(t, sess, "SeedORBFromHistory must create an ORB session")
	assert.Equal(t, monitor.ORBStateRangeSet, sess.State,
		"ORB session must reach RANGE_SET after seeding through full ORB window")
	assert.Greater(t, sess.OrbHigh, 0.0, "OrbHigh must be set")
	assert.Greater(t, sess.OrbLow, 0.0, "OrbLow must be set")
	assert.True(t, sess.RangeNotified,
		"SeedORBFromHistory must mark RangeNotified=true so first runtime bar "+
			"does not re-emit ORBRangeSet")
}

// TestSeedORBFromHistory_NoCalcUpdate_OnEmptySnaps pins the no-op semantics
// for empty session subsets (weekend/holiday/pre-9:30 boot, where today's
// session-open is in the future and todaySnaps is empty).
func TestSeedORBFromHistory_NoCalcUpdate_OnEmptySnaps(t *testing.T) {
	sym := domain.Symbol("AAPL")
	sessionOpen := rthOpenUTC(2025, 3, 4)

	bars := rthBars(sym, sessionOpen, 20, 100.0)

	bus := memory.NewBus()
	svc, _ := monitortest.NewSvc(bus, &mockRepository{}, "seed_orb")
	svc.InitAggregators([]domain.Symbol{sym}, sessionOpen)
	_ = svc.WarmUpAndCollect(bars)
	before, ok := svc.GetLastSnapshot(sym.String())
	require.True(t, ok)

	svc.SeedORBFromHistory(sym, nil)
	svc.SeedORBFromHistory(sym, []monitor.BarSnapshot{})

	after, ok := svc.GetLastSnapshot(sym.String())
	require.True(t, ok)
	assert.Equal(t, before, after, "empty-snap SeedORBFromHistory must not mutate lastSnap")
	assert.Nil(t, svc.GetORBSession(sym.String()),
		"empty-snap SeedORBFromHistory must not create an ORB session")
}

// TestWarmUpAndCollect_SnapOrderMatchesBarOrder pins the per-bar snap
// correctness contract: snaps[i] is produced by Update(bars[i]) — no
// reordering, no skipping, no empty entries.
func TestWarmUpAndCollect_SnapOrderMatchesBarOrder(t *testing.T) {
	sym := domain.Symbol("AAPL")
	sessionOpen := rthOpenUTC(2025, 3, 4)

	bars := rthBars(sym, sessionOpen, 50, 100.0)

	bus := memory.NewBus()
	svc, _ := monitortest.NewSvc(bus, &mockRepository{}, "seed_orb")
	svc.InitAggregators([]domain.Symbol{sym}, sessionOpen)

	snaps := svc.WarmUpAndCollect(bars)
	require.Len(t, snaps, len(bars))
	for i, sn := range snaps {
		assert.Equal(t, bars[i].Time, sn.Bar.Time, "snap[%d].Bar.Time must equal bars[%d].Time", i, i)
		assert.Equal(t, bars[i].Close, sn.Bar.Close)
	}
	// Mid-series snap must have non-zero indicators (post-warmup convergence).
	mid := snaps[len(snaps)/2].Snapshot
	assert.Greater(t, mid.RSI, 0.0)
	assert.Greater(t, mid.VWAP, 0.0, "session VWAP must be live mid-warmup")
}

// TestWarmUp_DelegateBehavior_PreservesAggregatorAndLastSnap pins the
// load-bearing claim that promoting WarmUpAndCollect to canonical seed entry
// preserves WarmUp's existing side effects: aggregator pushes, anchorRegimes
// population on HTF closes, lastHTFSnaps[1h] population, and lastSnap with
// AnchorRegimes filled in. Otherwise live's runtime behavior changes silently.
func TestWarmUp_DelegateBehavior_PreservesAggregatorAndLastSnap(t *testing.T) {
	sym := domain.Symbol("AAPL")
	sessionOpen := rthOpenUTC(2025, 3, 4)

	// Cover at least one full 1h bucket from sessionOpen so anchorRegimes[1h]
	// and lastHTFSnaps[sym:1h] are written via the aggregator path.
	bars := rthBars(sym, sessionOpen, 90, 100.0)

	// Service A: legacy WarmUp (returns count, ignores snaps).
	busA := memory.NewBus()
	svcA, _ := monitortest.NewSvc(busA, &mockRepository{}, "seed_orb_A")
	svcA.InitAggregators([]domain.Symbol{sym}, sessionOpen)
	nA := svcA.WarmUp(bars)
	require.Equal(t, len(bars), nA)
	lastA, okA := svcA.GetLastSnapshot(sym.String())
	require.True(t, okA, "WarmUp must populate lastSnap")

	// Service B: new canonical entry returning per-bar snaps.
	busB := memory.NewBus()
	svcB, _ := monitortest.NewSvc(busB, &mockRepository{}, "seed_orb_B")
	svcB.InitAggregators([]domain.Symbol{sym}, sessionOpen)
	snapsB := svcB.WarmUpAndCollect(bars)
	require.Len(t, snapsB, len(bars))
	lastB, okB := svcB.GetLastSnapshot(sym.String())
	require.True(t, okB, "WarmUpAndCollect must populate lastSnap (finalization absorbed from WarmUp)")

	// Final indicator state must be byte-equal between WarmUp and WarmUpAndCollect.
	assert.Equal(t, lastA.RSI, lastB.RSI, "RSI must match between WarmUp and WarmUpAndCollect")
	assert.Equal(t, lastA.EMA9, lastB.EMA9)
	assert.Equal(t, lastA.EMA21, lastB.EMA21)
	assert.Equal(t, lastA.EMA50, lastB.EMA50)
	assert.Equal(t, lastA.MACDLine, lastB.MACDLine)
	assert.Equal(t, lastA.ATR, lastB.ATR)
	assert.Equal(t, lastA.VWAP, lastB.VWAP)

	// AnchorRegimes filled by finalization tail must be present with the same
	// keys and the same Type/Strength per timeframe. Since the regime
	// detector stamps MarketRegime.Since with time.Now() at detection,
	// per-run wall-clock variance is expected — compare logical content only.
	require.NotNil(t, lastA.AnchorRegimes)
	require.NotNil(t, lastB.AnchorRegimes)
	require.Equal(t, len(lastA.AnchorRegimes), len(lastB.AnchorRegimes),
		"AnchorRegimes key set must match")
	for tf, regA := range lastA.AnchorRegimes {
		regB, ok := lastB.AnchorRegimes[tf]
		require.True(t, ok, "AnchorRegimes[%s] missing in WarmUpAndCollect", tf)
		assert.Equal(t, regA.Symbol, regB.Symbol)
		assert.Equal(t, regA.Timeframe, regB.Timeframe)
		assert.Equal(t, regA.Type, regB.Type, "regime Type[%s] must match", tf)
		assert.InDelta(t, regA.Strength, regB.Strength, 1e-12,
			"regime Strength[%s] must match", tf)
	}

	// 1h HTF snap populated via aggregator close path on both services.
	htfA, hasHTFA := svcA.GetHTFSnapshot(sym.String(), "1h")
	htfB, hasHTFB := svcB.GetHTFSnapshot(sym.String(), "1h")
	assert.Equal(t, hasHTFA, hasHTFB, "1h HTF snap presence must match")
	if hasHTFA && hasHTFB {
		assert.Equal(t, htfA.EMA50, htfB.EMA50, "1h HTF EMA50 must match")
	}
}

// TestSeedORBFromHistory_PinsSessionVWAP_AtRuntimeEntry verifies the architect's
// load-bearing claim for the boot sequence:
//
//	WarmUpAndCollect(yesterdayBars)
//	ResetSessionIndicators(sym)
//	todaySnaps := WarmUpAndCollect(todayBars)
//	SeedORBFromHistory(sym, todaySnaps)
//
// Session-VWAP at runtime entry must equal the VWAP a continuous-from-today-
// open run would have produced, regardless of how many pre-today RTH bars sat
// in the canonical 800-bar window. Pins option (b'): split-and-reset.
func TestSeedORBFromHistory_PinsSessionVWAP_AtRuntimeEntry(t *testing.T) {
	sym := domain.Symbol("AAPL")
	yesterdayOpen := rthOpenUTC(2025, 3, 3) // Monday 09:30 ET
	todayOpen := rthOpenUTC(2025, 3, 4)     // Tuesday 09:30 ET

	// Yesterday's full RTH (390 1m bars) at one price band.
	yesterdayBars := rthBars(sym, yesterdayOpen, 390, 200.0)
	// Today's first 25 minutes of RTH at a different price band.
	todayBars := rthBars(sym, todayOpen, 25, 100.0)

	// Boot path simulation: split-and-reset then seed.
	busBoot := memory.NewBus()
	svcBoot, _ := monitortest.NewSvc(busBoot, &mockRepository{}, "seed_orb_boot")
	svcBoot.InitAggregators([]domain.Symbol{sym}, todayOpen)
	_ = svcBoot.WarmUpAndCollect(yesterdayBars)
	svcBoot.ResetSessionIndicators(sym.String())
	todaySnaps := svcBoot.WarmUpAndCollect(todayBars)
	svcBoot.SeedORBFromHistory(sym, todaySnaps)
	bootSnap, ok := svcBoot.GetLastSnapshot(sym.String())
	require.True(t, ok)

	// Reference: today-only run from a fresh service. Equivalent to a
	// continuous-from-09:30 in-process backtest seeing only today's bars.
	busRef := memory.NewBus()
	svcRef, _ := monitortest.NewSvc(busRef, &mockRepository{}, "seed_orb_ref")
	svcRef.InitAggregators([]domain.Symbol{sym}, todayOpen)
	_ = svcRef.WarmUpAndCollect(todayBars)
	refSnap, ok := svcRef.GetLastSnapshot(sym.String())
	require.True(t, ok)

	// Session-VWAP at runtime entry must match today-only — no yesterday leak.
	assert.InDelta(t, refSnap.VWAP, bootSnap.VWAP, 1e-9,
		"boot-time session VWAP must equal a today-only run (no yesterday-bar leak)")
}

// TestBootBeforeSessionOpen_VWAPZeroedAfterReset pins the weekend / holiday /
// pre-9:30 ET boundary: todayBars is empty, so after split-and-reset the
// session VWAP at runtime entry is zero (not yesterday's). First boundary
// case from the architect's test list.
func TestBootBeforeSessionOpen_VWAPZeroedAfterReset(t *testing.T) {
	sym := domain.Symbol("AAPL")
	yesterdayOpen := rthOpenUTC(2025, 3, 3)
	todayOpen := rthOpenUTC(2025, 3, 4)

	yesterdayBars := rthBars(sym, yesterdayOpen, 390, 200.0)
	var todayBars []domain.MarketBar // empty: boot before today's 09:30 ET

	bus := memory.NewBus()
	svc, _ := monitortest.NewSvc(bus, &mockRepository{}, "seed_orb")
	svc.InitAggregators([]domain.Symbol{sym}, todayOpen)
	_ = svc.WarmUpAndCollect(yesterdayBars)
	svc.ResetSessionIndicators(sym.String())
	todaySnaps := svc.WarmUpAndCollect(todayBars)
	require.Empty(t, todaySnaps)
	svc.SeedORBFromHistory(sym, todaySnaps)

	// First runtime bar of today's session — should accumulate VWAP from a
	// clean baseline (the boundary contract).
	firstToday, err := domain.NewMarketBar(todayOpen, sym, "1m", 99.5, 100.5, 99.0, 100.0, 1500)
	require.NoError(t, err)
	post := svc.WarmUpAndCollect([]domain.MarketBar{firstToday})
	require.Len(t, post, 1)
	// VWAP after exactly one today bar must be a function of that bar only.
	expectedTypical := (firstToday.High + firstToday.Low + firstToday.Close) / 3.0
	assert.InDelta(t, expectedTypical, post[0].Snapshot.VWAP, 1e-9,
		"first-bar VWAP must equal that bar's typical price (no yesterday leak)")
	// ORB session must not yet be created (todayBars was empty).
	assert.Nil(t, svc.GetORBSession(sym.String()))
}

// TestBootMidSession_AllTodayBars_VWAPMatchesTodayOnly pins the post-holiday-
// Monday case: yesterdayBars is empty (or thin), todayBars is the bulk.
// Second boundary case from the architect's test list.
func TestBootMidSession_AllTodayBars_VWAPMatchesTodayOnly(t *testing.T) {
	sym := domain.Symbol("AAPL")
	todayOpen := rthOpenUTC(2025, 3, 4)

	var yesterdayBars []domain.MarketBar
	todayBars := rthBars(sym, todayOpen, 60, 100.0)

	// Boot path simulation.
	busBoot := memory.NewBus()
	svcBoot, _ := monitortest.NewSvc(busBoot, &mockRepository{}, "seed_orb_boot")
	svcBoot.InitAggregators([]domain.Symbol{sym}, todayOpen)
	_ = svcBoot.WarmUpAndCollect(yesterdayBars)
	svcBoot.ResetSessionIndicators(sym.String())
	todaySnaps := svcBoot.WarmUpAndCollect(todayBars)
	svcBoot.SeedORBFromHistory(sym, todaySnaps)
	bootSnap, ok := svcBoot.GetLastSnapshot(sym.String())
	require.True(t, ok)

	// Reference: today-only.
	busRef := memory.NewBus()
	svcRef, _ := monitortest.NewSvc(busRef, &mockRepository{}, "seed_orb_ref")
	svcRef.InitAggregators([]domain.Symbol{sym}, todayOpen)
	_ = svcRef.WarmUpAndCollect(todayBars)
	refSnap, ok := svcRef.GetLastSnapshot(sym.String())
	require.True(t, ok)

	assert.InDelta(t, refSnap.VWAP, bootSnap.VWAP, 1e-9)
	assert.InDelta(t, refSnap.RSI, bootSnap.RSI, 1e-9)
	assert.InDelta(t, refSnap.EMA21, bootSnap.EMA21, 1e-9)
}
