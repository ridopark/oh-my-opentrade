package ingest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubFetcher returns pre-configured bars for GetHistoricalBars.
type stubFetcher struct {
	bars map[domain.Symbol][]domain.MarketBar
}

func (f *stubFetcher) GetHistoricalBars(_ context.Context, sym domain.Symbol, _ domain.Timeframe, _, _ time.Time) ([]domain.MarketBar, error) {
	return f.bars[sym], nil
}

// stubSaver tracks saved bars and latest bar times.
type stubSaver struct {
	mu       sync.Mutex
	saved    []domain.MarketBar
	latestAt map[domain.Symbol]*time.Time
}

func newStubSaver() *stubSaver {
	return &stubSaver{latestAt: make(map[domain.Symbol]*time.Time)}
}

func (s *stubSaver) SaveMarketBars(_ context.Context, bars []domain.MarketBar) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, bars...)
	return len(bars), nil
}

func (s *stubSaver) GetLatestMarketBarTime(_ context.Context, sym domain.Symbol, _ domain.Timeframe) (*time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latestAt[sym], nil
}

func (s *stubSaver) getSaved() []domain.MarketBar {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]domain.MarketBar, len(s.saved))
	copy(cp, s.saved)
	return cp
}

func TestGapFill_FetchesAndSavesBars(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	bars := []domain.MarketBar{
		{Time: now.Add(-3 * time.Minute), Symbol: "AAPL", Timeframe: "1m", Open: 150, High: 151, Low: 149, Close: 150.5, Volume: 100},
		{Time: now.Add(-2 * time.Minute), Symbol: "AAPL", Timeframe: "1m", Open: 150.5, High: 152, Low: 150, Close: 151, Volume: 200},
		{Time: now.Add(-1 * time.Minute), Symbol: "AAPL", Timeframe: "1m", Open: 151, High: 153, Low: 150, Close: 152, Volume: 300},
	}

	fetcher := &stubFetcher{bars: map[domain.Symbol][]domain.MarketBar{"AAPL": bars}}
	saver := newStubSaver()
	filter := ingestion.NewAdaptiveFilter(20, 3.0)

	err := GapFill(context.Background(), GapFillConfig{
		Symbols:     []domain.Symbol{"AAPL"},
		Timeframe:   "1m",
		MaxLookback: time.Hour,
		Concurrency: 1,
		BatchSize:   10,
	}, fetcher, saver, filter, zerolog.Nop())

	require.NoError(t, err)
	saved := saver.getSaved()
	assert.Len(t, saved, 3)
}

func TestGapFill_SkipsUpToDateSymbol(t *testing.T) {
	now := time.Now().UTC()
	fetcher := &stubFetcher{bars: make(map[domain.Symbol][]domain.MarketBar)}
	saver := newStubSaver()
	saver.latestAt["AAPL"] = &now // Already up to date
	filter := ingestion.NewAdaptiveFilter(20, 3.0)

	err := GapFill(context.Background(), GapFillConfig{
		Symbols:     []domain.Symbol{"AAPL"},
		Timeframe:   "1m",
		MaxLookback: time.Hour,
		Concurrency: 1,
		BatchSize:   10,
	}, fetcher, saver, filter, zerolog.Nop())

	require.NoError(t, err)
	assert.Empty(t, saver.getSaved())
}

func TestGapFill_SeedsFilter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	bars := make([]domain.MarketBar, 25)
	for i := range bars {
		bars[i] = domain.MarketBar{
			Time:      now.Add(-time.Duration(25-i) * time.Minute),
			Symbol:    "MSFT",
			Timeframe: "1m",
			Open:      300 + float64(i),
			High:      301 + float64(i),
			Low:       299 + float64(i),
			Close:     300.5 + float64(i),
			Volume:    float64(1000 + i*10),
		}
	}

	fetcher := &stubFetcher{bars: map[domain.Symbol][]domain.MarketBar{"MSFT": bars}}
	saver := newStubSaver()
	filter := ingestion.NewAdaptiveFilter(20, 3.0)

	err := GapFill(context.Background(), GapFillConfig{
		Symbols:     []domain.Symbol{"MSFT"},
		Timeframe:   "1m",
		MaxLookback: time.Hour,
		Concurrency: 1,
		BatchSize:   500,
	}, fetcher, saver, filter, zerolog.Nop())

	require.NoError(t, err)

	// Filter should now process bars without rejection (it's been seeded)
	testBar := domain.MarketBar{
		Time: now, Symbol: "MSFT", Timeframe: "1m",
		Open: 325, High: 326, Low: 324, Close: 325.5, Volume: 1200,
	}
	result := filter.Process(testBar)
	assert.Equal(t, ingestion.FilterPass, result.Status)
}
