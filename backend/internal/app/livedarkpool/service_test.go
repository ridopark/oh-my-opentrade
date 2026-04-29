package livedarkpool_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/livedarkpool"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRepo records every SaveDarkPoolBars call and lets tests inject errors.
type fakeRepo struct {
	mu       sync.Mutex
	batches  [][]domain.DarkPoolBar
	saveErr  error
	saveCall int
}

func (r *fakeRepo) SaveDarkPoolBars(_ context.Context, bars []domain.DarkPoolBar) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saveCall++
	if r.saveErr != nil {
		return 0, r.saveErr
	}
	cp := make([]domain.DarkPoolBar, len(bars))
	copy(cp, bars)
	r.batches = append(r.batches, cp)
	return len(bars), nil
}

func (r *fakeRepo) totalSaved() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, b := range r.batches {
		n += len(b)
	}
	return n
}

func discardLogger() zerolog.Logger { return zerolog.New(io.Discard) }

func mkTrade(sym string, ts time.Time, price, size float64, exchange string) domain.MarketTrade {
	return domain.MarketTrade{
		Time:     ts,
		Symbol:   domain.Symbol(sym),
		Price:    price,
		Size:     size,
		Exchange: exchange,
	}
}

func TestService_AddTrade_LazyInitsAggregatorPerSymbol(t *testing.T) {
	repo := &fakeRepo{}
	now := time.Date(2026, 4, 27, 14, 12, 0, 0, time.UTC) // mid-bucket
	svc := livedarkpool.New(repo, discardLogger(), livedarkpool.WithNow(func() time.Time { return now }))

	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	svc.AddTrade(mkTrade("AAPL", base, 100, 1000, "D"))
	svc.AddTrade(mkTrade("MSFT", base, 350, 500, "D"))

	// Lookup hits via the snapshot fallback even before any flush; HasData
	// still reports false because nothing has been written to the cache yet.
	bar, ok := svc.Lookup("AAPL", base)
	require.True(t, ok)
	assert.InDelta(t, 1000.0, bar.DPVolume, 0.01)
	assert.False(t, svc.HasData())
}

func TestService_FlushNow_PopulatesCacheAndPersists(t *testing.T) {
	repo := &fakeRepo{}
	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	now := base.Add(13 * time.Minute) // 14:13 — buckets 14:00 and 14:05 are closed
	svc := livedarkpool.New(repo, discardLogger(), livedarkpool.WithNow(func() time.Time { return now }))

	// Two trades into 14:00 bucket, one into 14:05 bucket, one into 14:10 (in-flight).
	svc.AddTrade(mkTrade("AAPL", base, 100, 1000, "D"))
	svc.AddTrade(mkTrade("AAPL", base.Add(time.Minute), 101, 500, "D"))
	svc.AddTrade(mkTrade("AAPL", base.Add(5*time.Minute), 102, 2000, "D"))
	svc.AddTrade(mkTrade("AAPL", base.Add(10*time.Minute+30*time.Second), 103, 800, "D"))

	svc.FlushNow(context.Background())

	// 14:00 and 14:05 closed, 14:10 still in-flight.
	bar00, ok := svc.Lookup("AAPL", base)
	require.True(t, ok)
	assert.InDelta(t, 1500.0, bar00.DPVolume, 0.01)

	bar05, ok := svc.Lookup("AAPL", base.Add(5*time.Minute))
	require.True(t, ok)
	assert.InDelta(t, 2000.0, bar05.DPVolume, 0.01)

	// In-flight bucket is served via the aggregator snapshot fallback, so
	// Lookup hits with the partial accumulator state. This is the
	// bar-close-race fix: the strategy can read the just-closed bucket
	// before any push-emit or ticker has populated the cache.
	bar10, ok := svc.Lookup("AAPL", base.Add(10*time.Minute))
	require.True(t, ok, "in-flight bucket must be served via snapshot fallback")
	assert.InDelta(t, 800.0, bar10.DPVolume, 0.01)

	assert.True(t, svc.HasData())
	assert.Equal(t, 2, repo.totalSaved(), "both closed buckets persisted to darkpool_bars")
}

