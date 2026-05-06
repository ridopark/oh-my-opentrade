package backtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLiveMarket is a counted ports.OptionsMarketDataPort used to drive
// the live-fallback paths. Test sets resp/err per case and inspects calls.
type fakeLiveMarket struct {
	calls int
	resp  []domain.OptionContractSnapshot
	err   error
}

func (f *fakeLiveMarket) GetOptionChain(
	_ context.Context,
	_ domain.Symbol,
	_ time.Time,
	_ domain.OptionRight,
	_, _ int,
) ([]domain.OptionContractSnapshot, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

var _ ports.OptionsMarketDataPort = (*fakeLiveMarket)(nil)

// countingSyntheticRepo lets a test count whether synthetic was reached.
// We can't directly mock SyntheticChainGenerator (it's a concrete struct),
// so each test that needs to assert "synth was/was not called" uses a
// constantSpot fn wrapped in a counter.
func countingSpotFn(counter *int) func(context.Context, domain.Symbol, time.Time) (float64, error) {
	return func(_ context.Context, _ domain.Symbol, _ time.Time) (float64, error) {
		*counter++
		return 100.0, nil
	}
}

// histRepoWithExpiry returns a single in-window row, so DoltHub path hits.
type histRepoWithExpiry struct {
	expiryOffsetDays int
}

func (h histRepoWithExpiry) GetHistoricalChain(_ context.Context, sym domain.Symbol, asOf time.Time, right domain.OptionRight, _, _ int) ([]domain.HistoricalOptionChainRow, error) {
	return []domain.HistoricalOptionChainRow{{
		Symbol:     sym,
		Date:       asOf,
		Expiration: asOf.AddDate(0, 0, h.expiryOffsetDays),
		Strike:     100.0,
		Right:      right,
		Bid:        1.0, Ask: 1.1, Delta: 0.5, IV: 0.30,
	}}, nil
}
func (histRepoWithExpiry) GetHistoricalChainBulk(_ context.Context, _ []domain.Symbol, _, _ time.Time) ([]domain.HistoricalOptionChainRow, error) {
	return nil, nil
}
func (histRepoWithExpiry) GetHistoricalContract(_ context.Context, _ domain.Symbol, _ time.Time, _ float64, _ time.Time, _ domain.OptionRight) (*domain.HistoricalOptionChainRow, error) {
	return nil, nil
}
func (histRepoWithExpiry) HasData(_ context.Context, _ domain.Symbol, _ time.Time) (bool, error) {
	return true, nil
}
func (histRepoWithExpiry) SaveBatch(_ context.Context, _ []domain.HistoricalOptionChainRow) error {
	return nil
}

// makeLiveSnap builds a single OptionContractSnapshot for fakeLiveMarket
// to return on a "live hit" case.
func makeLiveSnap(sym string, asOf time.Time) domain.OptionContractSnapshot {
	c, _ := domain.NewOptionContract(sym, asOf.AddDate(0, 0, 7), 100.0, domain.OptionRightCall, domain.OptionStyleAmerican)
	return domain.OptionContractSnapshot{
		OptionContract: c,
		OptionQuote: domain.OptionQuote{
			Bid: 1.0, Ask: 1.1, Last: 1.05, Timestamp: asOf,
		},
		Greeks: domain.Greeks{Delta: 0.5, IV: 0.30},
	}
}

// Path 1: DoltHub hit -> never calls live or synth.
func TestAdapter_LiveFallback_DoltHubHit_NeverCallsLiveOrSynth(t *testing.T) {
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	adapter := NewHistoricalOptionsAdapter(histRepoWithExpiry{expiryOffsetDays: 7}, func() time.Time { return asOf })

	live := &fakeLiveMarket{}
	adapter.SetLiveChainFallback(live)

	synthCalls := 0
	adapter.SetSyntheticGenerator(NewSyntheticChainGenerator(
		defaultTestConfig(), countingSpotFn(&synthCalls), constantIV(0.30),
	))

	chain, err := adapter.GetOptionChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	require.NotEmpty(t, chain, "DoltHub row should satisfy the request")

	assert.Equal(t, 0, live.calls, "live must not be called when DoltHub satisfies the DTE window")
	assert.Equal(t, 0, synthCalls, "synth must not be called when DoltHub satisfies the DTE window")

	stats := adapter.StatsWithLive()
	assert.Equal(t, uint64(1), stats.HistHits)
	assert.Equal(t, uint64(0), stats.LiveHits)
	assert.Equal(t, uint64(0), stats.SynthHits)
	assert.Equal(t, uint64(0), stats.LiveErrors)
}

// Path 2: DoltHub miss + live hit -> never calls synth, LiveHits++.
func TestAdapter_LiveFallback_LiveHit_NeverCallsSynth(t *testing.T) {
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	adapter := NewHistoricalOptionsAdapter(stubHistOptRepo{}, func() time.Time { return asOf })

	live := &fakeLiveMarket{resp: []domain.OptionContractSnapshot{makeLiveSnap("AAPL", asOf)}}
	adapter.SetLiveChainFallback(live)

	synthCalls := 0
	adapter.SetSyntheticGenerator(NewSyntheticChainGenerator(
		defaultTestConfig(), countingSpotFn(&synthCalls), constantIV(0.30),
	))

	chain, err := adapter.GetOptionChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	require.Len(t, chain, 1, "live response should be returned as-is")

	assert.Equal(t, 1, live.calls)
	assert.Equal(t, 0, synthCalls, "synth must not be reached after a live hit")

	stats := adapter.StatsWithLive()
	assert.Equal(t, uint64(0), stats.HistHits)
	assert.Equal(t, uint64(1), stats.LiveHits)
	assert.Equal(t, uint64(0), stats.SynthHits)
	assert.Equal(t, uint64(0), stats.LiveErrors)
}

// Path 3: DoltHub miss + live empty -> calls synth, LiveHits unchanged.
func TestAdapter_LiveFallback_LiveEmpty_FallsToSynth(t *testing.T) {
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	adapter := NewHistoricalOptionsAdapter(stubHistOptRepo{}, func() time.Time { return asOf })

	live := &fakeLiveMarket{resp: nil}
	adapter.SetLiveChainFallback(live)

	synthCalls := 0
	adapter.SetSyntheticGenerator(NewSyntheticChainGenerator(
		defaultTestConfig(), countingSpotFn(&synthCalls), constantIV(0.30),
	))

	chain, err := adapter.GetOptionChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	assert.NotEmpty(t, chain, "synth must produce a chain when live is empty")

	assert.Equal(t, 1, live.calls)
	assert.Greater(t, synthCalls, 0, "synth must be reached on live-empty")

	stats := adapter.StatsWithLive()
	assert.Equal(t, uint64(0), stats.LiveHits, "empty live response must not bump LiveHits")
	assert.Equal(t, uint64(1), stats.SynthHits)
	assert.Equal(t, uint64(0), stats.LiveErrors)
}

// Path 4: DoltHub miss + live error -> calls synth, LiveErrors++.
func TestAdapter_LiveFallback_LiveError_FallsToSynth(t *testing.T) {
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	adapter := NewHistoricalOptionsAdapter(stubHistOptRepo{}, func() time.Time { return asOf })

	live := &fakeLiveMarket{err: errors.New("alpaca: list option contracts failed (status 401): unauthorized")}
	adapter.SetLiveChainFallback(live)

	synthCalls := 0
	adapter.SetSyntheticGenerator(NewSyntheticChainGenerator(
		defaultTestConfig(), countingSpotFn(&synthCalls), constantIV(0.30),
	))

	chain, err := adapter.GetOptionChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 1, 14)
	require.NoError(t, err, "live error must be swallowed; synth still runs")
	assert.NotEmpty(t, chain)

	assert.Equal(t, 1, live.calls)
	assert.Greater(t, synthCalls, 0, "synth must be reached on live-error")

	stats := adapter.StatsWithLive()
	assert.Equal(t, uint64(0), stats.LiveHits)
	assert.Equal(t, uint64(1), stats.SynthHits)
	assert.Equal(t, uint64(1), stats.LiveErrors)
}

