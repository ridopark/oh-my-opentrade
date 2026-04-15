// Port for read-only market-data gap detection.
// Implementations compare expected bar timestamps against persisted bars.
package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// GapRange describes a contiguous run of missing bars for a (symbol, timeframe).
// Half-open interval [Start, End). ExpectedCount/ActualCount are scan-window
// totals (not per-range), letting callers compute coverage without a second pass.
type GapRange struct {
	Symbol        domain.Symbol
	Timeframe     domain.Timeframe
	Start, End    time.Time
	ExpectedCount int
	ActualCount   int
}

// GapDetector reports missing bars in [from, to) by diffing the canonical
// expected timestamp set against actual rows persisted in market_bars.
type GapDetector interface {
	FindMissingBars(ctx context.Context, sym domain.Symbol, tf domain.Timeframe, from, to time.Time) ([]GapRange, error)
}
