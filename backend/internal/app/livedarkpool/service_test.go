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

	// Before any flush, cache must be empty (in-flight bucket only).
	_, ok := svc.Lookup("AAPL", base)
	assert.False(t, ok)
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

	_, ok = svc.Lookup("AAPL", base.Add(10*time.Minute))
	assert.False(t, ok, "in-flight bucket must not be in the cache yet")

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
