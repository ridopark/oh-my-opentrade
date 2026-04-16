package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// UniverseWindow is a single tradable interval [FromDate, ToDate) for a
// symbol. ToDate == nil means the symbol is still tradable as of the
// latest refresh. FromDate/ToDate are UTC-midnight DATE values (no
// intraday granularity) — listing changes are day-level in practice.
type UniverseWindow struct {
	Symbol   domain.Symbol
	FromDate time.Time
	ToDate   *time.Time
	Source   string
	Note     string
}

// UniverseHistoryPort records when each symbol was tradable so that
// backtests can filter out bars from periods when the symbol was
// delisted / not yet IPO'd — eliminating survivorship bias introduced
// by our always-current active-universe list.
type UniverseHistoryPort interface {
	// WasTradable returns true iff `at` falls inside any tradable
	// window for `sym`.
	WasTradable(ctx context.Context, sym domain.Symbol, at time.Time) (bool, error)
	// WindowsFor returns all tradable windows for `sym`, ordered
	// ascending by FromDate.
	WindowsFor(ctx context.Context, sym domain.Symbol) ([]UniverseWindow, error)
	// Upsert inserts or refreshes a window keyed by (symbol, from_date).
	Upsert(ctx context.Context, w UniverseWindow) error
	// ActiveSymbols returns the set of symbols tradable at `at`.
	ActiveSymbols(ctx context.Context, at time.Time) ([]domain.Symbol, error)
}