func TestService_FlushNow_NoBuckets_NoSaveCall(t *testing.T) {
	repo := &fakeRepo{}
	svc := livedarkpool.New(repo, discardLogger())
	svc.FlushNow(context.Background())
	assert.Equal(t, 0, repo.saveCall, "empty service must not call repo.SaveDarkPoolBars")
}

func TestService_FlushNow_AllInflight_NoSaveCall(t *testing.T) {
	repo := &fakeRepo{}
	now := time.Date(2026, 4, 27, 14, 3, 0, 0, time.UTC)
	svc := livedarkpool.New(repo, discardLogger(), livedarkpool.WithNow(func() time.Time { return now }))
	svc.AddTrade(mkTrade("AAPL", now, 100, 100, "D"))

	svc.FlushNow(context.Background())
	assert.Equal(t, 0, repo.saveCall, "in-flight-only state must not trigger a write")
	assert.False(t, svc.HasData())
}

func TestService_FlushNow_RepoError_RetainsCache(t *testing.T) {
	repo := &fakeRepo{saveErr: errors.New("connection reset")}
	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	now := base.Add(7 * time.Minute) // 14:00 bucket closed
	svc := livedarkpool.New(repo, discardLogger(), livedarkpool.WithNow(func() time.Time { return now }))

	svc.AddTrade(mkTrade("AAPL", base, 100, 1000, "D"))
	svc.FlushNow(context.Background())

	// Cache populated even though save failed — runner can still serve the
	// bucket from memory; next flush retry persists it.
	_, ok := svc.Lookup("AAPL", base)
	assert.True(t, ok, "cache must be populated even when save fails so the runner has the data")
}

func TestService_AddTrade_ExchangeFilter(t *testing.T) {
	// Lit (non-D) trades must increment totalVolume but not dpVolume —
	// matches backfill.DPAggregator semantics. This is the audit (5d)
	// precondition: same input = same output as omo-data backfill.
	repo := &fakeRepo{}
	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	now := base.Add(7 * time.Minute)
	svc := livedarkpool.New(repo, discardLogger(), livedarkpool.WithNow(func() time.Time { return now }))

	svc.AddTrade(mkTrade("AAPL", base, 100, 1000, "D"))
	svc.AddTrade(mkTrade("AAPL", base, 100, 4000, "K")) // lit
	svc.FlushNow(context.Background())

	bar, ok := svc.Lookup("AAPL", base)
	require.True(t, ok)
	assert.InDelta(t, 1000.0, bar.DPVolume, 0.01)
	assert.InDelta(t, 4000.0, bar.LitVolume, 0.01)
	assert.InDelta(t, 5000.0, bar.TotalVolume, 0.01)
	assert.InDelta(t, 0.20, bar.DPRatio, 0.001)
}

func TestService_AddTrade_EmptySymbol_Ignored(t *testing.T) {
	repo := &fakeRepo{}
	now := time.Now().UTC()
	svc := livedarkpool.New(repo, discardLogger(), livedarkpool.WithNow(func() time.Time { return now }))
	svc.AddTrade(domain.MarketTrade{Time: now, Price: 100, Size: 1, Exchange: "D"})
	svc.FlushNow(context.Background())
	assert.False(t, svc.HasData())
	assert.Equal(t, 0, repo.saveCall)
}

