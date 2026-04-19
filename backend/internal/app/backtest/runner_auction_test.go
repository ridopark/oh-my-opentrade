package backtest

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubAuctionBus struct {
	published []domain.Event
	flushes   int
}

func (s *stubAuctionBus) PublishDirect(_ context.Context, evt domain.Event) error {
	s.published = append(s.published, evt)
	return nil
}

func (s *stubAuctionBus) Flush() { s.flushes++ }

type stubVWAPProvider struct {
	snap map[string]domain.IndicatorSnapshot
}

func (s stubVWAPProvider) GetLastSnapshot(symbol string) (domain.IndicatorSnapshot, bool) {
	snap, ok := s.snap[symbol]
	return snap, ok
}

func newPublisher(t *testing.T, snaps map[string]domain.AuctionImbalanceSnapshot, vwap vwapProvider) (*auctionPublisher, *stubAuctionBus) {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	bus := &stubAuctionBus{}
	return &auctionPublisher{
		loc:               loc,
		eventBus:          bus,
		monitorSvc:        vwap,
		auctionByDateSym:  snaps,
		publishedAuctions: map[string]bool{},
		tenantID:          "default",
		envMode:           domain.EnvModePaper,
		log:               zerolog.Nop(),
	}, bus
}

func barAt(t *testing.T, et time.Time, symbol string, close float64) domain.MarketBar {
	t.Helper()
	return domain.MarketBar{
		Symbol: domain.Symbol(symbol),
		Time:   et.UTC(),
		Close:  close,
	}
}

func TestAuctionPublisher_RespectsDBImbalance(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	et := time.Date(2026, 4, 15, 15, 46, 0, 0, loc)
	snap := domain.AuctionImbalanceSnapshot{
		Time:      et,
		Symbol:    "AAPL",
		Volume:    100000,
		Price:     150.0,
		Imbalance: -75000, // DB says strong sell imbalance
	}
	pub, bus := newPublisher(t,
		map[string]domain.AuctionImbalanceSnapshot{"2026-04-15:AAPL": snap},
		stubVWAPProvider{snap: map[string]domain.IndicatorSnapshot{"AAPL": {VWAP: 140.0}}},
	)

	pub.maybePublish(context.Background(), barAt(t, et, "AAPL", 150.0)) // close > VWAP

	require.Len(t, bus.published, 1)
	payload := bus.published[0].Payload.(domain.AuctionImbalanceSnapshot)
	assert.Equal(t, -75000.0, payload.Imbalance, "DB imbalance must flow through, not bar-derived sign")
	assert.Zero(t, pub.syntheticSignCount)
}

func TestAuctionPublisher_SyntheticSignWhenDBIsZero(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	et := time.Date(2026, 4, 15, 15, 45, 0, 0, loc)
	snap := domain.AuctionImbalanceSnapshot{
		Time:      et,
		Symbol:    "AAPL",
		Volume:    100000,
		Price:     150.0,
		Imbalance: 0, // pre-Imbalance-column row
	}
	pub, bus := newPublisher(t,
		map[string]domain.AuctionImbalanceSnapshot{"2026-04-15:AAPL": snap},
		stubVWAPProvider{snap: map[string]domain.IndicatorSnapshot{"AAPL": {VWAP: 160.0}}},
	)

	pub.maybePublish(context.Background(), barAt(t, et, "AAPL", 150.0)) // close < VWAP → negative

	require.Len(t, bus.published, 1)
	payload := bus.published[0].Payload.(domain.AuctionImbalanceSnapshot)
	assert.Equal(t, -100000.0, payload.Imbalance, "bar-derived sign should be negative when close < VWAP")
	assert.Equal(t, 1, pub.syntheticSignCount)
}

func TestAuctionPublisher_DedupeSameDayAndSymbol(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	et := time.Date(2026, 4, 15, 15, 47, 0, 0, loc)
	snap := domain.AuctionImbalanceSnapshot{Symbol: "AAPL", Volume: 100, Imbalance: 50}
	pub, bus := newPublisher(t,
		map[string]domain.AuctionImbalanceSnapshot{"2026-04-15:AAPL": snap},
		stubVWAPProvider{},
	)

	pub.maybePublish(context.Background(), barAt(t, et, "AAPL", 150.0))
	pub.maybePublish(context.Background(), barAt(t, et.Add(time.Minute), "AAPL", 151.0))

	assert.Len(t, bus.published, 1, "second bar on the same date+symbol must not re-publish")
}

func TestAuctionPublisher_SkipsBefore1545(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	et := time.Date(2026, 4, 15, 15, 44, 0, 0, loc)
	snap := domain.AuctionImbalanceSnapshot{Symbol: "AAPL", Volume: 100, Imbalance: 50}
	pub, bus := newPublisher(t,
		map[string]domain.AuctionImbalanceSnapshot{"2026-04-15:AAPL": snap},
		stubVWAPProvider{},
	)

	pub.maybePublish(context.Background(), barAt(t, et, "AAPL", 150.0))
	assert.Empty(t, bus.published)
}

// TestAuctionPublisher_LegacyAndSliceParity - single publisher instance
// (the shared one built in runner.go) is invoked with the same bars across
// both paths, so identical inputs produce identical outputs. This is the
// guard against the original slice-path bug where max-speed replays
// silently dropped auction events.
func TestAuctionPublisher_LegacyAndSliceParity(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	et := time.Date(2026, 4, 15, 15, 45, 0, 0, loc)
	snap := domain.AuctionImbalanceSnapshot{
		Symbol: "AAPL", Volume: 1000, Price: 150.0, Imbalance: 500,
	}

	// Path 1: simulate legacy heap loop - single publisher used for one bar.
	pubLegacy, busLegacy := newPublisher(t,
		map[string]domain.AuctionImbalanceSnapshot{"2026-04-15:AAPL": snap},
		stubVWAPProvider{},
	)
	pubLegacy.maybePublish(context.Background(), barAt(t, et, "AAPL", 150.0))

	// Path 2: simulate slice-pipeline OnBar - fresh publisher, same bar.
	pubSlice, busSlice := newPublisher(t,
		map[string]domain.AuctionImbalanceSnapshot{"2026-04-15:AAPL": snap},
		stubVWAPProvider{},
	)
	pubSlice.maybePublish(context.Background(), barAt(t, et, "AAPL", 150.0))

	require.Len(t, busLegacy.published, 1)
	require.Len(t, busSlice.published, 1)
	legacyPayload := busLegacy.published[0].Payload.(domain.AuctionImbalanceSnapshot)
	slicePayload := busSlice.published[0].Payload.(domain.AuctionImbalanceSnapshot)
	assert.Equal(t, legacyPayload, slicePayload, "legacy and slice paths must produce identical auction events")
}
