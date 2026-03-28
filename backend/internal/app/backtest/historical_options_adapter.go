// Package backtest — historical_options_adapter.go adapts HistoricalOptionsPort
// (DoltHub-backed historical data) to OptionsMarketDataPort so that the
// RiskSizer can select option contracts during backtests.
package backtest

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// HistoricalOptionsAdapter wraps a HistoricalOptionsPort to satisfy
// OptionsMarketDataPort for backtesting. It uses a clock function to
// determine the "current" backtest date for historical lookups.
type HistoricalOptionsAdapter struct {
	repo    ports.HistoricalOptionsPort
	clockFn func() time.Time
}

// NewHistoricalOptionsAdapter creates an adapter that bridges historical
// option data into the live OptionsMarketDataPort interface.
func NewHistoricalOptionsAdapter(repo ports.HistoricalOptionsPort, clockFn func() time.Time) *HistoricalOptionsAdapter {
	return &HistoricalOptionsAdapter{repo: repo, clockFn: clockFn}
}

// GetOptionChain implements ports.OptionsMarketDataPort by querying the
// historical options repo for the backtest's current simulated date.
func (a *HistoricalOptionsAdapter) GetOptionChain(
	ctx context.Context,
	underlying domain.Symbol,
	expiry time.Time,
	right domain.OptionRight,
) ([]domain.OptionContractSnapshot, error) {
	now := a.clockFn()

	// Fetch ALL available expirations and let the contract selector filter.
	// DoltHub data has sparse expirations (monthly only, ~3 per day), so
	// narrow DTE ranges miss most contracts. The selector's DTE/delta/spread
	// filters handle the actual selection.
	_ = expiry // target expiry is informational; we fetch everything
	minDTE := 1
	maxDTE := 365

	rows, err := a.repo.GetHistoricalChain(ctx, underlying, now, right, minDTE, maxDTE)
	if err != nil {
		return nil, err
	}

	snapshots := make([]domain.OptionContractSnapshot, 0, len(rows))
	for _, r := range rows {
		contract, cErr := domain.NewOptionContract(
			string(r.Symbol),
			r.Expiration,
			r.Strike,
			r.Right,
			domain.OptionStyleAmerican,
		)
		if cErr != nil {
			continue
		}

		snapshots = append(snapshots, domain.OptionContractSnapshot{
			OptionContract: contract,
			OptionQuote: domain.OptionQuote{
				Bid:       r.Bid,
				Ask:       r.Ask,
				Last:      r.Mid(),
				Timestamp: r.Date,
			},
			Greeks: domain.Greeks{
				Delta: r.Delta,
				Gamma: r.Gamma,
				Theta: r.Theta,
				Vega:  r.Vega,
				Rho:   r.Rho,
				IV:    r.IV,
			},
			OpenInterest: estimateOpenInterest(r),
		})
	}

	return snapshots, nil
}

// estimateOpenInterest returns a reasonable OI estimate from historical data.
// DoltHub data may not include OI, so we default to a value that passes
// the min_open_interest filter (typically 100) for liquid contracts.
func estimateOpenInterest(r domain.HistoricalOptionChainRow) int {
	// If bid and ask are both present with reasonable spread, assume liquid.
	if r.Bid > 0 && r.Ask > 0 && r.Ask > r.Bid {
		spread := (r.Ask - r.Bid) / r.Ask
		if spread < 0.20 {
			return 500 // liquid
		}
		return 50 // illiquid
	}
	return 10 // very illiquid / no quotes
}
