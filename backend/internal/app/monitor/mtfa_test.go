package monitor_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mtfaSessionOpen is a fixed NYSE open for MTFA tests: 2026-03-04 14:30 UTC (9:30 AM ET).
var mtfaSessionOpen = time.Date(2026, 3, 4, 14, 30, 0, 0, time.UTC)

// makeMTFAEvent creates a MarketBarSanitized event with a unique idempotency key per bar.
func makeMTFAEvent(t *testing.T, bar domain.MarketBar) domain.Event {
	t.Helper()
	idemKey := fmt.Sprintf("mtfa-%s-%d", bar.Symbol, bar.Time.UnixNano())
	ev, err := domain.NewEvent(
		domain.EventMarketBarSanitized,
		"tenant-mtfa",
		domain.EnvModePaper,
		idemKey,
		bar,
	)
	require.NoError(t, err)
	return *ev
}

// makeMTFA1mBar creates a 1m bar at the given minute offset from mtfaSessionOpen.
func makeMTFA1mBar(t *testing.T, sym domain.Symbol, minuteOffset int, price, volume float64) domain.MarketBar {
	t.Helper()
	bar, err := domain.NewMarketBar(
		mtfaSessionOpen.Add(time.Duration(minuteOffset)*time.Minute),
		sym, "1m",
		price, price+0.10, price-0.10, price, volume,
	)
	require.NoError(t, err)
	return bar
}

// setupMTFAService creates a monitor service wired to a memory event bus,
// initialises aggregators for the given symbols, and starts the service.
func setupMTFAService(t *testing.T, symbols ...domain.Symbol) (*monitor.Service, *memory.Bus) {
	t.Helper()
	bus := memory.NewBus()
	svc := monitor.NewService(bus, &mockRepository{}, zerolog.Nop())
	svc.InitAggregators(symbols, mtfaSessionOpen)
	require.NoError(t, svc.Start(context.Background()))
	return svc, bus
}

// collectBars subscribes to EventMarketBarSanitized and collects bars matching the given timeframe.
type barCollector struct {
	mu   sync.Mutex
	bars []domain.MarketBar
	tf   domain.Timeframe
}

func newBarCollector(t *testing.T, bus *memory.Bus, tf domain.Timeframe) *barCollector {
	t.Helper()
	bc := &barCollector{tf: tf}
	err := bus.Subscribe(context.Background(), domain.EventMarketBarSanitized, func(_ context.Context, ev domain.Event) error {
		b, ok := ev.Payload.(domain.MarketBar)
		if !ok {
			return nil
		}
		if b.Timeframe == tf {
			bc.mu.Lock()
			bc.bars = append(bc.bars, b)
			bc.mu.Unlock()
		}
		return nil
	})
	require.NoError(t, err)
	return bc
}

func (bc *barCollector) get() []domain.MarketBar {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	cp := make([]domain.MarketBar, len(bc.bars))
	copy(cp, bc.bars)
	return cp
}

// snapCollector subscribes to EventStateUpdated and collects IndicatorSnapshot payloads.
type snapCollector struct {
	mu    sync.Mutex
	snaps []domain.IndicatorSnapshot
}

func newSnapCollector(t *testing.T, bus *memory.Bus) *snapCollector {
	t.Helper()
	sc := &snapCollector{}
	err := bus.Subscribe(context.Background(), domain.EventStateUpdated, func(_ context.Context, ev domain.Event) error {
		s, ok := ev.Payload.(domain.IndicatorSnapshot)
		if !ok {
			return nil
		}
		sc.mu.Lock()
		sc.snaps = append(sc.snaps, s)
		sc.mu.Unlock()
		return nil
	})
	require.NoError(t, err)
	return sc
}

func (sc *snapCollector) get() []domain.IndicatorSnapshot {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	cp := make([]domain.IndicatorSnapshot, len(sc.snaps))
	copy(cp, sc.snaps)
	return cp
}

func (sc *snapCollector) last() domain.IndicatorSnapshot {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.snaps[len(sc.snaps)-1]
}

// regimeCollector subscribes to EventRegimeShifted and collects MarketRegime payloads.
type regimeCollector struct {
	mu      sync.Mutex
	regimes []domain.MarketRegime
}

