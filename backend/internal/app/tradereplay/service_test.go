package tradereplay_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/tradereplay"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeReader returns canned per-symbol trade slices. tradesBySym is keyed by
// the symbol string. errBySym lets tests inject per-symbol read failures.
type fakeReader struct {
	tradesBySym map[string][]domain.MarketTrade
	errBySym    map[string]error
	calls       []readCall
}

type readCall struct {
	symbol string
	from   time.Time
	to     time.Time
}

func (r *fakeReader) GetMarketTrades(_ context.Context, sym domain.Symbol, from, to time.Time) ([]domain.MarketTrade, error) {
	r.calls = append(r.calls, readCall{symbol: string(sym), from: from, to: to})
	if err := r.errBySym[string(sym)]; err != nil {
		return nil, err
	}
	return append([]domain.MarketTrade(nil), r.tradesBySym[string(sym)]...), nil
}

func mkTrade(sym string, ts time.Time, price, size float64, exchange string) domain.MarketTrade {
	return domain.MarketTrade{
		Time:     ts,
		Symbol:   domain.Symbol(sym),
		Price:    price,
		Size:     size,
		Exchange: exchange,
	}
}

func discardLogger() zerolog.Logger { return zerolog.New(io.Discard) }

func TestService_Replay_HappyPath_FeedsTradesInOrder(t *testing.T) {
	// t0 must be in the past — Replay caps the window at time.Now() and a
	// future since/to range becomes a no-op (see TestService_Replay_EmptyWindow).
	t0 := time.Now().UTC().Add(-10 * time.Minute)
	reader := &fakeReader{tradesBySym: map[string][]domain.MarketTrade{
		"AAPL": {
			mkTrade("AAPL", t0, 100.0, 10, "D"),
			mkTrade("AAPL", t0.Add(1*time.Second), 100.5, 20, ""),
			mkTrade("AAPL", t0.Add(2*time.Second), 101.0, 5, "D"),
		},
		"MSFT": {
			mkTrade("MSFT", t0, 350.0, 1, ""),
		},
	}}
	svc := tradereplay.New(reader, discardLogger())

	var got []domain.MarketTrade
	sink := func(_ context.Context, tr domain.MarketTrade) error {
		got = append(got, tr)
		return nil
	}

	stats, err := svc.Replay(context.Background(), t0.Add(-5*time.Minute), []domain.Symbol{"AAPL", "MSFT"}, sink)

	require.NoError(t, err)
	require.Len(t, got, 4)
	// Per-symbol order preserved (AAPL block first, then MSFT) and within
	// each block strictly ascending.
	assert.Equal(t, "AAPL", string(got[0].Symbol))
	assert.True(t, got[0].Time.Before(got[1].Time))
	assert.True(t, got[1].Time.Before(got[2].Time))
	assert.Equal(t, "MSFT", string(got[3].Symbol))

	assert.Equal(t, 2, stats.Symbols)
	assert.Equal(t, 0, stats.SymbolsFailed)
	assert.Equal(t, 4, stats.Trades)
	assert.Equal(t, 3, stats.PerSymbol["AAPL"].Trades)
	assert.Equal(t, t0, stats.PerSymbol["AAPL"].First)
	assert.Equal(t, t0.Add(2*time.Second), stats.PerSymbol["AAPL"].Last)
	assert.Equal(t, 1, stats.PerSymbol["MSFT"].Trades)
}

func TestService_Replay_EmptyWindow_ReturnsZeroStats(t *testing.T) {
	reader := &fakeReader{}
	svc := tradereplay.New(reader, discardLogger())

	// since in the future relative to now → empty window, not an error.
	stats, err := svc.Replay(context.Background(), time.Now().Add(time.Hour), []domain.Symbol{"AAPL"}, tradereplay.LoggingSink())

	require.NoError(t, err)
	assert.Equal(t, 0, stats.Trades)
	assert.Empty(t, reader.calls, "reader must not be called when window is empty")
}

