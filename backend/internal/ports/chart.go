package ports

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// ChartOptions carries all annotation data for a candlestick chart render.
type ChartOptions struct {
	Levels  []domain.PriceLevel
	Markers []domain.TimeMarker
	PnL     *ChartPnL
}

// ChartPnL holds realized trade outcome data rendered in the chart title.
type ChartPnL struct {
	PnLPct       float64
	PnLUSD       float64
	HoldDuration string
}

type ChartGeneratorPort interface {
	GenerateCandlestickChart(ctx context.Context, bars []domain.MarketBar, title string, opts ChartOptions) ([]byte, error)
}
