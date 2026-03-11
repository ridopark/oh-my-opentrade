package monitor_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/require"
)

func createBarAt(t *testing.T, sym domain.Symbol, barTime time.Time, o, h, l, c, v float64) domain.MarketBar {
	bar, err := domain.NewMarketBar(barTime, sym, "1m", o, h, l, c, v)
	require.NoError(t, err)
	return bar
}

func createSnap(sym domain.Symbol, barTime time.Time, volume, volumeSMA float64) domain.IndicatorSnapshot {
	snap, _ := domain.NewIndicatorSnapshot(barTime, sym, "1m", 50, 50, 50, 100, 100, 100, volume, volumeSMA)
	return snap
}

func TestORBTracker_RangeFormation_TransitionsToRangeSet(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	tr := monitor.NewORBTracker()
	cfg := monitor.DefaultORBConfig()
	cfg.WindowMinutes = 30

	var expectedHigh float64 = 0
	var expectedLow float64 = 1e9
	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		o := 100.0
		h := 100.0 + float64(i%5)
		l := 95.0 - float64(i%3)
		c := 99.0
		if h > expectedHigh {
			expectedHigh = h
		}
		if l < expectedLow {
			expectedLow = l
		}
		bar := createBarAt(t, sym, bt, o, h, l, c, 10)
		snap := createSnap(sym, bt, 10, 10)
		setup, detected := tr.OnBar(bar, snap, cfg, false)
		require.False(t, detected)
		require.Nil(t, setup)
	}

	post := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	bar := createBarAt(t, sym, post, 100, 101, 99, 100, 10)
	snap := createSnap(sym, post, 10, 10)
	setup, detected := tr.OnBar(bar, snap, cfg, false)
	require.False(t, detected)
	require.Nil(t, setup)

	sess := tr.GetSession(sym.String())
	require.NotNil(t, sess)
	require.Equal(t, monitor.ORBStateRangeSet, sess.State)
	require.Equal(t, expectedHigh, sess.OrbHigh)
	require.Equal(t, expectedLow, sess.OrbLow)
}

func TestORBTracker_MissingBarsTolerance(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")

	t.Run("invalid when too many missing", func(t *testing.T) {
		tr := monitor.NewORBTracker()
		cfg := monitor.DefaultORBConfig()
		cfg.WindowMinutes = 30
		cfg.AllowMissingBars = 1
		for i := 0; i < 28; i++ {
			bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
			bar := createBarAt(t, sym, bt, 100, 101, 99, 100, 10)
			snap := createSnap(sym, bt, 10, 10)
			tr.OnBar(bar, snap, cfg, false)
		}
		post := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
		bar := createBarAt(t, sym, post, 100, 101, 99, 100, 10)
		snap := createSnap(sym, post, 10, 10)
		tr.OnBar(bar, snap, cfg, false)
		sess := tr.GetSession(sym.String())
		require.Equal(t, monitor.ORBStateInvalid, sess.State)
	})

	t.Run("range set when within tolerance", func(t *testing.T) {
		tr := monitor.NewORBTracker()
		cfg := monitor.DefaultORBConfig()
		cfg.WindowMinutes = 30
		cfg.AllowMissingBars = 1
		for i := 0; i < 29; i++ {
			bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
			bar := createBarAt(t, sym, bt, 100, 101, 99, 100, 10)
			snap := createSnap(sym, bt, 10, 10)
			tr.OnBar(bar, snap, cfg, false)
		}
		post := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
		bar := createBarAt(t, sym, post, 100, 101, 99, 100, 10)
		snap := createSnap(sym, post, 10, 10)
		tr.OnBar(bar, snap, cfg, false)
		sess := tr.GetSession(sym.String())
		require.Equal(t, monitor.ORBStateRangeSet, sess.State)
	})
}

func TestORBTracker_BreakoutGating_RVOL(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	tr := monitor.NewORBTracker()
	cfg := monitor.DefaultORBConfig()
	cfg.WindowMinutes = 30

	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		bar := createBarAt(t, sym, bt, 100, 101, 99, 100, 10)
		snap := createSnap(sym, bt, 10, 10)
		tr.OnBar(bar, snap, cfg, false)
	}
	post := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	tr.OnBar(createBarAt(t, sym, post, 100, 101, 99, 100, 10), createSnap(sym, post, 10, 10), cfg, false)
	require.Equal(t, monitor.ORBStateRangeSet, tr.GetSession(sym.String()).State)

	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	breakBar := createBarAt(t, sym, breakT, 100, 103, 100, 103, 10)
	lowRVOLSnap := createSnap(sym, breakT, 10, 20)
	tr.OnBar(breakBar, lowRVOLSnap, cfg, false)
	require.Equal(t, monitor.ORBStateRangeSet, tr.GetSession(sym.String()).State)

	highRVOLSnap := createSnap(sym, breakT.Add(time.Minute), 30, 10)
	breakBar2 := createBarAt(t, sym, breakT.Add(time.Minute), 100, 103, 100, 103, 30)
	tr.OnBar(breakBar2, highRVOLSnap, cfg, false)
	require.Equal(t, monitor.ORBStateAwaitingRetest, tr.GetSession(sym.String()).State)
}

