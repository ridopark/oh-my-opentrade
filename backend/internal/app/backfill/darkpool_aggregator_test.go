package backfill

import (
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDPAggregator_FiveMinuteBuckets(t *testing.T) {
	agg := NewDPAggregator("AAPL")

	base := time.Date(2025, 6, 2, 14, 0, 0, 0, time.UTC)

	// Two trades in the 14:00 bucket
	agg.AddTrade(base.Add(30*time.Second), "D", 150.0, 100)
	agg.AddTrade(base.Add(2*time.Minute), "Q", 150.5, 200) // lit

	// One trade in the 14:05 bucket
	agg.AddTrade(base.Add(5*time.Minute+10*time.Second), "D", 151.0, 50)

	bars := agg.Flush()
	require.Len(t, bars, 2)

	// Bars should be sorted by time.
	assert.Equal(t, base, bars[0].Time)
	assert.Equal(t, base.Add(5*time.Minute), bars[1].Time)

	// First bucket: 1 DP trade (100 shares) + 1 lit (200 shares)
	assert.Equal(t, "AAPL", string(bars[0].Symbol))
	assert.Equal(t, "5m", string(bars[0].Timeframe))
	assert.InDelta(t, 100.0, bars[0].DPVolume, 0.01)
	assert.Equal(t, 1, bars[0].DPTrades)
	assert.InDelta(t, 200.0, bars[0].LitVolume, 0.01)
	assert.InDelta(t, 300.0, bars[0].TotalVolume, 0.01)
	assert.InDelta(t, 100.0/300.0, bars[0].DPRatio, 0.001)
	assert.InDelta(t, 150.0, bars[0].DPVWAP, 0.01) // single DP trade

	// Second bucket: 1 DP trade
	assert.InDelta(t, 50.0, bars[1].DPVolume, 0.01)
	assert.Equal(t, 1, bars[1].DPTrades)
	assert.InDelta(t, 0.0, bars[1].LitVolume, 0.01)
	assert.InDelta(t, 50.0, bars[1].TotalVolume, 0.01)
	assert.InDelta(t, 1.0, bars[1].DPRatio, 0.001)
}

func TestDPAggregator_BuySellClassification(t *testing.T) {
	agg := NewDPAggregator("MSFT")

	base := time.Date(2025, 6, 2, 14, 0, 0, 0, time.UTC)

	// First DP trade at 100.0 — running VWAP starts at 0, so price >= VWAP → buy.
	agg.AddTrade(base, "D", 100.0, 100)
	// Second DP trade at 99.0 — running VWAP is 100.0, price < VWAP → sell.
	agg.AddTrade(base.Add(time.Second), "D", 99.0, 50)
	// Third DP trade at 100.5 — running VWAP ~99.67, price >= VWAP → buy.
	agg.AddTrade(base.Add(2*time.Second), "D", 100.5, 50)

	bars := agg.Flush()
	require.Len(t, bars, 1)

	assert.InDelta(t, 150.0, bars[0].BuyVolume, 0.01)  // first (100) + third (50)
	assert.InDelta(t, 50.0, bars[0].SellVolume, 0.01)   // second (50)
}

func TestDPAggregator_LargePrintDetection(t *testing.T) {
	agg := NewDPAggregator("SPY")

	base := time.Date(2025, 6, 2, 14, 0, 0, 0, time.UTC)

	// Large print: 500 * 500 = $250,000 >= $200K threshold
	agg.AddTrade(base, "D", 500.0, 500)
	// Small print: 500 * 100 = $50,000 < $200K threshold
	agg.AddTrade(base.Add(time.Second), "D", 500.0, 100)

	bars := agg.Flush()
	require.Len(t, bars, 1)

	assert.InDelta(t, 500.0, bars[0].LargePrintVolume, 0.01)
	assert.Equal(t, 1, bars[0].LargePrintCount)
	assert.InDelta(t, 500.0, bars[0].MaxPrintSize, 0.01)
}

func TestDPAggregator_EmptyFlush(t *testing.T) {
	agg := NewDPAggregator("TSLA")
	bars := agg.Flush()
	assert.Nil(t, bars)
}

func TestDPAggregator_NoDP(t *testing.T) {
	agg := NewDPAggregator("GOOG")

	base := time.Date(2025, 6, 2, 14, 0, 0, 0, time.UTC)

	// Only lit trades
	agg.AddTrade(base, "Q", 2800.0, 100)
	agg.AddTrade(base.Add(time.Second), "N", 2801.0, 50)

	bars := agg.Flush()
	require.Len(t, bars, 1)

	assert.InDelta(t, 0.0, bars[0].DPVolume, 0.01)
	assert.Equal(t, 0, bars[0].DPTrades)
	assert.InDelta(t, 150.0, bars[0].LitVolume, 0.01)
	assert.InDelta(t, 150.0, bars[0].TotalVolume, 0.01)
	assert.InDelta(t, 0.0, bars[0].DPRatio, 0.001)
	assert.InDelta(t, 0.0, bars[0].DPVWAP, 0.01)
}

func TestDPAggregator_FlushResetsState(t *testing.T) {
	agg := NewDPAggregator("NVDA")

	base := time.Date(2025, 6, 2, 14, 0, 0, 0, time.UTC)
	agg.AddTrade(base, "D", 300.0, 100)

	bars1 := agg.Flush()
	require.Len(t, bars1, 1)

	// After flush, new trades should start fresh.
	agg.AddTrade(base.Add(10*time.Minute), "D", 305.0, 200)
	bars2 := agg.Flush()
	require.Len(t, bars2, 1)

	assert.Equal(t, base.Add(10*time.Minute), bars2[0].Time)
	assert.InDelta(t, 200.0, bars2[0].DPVolume, 0.01)
}

// TestDPAggregator_FlushClosed_LeavesInflightBucket exercises the live path:
// FlushClosed must emit only buckets whose 5m window has ended on or before
// now.Truncate(5m). The bucket containing `now` itself is in-flight and stays
// in memory so the next ticker doesn't see a partial double-emit.
func TestDPAggregator_FlushClosed_LeavesInflightBucket(t *testing.T) {
	agg := NewDPAggregator("AAPL")
	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC) // 14:00 UTC = aligned 5m start

	// Three buckets: 14:00 (closed), 14:05 (closed), 14:10 (in-flight).
	agg.AddTrade(base, "D", 100, 1000)
	agg.AddTrade(base.Add(5*time.Minute), "D", 101, 2000)
	agg.AddTrade(base.Add(10*time.Minute+30*time.Second), "D", 102, 500)

	now := base.Add(13 * time.Minute) // mid-14:10 bucket
	bars := agg.FlushClosed(now)
	require.Len(t, bars, 2, "only 14:00 and 14:05 are closed at now=14:13")
	assert.Equal(t, base, bars[0].Time)
	assert.Equal(t, base.Add(5*time.Minute), bars[1].Time)

	// 14:10 bucket survives. Re-flushing with a later cutoff in the same
	// bucket produces no new bars (the in-flight one still hasn't closed).
	more := agg.FlushClosed(base.Add(14 * time.Minute))
	assert.Empty(t, more, "in-flight bucket must not be re-emitted while still open")

	// Once now advances past 14:15 the bucket closes and is emitted.
	closed := agg.FlushClosed(base.Add(15 * time.Minute))
	require.Len(t, closed, 1)
	assert.Equal(t, base.Add(10*time.Minute), closed[0].Time)
	assert.InDelta(t, 500.0, closed[0].DPVolume, 0.01)
}