func newRegimeCollector(t *testing.T, bus *memory.Bus) *regimeCollector {
	t.Helper()
	rc := &regimeCollector{}
	err := bus.Subscribe(context.Background(), domain.EventRegimeShifted, func(_ context.Context, ev domain.Event) error {
		r, ok := ev.Payload.(domain.MarketRegime)
		if !ok {
			return nil
		}
		rc.mu.Lock()
		rc.regimes = append(rc.regimes, r)
		rc.mu.Unlock()
		return nil
	})
	require.NoError(t, err)
	return rc
}

func (rc *regimeCollector) get() []domain.MarketRegime {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	cp := make([]domain.MarketRegime, len(rc.regimes))
	copy(cp, rc.regimes)
	return cp
}

func (rc *regimeCollector) filter(tf domain.Timeframe) []domain.MarketRegime {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	var out []domain.MarketRegime
	for _, r := range rc.regimes {
		if r.Timeframe == tf {
			out = append(out, r)
		}
	}
	return out
}

func (rc *regimeCollector) reset() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.regimes = nil
}

// feedBars publishes N 1m bars through the event bus at sequential minute offsets.
func feedBars(t *testing.T, bus *memory.Bus, sym domain.Symbol, startMinute, count int, price, volume float64) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < count; i++ {
		bar := makeMTFA1mBar(t, sym, startMinute+i, price, volume)
		require.NoError(t, bus.Publish(ctx, makeMTFAEvent(t, bar)))
	}
}

// ─── Test 1: 5 × 1m bars produce a 5m HTF bar via event bus ────────────────

func TestMTFA_5mBarAggregation(t *testing.T) {
	sym := domain.Symbol("AAPL")
	_, bus := setupMTFAService(t, sym)
	bc := newBarCollector(t, bus, "5m")

	feedBars(t, bus, sym, 0, 5, 150.0, 1000)

	bars := bc.get()
	require.NotEmpty(t, bars, "expected at least one 5m HTF bar")
	assert.Equal(t, domain.Timeframe("5m"), bars[0].Timeframe)
	assert.Equal(t, sym, bars[0].Symbol)
}

// ─── Test 2: 15 × 1m bars produce a 15m HTF bar ────────────────────────────

func TestMTFA_15mBarAggregation(t *testing.T) {
	sym := domain.Symbol("AAPL")
	_, bus := setupMTFAService(t, sym)
	bc := newBarCollector(t, bus, "15m")

	feedBars(t, bus, sym, 0, 15, 150.0, 1000)

	bars := bc.get()
	require.NotEmpty(t, bars, "expected at least one 15m HTF bar")
	assert.Equal(t, domain.Timeframe("15m"), bars[0].Timeframe)
	assert.Equal(t, sym, bars[0].Symbol)
}

// ─── Test 3: 1m snapshot enriched with anchor regimes after 5m closes ───────

func TestMTFA_AnchorRegimeEnrichment(t *testing.T) {
	sym := domain.Symbol("AAPL")
	_, bus := setupMTFAService(t, sym)
	sc := newSnapCollector(t, bus)

	// Feed 5 bars to close first 5m bucket, then 1 more to get a snapshot AFTER regime cached.
	feedBars(t, bus, sym, 0, 6, 150.0, 1000)

	snaps := sc.get()
	require.NotEmpty(t, snaps)

	// The last snapshot (bar 5, minute 5 — after 5m close) should have AnchorRegimes populated.
	last := snaps[len(snaps)-1]
	require.NotNil(t, last.AnchorRegimes, "AnchorRegimes should be non-nil after 5m bar closes")
	_, has5m := last.AnchorRegimes[domain.Timeframe("5m")]
	assert.True(t, has5m, "AnchorRegimes should contain a 5m entry")
}

// ─── Test 4: AnchorRegimes is empty before first HTF bar closes ─────────────

