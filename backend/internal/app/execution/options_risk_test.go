package execution_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────

func makeOptionIntent(maxLossUSD float64, quantity float64, limitPrice float64, dir domain.Direction) domain.OrderIntent {
	inst, _ := domain.NewInstrument(domain.InstrumentTypeOption, "AAPL270119C00190000", "AAPL")
	id := uuid.New()
	intent, _ := domain.NewOptionOrderIntent(
		id, "tenant1", domain.EnvModePaper,
		inst, dir,
		limitPrice, quantity,
		"test", "test", 0.8, "key-"+id.String(),
		maxLossUSD,
	)
	return intent
}

func makeSnapshot(delta float64, bid, ask float64, iv float64, openInterest int, daysToExpiry int, now time.Time) domain.OptionContractSnapshot {
	expiry := now.AddDate(0, 0, daysToExpiry)
	right := domain.OptionRightCall
	if delta < 0 {
		right = domain.OptionRightPut
	}
	occ := domain.FormatOCCSymbol("AAPL", expiry, right, 190.0)
	contract := domain.OptionContract{
		ContractSymbol: domain.Symbol(occ),
		Underlying:     domain.Symbol("AAPL"),
		Expiry:         expiry,
		Strike:         190.0,
		Right:          right,
		Style:          domain.OptionStyleAmerican,
		Multiplier:     100,
	}
	return domain.OptionContractSnapshot{
		OptionContract: contract,
		OptionQuote:    domain.OptionQuote{Bid: bid, Ask: ask, Last: (bid + ask) / 2.0},
		Greeks:         domain.Greeks{Delta: delta, IV: iv},
		OpenInterest:   openInterest,
	}
}

// ─────────────────────────────────────────────
// ValidateOptionIntent
// ─────────────────────────────────────────────

func TestOptionsRiskEngine_HappyPath(t *testing.T) {
	eng := execution.NewOptionsRiskEngine(0.02, 100, 0.10, 1.0, 30, time.Now)
	intent := makeOptionIntent(200.0, 1.0, 3.20, domain.DirectionLong)
	err := eng.ValidateOptionIntent(intent, 10_000.0)
	require.NoError(t, err)
}

func TestOptionsRiskEngine_ExceedsMaxRisk(t *testing.T) {
	eng := execution.NewOptionsRiskEngine(0.02, 100, 0.10, 1.0, 30, time.Now)
	// maxAllowed = 0.02 * 10000 = 200, but MaxLossUSD = 300
	intent := makeOptionIntent(300.0, 1.0, 3.20, domain.DirectionLong)
	err := eng.ValidateOptionIntent(intent, 10_000.0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "300")
	assert.Contains(t, err.Error(), "200")
}

