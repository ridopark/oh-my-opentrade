package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// RepositoryPort defines the interface for data persistence operations.
type RepositoryPort interface {
	SaveMarketBar(ctx context.Context, bar domain.MarketBar) error
	GetMarketBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
	SaveTrade(ctx context.Context, trade domain.Trade) error
	GetTrades(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.Trade, error)
	SaveStrategyDNA(ctx context.Context, dna domain.StrategyDNA) error
	GetLatestStrategyDNA(ctx context.Context, tenantID string, envMode domain.EnvMode) (*domain.StrategyDNA, error)
}
