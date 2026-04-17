package datarefresh

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- mocks ---

type mockMarketData struct {
	mu   sync.Mutex
	bars map[domain.Symbol][]domain.MarketBar // keyed by symbol only for simplicity
	// record the last (from, to) window queried per symbol
	windows map[domain.Symbol][2]time.Time
}

func newMockMarketData() *mockMarketData {
	return &mockMarketData{
		bars:    map[domain.Symbol][]domain.MarketBar{},
		windows: map[domain.Symbol][2]time.Time{},
	}
}

func (m *mockMarketData) StreamBars(_ context.Context, _ []domain.Symbol, _ domain.Timeframe, _ ports.BarHandler) error {
	return nil
}
func (m *mockMarketData) GetHistoricalBars(_ context.Context, sym domain.Symbol, _ domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.windows[sym] = [2]time.Time{from, to}
	return m.bars[sym], nil
}
func (m *mockMarketData) Close() error { return nil }

type savedBars struct {
	mu   sync.Mutex
	bars []domain.MarketBar
}

func (s *savedBars) all() []domain.MarketBar {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.MarketBar, len(s.bars))
	copy(out, s.bars)
	return out
}

type mockBarStore struct {
	saved savedBars
}

func (r *mockBarStore) SaveMarketBars(_ context.Context, bars []domain.MarketBar) (int, error) {
	r.saved.mu.Lock()
	defer r.saved.mu.Unlock()
	r.saved.bars = append(r.saved.bars, bars...)
	return len(bars), nil
}
func (r *mockBarStore) GetMarketBars(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _, _ time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}
func (r *mockBarStore) UpdateBarIndicators(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _ time.Time, _, _, _, _ float64, _ map[string]float64) error {
	return nil
}

type mockTradeFetcher struct {
	mu      sync.Mutex
	calledOn []domain.Symbol
}

func (m *mockTradeFetcher) GetHistoricalTrades(_ context.Context, sym domain.Symbol, _, _ time.Time, _ func(alpaca.HistoricalTrade)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calledOn = append(m.calledOn, sym)
	return nil
}

type mockDPStore struct{}

func (m *mockDPStore) SaveDarkPoolBars(_ context.Context, _ []domain.DarkPoolBar) (int, error) {
	return 0, nil
}
func (m *mockDPStore) GetLatestDarkPoolBarTime(_ context.Context, _ domain.Symbol, _ domain.Timeframe) (*time.Time, error) {
	return nil, nil
}

type mockVIX struct{}

func (mockVIX) SetVIXLevel(float64) {}

// --- helpers ---

func makeMinuteBars(sym domain.Symbol, start time.Time, n int) []domain.MarketBar {
	out := make([]domain.MarketBar, 0, n)
	for i := 0; i < n; i++ {
		p := 100.0 + float64(i)
		b, _ := domain.NewMarketBar(
			start.Add(time.Duration(i)*time.Minute),
			sym,
			domain.Timeframe("1m"),
			p, p+0.5, p-0.5, p+0.2, 10,
		)
		out = append(out, b)
	}
	return out
}

func newTestService(md ports.MarketDataPort, repo BarStore) *Service {
	s := NewService(Config{
		VIXSymbol:      "VIX",
		IndexSymbols:   []string{"SPY"},
		TradingSymbols: []string{"AAPL", "BTC/USD"},
	}, md, repo, mockVIX{}, zerolog.Nop())
	return s
}

// --- tests ---

func TestBackfillIntradayBars_CryptoUsesClockAlignedAggregator(t *testing.T) {
	md := newMockMarketData()
	repo := &mockBarStore{}

	// Timestamps are computed relative to time.Now() so the test stays
	// deterministic across wall-clock days. AggregateHTF's equity path picks
	// its anchor from now; anchoring the bars to the same session keeps
	// aggregation well-defined on weekends, overnights, and any weekday.
	now := time.Now()

	// BTC/USD bars inside the crypto 24h window — clock-aligned aggregator
	// would reject nothing because it isn't session-anchored.
	btcStart := now.Add(-12 * time.Hour).UTC().Truncate(time.Minute)
	md.bars[domain.Symbol("BTC/USD")] = makeMinuteBars("BTC/USD", btcStart, 60)

	// AAPL bars anchored at whichever RTH session AggregateHTF will pick.
	loc, _ := time.LoadLocation("America/New_York")
	nowET := now.In(loc)
	aaplAnchor := time.Date(nowET.Year(), nowET.Month(), nowET.Day(), 9, 30, 0, 0, loc)
	if nowET.Before(aaplAnchor) {
		prev, _ := domain.PreviousRTHSession(now)
		aaplAnchor = prev
	}
	aaplStart := aaplAnchor.UTC()
	md.bars[domain.Symbol("AAPL")] = makeMinuteBars("AAPL", aaplStart, 60)

	s := newTestService(md, repo)
	s.backfillIntradayBars(context.Background(), []string{"AAPL", "BTC/USD"})

	// Crypto branch must use a 24h window (from ~ now-24h, NOT today-4am-ET).
	btcWin, ok := md.windows[domain.Symbol("BTC/USD")]
	require.True(t, ok)
	spread := btcWin[1].Sub(btcWin[0])
	assert.InDelta(t, 24*time.Hour, spread, float64(5*time.Minute), "crypto window should be ~24h")

	// Crypto HTF bars must have reached the repo (5m/15m/1h present).
	saw := map[string]map[domain.Timeframe]int{
		"BTC/USD": {},
		"AAPL":    {},
	}
	for _, b := range repo.saved.all() {
		if _, ok := saw[string(b.Symbol)]; ok {
			saw[string(b.Symbol)][b.Timeframe]++
		}
	}
	assert.Greater(t, saw["BTC/USD"]["5m"], 0, "crypto 5m HTF bars must reach repo")
	assert.Greater(t, saw["BTC/USD"]["15m"], 0, "crypto 15m HTF bars must reach repo")
	assert.Greater(t, saw["BTC/USD"]["1h"], 0, "crypto 1h HTF bars must reach repo")

	// Sanity: equity also produced HTF bars.
	assert.Greater(t, saw["AAPL"]["5m"], 0)
}

func TestRefreshDarkPoolBars_SkipsCrypto(t *testing.T) {
	md := newMockMarketData()
	repo := &mockBarStore{}
	tf := &mockTradeFetcher{}
	dp := &mockDPStore{}

	s := newTestService(md, repo)
	s.SetDarkPool(tf, dp)

	s.refreshDarkPoolBars(context.Background(), []string{"AAPL", "BTC/USD", "ETH/USD", "MSFT"})

	tf.mu.Lock()
	defer tf.mu.Unlock()
	for _, sym := range tf.calledOn {
		assert.False(t, sym.IsCryptoSymbol(), "dark pool fetch must not be called for crypto: got %s", sym)
	}
	// Confirm equity symbols were still attempted.
	seenAAPL := false
	for _, sym := range tf.calledOn {
		if sym == domain.Symbol("AAPL") {
			seenAAPL = true
		}
	}
	assert.True(t, seenAAPL, "expected AAPL to be fetched")
}