func TestOptionsRiskEngine_NilInstrument(t *testing.T) {
	eng := execution.NewOptionsRiskEngine(0.02, 100, 0.10, 1.0, 30, time.Now)
	intent := domain.OrderIntent{
		MaxLossUSD: 200.0,
		Quantity:   1.0,
		LimitPrice: 3.20,
		StopLoss:   3.20,
	}
	err := eng.ValidateOptionIntent(intent, 10_000.0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instrument is required")
}

func TestOptionsRiskEngine_EquityInstrument(t *testing.T) {
	eng := execution.NewOptionsRiskEngine(0.02, 100, 0.10, 1.0, 30, time.Now)
	inst, _ := domain.NewInstrument(domain.InstrumentTypeEquity, "AAPL", "")
	intent := domain.OrderIntent{
		Instrument: &inst,
		MaxLossUSD: 200.0,
		Quantity:   1.0,
		LimitPrice: 3.20,
		StopLoss:   3.20,
	}
	err := eng.ValidateOptionIntent(intent, 10_000.0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OPTION")
}

func TestOptionsRiskEngine_ZeroMaxLoss(t *testing.T) {
	eng := execution.NewOptionsRiskEngine(0.02, 100, 0.10, 1.0, 30, time.Now)
	intent := makeOptionIntent(0.0, 1.0, 3.20, domain.DirectionLong)
	// NewOptionOrderIntent would reject this, so craft manually
	inst, _ := domain.NewInstrument(domain.InstrumentTypeOption, "AAPL270119C00190000", "AAPL")
	badIntent := domain.OrderIntent{
		Instrument: &inst,
		MaxLossUSD: 0.0,
		Quantity:   1.0,
		LimitPrice: 3.20,
		StopLoss:   3.20,
	}
	_ = intent
	err := eng.ValidateOptionIntent(badIntent, 10_000.0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MaxLossUSD")
}

func TestOptionsRiskEngine_ZeroQuantity(t *testing.T) {
	eng := execution.NewOptionsRiskEngine(0.02, 100, 0.10, 1.0, 30, time.Now)
	inst, _ := domain.NewInstrument(domain.InstrumentTypeOption, "AAPL270119C00190000", "AAPL")
	intent := domain.OrderIntent{
		Instrument: &inst,
		MaxLossUSD: 200.0,
		Quantity:   0.0,
		LimitPrice: 3.20,
		StopLoss:   3.20,
	}
	err := eng.ValidateOptionIntent(intent, 10_000.0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quantity")
}

// ─────────────────────────────────────────────
// ValidateOptionLiquidity
// ─────────────────────────────────────────────

func TestOptionsRiskEngine_LiquidityOK(t *testing.T) {
	now := time.Now()
	eng := execution.NewOptionsRiskEngine(0.02, 100, 0.10, 1.0, 30, func() time.Time { return now })
	snap := makeSnapshot(0.48, 3.0, 3.20, 0.30, 200, 40, now)
	err := eng.ValidateOptionLiquidity(snap)
	require.NoError(t, err)
}

func TestOptionsRiskEngine_LiquidityLowOI(t *testing.T) {
	now := time.Now()
	eng := execution.NewOptionsRiskEngine(0.02, 100, 0.10, 1.0, 30, func() time.Time { return now })
	snap := makeSnapshot(0.48, 3.0, 3.20, 0.30, 50, 40, now) // OI=50 < 100
	err := eng.ValidateOptionLiquidity(snap)
	require.Error(t, err)
}

func TestOptionsRiskEngine_LiquidityWideSpread(t *testing.T) {
	now := time.Now()
	eng := execution.NewOptionsRiskEngine(0.02, 100, 0.10, 1.0, 30, func() time.Time { return now })
	snap := makeSnapshot(0.48, 3.0, 5.0, 0.30, 200, 40, now) // spread = 0.40 > 0.10
	err := eng.ValidateOptionLiquidity(snap)
	require.Error(t, err)
}

// ─────────────────────────────────────────────
// ValidateOptionExpiry
// ─────────────────────────────────────────────

func TestOptionsRiskEngine_ExpiryOK(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	eng := execution.NewOptionsRiskEngine(0.02, 100, 0.10, 1.0, 30, func() time.Time { return now })
	snap := makeSnapshot(0.48, 3.0, 3.20, 0.30, 200, 40, now)
	err := eng.ValidateOptionExpiry(snap.OptionContract, 30)
	require.NoError(t, err)
}

func TestOptionsRiskEngine_ExpiryTooSoon(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	eng := execution.NewOptionsRiskEngine(0.02, 100, 0.10, 1.0, 30, func() time.Time { return now })
	snap := makeSnapshot(0.48, 3.0, 3.20, 0.30, 200, 20, now) // DTE=20 < minDTE=30
	err := eng.ValidateOptionExpiry(snap.OptionContract, 30)
	require.Error(t, err)
}

// ─────────────────────────────────────────────
// ValidateOptionVolatility
// ─────────────────────────────────────────────

func TestOptionsRiskEngine_VolatilityOK(t *testing.T) {
	now := time.Now()
	eng := execution.NewOptionsRiskEngine(0.02, 100, 0.10, 1.0, 30, func() time.Time { return now })
	snap := makeSnapshot(0.48, 3.0, 3.20, 0.80, 200, 40, now) // IV=0.80 <= 1.0
	err := eng.ValidateOptionVolatility(snap)
	require.NoError(t, err)
}

func TestOptionsRiskEngine_VolatilityTooHigh(t *testing.T) {
	now := time.Now()
	eng := execution.NewOptionsRiskEngine(0.02, 100, 0.10, 1.0, 30, func() time.Time { return now })
	snap := makeSnapshot(0.48, 3.0, 3.20, 1.50, 200, 40, now) // IV=1.50 > 1.0
	err := eng.ValidateOptionVolatility(snap)
	require.Error(t, err)
}
