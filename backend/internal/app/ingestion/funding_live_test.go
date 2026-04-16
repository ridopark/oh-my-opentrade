package ingestion

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFundingLive_PollLoop_InsertsNewRates(t *testing.T) {
	callCount := 0
	ts1 := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	ts2 := time.Date(2026, 4, 15, 8, 0, 0, 0, time.UTC)

	source := &mockFundingSource{
		latestFunc: func(_ context.Context, venue domain.Venue, sym domain.Symbol) (ports.FundingRate, error) {
			callCount++
			ts := ts1
			if callCount > 1 {
				ts = ts2
			}
			return ports.FundingRate{
				Venue:         venue,
				Symbol:        sym,
				Timestamp:     ts,
				Rate:          0.0001 * float64(callCount),
				IntervalHours: 8,
			}, nil
		},
		streamFunc: func(_ context.Context, _ domain.Venue, _ domain.Symbol) (<-chan ports.FundingRate, error) {
			return nil, errors.New("not supported")
		},
	}

	db := &mockFundingDB{}
	repo := timescaledb.NewFundingRepo(db, zerolog.Nop())
	live := NewFundingLive(source, repo, zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	err := live.Run(ctx, domain.VenueBybit, []domain.Symbol{"BTC/USD"}, 100*time.Millisecond)
	// Should exit cleanly with context error.
	require.ErrorIs(t, err, context.DeadlineExceeded)
	// First immediate poll + at least 1 ticker poll.
	assert.GreaterOrEqual(t, callCount, 2, "should poll at least twice")
	assert.GreaterOrEqual(t, db.execCalls, 2, "should persist at least 2 distinct rates")
}

func TestFundingLive_PollLoop_SkipsDuplicateTimestamp(t *testing.T) {
	fixedTS := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	source := &mockFundingSource{
		latestFunc: func(_ context.Context, venue domain.Venue, sym domain.Symbol) (ports.FundingRate, error) {
			return ports.FundingRate{
				Venue:     venue,
				Symbol:    sym,
				Timestamp: fixedTS,
				Rate:      0.0001,
			}, nil
		},
		streamFunc: func(_ context.Context, _ domain.Venue, _ domain.Symbol) (<-chan ports.FundingRate, error) {
			return nil, errors.New("not supported")
		},
	}

	db := &mockFundingDB{}
	repo := timescaledb.NewFundingRepo(db, zerolog.Nop())
	live := NewFundingLive(source, repo, zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	_ = live.Run(ctx, domain.VenueBybit, []domain.Symbol{"BTC/USD"}, 50*time.Millisecond)
	// Only the first poll should actually insert; subsequent polls return the
	// same timestamp and should be skipped.
	assert.Equal(t, 1, db.execCalls, "should insert only once for duplicate timestamps")
}

func TestFundingLive_StreamMode(t *testing.T) {
	ch := make(chan ports.FundingRate, 2)
	ch <- ports.FundingRate{
		Venue:     domain.VenueHyperliquid,
		Symbol:    "BTC/USD",
		Timestamp: time.Now(),
		Rate:      0.0001,
	}
	ch <- ports.FundingRate{
		Venue:     domain.VenueHyperliquid,
		Symbol:    "BTC/USD",
		Timestamp: time.Now().Add(time.Second),
		Rate:      0.00012,
	}

	source := &mockFundingSource{
		streamFunc: func(_ context.Context, _ domain.Venue, _ domain.Symbol) (<-chan ports.FundingRate, error) {
			return ch, nil
		},
	}

	db := &mockFundingDB{}
	repo := timescaledb.NewFundingRepo(db, zerolog.Nop())
	live := NewFundingLive(source, repo, zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := live.Run(ctx, domain.VenueHyperliquid, []domain.Symbol{"BTC/USD"}, time.Minute)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	// Give the goroutine a moment to process.
	time.Sleep(50 * time.Millisecond)
	assert.GreaterOrEqual(t, db.execCalls, 1, "should persist at least one streamed rate")
}

func TestFundingLive_LatestError(t *testing.T) {
	source := &mockFundingSource{
		latestFunc: func(_ context.Context, _ domain.Venue, _ domain.Symbol) (ports.FundingRate, error) {
			return ports.FundingRate{}, errors.New("network error")
		},
		streamFunc: func(_ context.Context, _ domain.Venue, _ domain.Symbol) (<-chan ports.FundingRate, error) {
			return nil, errors.New("not supported")
		},
	}

	db := &mockFundingDB{}
	repo := timescaledb.NewFundingRepo(db, zerolog.Nop())
	live := NewFundingLive(source, repo, zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	err := live.Run(ctx, domain.VenueBybit, []domain.Symbol{"BTC/USD"}, 50*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 0, db.execCalls, "should not persist when Latest() fails")
}