func TestORBTracker_RetestConfirm_EmitsSetup(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	tr := monitor.NewORBTracker()
	cfg := monitor.DefaultORBConfig()
	cfg.WindowMinutes = 30
	cfg.BreakoutConfirmBps = 2
	cfg.TouchToleranceBps = 2
	cfg.HoldConfirmBps = 0
	cfg.MaxRetestBars = 15

	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		bar := createBarAt(t, sym, bt, 100, 101, 99, 100, 10)
		snap := createSnap(sym, bt, 10, 10)
		tr.OnBar(bar, snap, cfg, false)
	}
	post := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	tr.OnBar(createBarAt(t, sym, post, 100, 101, 99, 100, 10), createSnap(sym, post, 10, 10), cfg, false)

	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	breakBar := createBarAt(t, sym, breakT, 100, 104, 100, 104, 50)
	breakSnap := createSnap(sym, breakT, 50, 10)
	tr.OnBar(breakBar, breakSnap, cfg, false)
	require.Equal(t, monitor.ORBStateAwaitingRetest, tr.GetSession(sym.String()).State)

	retestT := breakT.Add(time.Minute)
	retestBar := createBarAt(t, sym, retestT, 104, 104, 101, 103, 20)
	retestSnap := createSnap(sym, retestT, 20, 10)
	setup, detected := tr.OnBar(retestBar, retestSnap, cfg, false)
	require.True(t, detected)
	require.NotNil(t, setup)
	require.Equal(t, sym, setup.Symbol)
	require.Equal(t, domain.DirectionLong, setup.Direction)
	require.Equal(t, "ORB Break & Retest", setup.Trigger)
	require.Equal(t, tr.GetSession(sym.String()).OrbHigh, setup.ORBHigh)
	require.Equal(t, tr.GetSession(sym.String()).OrbLow, setup.ORBLow)
	require.GreaterOrEqual(t, setup.Confidence, 0.50)
	require.Equal(t, monitor.ORBStateDoneForSession, tr.GetSession(sym.String()).State)
}

func TestORBTracker_RetestTimeout(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	tr := monitor.NewORBTracker()
	cfg := monitor.DefaultORBConfig()
	cfg.WindowMinutes = 30
	cfg.MaxRetestBars = 3

	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		tr.OnBar(createBarAt(t, sym, bt, 100, 101, 99, 100, 10), createSnap(sym, bt, 10, 10), cfg, false)
	}
	post := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	tr.OnBar(createBarAt(t, sym, post, 100, 101, 99, 100, 10), createSnap(sym, post, 10, 10), cfg, false)
	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	tr.OnBar(createBarAt(t, sym, breakT, 100, 104, 100, 104, 50), createSnap(sym, breakT, 50, 10), cfg, false)

	for i := 0; i < 4; i++ {
		bt := breakT.Add(time.Duration(i+1) * time.Minute)
		tr.OnBar(createBarAt(t, sym, bt, 104, 105, 103, 104, 10), createSnap(sym, bt, 10, 10), cfg, false)
	}
	require.Equal(t, monitor.ORBStateRangeSet, tr.GetSession(sym.String()).State, "retest timeout should cycle to RANGE_SET")
}

func TestORBTracker_ChopInvalidation_CyclesToRangeSet(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	tr := monitor.NewORBTracker()
	cfg := monitor.DefaultORBConfig()
	cfg.WindowMinutes = 30

	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		tr.OnBar(createBarAt(t, sym, bt, 100, 101, 99, 100, 10), createSnap(sym, bt, 10, 10), cfg, false)
	}
	post := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	tr.OnBar(createBarAt(t, sym, post, 100, 101, 99, 100, 10), createSnap(sym, post, 10, 10), cfg, false)
	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	tr.OnBar(createBarAt(t, sym, breakT, 100, 104, 100, 104, 50), createSnap(sym, breakT, 50, 10), cfg, false)

	invalidateT := breakT.Add(time.Minute)
	bar := createBarAt(t, sym, invalidateT, 104, 104, 90, 90, 10)
	snap := createSnap(sym, invalidateT, 10, 10)
	tr.OnBar(bar, snap, cfg, false)
	require.Equal(t, monitor.ORBStateRangeSet, tr.GetSession(sym.String()).State, "breakout invalidation should cycle to RANGE_SET")
}

