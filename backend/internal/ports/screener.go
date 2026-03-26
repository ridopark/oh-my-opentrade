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
	// Quantitative signals (enriched after Pass0)
	DailyATRPct *float64 // ATR(14) / close * 100
	NR7         *bool    // narrowest range in 7 sessions
	EMA200Bias  *string  // "BULLISH", "BEARISH", "NEUTRAL"
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
