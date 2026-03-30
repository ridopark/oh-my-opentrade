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

// collectingWriter captures bars enqueued to the writer for assertions.
type collectingWriter struct {
	mu   sync.Mutex
	bars []domain.MarketBar
}

func (c *collectingWriter) SaveMarketBars(_ context.Context, bars []domain.MarketBar) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bars = append(c.bars, bars...)
	return len(bars), nil
}

func (c *collectingWriter) getBars() []domain.MarketBar {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]domain.MarketBar, len(c.bars))
	copy(cp, c.bars)
	return cp
}

func newTestPipeline(saver *collectingWriter) (*Pipeline, *ingestion.AsyncBarWriter) {
	filter := ingestion.NewAdaptiveFilter(20, 3.0)
	filter.SetPassthrough(true)

	writer := ingestion.NewAsyncBarWriter(saver, zerolog.Nop(),
		ingestion.WithBatchSize(100),
		ingestion.WithFlushInterval(50*time.Millisecond),
	)
	writer.Start()

	sessionOpen := time.Date(2026, 3, 28, 13, 30, 0, 0, time.UTC) // 09:30 ET
	p := NewPipeline(filter, writer,
		[]domain.Symbol{"AAPL"},
		nil,
		sessionOpen,
		zerolog.Nop(),
	)
	return p, writer
}

func makeBar(sym string, t time.Time, close, volume float64) domain.MarketBar {
	bar, _ := domain.NewMarketBar(t, domain.Symbol(sym), "1m", close-0.1, close+0.1, close-0.2, close, volume)
	return bar
}

func TestPipeline_HandleBar_Persists1mBar(t *testing.T) {
	saver := &collectingWriter{}
	p, writer := newTestPipeline(saver)
	defer writer.Close()

	bar := makeBar("AAPL", time.Date(2026, 3, 28, 14, 0, 0, 0, time.UTC), 150.0, 1000)
	err := p.HandleBar(context.Background(), bar)
	require.NoError(t, err)

	// Wait for async flush
	time.Sleep(200 * time.Millisecond)

	bars := saver.getBars()
	require.NotEmpty(t, bars)

	// First bar should be the 1m bar
	found := false
	for _, b := range bars {
		if b.Timeframe == "1m" && b.Symbol == "AAPL" {
			found = true
			assert.InDelta(t, 150.0, b.Close, 0.01)
		}
	}
	assert.True(t, found, "expected 1m bar to be persisted")
}

func TestPipeline_HandleBar_ProducesHTFBars(t *testing.T) {
	saver := &collectingWriter{}
	p, writer := newTestPipeline(saver)
	defer writer.Close()

	// Feed 6 consecutive 1m bars (covering a full 5m bucket: 13:30-13:35 UTC = 09:30-09:35 ET)
	baseTime := time.Date(2026, 3, 28, 13, 30, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		bar := makeBar("AAPL", baseTime.Add(time.Duration(i)*time.Minute), 150.0+float64(i), 1000)
		err := p.HandleBar(context.Background(), bar)
		require.NoError(t, err)
	}

	time.Sleep(200 * time.Millisecond)

	bars := saver.getBars()

	// Should have 6 x 1m bars + at least one 5m bar
	var has1m, has5m int
	for _, b := range bars {
		switch b.Timeframe {
		case "1m":
			has1m++
		case "5m":
			has5m++
		}
	}
	assert.Equal(t, 6, has1m, "expected 6 1m bars")
	assert.GreaterOrEqual(t, has5m, 1, "expected at least one 5m bar from aggregation")
}

func TestPipeline_ResetSession(t *testing.T) {
	saver := &collectingWriter{}
	filter := ingestion.NewAdaptiveFilter(20, 3.0)
	filter.SetPassthrough(true)

	writer := ingestion.NewAsyncBarWriter(saver, zerolog.Nop(),
		ingestion.WithBatchSize(100),
		ingestion.WithFlushInterval(50*time.Millisecond),
	)
	writer.Start()
	defer writer.Close()

	sessionOpen := time.Date(2026, 3, 28, 13, 30, 0, 0, time.UTC)
	p := NewPipeline(filter, writer,
		[]domain.Symbol{"AAPL"},
		nil,
		sessionOpen,
		zerolog.Nop(),
	)

	newSession := time.Date(2026, 3, 31, 13, 30, 0, 0, time.UTC)
	p.ResetSession([]domain.Symbol{"AAPL"}, newSession)

	// After reset, feeding bars from the new session should work
	bar := makeBar("AAPL", newSession, 155.0, 2000)
	err := p.HandleBar(context.Background(), bar)
	require.NoError(t, err)
}
