package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// OptionsMarketDataPort defines the interface for fetching options market data.
type OptionsMarketDataPort interface {
	// GetOptionChain returns all option contract snapshots for the given
	// underlying, expiry (the target DTE midpoint), option right, and the
	// strategy's full DTE window. The target expiry is retained for back-
	// compat with live adapters whose REST APIs take a single date; the
	// DTE range is additionally supplied so backtest adapters with
	// synthetic-chain support can generate multiple expiries at once.
	// Live adapters are free to ignore minDTE/maxDTE.
	GetOptionChain(ctx context.Context, underlying domain.Symbol, expiry time.Time, right domain.OptionRight, minDTE, maxDTE int) ([]domain.OptionContractSnapshot, error)
}