func TestMTFA_EmptyAnchorRegimesBeforeFirstHTFBar(t *testing.T) {
	sym := domain.Symbol("AAPL")
	_, bus := setupMTFAService(t, sym)
	sc := newSnapCollector(t, bus)

	// Feed only 3 bars — not enough to close a 5m bucket.
	feedBars(t, bus, sym, 0, 3, 150.0, 1000)

	snaps := sc.get()
	require.NotEmpty(t, snaps)

	last := snaps[len(snaps)-1]
	// AnchorRegimes is allocated (make(map...)) but should have no 5m/15m entries.
	has5m := false
	if last.AnchorRegimes != nil {
		_, has5m = last.AnchorRegimes[domain.Timeframe("5m")]
	}
	assert.False(t, has5m, "AnchorRegimes should not have 5m entry before any 5m bar closes")
}

// ─── Test 5: Multi-symbol isolation ─────────────────────────────────────────

func TestMTFA_MultiSymbolIsolation(t *testing.T) {
	symA := domain.Symbol("AAPL")
	symB := domain.Symbol("MSFT")
	_, bus := setupMTFAService(t, symA, symB)
	bc := newBarCollector(t, bus, "5m")

	// Feed 5 bars for AAPL (closes 5m), but only 3 bars for MSFT (not enough).
	feedBars(t, bus, symA, 0, 5, 150.0, 1000)
	feedBars(t, bus, symB, 0, 3, 300.0, 2000)

	bars := bc.get()
	var aaplHTF, msftHTF int
	for _, b := range bars {
		if b.Symbol == symA {
			aaplHTF++
		}
		if b.Symbol == symB {
			msftHTF++
		}
	}

	assert.GreaterOrEqual(t, aaplHTF, 1, "AAPL should have at least one 5m HTF bar")
	assert.Equal(t, 0, msftHTF, "MSFT should not have any 5m HTF bar (only 3 bars fed)")
}

// ─── Test 6: Per-{symbol,timeframe} indicator separation ────────────────────

func TestMTFA_IndicatorSeparation(t *testing.T) {
	sym := domain.Symbol("AAPL")
	_, bus := setupMTFAService(t, sym)
	sc := newSnapCollector(t, bus)

	// Feed 21 bars with ascending prices to build up EMAs.
	ctx := context.Background()
	for i := 0; i < 21; i++ {
		bar := makeMTFA1mBar(t, sym, i, 100.0+float64(i)*1.0, 1000)
		require.NoError(t, bus.Publish(ctx, makeMTFAEvent(t, bar)))
	}

	snaps := sc.get()
	// We should have 21 StateUpdated events (one per 1m bar).
	require.Len(t, snaps, 21, "one StateUpdated per 1m bar")

	// Each snapshot should be 1m (the enriched 1m, not HTF).
	for _, snap := range snaps {
		assert.Equal(t, domain.Timeframe("1m"), snap.Timeframe, "StateUpdated should always be 1m snapshot")
	}

	// After 21 bars, the last snapshot should have initialised EMA21.
	last := snaps[len(snaps)-1]
	assert.Greater(t, last.EMA21, 0.0, "EMA21 should be initialised after 21 bars")
}

// ─── Test 7: HTF bar re-entry guard prevents infinite loop ──────────────────

func TestMTFA_HTFBarNotReprocessed(t *testing.T) {
	sym := domain.Symbol("AAPL")
	svc, bus := setupMTFAService(t, sym)

	// Feed 5 bars to close a 5m bucket. The HTF bar is re-published to the event bus,
	// which re-triggers HandleMarketBar via the subscription. The guard (bar.Timeframe != "1m")
	// causes it to return nil immediately.
	feedBars(t, bus, sym, 0, 5, 150.0, 1000)

	// If the guard didn't work, we'd get infinite recursion or a panic.
	// Additionally verify we can still process bars after without error.
	ctx := context.Background()
	bar := makeMTFA1mBar(t, sym, 5, 152.0, 1000)
	err := svc.HandleMarketBar(ctx, makeMTFAEvent(t, bar))
	assert.NoError(t, err, "should still handle bars normally after HTF bar publication")
}

// ─── Test 8: Regime hysteresis for anchor timeframes ────────────────────────

