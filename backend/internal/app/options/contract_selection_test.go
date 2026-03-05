package options_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/options"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────

func defaultConstraints() options.ContractSelectionConstraints {
	return options.ContractSelectionConstraints{
		MinDTE:          35,
		MaxDTE:          45,
		TargetDeltaLow:  0.40,
		TargetDeltaHigh: 0.55,
		MinOpenInterest: 100,
		MaxSpreadPct:    0.10,
		MaxIV:           1.0,
	}
}

// makeSnapshot builds an OptionContractSnapshot with the given parameters.
// daysToExpiry relative to now (via the injected clock).
func makeSnapshot(
	underlying string,
	daysToExpiry int,
	strike float64,
	delta float64,
	bid, ask float64,
	iv float64,
	openInterest int,
	now time.Time,
) domain.OptionContractSnapshot {
	expiry := now.AddDate(0, 0, daysToExpiry)
	right := domain.OptionRightCall
	if delta < 0 {
		right = domain.OptionRightPut
	}
	occ := domain.FormatOCCSymbol(underlying, expiry, right, strike)
	contract := domain.OptionContract{
		ContractSymbol: domain.Symbol(occ),
		Underlying:     domain.Symbol(underlying),
		Expiry:         expiry,
		Strike:         strike,
		Right:          right,
		Style:          domain.OptionStyleAmerican,
		Multiplier:     100,
	}
	last := (bid + ask) / 2.0
	greeks := domain.Greeks{Delta: delta, IV: iv}
	return domain.OptionContractSnapshot{
		OptionContract: contract,
		OptionQuote:    domain.OptionQuote{Bid: bid, Ask: ask, Last: last},
		Greeks:         greeks,
		OpenInterest:   openInterest,
	}
}

// ─────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────

func TestSelectBestContract_PicksClosestToMidDelta(t *testing.T) {
	// now is fixed for determinism
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := options.NewContractSelectionService(defaultConstraints(), func() time.Time { return now })

	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 40, 190.0, 0.41, 3.0, 3.20, 0.30, 200, now), // delta closest to 0.475
		makeSnapshot("AAPL", 40, 195.0, 0.50, 2.5, 2.70, 0.30, 200, now), // delta = 0.50, also passes
		makeSnapshot("AAPL", 40, 200.0, 0.43, 2.0, 2.20, 0.30, 200, now), // delta 0.43
	}
	// midpoint = (0.40+0.55)/2 = 0.475; closest = 0.50
	best, err := svc.SelectBestContract(domain.DirectionLong, domain.RegimeTrend, chain)
	require.NoError(t, err)
	assert.InDelta(t, 0.50, best.Greeks.Delta, 1e-9)
}

func TestSelectBestContract_EmptyChain(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := options.NewContractSelectionService(defaultConstraints(), func() time.Time { return now })
	_, err := svc.SelectBestContract(domain.DirectionLong, domain.RegimeTrend, []domain.OptionContractSnapshot{})
	require.Error(t, err)
}

func TestSelectBestContract_RejectsDTETooLow(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := options.NewContractSelectionService(defaultConstraints(), func() time.Time { return now })

	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 20, 190.0, 0.48, 3.0, 3.20, 0.30, 200, now), // DTE=20 < MinDTE=35
	}
	_, err := svc.SelectBestContract(domain.DirectionLong, domain.RegimeTrend, chain)
	require.Error(t, err)
}

func TestSelectBestContract_RejectsDTETooHigh(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := options.NewContractSelectionService(defaultConstraints(), func() time.Time { return now })

	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 60, 190.0, 0.48, 3.0, 3.20, 0.30, 200, now), // DTE=60 > MaxDTE=45
	}
	_, err := svc.SelectBestContract(domain.DirectionLong, domain.RegimeTrend, chain)
	require.Error(t, err)
}

func TestSelectBestContract_RejectsDeltaOutOfRange(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := options.NewContractSelectionService(defaultConstraints(), func() time.Time { return now })

	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 40, 200.0, 0.30, 3.0, 3.20, 0.30, 200, now), // delta 0.30 < 0.40
		makeSnapshot("AAPL", 40, 180.0, 0.70, 3.0, 3.20, 0.30, 200, now), // delta 0.70 > 0.55
	}
	_, err := svc.SelectBestContract(domain.DirectionLong, domain.RegimeTrend, chain)
	require.Error(t, err)
}

func TestSelectBestContract_RejectsLowOpenInterest(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := options.NewContractSelectionService(defaultConstraints(), func() time.Time { return now })

	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 40, 190.0, 0.48, 3.0, 3.20, 0.30, 50, now), // OI=50 < 100
	}
	_, err := svc.SelectBestContract(domain.DirectionLong, domain.RegimeTrend, chain)
	require.Error(t, err)
}