// Path 5: DoltHub miss + live nil + synth disabled -> empty result.
func TestAdapter_LiveFallback_NoLive_NoSynth_Empty(t *testing.T) {
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	adapter := NewHistoricalOptionsAdapter(stubHistOptRepo{}, func() time.Time { return asOf })
	// no SetLiveChainFallback, no SetSyntheticGenerator

	chain, err := adapter.GetOptionChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	assert.Empty(t, chain, "no live + no synth + no DoltHub data must return empty")

	stats := adapter.StatsWithLive()
	assert.Equal(t, ChainSourceStats{}, stats, "all counters must be zero in the off-by-default case")
}

// Path 6: loaded=true && !hasExpiryInDTERange -> falls to live before synth.
// DoltHub PreLoaded a 30-DTE row; strategy wants 1..14. New behavior: this
// must hit live FIRST, not synth.
func TestAdapter_LiveFallback_PreloadedOutOfRange_FallsToLiveBeforeSynth(t *testing.T) {
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	// Use bulk repo so PreLoad populates the cache with an out-of-range row.
	repo := bulkStubRepo{rows: []domain.HistoricalOptionChainRow{
		makeRow("AAPL", 100.0, asOf.AddDate(0, 0, 30), asOf),
	}}
	adapter := NewHistoricalOptionsAdapter(repo, func() time.Time { return asOf })
	require.NoError(t, adapter.PreLoad(context.Background(), []domain.Symbol{"AAPL"}, asOf, asOf.AddDate(0, 0, 1)))
	require.True(t, adapter.IsLoaded(), "cache must be loaded for this path")

	live := &fakeLiveMarket{resp: []domain.OptionContractSnapshot{makeLiveSnap("AAPL", asOf)}}
	adapter.SetLiveChainFallback(live)

	synthCalls := 0
	adapter.SetSyntheticGenerator(NewSyntheticChainGenerator(
		defaultTestConfig(), countingSpotFn(&synthCalls), constantIV(0.30),
	))

	chain, err := adapter.GetOptionChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	require.Len(t, chain, 1, "live hit must be returned")

	assert.Equal(t, 1, live.calls, "live MUST be tried when preloaded chain has no in-range expiry")
	assert.Equal(t, 0, synthCalls, "synth MUST NOT run when live hits")

	stats := adapter.StatsWithLive()
	assert.Equal(t, uint64(1), stats.LiveHits)
	assert.Equal(t, uint64(0), stats.SynthHits)
}

// TestAdapter_LiveFallback_OffByDefault_Identity asserts the plan
// acceptance criterion #5: an adapter with no SetLiveChainFallback call
// behaves identically to current main. Same fixture as the existing
// synth-fallback test; result must match.
func TestAdapter_LiveFallback_OffByDefault_Identity(t *testing.T) {
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	adapter := NewHistoricalOptionsAdapter(stubHistOptRepo{}, func() time.Time { return asOf })
	adapter.SetSyntheticGenerator(NewSyntheticChainGenerator(
		defaultTestConfig(), constantSpot(100.0), constantIV(0.30),
	))

	chain, err := adapter.GetOptionChain(context.Background(), "AAPL", asOf.AddDate(0, 0, 7), domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	assert.NotEmpty(t, chain, "with live disabled, behavior must match the synth-only path")

	stats := adapter.StatsWithLive()
	assert.Equal(t, uint64(0), stats.LiveHits)
	assert.Equal(t, uint64(0), stats.LiveErrors)
	assert.Equal(t, uint64(1), stats.SynthHits)
}