// TestService_AddTrade_PushEmitsClosedBucketsToCache exercises the
// transition push-emit: when a trade arrives in a strictly newer 5m bucket,
// prior buckets must appear in the cache without any FlushNow / ticker tick.
// This closes the cache-vs-strategy timing race observed in the parity plan.
func TestService_AddTrade_PushEmitsClosedBucketsToCache(t *testing.T) {
	repo := &fakeRepo{}
	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	// `now` is irrelevant for push-emit — drain is driven by the trade itself.
	svc := livedarkpool.New(repo, discardLogger(), livedarkpool.WithNow(func() time.Time { return base }))

	svc.AddTrade(mkTrade("AAPL", base, 100, 1000, "D"))
	// In-flight bucket is served via the snapshot fallback even before any
	// transition. This is intentional — covers the bar-close race where the
	// strategy queries the just-closed bucket before push-emit can fire.
	preBar, ok := svc.Lookup("AAPL", base)
	require.True(t, ok, "snapshot fallback must serve in-flight bucket")
	assert.InDelta(t, 1000.0, preBar.DPVolume, 0.01)

	// Trade in 14:05 triggers push-emit of 14:00, populating the cache.
	svc.AddTrade(mkTrade("AAPL", base.Add(5*time.Minute+100*time.Millisecond), 101, 500, "D"))

	bar, ok := svc.Lookup("AAPL", base)
	require.True(t, ok, "push-emit must populate cache before any FlushNow / ticker")
	assert.InDelta(t, 1000.0, bar.DPVolume, 0.01)

	// 14:05 itself is now in-flight — snapshot fallback returns the partial
	// accumulator state (just the one trade we put there).
	bar05, ok := svc.Lookup("AAPL", base.Add(5*time.Minute))
	require.True(t, ok)
	assert.InDelta(t, 500.0, bar05.DPVolume, 0.01)

	// Save has not happened yet — pendingPersist holds the bar until the ticker.
	assert.Equal(t, 0, repo.saveCall)
}

// TestService_Lookup_SnapshotFallback exercises the cache-miss path: when no
// push-emit or ticker has populated the cache yet, Lookup must serve the
// per-symbol aggregator's in-flight state directly. Closes the bar-close
// race where the strategy reads the just-closed bucket within ~30ms of the
// boundary, before any trade in the next bucket has triggered push-emit.
func TestService_Lookup_SnapshotFallback(t *testing.T) {
	repo := &fakeRepo{}
	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	svc := livedarkpool.New(repo, discardLogger(), livedarkpool.WithNow(func() time.Time { return base }))

	svc.AddTrade(mkTrade("AAPL", base.Add(30*time.Second), 100, 1000, "D"))
	svc.AddTrade(mkTrade("AAPL", base.Add(time.Minute), 101, 4000, "Q"))

	// No flush, no transition. Cache is empty. Lookup must still return
	// the bar via the aggregator snapshot path.
	bar, ok := svc.Lookup("AAPL", base)
	require.True(t, ok)
	assert.InDelta(t, 1000.0, bar.DPVolume, 0.01)
	assert.InDelta(t, 4000.0, bar.LitVolume, 0.01)
	assert.InDelta(t, 5000.0, bar.TotalVolume, 0.01)
	assert.InDelta(t, 0.20, bar.DPRatio, 0.001)

	// Unknown symbol returns false — no aggregator instantiated.
	_, ok = svc.Lookup("MSFT", base)
	assert.False(t, ok)

	// Symbol with aggregator but bucket with no trades returns false.
	_, ok = svc.Lookup("AAPL", base.Add(15*time.Minute))
	assert.False(t, ok)
}

// TestService_FlushNow_DrainsPendingPersist: after a push-emit transition,
// FlushNow must persist the buffered bars and clear the buffer so a second
// FlushNow with no new trades is a no-op (no double-persist).
func TestService_FlushNow_DrainsPendingPersist(t *testing.T) {
	repo := &fakeRepo{}
	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	now := base.Add(7 * time.Minute) // 14:00 closed by time, but we'll trigger via push-emit too
	svc := livedarkpool.New(repo, discardLogger(), livedarkpool.WithNow(func() time.Time { return now }))

	svc.AddTrade(mkTrade("AAPL", base, 100, 1000, "D"))
	svc.AddTrade(mkTrade("AAPL", base.Add(5*time.Minute), 101, 500, "D")) // push-emit 14:00

	svc.FlushNow(context.Background())
	require.Equal(t, 1, repo.totalSaved(), "first flush persists the push-emitted bar")

	// Second flush with no new trades and no time advance: nothing to persist.
	svc.FlushNow(context.Background())
	assert.Equal(t, 1, repo.totalSaved(), "second flush must not double-persist")
}