func TestSelectBestContract_RejectsWideBidAskSpread(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := options.NewContractSelectionService(defaultConstraints(), func() time.Time { return now })

	// spread = (ask-bid)/ask = (5.0-3.0)/5.0 = 0.40 > 0.10
	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 40, 190.0, 0.48, 3.0, 5.0, 0.30, 200, now),
	}
	_, err := svc.SelectBestContract(domain.DirectionLong, domain.RegimeTrend, chain)
	require.Error(t, err)
}

func TestSelectBestContract_RejectsHighIV(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := options.NewContractSelectionService(defaultConstraints(), func() time.Time { return now })

	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 40, 190.0, 0.48, 3.0, 3.20, 1.50, 200, now), // IV=1.50 > 1.0
	}
	_, err := svc.SelectBestContract(domain.DirectionLong, domain.RegimeTrend, chain)
	require.Error(t, err)
}

func TestSelectBestContract_AcceptsShortDirection(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := options.NewContractSelectionService(defaultConstraints(), func() time.Time { return now })

	// SHORT direction should select from the chain (put deltas are negative)
	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 40, 190.0, -0.48, 3.0, 3.20, 0.30, 200, now),
	}
	best, err := svc.SelectBestContract(domain.DirectionShort, domain.RegimeTrend, chain)
	require.NoError(t, err)
	assert.InDelta(t, -0.48, best.Greeks.Delta, 1e-9)
}

func TestSelectBestContract_AcceptsBalanceRegime(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := options.NewContractSelectionService(defaultConstraints(), func() time.Time { return now })

	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 40, 190.0, 0.48, 3.0, 3.20, 0.30, 200, now),
	}
	best, err := svc.SelectBestContract(domain.DirectionLong, domain.RegimeBalance, chain)
	require.NoError(t, err)
	assert.InDelta(t, 0.48, best.Greeks.Delta, 1e-9)
}

func TestSelectBestContract_AcceptsReversalRegime(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := options.NewContractSelectionService(defaultConstraints(), func() time.Time { return now })

	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 40, 190.0, 0.48, 3.0, 3.20, 0.30, 200, now),
	}
	best, err := svc.SelectBestContract(domain.DirectionLong, domain.RegimeReversal, chain)
	require.NoError(t, err)
	assert.InDelta(t, 0.48, best.Greeks.Delta, 1e-9)
}

func TestSelectBestContract_AbsDeltaForPuts(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := options.NewContractSelectionService(defaultConstraints(), func() time.Time { return now })

	// Put with delta = -0.48 should pass (abs = 0.48, within [0.40, 0.55])
	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 40, 190.0, -0.48, 3.0, 3.20, 0.30, 200, now),
	}
	best, err := svc.SelectBestContract(domain.DirectionLong, domain.RegimeTrend, chain)
	require.NoError(t, err)
	assert.InDelta(t, -0.48, best.Greeks.Delta, 1e-9)
}

func TestSelectBestContract_ShortDirectionWithBalance(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := options.NewContractSelectionService(defaultConstraints(), func() time.Time { return now })

	// SHORT + BALANCE: should work with put contracts
	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 40, 190.0, -0.50, 3.0, 3.20, 0.30, 200, now),
		makeSnapshot("AAPL", 40, 185.0, -0.42, 2.5, 2.70, 0.30, 200, now),
	}
	best, err := svc.SelectBestContract(domain.DirectionShort, domain.RegimeBalance, chain)
	require.NoError(t, err)
	// midpoint = 0.475; |0.50 - 0.475| = 0.025, |0.42 - 0.475| = 0.055; picks -0.50
	assert.InDelta(t, -0.50, best.Greeks.Delta, 1e-9)
}

func TestSelectBestContract_ShortDirectionWithReversal(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := options.NewContractSelectionService(defaultConstraints(), func() time.Time { return now })

	// SHORT + REVERSAL: should work with put contracts
	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 40, 190.0, -0.45, 3.0, 3.20, 0.30, 200, now),
	}
	best, err := svc.SelectBestContract(domain.DirectionShort, domain.RegimeReversal, chain)
	require.NoError(t, err)
	assert.InDelta(t, -0.45, best.Greeks.Delta, 1e-9)
}

func TestSelectBestContract_RejectsInvalidDirection(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := options.NewContractSelectionService(defaultConstraints(), func() time.Time { return now })

	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 40, 190.0, 0.48, 3.0, 3.20, 0.30, 200, now),
	}
	_, err := svc.SelectBestContract(domain.Direction("INVALID"), domain.RegimeTrend, chain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported direction")
}
