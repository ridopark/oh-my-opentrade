package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// IVHistoryPort defines the interface for storing and querying IV snapshots.
type IVHistoryPort interface {
	SaveIVSnapshot(ctx context.Context, snap domain.IVSnapshot) error
	GetIVStats(ctx context.Context, symbol domain.Symbol, asOf time.Time, lookbackDays int) (domain.IVStats, error)
	GetLatestIV(ctx context.Context, symbol domain.Symbol) (domain.IVSnapshot, error)
}