func TestMTFA_RegimeHysteresis(t *testing.T) {
	// Test directly on RegimeDetector for precision:
	// For 5m (anchor TF), regime change requires 3 consecutive bars in new regime.
	rd := monitor.NewRegimeDetector()
	sym, _ := domain.NewSymbol("AAPL")

	// Initial detection: TREND (EMA9=105 >> EMA21=100 → >1% divergence).
	trend, _ := domain.NewIndicatorSnapshot(time.Now(), sym, "5m", 60, 80, 75, 105, 100, 0, 1, 1)
	_, changed := rd.Detect(trend)
	require.True(t, changed, "first detection always emits changed=true")

	// Try to shift to BALANCE — first bar should NOT change confirmed regime.
	balance1, _ := domain.NewIndicatorSnapshot(time.Now(), sym, "5m", 50, 50, 50, 101, 100.8, 0, 1, 1)
	_, changed = rd.Detect(balance1)
	require.False(t, changed, "1st BALANCE bar should not shift anchor regime (need 3)")

	// Second BALANCE bar — still not enough.
	balance2, _ := domain.NewIndicatorSnapshot(time.Now(), sym, "5m", 50, 50, 50, 101, 100.8, 0, 1, 1)
	_, changed = rd.Detect(balance2)
	require.False(t, changed, "2nd BALANCE bar should not shift anchor regime")

	// Third BALANCE bar — NOW the regime should confirm.
	balance3, _ := domain.NewIndicatorSnapshot(time.Now(), sym, "5m", 50, 50, 50, 101, 100.8, 0, 1, 1)
	reg, changed := rd.Detect(balance3)
	require.True(t, changed, "3rd consecutive BALANCE bar should confirm anchor regime shift")
	assert.Equal(t, domain.RegimeBalance, reg.Type)
}

// ─── Test 9: 1m regime changes are immediate (no hysteresis) ────────────────

func TestMTFA_1mRegimeImmediate(t *testing.T) {
	rd := monitor.NewRegimeDetector()
	sym, _ := domain.NewSymbol("AAPL")

	// Initial: BALANCE on 1m.
	bal, _ := domain.NewIndicatorSnapshot(time.Now(), sym, "1m", 50, 50, 50, 100.1, 100.0, 0, 1, 1)
	_, changed := rd.Detect(bal)
	require.True(t, changed, "first detection")

	// Immediate shift to TREND on 1m — no hysteresis required.
	trend, _ := domain.NewIndicatorSnapshot(time.Now(), sym, "1m", 60, 80, 75, 105, 100, 0, 1, 1)
	reg, changed := rd.Detect(trend)
	assert.True(t, changed, "1m regime should shift immediately")
	assert.Equal(t, domain.RegimeTrend, reg.Type)
}

// ─── Test 10: Aggregator reset clears state for new session ─────────────────

func TestMTFA_AggregatorResetClearsState(t *testing.T) {
	sym := domain.Symbol("AAPL")
	svc, bus := setupMTFAService(t, sym)

	// Feed 4 bars (not enough to close 5m bucket).
	feedBars(t, bus, sym, 0, 4, 150.0, 1000)

	// Reset aggregators for a new session (next day).
	newSessionOpen := mtfaSessionOpen.Add(24 * time.Hour)
	svc.ResetAggregators(newSessionOpen)

	bc := newBarCollector(t, bus, "5m")

	// Feed 5 bars from the new session → should close a fresh 5m bucket.
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		bar, err := domain.NewMarketBar(
			newSessionOpen.Add(time.Duration(i)*time.Minute),
			sym, "1m",
			200.0, 200.10, 199.90, 200.0, 1000,
		)
		require.NoError(t, err)
		idemKey := fmt.Sprintf("mtfa-reset-%d", i)
		ev, err := domain.NewEvent(domain.EventMarketBarSanitized, "tenant-mtfa", domain.EnvModePaper, idemKey, bar)
		require.NoError(t, err)
		require.NoError(t, bus.Publish(ctx, *ev))
	}

	bars := bc.get()
	require.NotEmpty(t, bars, "should produce a 5m bar after reset + 5 new-session bars")
}

// ─── Test 11: EventRegimeShifted published on first anchor detection ────────

