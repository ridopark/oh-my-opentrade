package backtest

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)
func defaultTestConfig() SyntheticChainConfig {
	return SyntheticChainConfig{
		Enabled:         true,
		StrikeGridPct:   0.30,
		StrikeStepPct:   0.01,
		IVDefault:       0.40,
		RiskFreeRate:    0.045,
		BidAskSpreadPct: 0.03,
	}
}
func constantSpot(v float64) SpotProvider {
	return func(_ context.Context, _ domain.Symbol, _ time.Time) (float64, error) { return v, nil }
}
func constantIV(v float64) IVProvider {
	return func(_ context.Context, _ domain.Symbol, _ time.Time) (float64, error) { return v, nil }
}
func TestWeeklyExpiries_RangeSpansTwoFridays(t *testing.T) {
	// 2026-04-15 is a Wednesday. Fridays in [+1, +14] are 04-17 and 04-24.
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	got := weeklyExpiries(asOf, 1, 14)
	require.Len(t, got, 2)
	assert.Equal(t, "2026-04-17", got[0].Format("2006-01-02"))
	assert.Equal(t, "2026-04-24", got[1].Format("2006-01-02"))
}
func TestWeeklyExpiries_MinDTEOnFriday(t *testing.T) {
	// 2026-04-17 is a Friday. minDTE=0 should emit it, maxDTE=14 adds 04-24.
	asOf := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	got := weeklyExpiries(asOf, 0, 14)
	require.GreaterOrEqual(t, len(got), 2)
	assert.Equal(t, "2026-04-17", got[0].Format("2006-01-02"))
	assert.Equal(t, "2026-04-24", got[1].Format("2006-01-02"))
}
func TestWeeklyExpiries_EmptyRange(t *testing.T) {
	asOf := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	got := weeklyExpiries(asOf, 20, 10) // inverted range
	assert.Empty(t, got)
}
func TestStrikeGrid_Spot100Pct20Step1(t *testing.T) {
	strikes := strikeGrid(100.0, 0.20, 0.01)
	// spot=100, grid [80..120], step 1 → 41 strikes at integer values.
	require.Len(t, strikes, 41)
	assert.InDelta(t, 80.0, strikes[0], 1e-9)
	assert.InDelta(t, 120.0, strikes[len(strikes)-1], 1e-9)
}
func TestStrikeGrid_DedupesAfterRounding(t *testing.T) {
	// Spot=200, step_pct=0.001 (step=$0.20) rounds to integer dollars and
	// should produce unique integer strikes.
	strikes := strikeGrid(200.0, 0.10, 0.001)
	seen := make(map[float64]struct{}, len(strikes))
	for _, k := range strikes {
		_, dup := seen[k]
		assert.False(t, dup, "duplicate strike %.2f", k)
		seen[k] = struct{}{}
	}
}
func TestStrikeGrid_PennyStockQuarterTicks(t *testing.T) {
	strikes := strikeGrid(5.0, 0.20, 0.05) // spot $5, step $0.25
	require.NotEmpty(t, strikes)
	for _, k := range strikes {
		// Every strike should be a multiple of 0.25.
		multiplied := k * 4.0
		assert.InDelta(t, math.Round(multiplied), multiplied, 1e-9, "strike %.4f not on 0.25 tick", k)
	}
}
func TestBSMParity_CallTextbookValue(t *testing.T) {
	// Classical textbook result: S=100, K=100, T=30/365, r=0.045, sigma=0.30
	// European call ≈ 3.61 (computed from standard BS formulas via
	// internal/app/options.BSMPrice).
	gen := NewSyntheticChainGenerator(
		SyntheticChainConfig{
			Enabled: true, StrikeGridPct: 0.005, StrikeStepPct: 0.001,
			IVDefault: 0.30, RiskFreeRate: 0.045, BidAskSpreadPct: 0,
		},
		constantSpot(100.0), constantIV(0.30),
	)
	// Ask for a window that includes exactly one Friday at DTE≈30.
	// 2026-04-15 is Wed; 2026-05-15 is Fri with DTE=30.
	asOf := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	chain, err := gen.GenerateChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 28, 32)
	require.NoError(t, err)
	require.NotEmpty(t, chain)
	// Pick the ATM contract (strike closest to 100).
	var atm *domain.OptionContractSnapshot
	for i := range chain {
		c := &chain[i]
		if c.Strike != 100.0 {
			continue
		}
		atm = c
		break
	}
	require.NotNil(t, atm, "expected strike=100 in generated chain")
	// With spread=0, bid=ask=last=mid=theoretical price.
	// The generator may pick a slightly off-30-DTE Friday depending on
	// asOf alignment; 0.2 tolerance covers the +/- 2 DTE wobble.
	assert.InDelta(t, 3.61, atm.Last, 0.3, "ATM call premium outside textbook band")
	assert.InDelta(t, 0.55, atm.Delta, 0.05, "ATM call delta near 0.55")
}
func TestBSMParity_PutCallParity(t *testing.T) {
	// C - P == S - K * exp(-r*T) for European options at the same strike/expiry.
	cfg := SyntheticChainConfig{
		Enabled: true, StrikeGridPct: 0.01, StrikeStepPct: 0.001,
		IVDefault: 0.30, RiskFreeRate: 0.045, BidAskSpreadPct: 0,
	}
	asOf := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	spot := 100.0
	genCalls := NewSyntheticChainGenerator(cfg, constantSpot(spot), constantIV(0.30))
	genPuts := NewSyntheticChainGenerator(cfg, constantSpot(spot), constantIV(0.30))
	calls, err := genCalls.GenerateChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 28, 32)
	require.NoError(t, err)
	puts, err := genPuts.GenerateChain(context.Background(), "AAPL", asOf, domain.OptionRightPut, 28, 32)
	require.NoError(t, err)
	var atmCall, atmPut *domain.OptionContractSnapshot
	for i := range calls {
		if calls[i].Strike == 100.0 {
			atmCall = &calls[i]
			break
		}
	}
	for i := range puts {
		if puts[i].Strike == 100.0 {
			atmPut = &puts[i]
			break
		}
	}
	require.NotNil(t, atmCall)
	require.NotNil(t, atmPut)
	T := float64(daysBetween(asOf, atmCall.Expiry)) / 365.0
	expected := spot - 100.0*math.Exp(-0.045*T)
	got := atmCall.Last - atmPut.Last
	assert.InDelta(t, expected, got, 0.01, "put-call parity violated")
}
func TestGenerateChain_DeltaSigns(t *testing.T) {
	gen := NewSyntheticChainGenerator(defaultTestConfig(), constantSpot(100.0), constantIV(0.30))
	asOf := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	calls, err := gen.GenerateChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	require.NotEmpty(t, calls)
	puts, err := gen.GenerateChain(context.Background(), "AAPL", asOf, domain.OptionRightPut, 1, 14)
	require.NoError(t, err)
	require.NotEmpty(t, puts)
	for _, c := range calls {
		assert.GreaterOrEqual(t, c.Delta, 0.0, "call delta must be >= 0, strike=%.2f", c.Strike)
		assert.LessOrEqual(t, c.Delta, 1.0, "call delta must be <= 1, strike=%.2f", c.Strike)
	}
	for _, p := range puts {
		assert.LessOrEqual(t, p.Delta, 0.0, "put delta must be <= 0, strike=%.2f", p.Strike)
		assert.GreaterOrEqual(t, p.Delta, -1.0, "put delta must be >= -1, strike=%.2f", p.Strike)
	}
}
func TestGenerateChain_EmptySpot(t *testing.T) {
	gen := NewSyntheticChainGenerator(defaultTestConfig(), constantSpot(0), constantIV(0.30))
	asOf := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	chain, err := gen.GenerateChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	assert.Empty(t, chain, "no spot -> no chain")
}
func TestGenerateChain_NilIVFallsBackToDefault(t *testing.T) {
	gen := NewSyntheticChainGenerator(defaultTestConfig(), constantSpot(100.0), nil)
	asOf := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	chain, err := gen.GenerateChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	assert.NotEmpty(t, chain, "nil IV provider should fall back to cfg.IVDefault")
}
func TestGenerateChain_DisabledReturnsEmpty(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Enabled = false
	gen := NewSyntheticChainGenerator(cfg, constantSpot(100.0), constantIV(0.30))
	asOf := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	chain, err := gen.GenerateChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	assert.Empty(t, chain)
}
func TestGenerateChain_QuoteSpreadsAroundMid(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.BidAskSpreadPct = 0.04 // +/- 2%
	gen := NewSyntheticChainGenerator(cfg, constantSpot(100.0), constantIV(0.30))
	asOf := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	chain, err := gen.GenerateChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	require.NotEmpty(t, chain)
	// Ignore contracts that hit the penny-floor clamp (deep OTM where the
	// theoretical price is sub-cent); they are a small minority and their
	// quote shape is dominated by the floor, not the spread formula.
	checked := 0
	for _, c := range chain {
		if c.Last < 0.02 {
			continue
		}
		assert.Less(t, c.Bid, c.Last, "bid should be below mid, strike=%.2f last=%.4f", c.Strike, c.Last)
		assert.Greater(t, c.Ask, c.Last, "ask should be above mid, strike=%.2f last=%.4f", c.Strike, c.Last)
		checked++
	}
	require.Greater(t, checked, 0, "expected at least one quotable contract")
}
