package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// HistoricalOptionsPort provides access to historical option chain data.
type HistoricalOptionsPort interface {
	// GetHistoricalChain returns option contracts for a symbol on a given date,
	// filtered by right (CALL/PUT) and expiration range (minDTE..maxDTE from date).
	GetHistoricalChain(ctx context.Context, symbol domain.Symbol, date time.Time,
		right domain.OptionRight, minDTE, maxDTE int) ([]domain.HistoricalOptionChainRow, error)

	// GetHistoricalContract returns the contract closest to the given strike/expiry/right
	// on a given date. Returns nil if no match found.
	GetHistoricalContract(ctx context.Context, symbol domain.Symbol, date time.Time,
		strike float64, expiry time.Time, right domain.OptionRight) (*domain.HistoricalOptionChainRow, error)

	// HasData reports whether historical option data exists for the symbol on the given date.
	HasData(ctx context.Context, symbol domain.Symbol, date time.Time) (bool, error)

	// SaveBatch inserts a batch of historical option chain rows (upsert).
	SaveBatch(ctx context.Context, rows []domain.HistoricalOptionChainRow) error
}