func TestMTFA_RegimeShiftedEventPublished(t *testing.T) {
	sym := domain.Symbol("AAPL")
	_, bus := setupMTFAService(t, sym)
	rc := newRegimeCollector(t, bus)

	// Feed 5 flat-price bars to close first 5m candle.
	feedBars(t, bus, sym, 0, 5, 100.0, 1000)

	// On first 5m candle close, regime detector sees it for the first time → changed=true.
	anchor5m := rc.filter("5m")
	assert.NotEmpty(t, anchor5m, "should emit EventRegimeShifted for 5m anchor on first detection")
}

// ─── Test 12: Multiple 5m candles aggregate independently ───────────────────

func TestMTFA_Multiple5mCandles(t *testing.T) {
	sym := domain.Symbol("AAPL")
	_, bus := setupMTFAService(t, sym)
	bc := newBarCollector(t, bus, "5m")

	// Feed 10 bars → should produce 2 complete 5m candles.
	feedBars(t, bus, sym, 0, 10, 150.0, 1000)

	bars := bc.get()
	require.GreaterOrEqual(t, len(bars), 2, "10 1m bars should produce 2 complete 5m candles")

	// Each should be for the correct symbol and timeframe.
	for _, b := range bars {
		assert.Equal(t, sym, b.Symbol)
		assert.Equal(t, domain.Timeframe("5m"), b.Timeframe)
	}
}

// ─── Test 13: 5m OHLCV aggregation correctness ─────────────────────────────

func TestMTFA_5mOHLCVCorrectness(t *testing.T) {
	sym := domain.Symbol("AAPL")
	_, bus := setupMTFAService(t, sym)
	bc := newBarCollector(t, bus, "5m")

	// Feed 5 bars with known OHLCV values.
	ctx := context.Background()
	prices := []struct{ o, h, l, c, v float64 }{
		{100.0, 102.0, 99.0, 101.0, 100},
		{101.0, 103.0, 100.0, 102.0, 200},
		{102.0, 105.0, 101.0, 104.0, 300},
		{104.0, 104.5, 98.0, 99.0, 150},
		{99.0, 101.0, 97.0, 100.5, 250},
	}
	for i, p := range prices {
		bar, err := domain.NewMarketBar(
			mtfaSessionOpen.Add(time.Duration(i)*time.Minute),
			sym, "1m",
			p.o, p.h, p.l, p.c, p.v,
		)
		require.NoError(t, err)
		require.NoError(t, bus.Publish(ctx, makeMTFAEvent(t, bar)))
	}

	bars := bc.get()
	require.NotEmpty(t, bars)
	htf := bars[0]

	// Expected: Open=first.Open=100, High=max(highs)=105, Low=min(lows)=97, Close=last.Close=100.5
	// Volume=sum=100+200+300+150+250=1000
	assert.Equal(t, 100.0, htf.Open, "5m Open should be first bar's Open")
	assert.Equal(t, 105.0, htf.High, "5m High should be max of all highs")
	assert.Equal(t, 97.0, htf.Low, "5m Low should be min of all lows")
	assert.Equal(t, 100.5, htf.Close, "5m Close should be last bar's Close")
	assert.Equal(t, 1000.0, htf.Volume, "5m Volume should be sum of all volumes")
}

// ─── Test 14: 60 × 1m bars produce a 1h HTF bar via event bus ──────────────

func TestMTFA_1hBarAggregation(t *testing.T) {
	sym := domain.Symbol("AAPL")
	_, bus := setupMTFAService(t, sym)
	bc := newBarCollector(t, bus, "1h")

	feedBars(t, bus, sym, 0, 60, 150.0, 1000)

	bars := bc.get()
	require.NotEmpty(t, bars, "expected at least one 1h HTF bar after 60 1m bars")
	assert.Equal(t, domain.Timeframe("1h"), bars[0].Timeframe)
	assert.Equal(t, sym, bars[0].Symbol)
}

// ─── Test 15: 1h anchor regime enriches 1m snapshot after 60 bars ───────────