// TestDPAggregator_FlushClosed_NoBuckets handles the boot-replay case where
// FlushClosed is called before any trade arrives.
func TestDPAggregator_FlushClosed_NoBuckets(t *testing.T) {
	agg := NewDPAggregator("AAPL")
	bars := agg.FlushClosed(time.Now())
	assert.Empty(t, bars)
}

// TestDPAggregator_FlushClosed_AllInflight checks that when every bucket
// is still open, FlushClosed returns nothing and leaves state intact for
// the next pass.
func TestDPAggregator_FlushClosed_AllInflight(t *testing.T) {
	agg := NewDPAggregator("AAPL")
	now := time.Date(2026, 4, 27, 14, 3, 0, 0, time.UTC)
	agg.AddTrade(now, "D", 100, 100)

	bars := agg.FlushClosed(now)
	assert.Empty(t, bars)

	// State preserved: a subsequent Flush after the bucket closes still
	// emits the trade.
	closed := agg.FlushClosed(now.Add(5 * time.Minute))
	require.Len(t, closed, 1)
	assert.InDelta(t, 100.0, closed[0].DPVolume, 0.01)
}

// TestDPAggregator_OnBucketClosed_FirstTrade verifies that the very first
// trade for a fresh aggregator never invokes the callback — there is no
// prior bucket to close.
func TestDPAggregator_OnBucketClosed_FirstTrade(t *testing.T) {
	agg := NewDPAggregator("AAPL")
	var calls int
	agg.SetOnBucketClosed(func([]domain.DarkPoolBar) { calls++ })

	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	agg.AddTrade(base, "D", 100, 50)

	assert.Equal(t, 0, calls, "first trade must not trigger transition callback")
}

