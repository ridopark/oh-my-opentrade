package execution

import (
	"errors"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// OptionsRiskEngine validates option-specific risk constraints.
// It is separate from the existing RiskEngine, which handles equities.
type OptionsRiskEngine struct {
	maxRiskPct      float64
	minOpenInterest int
	maxSpreadPct    float64
	maxIVCeiling    float64
	minDTE          int
	now             func() time.Time
}

// NewOptionsRiskEngine creates an OptionsRiskEngine.
//
//   - maxRiskPct:      max fraction of account equity (e.g. 0.02 = 2%)
//   - minOpenInterest: minimum open interest for liquidity check
//   - maxSpreadPct:    max (ask-bid)/ask for liquidity check
//   - maxIVCeiling:    maximum implied volatility ceiling (e.g. 1.0 = 100%)
//   - minDTE:          minimum days to expiry for expiry check
//   - now:             clock injection for testability
func NewOptionsRiskEngine(
	maxRiskPct float64,
	minOpenInterest int,
	maxSpreadPct float64,
	maxIVCeiling float64,
	minDTE int,
	now func() time.Time,
) *OptionsRiskEngine {
	return &OptionsRiskEngine{
		maxRiskPct:      maxRiskPct,
		minOpenInterest: minOpenInterest,
		maxSpreadPct:    maxSpreadPct,
		maxIVCeiling:    maxIVCeiling,
		minDTE:          minDTE,
		now:             now,
	}
}

// ValidateOptionIntent validates an option order intent against risk constraints.
// Checks: instrument type, MaxLossUSD > 0, Quantity > 0, and MaxLossUSD <= maxRiskPct * equity.
func (e *OptionsRiskEngine) ValidateOptionIntent(intent domain.OrderIntent, accountEquity float64) error {
	if intent.Instrument == nil {
		return errors.New("instrument is required for option risk check")
	}
	if intent.Instrument.Type != domain.InstrumentTypeOption {
		return fmt.Errorf("instrument must be of type OPTION, got %s", intent.Instrument.Type)
	}
	if intent.MaxLossUSD <= 0 {
		return errors.New("MaxLossUSD must be > 0 for option orders")
	}
	if intent.Quantity <= 0 {
		return fmt.Errorf("Quantity must be > 0, got %g", intent.Quantity)
	}

	maxAllowed := e.maxRiskPct * accountEquity
	if intent.MaxLossUSD > maxAllowed {
		return fmt.Errorf(
			"option max loss %.2f exceeds maximum allowed %.2f (%.1f%% of %.2f equity)",
			intent.MaxLossUSD, maxAllowed, e.maxRiskPct*100, accountEquity,
		)
	}
	return nil
}

// ValidateOptionLiquidity checks that an option contract snapshot meets
// minimum liquidity requirements (open interest and bid-ask spread).
func (e *OptionsRiskEngine) ValidateOptionLiquidity(snap domain.OptionContractSnapshot) error {
	if snap.OpenInterest < e.minOpenInterest {
		return fmt.Errorf(
			"open interest %d is below minimum %d",
			snap.OpenInterest, e.minOpenInterest,
		)
	}
	if snap.OptionQuote.Ask > 0 {
		spreadPct := (snap.OptionQuote.Ask - snap.OptionQuote.Bid) / snap.OptionQuote.Ask
		if spreadPct > e.maxSpreadPct {
			return fmt.Errorf(
				"bid-ask spread %.2f%% exceeds maximum %.2f%%",
				spreadPct*100, e.maxSpreadPct*100,
			)
		}
	}
	return nil
}

// ValidateOptionVolatility checks that the contract's implied volatility
// is below the engine's ceiling.
func (e *OptionsRiskEngine) ValidateOptionVolatility(snap domain.OptionContractSnapshot) error {
	if snap.Greeks.IV > e.maxIVCeiling {
		return fmt.Errorf(
			"implied volatility %.2f exceeds ceiling %.2f",
			snap.Greeks.IV, e.maxIVCeiling,
		)
	}
	return nil
}

// ValidateOptionExpiry checks that the contract has at least minDTE days remaining.
func (e *OptionsRiskEngine) ValidateOptionExpiry(contract domain.OptionContract, minDTE int) error {
	now := e.now()
	dte := int(contract.Expiry.Sub(now).Hours() / 24)
	if dte < minDTE {
		return fmt.Errorf(
			"contract expires in %d days, minimum required is %d",
			dte, minDTE,
		)
	}
	return nil
}
