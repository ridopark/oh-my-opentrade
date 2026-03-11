package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain/screener"
)

type Snapshot struct {
	Symbol          string
	PrevClose       *float64
	PreMarketPrice  *float64
	PreMarketVolume *int64
	LastTradePrice  *float64
	DailyVolume     *int64
	PrevDailyVolume *int64
}

type SnapshotPort interface {
	GetSnapshots(ctx context.Context, symbols []string, asOf time.Time) (map[string]Snapshot, error)
}

type ScreenerRepoPort interface {
	SaveResults(ctx context.Context, results []screener.ScreenerResult) error
}

type AIScreenerRepoPort interface {
	SaveAIResults(ctx context.Context, results []screener.AIScreenerResult) error
	GetLatestAIResults(ctx context.Context, tenantID, envMode, strategyKey string) ([]screener.AIScreenerResult, error)
}

type NewsScorerPort interface {
	Score(ctx context.Context, symbols []string, asOf time.Time) (map[string]float64, error)
}