// TestDPAggregator_OnBucketClosed_SameBucket: trades within the same 5m
// bucket must not invoke the callback regardless of intra-bucket ordering.
func TestDPAggregator_OnBucketClosed_SameBucket(t *testing.T) {
	agg := NewDPAggregator("AAPL")
	var calls int
	agg.SetOnBucketClosed(func([]domain.DarkPoolBar) { calls++ })

	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	agg.AddTrade(base, "D", 100, 50)
	agg.AddTrade(base.Add(30*time.Second), "D", 101, 25)
	agg.AddTrade(base.Add(4*time.Minute+59*time.Second), "Q", 100.5, 100)

	assert.Equal(t, 0, calls, "same-bucket trades must not trigger transition callback")
}

// TestDPAggregator_OnBucketClosed_TransitionEmits: the moment a tick lands in
// a strictly newer bucket, prior buckets are drained and surfaced via the
// callback. The new bucket survives in-flight.
func TestDPAggregator_OnBucketClosed_TransitionEmits(t *testing.T) {
	agg := NewDPAggregator("AAPL")
	var emitted [][]domain.DarkPoolBar
	agg.SetOnBucketClosed(func(bars []domain.DarkPoolBar) {
		emitted = append(emitted, bars)
	})

	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	agg.AddTrade(base, "D", 100, 1000) // bucket 14:00
	agg.AddTrade(base.Add(5*time.Minute+1*time.Second), "D", 101, 500) // bucket 14:05 — transition

	require.Len(t, emitted, 1)
	require.Len(t, emitted[0], 1)
	assert.Equal(t, base, emitted[0][0].Time)
	assert.InDelta(t, 1000.0, emitted[0][0].DPVolume, 0.01)

	// 14:05 bucket survives in-flight; a final Flush returns it.
	final := agg.Flush()
	require.Len(t, final, 1)
	assert.Equal(t, base.Add(5*time.Minute), final[0].Time)
	assert.InDelta(t, 500.0, final[0].DPVolume, 0.01)
}

// TestDPAggregator_OnBucketClosed_SkipAhead: a tick that skips multiple
// buckets must drain all prior buckets in time order in a single callback.
func TestDPAggregator_OnBucketClosed_SkipAhead(t *testing.T) {
	agg := NewDPAggregator("AAPL")
	var got [][]domain.DarkPoolBar
	agg.SetOnBucketClosed(func(bars []domain.DarkPoolBar) {
		got = append(got, bars)
	})

	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	agg.AddTrade(base, "D", 100, 100)
	agg.AddTrade(base.Add(5*time.Minute), "D", 101, 200)
	// Skip 14:10, jump to 14:15.
	agg.AddTrade(base.Add(15*time.Minute), "D", 102, 300)

	// Two transitions: 14:00→14:05 (drains 14:00) and 14:05→14:15 (drains 14:05).
	require.Len(t, got, 2)
	require.Len(t, got[0], 1)
	assert.Equal(t, base, got[0][0].Time)
	require.Len(t, got[1], 1)
	assert.Equal(t, base.Add(5*time.Minute), got[1][0].Time)
}

