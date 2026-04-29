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

// TestSelectBestContract_CalendarDTE_MidDayNow locks in the fix for the
// wall-clock DTE rounding bug: a contract expiring exactly 5 calendar days
// from today must pass a MinDTE=5 filter even when `now` has an afternoon
// wall-clock time. The previous implementation computed
// int(expiry.Sub(now).Hours()/24) = int(4.4) = 4 and rejected it.
func TestSelectBestContract_CalendarDTE_MidDayNow(t *testing.T) {
	et, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	now := time.Date(2026, 4, 22, 13, 25, 0, 0, et)
	expiry := time.Date(2026, 4, 27, 0, 0, 0, 0, et) // 5 calendar days out
	occ := domain.FormatOCCSymbol("QQQ", expiry, domain.OptionRightCall, 500.0)

	snap := domain.OptionContractSnapshot{
		OptionContract: domain.OptionContract{
			ContractSymbol: domain.Symbol(occ),
			Underlying:     "QQQ",
			Expiry:         expiry,
			Strike:         500.0,
			Right:          domain.OptionRightCall,
			Style:          domain.OptionStyleAmerican,
			Multiplier:     100,
		},
		OptionQuote:  domain.OptionQuote{Bid: 2.90, Ask: 3.00, Last: 2.95},
		Greeks:       domain.Greeks{Delta: 0.48, IV: 0.30},
		OpenInterest: 200,
	}
	cfg := options.ContractSelectionConstraints{
		MinDTE: 5, MaxDTE: 14,
		TargetDeltaLow: 0.40, TargetDeltaHigh: 0.55,
		MinOpenInterest: 100, MaxSpreadPct: 0.10, MaxIV: 1.0,
	}
	svc := options.NewContractSelectionService(cfg, func() time.Time { return now })

	best, err := svc.SelectBestContract(domain.DirectionLong, domain.RegimeTrend, []domain.OptionContractSnapshot{snap})
	require.NoError(t, err)
	assert.Equal(t, domain.Symbol(occ), best.OptionContract.ContractSymbol)
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
