package backfill

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

// RepoGapDetector adapts a *timescaledb.Repository to the GapDetector interface.
type RepoGapDetector struct {
	Repo *timescaledb.Repository
}

func (a *RepoGapDetector) FindDataGaps(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time, minGap time.Duration) ([]GapInfo, error) {
	tsGaps, err := a.Repo.FindDataGaps(ctx, symbol, timeframe, from, to, minGap)
	if err != nil {
		return nil, err
	}
	gaps := make([]GapInfo, len(tsGaps))
	for i, g := range tsGaps {
		gaps[i] = GapInfo{Start: g.Start, End: g.End, Duration: g.Duration}
	}
	return gaps, nil
}

func (a *RepoGapDetector) GetMarketBarRange(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) (first, last *time.Time, count int, err error) {
	return a.Repo.GetMarketBarRange(ctx, symbol, timeframe, from, to)
}