// TestDPAggregator_OnBucketClosed_LatePrintReopens: a late print arriving in
// an already-drained bucket re-creates that bucket in the window map. It does
// not retrigger the callback (no transition past latestBucket); the next
// forward transition or FlushClosed picks it up with the late print included.
func TestDPAggregator_OnBucketClosed_LatePrintReopens(t *testing.T) {
	agg := NewDPAggregator("AAPL")
	var got [][]domain.DarkPoolBar
	agg.SetOnBucketClosed(func(bars []domain.DarkPoolBar) {
		got = append(got, bars)
	})

	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	agg.AddTrade(base, "D", 100, 100)                    // bucket 14:00
	agg.AddTrade(base.Add(5*time.Minute), "D", 101, 200) // bucket 14:05 — drains 14:00
	require.Len(t, got, 1)

	// Late print landing back in the 14:00 bucket. No new callback (bucket
	// is older than latestBucket), but the window is recreated.
	agg.AddTrade(base.Add(30*time.Second), "D", 99.5, 50)
	assert.Len(t, got, 1, "late print to drained bucket must not retrigger callback")

	// Forward transition to 14:10 drains the re-opened 14:00 bucket and 14:05.
	agg.AddTrade(base.Add(10*time.Minute), "D", 102, 300)
	require.Len(t, got, 2)
	assert.Len(t, got[1], 2, "re-opened 14:00 plus closed 14:05 drain together")
	assert.Equal(t, base, got[1][0].Time)
	assert.InDelta(t, 50.0, got[1][0].DPVolume, 0.01, "drained 14:00 contains only the late print volume")
	assert.Equal(t, base.Add(5*time.Minute), got[1][1].Time)
}

// TestDPAggregator_OnBucketClosed_NotInstalled: with no callback set, the
// aggregator behaves exactly as before — no transition drain, all data
// lives in the window map until Flush/FlushClosed is called.
func TestDPAggregator_OnBucketClosed_NotInstalled(t *testing.T) {
	agg := NewDPAggregator("AAPL")

	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	agg.AddTrade(base, "D", 100, 100)
	agg.AddTrade(base.Add(5*time.Minute), "D", 101, 200)
	agg.AddTrade(base.Add(10*time.Minute), "D", 102, 300)

	bars := agg.Flush()
	require.Len(t, bars, 3, "without callback, all buckets remain until Flush")
}

// TestDPAggregator_Snapshot_ReadOnly: Snapshot must return current bucket
// state without draining or mutating the aggregator. A subsequent Flush
// produces the same data because the bucket is still there.
func TestDPAggregator_Snapshot_ReadOnly(t *testing.T) {
	agg := NewDPAggregator("AAPL")
	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	agg.AddTrade(base, "D", 100, 500)
	agg.AddTrade(base.Add(time.Minute), "Q", 101, 2000)

	snap, ok := agg.Snapshot(base)
	require.True(t, ok)
	assert.InDelta(t, 500.0, snap.DPVolume, 0.01)
	assert.InDelta(t, 2000.0, snap.LitVolume, 0.01)
	assert.InDelta(t, 2500.0, snap.TotalVolume, 0.01)

	// State preserved: Flush still produces the same bucket.
	bars := agg.Flush()
	require.Len(t, bars, 1)
	assert.InDelta(t, 500.0, bars[0].DPVolume, 0.01)
	assert.InDelta(t, 2500.0, bars[0].TotalVolume, 0.01)
}

// TestDPAggregator_Snapshot_BucketAlignment: Snapshot truncates the query
// time to the 5m boundary, so any time within the bucket window returns the
// same bar.
func TestDPAggregator_Snapshot_BucketAlignment(t *testing.T) {
	agg := NewDPAggregator("AAPL")
	base := time.Date(2026, 4, 27, 14, 5, 0, 0, time.UTC)
	agg.AddTrade(base.Add(2*time.Minute+30*time.Second), "D", 100, 1000)

	for _, off := range []time.Duration{0, time.Second, 2 * time.Minute, 4*time.Minute + 59*time.Second} {
		snap, ok := agg.Snapshot(base.Add(off))
		require.True(t, ok, "offset %v should hit the same bucket", off)
		assert.InDelta(t, 1000.0, snap.DPVolume, 0.01, "offset %v", off)
		assert.Equal(t, base, snap.Time, "all offsets resolve to bucket start time")
	}
}

// TestDPAggregator_Snapshot_NoData: querying a bucket with no trades returns
// (zero, false).
func TestDPAggregator_Snapshot_NoData(t *testing.T) {
	agg := NewDPAggregator("AAPL")
	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)

	_, ok := agg.Snapshot(base)
	assert.False(t, ok, "empty aggregator must report no data")

	// One trade in 14:00; query 14:05 still returns false.
	agg.AddTrade(base, "D", 100, 1000)
	_, ok = agg.Snapshot(base.Add(5 * time.Minute))
	assert.False(t, ok, "bucket with no trades must report no data")
}

