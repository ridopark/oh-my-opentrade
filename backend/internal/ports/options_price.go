package ports

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// OptionsPricePort fetches live bid/ask/last quotes for a batch of option contract symbols.
// Used by the position monitor to keep option position prices fresh so that
// price-dependent exit rules (MAX_LOSS, PROFIT_TARGET) can fire intraday.
type OptionsPricePort interface {
	GetOptionPrices(ctx context.Context, symbols []domain.Symbol) (map[domain.Symbol]domain.OptionQuote, error)
}
