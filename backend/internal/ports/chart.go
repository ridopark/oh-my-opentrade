package ports

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// ChartGeneratorPort generates chart images from market data.
type ChartGeneratorPort interface {
	// GenerateCandlestickChart renders OHLCV bars as a candlestick chart PNG.
	// Returns the PNG bytes. title is used as the chart heading (e.g. "AAPL — 5min").
	GenerateCandlestickChart(ctx context.Context, bars []domain.MarketBar, title string) ([]byte, error)
}
