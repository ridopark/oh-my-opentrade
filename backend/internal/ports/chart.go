package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// ChartOptions carries all annotation data for a candlestick chart render.
type ChartOptions struct {
	Levels      []domain.PriceLevel
	Markers     []domain.TimeMarker
	PnL         *ChartPnL
	WindowStart time.Time // if non-zero, fetch bars starting from this time (minus padding)
	WindowEnd   time.Time // if non-zero, fetch bars up to this time (plus padding)
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