func TestORBTracker_ReplayMode_NoSetupReturned(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	tr := monitor.NewORBTracker()
	cfg := monitor.DefaultORBConfig()
	cfg.WindowMinutes = 30

	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		tr.OnBar(createBarAt(t, sym, bt, 100, 101, 99, 100, 10), createSnap(sym, bt, 10, 10), cfg, true)
	}
	post := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	tr.OnBar(createBarAt(t, sym, post, 100, 101, 99, 100, 10), createSnap(sym, post, 10, 10), cfg, true)
	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	tr.OnBar(createBarAt(t, sym, breakT, 100, 104, 100, 104, 50), createSnap(sym, breakT, 50, 10), cfg, true)
	retestT := breakT.Add(time.Minute)
	setup, detected := tr.OnBar(createBarAt(t, sym, retestT, 104, 104, 101, 103, 20), createSnap(sym, retestT, 20, 10), cfg, true)
	require.False(t, detected)
	require.Nil(t, setup)
	require.Equal(t, monitor.ORBStateRangeSet, tr.GetSession(sym.String()).State, "replay-suppressed signal should cycle to RANGE_SET")
}

func TestORBTracker_ConfidenceFormula(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	tr := monitor.NewORBTracker()
	cfg := monitor.DefaultORBConfig()
	cfg.WindowMinutes = 30

	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		tr.OnBar(createBarAt(t, sym, bt, 100, 101, 99, 100, 10), createSnap(sym, bt, 10, 10), cfg, false)
	}
	post := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	tr.OnBar(createBarAt(t, sym, post, 100, 101, 99, 100, 10), createSnap(sym, post, 10, 10), cfg, false)
	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	tr.OnBar(createBarAt(t, sym, breakT, 100, 104, 100, 104, 50), createSnap(sym, breakT, 50, 10), cfg, false)
	retestT := breakT.Add(time.Minute)
	setup, detected := tr.OnBar(createBarAt(t, sym, retestT, 102, 104, 101, 103, 20), createSnap(sym, retestT, 20, 10), cfg, false)
	require.True(t, detected)
	require.NotNil(t, setup)

	expected := 0.50 + 0.25 + 0.10 + 0.10
	require.InEpsilon(t, expected, setup.Confidence, 1e-9)
}

func TestORBTracker_NewSessionResets(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	tr := monitor.NewORBTracker()
	cfg := monitor.DefaultORBConfig()

	bt := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	tr.OnBar(createBarAt(t, sym, bt, 100, 101, 99, 100, 10), createSnap(sym, bt, 10, 10), cfg, false)
	sess1 := tr.GetSession(sym.String())
	require.NotNil(t, sess1)
	require.Equal(t, "2025-03-04", sess1.SessionKey)

	bt2 := time.Date(2025, 3, 5, 14, 30, 0, 0, time.UTC)
	tr.OnBar(createBarAt(t, sym, bt2, 100, 101, 99, 100, 10), createSnap(sym, bt2, 10, 10), cfg, false)
	sess2 := tr.GetSession(sym.String())
	require.NotNil(t, sess2)
	require.Equal(t, "2025-03-05", sess2.SessionKey)
	require.Equal(t, monitor.ORBStateFormingRange, sess2.State)
}

func TestORBTracker_ShortBreakoutFlow(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	tr := monitor.NewORBTracker()
	cfg := monitor.DefaultORBConfig()
	cfg.WindowMinutes = 30

	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		tr.OnBar(createBarAt(t, sym, bt, 100, 101, 99, 100, 10), createSnap(sym, bt, 10, 10), cfg, false)
	}
	post := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	tr.OnBar(createBarAt(t, sym, post, 100, 101, 99, 100, 10), createSnap(sym, post, 10, 10), cfg, false)

	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	tr.OnBar(createBarAt(t, sym, breakT, 100, 100, 95, 95, 50), createSnap(sym, breakT, 50, 10), cfg, false)
	require.Equal(t, monitor.ORBStateAwaitingRetest, tr.GetSession(sym.String()).State)

	retestT := breakT.Add(time.Minute)
	setup, detected := tr.OnBar(createBarAt(t, sym, retestT, 95, 99, 95, 98, 20), createSnap(sym, retestT, 20, 10), cfg, false)
	require.True(t, detected)
	require.NotNil(t, setup)
	require.Equal(t, domain.DirectionShort, setup.Direction)
}