// TestDPAggregator_Snapshot_AfterPushEmit: a snapshot of a bucket already
// drained via push-emit returns false (window was removed from the map).
// Cache is the authoritative source for drained buckets; in-flight only
// for live ones.
func TestDPAggregator_Snapshot_AfterPushEmit(t *testing.T) {
	agg := NewDPAggregator("AAPL")
	agg.SetOnBucketClosed(func([]domain.DarkPoolBar) {})

	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	agg.AddTrade(base, "D", 100, 1000)
	agg.AddTrade(base.Add(5*time.Minute), "D", 101, 500) // transition drains 14:00

	_, ok := agg.Snapshot(base)
	assert.False(t, ok, "drained bucket no longer in window map")

	snap, ok := agg.Snapshot(base.Add(5 * time.Minute))
	require.True(t, ok, "in-flight bucket still present")
	assert.InDelta(t, 500.0, snap.DPVolume, 0.01)
}

// TestDPAggregator_LookupAcrossTimezones: a trade time delivered with a
// non-UTC location (e.g. CDT from a DB driver configured for local time)
// must produce a bucket key reachable by a UTC lookup of the same instant.
// Without UTC normalization Go map equality would fail because time.Time
// compares both instant AND Location.
func TestDPAggregator_LookupAcrossTimezones(t *testing.T) {
	cdt := time.FixedZone("CDT", -5*60*60)
	agg := NewDPAggregator("AAPL")

	// Trade timestamp arrives in CDT.
	tradeTime := time.Date(2026, 4, 27, 10, 35, 30, 0, cdt) // 15:35:30 UTC
	agg.AddTrade(tradeTime, "D", 100, 500)

	// Lookup arrives in UTC for the same instant.
	utcQuery := time.Date(2026, 4, 27, 15, 35, 0, 0, time.UTC)
	bar, ok := agg.Snapshot(utcQuery)
	require.True(t, ok, "snapshot must hit despite cross-zone trade vs query times")
	assert.InDelta(t, 500.0, bar.DPVolume, 0.01)
	assert.Equal(t, time.UTC, bar.Time.Location(), "bar.Time must be UTC")
	assert.True(t, bar.Time.Equal(utcQuery), "bar.Time matches the canonical bucket")
}

// TestDPAggregator_ConcurrentAddTradeAndFlush exercises the mutex: 1000
// trades from multiple goroutines while another goroutine repeatedly
// flushes. The combined DP volume across all flushes plus the residual
// in-flight bucket must equal the total submitted volume — the mutex
// prevents lost updates and double-counting.
func TestDPAggregator_ConcurrentAddTradeAndFlush(t *testing.T) {
	agg := NewDPAggregator("AAPL")
	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)

	const writers = 8
	const tradesPerWriter = 125
	totalExpected := float64(writers * tradesPerWriter)

	done := make(chan struct{})
	var collectedVol float64
	var mu sync.Mutex
	var flushWG sync.WaitGroup
	flushWG.Add(1)
	go func() {
		defer flushWG.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			bars := agg.FlushClosed(base.Add(15 * time.Minute))
			for _, b := range bars {
				mu.Lock()
				collectedVol += b.DPVolume
				mu.Unlock()
			}
			time.Sleep(time.Microsecond)
		}
	}()

	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < tradesPerWriter; i++ {
				// Spread trades across the 14:00 and 14:05 buckets;
				// both close before the flusher's cutoff of 14:15.
				bucket := base.Add(time.Duration(i%2) * 5 * time.Minute)
				agg.AddTrade(bucket.Add(time.Duration(i)*time.Microsecond), "D", 100, 1)
			}
		}(w)
	}
	wg.Wait()
	close(done)
	flushWG.Wait() // ensure flusher goroutine has exited before reading collectedVol

	// Drain remainder.
	bars := agg.Flush()
	for _, b := range bars {
		collectedVol += b.DPVolume
	}
	assert.InDelta(t, totalExpected, collectedVol, 0.01, "concurrent AddTrade/FlushClosed must not lose or double-count volume")
}