// TestService_FlushNow_MergesPushEmitWithTimeBasedDrain: two symbols, one
// with a transition trade (push-emit) and one quiet (time-based drain). A
// single FlushNow must persist both in one SaveDarkPoolBars call.
func TestService_FlushNow_MergesPushEmitWithTimeBasedDrain(t *testing.T) {
	repo := &fakeRepo{}
	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	now := base.Add(7 * time.Minute)
	svc := livedarkpool.New(repo, discardLogger(), livedarkpool.WithNow(func() time.Time { return now }))

	// AAPL: transition trade — push-emits 14:00 to cache + pendingPersist.
	svc.AddTrade(mkTrade("AAPL", base, 100, 1000, "D"))
	svc.AddTrade(mkTrade("AAPL", base.Add(5*time.Minute), 101, 500, "D"))

	// MSFT: only one trade in 14:00, no follow-up — relies on time-based drain.
	svc.AddTrade(mkTrade("MSFT", base, 350, 800, "D"))

	svc.FlushNow(context.Background())

	require.Equal(t, 1, repo.saveCall, "push-emit and time-based drain must batch into one save call")
	assert.Equal(t, 2, repo.totalSaved())

	bAAPL, ok := svc.Lookup("AAPL", base)
	require.True(t, ok)
	assert.InDelta(t, 1000.0, bAAPL.DPVolume, 0.01)

	bMSFT, ok := svc.Lookup("MSFT", base)
	require.True(t, ok)
	assert.InDelta(t, 800.0, bMSFT.DPVolume, 0.01)
}

// TestService_FlushNow_RepoError_PreservesPendingPersist: when the repo
// fails, push-emitted bars must be re-queued so the next flush retries them.
// Cache stays populated regardless so the runner is unaffected.
func TestService_FlushNow_RepoError_PreservesPendingPersist(t *testing.T) {
	repo := &fakeRepo{saveErr: errors.New("connection reset")}
	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	svc := livedarkpool.New(repo, discardLogger(), livedarkpool.WithNow(func() time.Time { return base }))

	svc.AddTrade(mkTrade("AAPL", base, 100, 1000, "D"))
	svc.AddTrade(mkTrade("AAPL", base.Add(5*time.Minute), 101, 500, "D")) // push-emit

	svc.FlushNow(context.Background())
	require.Equal(t, 1, repo.saveCall)
	require.Equal(t, 0, repo.totalSaved(), "save failed on first attempt")

	_, ok := svc.Lookup("AAPL", base)
	assert.True(t, ok, "cache populated despite save failure")

	// Recover and retry — the previously-failed bar must persist on this pass.
	repo.mu.Lock()
	repo.saveErr = nil
	repo.mu.Unlock()

	svc.FlushNow(context.Background())
	assert.Equal(t, 1, repo.totalSaved(), "retry persists the bar that failed earlier")
}

// TestService_Run_FlushesOnTicker drives Run with a short flushInterval and
// asserts the ticker actually persists bars without manual FlushNow calls.
func TestService_Run_FlushesOnTicker(t *testing.T) {
	repo := &fakeRepo{}
	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	now := base.Add(7 * time.Minute) // 14:00 closed
	svc := livedarkpool.New(
		repo, discardLogger(),
		livedarkpool.WithNow(func() time.Time { return now }),
		livedarkpool.WithFlushInterval(10*time.Millisecond),
	)
	svc.AddTrade(mkTrade("AAPL", base, 100, 1000, "D"))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go svc.Run(ctx)

	// Wait for at least one flush tick.
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		if svc.HasData() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	assert.True(t, svc.HasData(), "ticker must have flushed within the test window")
	assert.Greater(t, repo.totalSaved(), 0)
}

// TestService_ConcurrentAddTradeAndLookup is a race-detector smoke test
// that exercises the Service.mu boundaries while a flush ticker fires.
func TestService_ConcurrentAddTradeAndLookup(t *testing.T) {
	repo := &fakeRepo{}
	base := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	now := base.Add(7 * time.Minute)
	svc := livedarkpool.New(
		repo, discardLogger(),
		livedarkpool.WithNow(func() time.Time { return now }),
		livedarkpool.WithFlushInterval(time.Millisecond),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go svc.Run(ctx)

	const writers = 4
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				svc.AddTrade(mkTrade("AAPL", base.Add(time.Duration(i)*time.Microsecond), 100, 1, "D"))
				_, _ = svc.Lookup("AAPL", base) // race candidate with ticker flushing
			}
		}(w)
	}
	wg.Wait()
	cancel()
	// Test passes if the race detector reports nothing.
}