func TestService_Replay_NilSink_ReturnsError(t *testing.T) {
	svc := tradereplay.New(&fakeReader{}, discardLogger())
	_, err := svc.Replay(context.Background(), time.Now().Add(-time.Hour), []domain.Symbol{"AAPL"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sink is nil")
}

func TestService_Replay_ZeroSince_ReturnsError(t *testing.T) {
	svc := tradereplay.New(&fakeReader{}, discardLogger())
	_, err := svc.Replay(context.Background(), time.Time{}, []domain.Symbol{"AAPL"}, tradereplay.LoggingSink())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "since is zero")
}

func TestService_Replay_ReadError_SkipsSymbolAndContinues(t *testing.T) {
	t0 := time.Now().UTC().Add(-10 * time.Minute)
	boom := errors.New("simulated DB failure")
	reader := &fakeReader{
		tradesBySym: map[string][]domain.MarketTrade{
			"GOOD": {mkTrade("GOOD", t0, 50.0, 1, "")},
		},
		errBySym: map[string]error{"BAD": boom},
	}
	svc := tradereplay.New(reader, discardLogger())

	var dispatched []string
	sink := func(_ context.Context, tr domain.MarketTrade) error {
		dispatched = append(dispatched, string(tr.Symbol))
		return nil
	}

	stats, err := svc.Replay(context.Background(), t0.Add(-time.Minute), []domain.Symbol{"BAD", "GOOD"}, sink)

	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, []string{"GOOD"}, dispatched, "good symbol still replays after bad one fails")
	assert.Equal(t, 1, stats.Symbols)
	assert.Equal(t, 1, stats.SymbolsFailed)
	assert.Equal(t, 1, stats.Trades)
	assert.True(t, stats.PerSymbol["BAD"].ReadError)
	assert.False(t, stats.PerSymbol["GOOD"].ReadError)
}

func TestService_Replay_SinkError_StopsCurrentSymbolAndContinues(t *testing.T) {
	t0 := time.Now().UTC().Add(-10 * time.Minute)
	reader := &fakeReader{tradesBySym: map[string][]domain.MarketTrade{
		"AAPL": {
			mkTrade("AAPL", t0, 100.0, 1, ""),
			mkTrade("AAPL", t0.Add(time.Second), 100.5, 1, ""),
			mkTrade("AAPL", t0.Add(2*time.Second), 101.0, 1, ""),
		},
		"MSFT": {
			mkTrade("MSFT", t0, 350.0, 1, ""),
		},
	}}
	svc := tradereplay.New(reader, discardLogger())

	boom := errors.New("aggregator wedged")
	failOnSecond := false
	sinkCalls := 0
	sink := func(_ context.Context, tr domain.MarketTrade) error {
		sinkCalls++
		if string(tr.Symbol) == "AAPL" && !failOnSecond {
			failOnSecond = true
			return nil
		}
		if string(tr.Symbol) == "AAPL" {
			return boom
		}
		return nil
	}

	stats, err := svc.Replay(context.Background(), t0.Add(-time.Minute), []domain.Symbol{"AAPL", "MSFT"}, sink)

	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	// AAPL: 1 successful, 1 error → loop exits before 3rd. MSFT still runs.
	assert.Equal(t, 3, sinkCalls, "sink stops AAPL after error and runs MSFT once")
	assert.Equal(t, 1, stats.Symbols, "MSFT counted as successful symbol")
	assert.Equal(t, 1, stats.SymbolsFailed, "AAPL counted as failed symbol")
	assert.True(t, stats.PerSymbol["AAPL"].SinkError)
	assert.Equal(t, 1, stats.PerSymbol["AAPL"].Trades, "first AAPL trade was dispatched before the error")
	assert.Equal(t, 1, stats.PerSymbol["MSFT"].Trades)
}

func TestService_Replay_RespectsContextCancellation(t *testing.T) {
	t0 := time.Now().UTC().Add(-time.Minute)
	reader := &fakeReader{tradesBySym: map[string][]domain.MarketTrade{
		"AAPL": {mkTrade("AAPL", t0, 100, 1, "")},
		"MSFT": {mkTrade("MSFT", t0, 350, 1, "")},
	}}
	svc := tradereplay.New(reader, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Replay starts so the very first symbol bails.

	stats, err := svc.Replay(ctx, t0.Add(-time.Minute), []domain.Symbol{"AAPL", "MSFT"}, tradereplay.LoggingSink())

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, stats.Trades, "cancellation aborts before first read")
	assert.Empty(t, reader.calls, "reader is never invoked after cancellation")
}

func TestService_Replay_PassesWindowToReader(t *testing.T) {
	since := time.Now().UTC().Add(-30 * time.Minute)
	reader := &fakeReader{tradesBySym: map[string][]domain.MarketTrade{}}
	svc := tradereplay.New(reader, discardLogger())

	_, err := svc.Replay(context.Background(), since, []domain.Symbol{"AAPL"}, tradereplay.LoggingSink())
	require.NoError(t, err)
	require.Len(t, reader.calls, 1)
	assert.Equal(t, "AAPL", reader.calls[0].symbol)
	assert.Equal(t, since, reader.calls[0].from)
	// The replayer captures `to` at entry; it must be >= the test's start
	// time (within a generous fudge factor for slow CI).
	assert.True(t, reader.calls[0].to.After(since))
}
