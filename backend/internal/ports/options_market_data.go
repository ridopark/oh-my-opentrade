package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// OptionsMarketDataPort defines the interface for fetching options market data.
type OptionsMarketDataPort interface {
	// GetOptionChain returns all option contract snapshots for the given underlying,
	// expiry date, and option right (call or put).
	GetOptionChain(ctx context.Context, underlying domain.Symbol, expiry time.Time, right domain.OptionRight) ([]domain.OptionContractSnapshot, error)
}