func TestMTFA_1hAnchorRegimeEnrichment(t *testing.T) {
	sym := domain.Symbol("AAPL")
	_, bus := setupMTFAService(t, sym)
	sc := newSnapCollector(t, bus)

	// Feed 60 bars to close the first 1h bucket, then 1 more so the 1m snapshot
	// is enriched with the cached 1h anchor regime.
	feedBars(t, bus, sym, 0, 61, 150.0, 1000)

	snaps := sc.get()
	require.NotEmpty(t, snaps)

	last := snaps[len(snaps)-1]
	require.NotNil(t, last.AnchorRegimes, "AnchorRegimes should be non-nil after 1h bar closes")
	_, has1h := last.AnchorRegimes[domain.Timeframe("1h")]
	assert.True(t, has1h, "AnchorRegimes should contain a 1h entry after 60 bars close")
}

// ─── Test 16: Adding 1h aggregator does not break existing 5m bars ──────────

func TestMTFA_1hDoesNotBreak5m(t *testing.T) {
	sym := domain.Symbol("AAPL")
	_, bus := setupMTFAService(t, sym)
	bc5m := newBarCollector(t, bus, "5m")
	bc1h := newBarCollector(t, bus, "1h")

	// Feed 60 bars → should produce 12 complete 5m candles + 1 complete 1h candle.
	feedBars(t, bus, sym, 0, 60, 150.0, 1000)

	bars5m := bc5m.get()
	bars1h := bc1h.get()

	assert.GreaterOrEqual(t, len(bars5m), 12, "60 1m bars should produce at least 12 complete 5m candles")
	assert.GreaterOrEqual(t, len(bars1h), 1, "60 1m bars should produce at least 1 complete 1h candle")

	// All 5m bars should be correctly typed.
	for _, b := range bars5m {
		assert.Equal(t, domain.Timeframe("5m"), b.Timeframe)
		assert.Equal(t, sym, b.Symbol)
	}
}

// ─── Test 17: Static 1D HTFData appears on 1m snapshot ──────────────────────

func TestMTFA_StaticDailyHTFDataOnSnapshot(t *testing.T) {
	sym := domain.Symbol("AAPL")
	svc, bus := setupMTFAService(t, sym)
	sc := newSnapCollector(t, bus)

	svc.SetStaticHTFData("AAPL", "1d", domain.HTFData{EMA200: 200.0, Bias: "BULLISH"})

	feedBars(t, bus, sym, 0, 6, 250.0, 1000)

	snaps := sc.get()
	require.NotEmpty(t, snaps)
	last := snaps[len(snaps)-1]

	require.NotNil(t, last.HTF, "HTF map should be populated when static data is set")
	daily, ok := last.HTF[domain.Timeframe("1d")]
	require.True(t, ok, "HTF should contain 1d entry")
	assert.Equal(t, 200.0, daily.EMA200)
	assert.Equal(t, "BULLISH", daily.Bias, "price 250 > EMA200 200 → BULLISH")
}

// ─── Test 18: Bias recomputes dynamically from current price ────────────────

func TestMTFA_DailyBiasRecomputesFromPrice(t *testing.T) {
	sym := domain.Symbol("AAPL")
	svc, bus := setupMTFAService(t, sym)
	sc := newSnapCollector(t, bus)

	svc.SetStaticHTFData("AAPL", "1d", domain.HTFData{EMA200: 100.0})

	feedBars(t, bus, sym, 0, 1, 80.0, 1000)
	snaps := sc.get()
	last := snaps[len(snaps)-1]
	require.NotNil(t, last.HTF)
	assert.Equal(t, "BEARISH", last.HTF[domain.Timeframe("1d")].Bias, "price 80 < EMA200 100 → BEARISH")
}

// ─── Test 19: No HTF data when nothing configured ──────────────────────────

func TestMTFA_NoHTFDataWhenEmpty(t *testing.T) {
	sym := domain.Symbol("AAPL")
	_, bus := setupMTFAService(t, sym)
	sc := newSnapCollector(t, bus)

	feedBars(t, bus, sym, 0, 3, 150.0, 1000)
	snaps := sc.get()
	last := snaps[len(snaps)-1]
	assert.Nil(t, last.HTF, "HTF should be nil when no HTF data is configured")
}
