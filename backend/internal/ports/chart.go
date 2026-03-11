package ports

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

type ChartGeneratorPort interface {
	GenerateCandlestickChart(ctx context.Context, bars []domain.MarketBar, title string, levels []domain.PriceLevel) ([]byte, error)
}
