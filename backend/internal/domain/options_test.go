package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────
// InstrumentType
// ─────────────────────────────────────────────

func TestInstrumentType_Constants(t *testing.T) {
	assert.Equal(t, domain.InstrumentType("EQUITY"), domain.InstrumentTypeEquity)
	assert.Equal(t, domain.InstrumentType("OPTION"), domain.InstrumentTypeOption)
	assert.Equal(t, domain.InstrumentType("CRYPTO"), domain.InstrumentTypeCrypto)
}

// ─────────────────────────────────────────────
// OptionRight
// ─────────────────────────────────────────────

func TestOptionRight_Constants(t *testing.T) {
	assert.Equal(t, domain.OptionRight("CALL"), domain.OptionRightCall)
	assert.Equal(t, domain.OptionRight("PUT"), domain.OptionRightPut)
}

func TestNewOptionRight_Valid(t *testing.T) {
	r, err := domain.NewOptionRight("CALL")
	require.NoError(t, err)
	assert.Equal(t, domain.OptionRightCall, r)

	r, err = domain.NewOptionRight("PUT")
	require.NoError(t, err)
	assert.Equal(t, domain.OptionRightPut, r)
}

func TestNewOptionRight_Invalid(t *testing.T) {
	_, err := domain.NewOptionRight("INVALID")
	require.Error(t, err)
}

// ─────────────────────────────────────────────
// OptionStyle
// ─────────────────────────────────────────────

func TestOptionStyle_Constants(t *testing.T) {
	assert.Equal(t, domain.OptionStyle("AMERICAN"), domain.OptionStyleAmerican)
}

func TestNewOptionStyle_Valid(t *testing.T) {
	s, err := domain.NewOptionStyle("AMERICAN")
	require.NoError(t, err)
	assert.Equal(t, domain.OptionStyleAmerican, s)
}

func TestNewOptionStyle_Invalid(t *testing.T) {
	_, err := domain.NewOptionStyle("EUROPEAN")
	require.Error(t, err)
}

// ─────────────────────────────────────────────
// Instrument
// ─────────────────────────────────────────────

func TestNewInstrument_OptionValid(t *testing.T) {
	inst, err := domain.NewInstrument(domain.InstrumentTypeOption, "AAPL240119C00190000", "AAPL")
	require.NoError(t, err)
	assert.Equal(t, domain.InstrumentTypeOption, inst.Type)
	assert.Equal(t, domain.Symbol("AAPL240119C00190000"), inst.Symbol)
	assert.Equal(t, domain.Symbol("AAPL"), inst.UnderlyingSymbol)
}

func TestNewInstrument_EquityValid(t *testing.T) {
	inst, err := domain.NewInstrument(domain.InstrumentTypeEquity, "AAPL", "")
	require.NoError(t, err)
	assert.Equal(t, domain.InstrumentTypeEquity, inst.Type)
	assert.Equal(t, domain.Symbol("AAPL"), inst.Symbol)
	assert.Equal(t, domain.Symbol(""), inst.UnderlyingSymbol)
}

func TestNewInstrument_EmptyType(t *testing.T) {
	_, err := domain.NewInstrument("", "AAPL", "")
	require.Error(t, err)
}

func TestNewInstrument_EmptySymbol(t *testing.T) {
	_, err := domain.NewInstrument(domain.InstrumentTypeOption, "", "AAPL")
	require.Error(t, err)
}

func TestNewInstrument_CryptoValid(t *testing.T) {
	inst, err := domain.NewInstrument(domain.InstrumentTypeCrypto, "BTC/USD", "")
	require.NoError(t, err)
	assert.Equal(t, domain.InstrumentTypeCrypto, inst.Type)
	assert.Equal(t, domain.Symbol("BTC/USD"), inst.Symbol)
	assert.Equal(t, domain.Symbol(""), inst.UnderlyingSymbol)
}

// ─────────────────────────────────────────────
// OptionQuote
// ─────────────────────────────────────────────

func TestOptionQuote_Fields(t *testing.T) {
	ts := time.Now()
	q := domain.OptionQuote{
		Bid:       3.10,
		Ask:       3.20,
		Last:      3.15,
		Timestamp: ts,
	}
	assert.Equal(t, 3.10, q.Bid)
	assert.Equal(t, 3.20, q.Ask)
	assert.Equal(t, 3.15, q.Last)
	assert.Equal(t, ts, q.Timestamp)
}

// ─────────────────────────────────────────────
// Greeks
// ─────────────────────────────────────────────

func TestNewGreeks_CallDeltaValid(t *testing.T) {
	g, err := domain.NewGreeks(0.52, 0.04, -0.12, 0.18, 0.03, 0.32)
	require.NoError(t, err)
	assert.InDelta(t, 0.52, g.Delta, 1e-9)
	assert.InDelta(t, 0.32, g.IV, 1e-9)
}

func TestNewGreeks_PutDeltaValid(t *testing.T) {
	_, err := domain.NewGreeks(-0.5, 0.04, -0.12, 0.18, 0.03, 0.32)
	require.NoError(t, err)
}

func TestNewGreeks_DeltaTooLarge(t *testing.T) {
	_, err := domain.NewGreeks(-1.5, 0.04, -0.12, 0.18, 0.03, 0.32)
	require.Error(t, err)
}

func TestNewGreeks_DeltaTooLargePositive(t *testing.T) {
	_, err := domain.NewGreeks(1.1, 0.04, -0.12, 0.18, 0.03, 0.32)
	require.Error(t, err)
}

func TestNewGreeks_NegativeIV(t *testing.T) {
	_, err := domain.NewGreeks(0.5, 0.04, -0.12, 0.18, 0.03, -0.01)
	require.Error(t, err)
}

func TestNewGreeks_ZeroIVAllowed(t *testing.T) {
	_, err := domain.NewGreeks(0.5, 0.04, -0.12, 0.18, 0.03, 0.0)
	require.NoError(t, err)
}

// ─────────────────────────────────────────────
// OptionContract entity
// ─────────────────────────────────────────────

func TestNewOptionContract_OCCSymbol(t *testing.T) {
	// Use a future date for contract creation
	expiry := time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC)
	c, err := domain.NewOptionContract("AAPL", expiry, 190.0, domain.OptionRightCall, domain.OptionStyleAmerican)
	require.NoError(t, err)
	// Verify OCC format: AAPL270119C00190000
	assert.Equal(t, domain.Symbol("AAPL270119C00190000"), c.ContractSymbol)
	assert.Equal(t, domain.Symbol("AAPL"), c.Underlying)
	assert.Equal(t, 100, c.Multiplier)
	assert.Equal(t, domain.OptionRightCall, c.Right)
	assert.Equal(t, domain.OptionStyleAmerican, c.Style)
	assert.InDelta(t, 190.0, c.Strike, 1e-9)
}

func TestNewOptionContract_PutOCCSymbol(t *testing.T) {
	expiry := time.Date(2027, 3, 15, 0, 0, 0, 0, time.UTC)
	c, err := domain.NewOptionContract("MSFT", expiry, 375.5, domain.OptionRightPut, domain.OptionStyleAmerican)
	require.NoError(t, err)
	assert.Equal(t, domain.Symbol("MSFT270315P00375500"), c.ContractSymbol)
}

// ─────────────────────────────────────────────
// FormatOCCSymbol helper
// ─────────────────────────────────────────────

func TestFormatOCCSymbol_Call(t *testing.T) {
	result := domain.FormatOCCSymbol("AAPL", time.Date(2024, 1, 19, 0, 0, 0, 0, time.UTC), domain.OptionRightCall, 190.0)
	assert.Equal(t, "AAPL240119C00190000", result)
}

func TestFormatOCCSymbol_Put(t *testing.T) {
	result := domain.FormatOCCSymbol("MSFT", time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC), domain.OptionRightPut, 375.5)
	assert.Equal(t, "MSFT240315P00375500", result)
}

func TestNewOptionContract_ZeroStrike(t *testing.T) {
	expiry := time.Date(2025, 1, 19, 0, 0, 0, 0, time.UTC)
	_, err := domain.NewOptionContract("AAPL", expiry, 0, domain.OptionRightCall, domain.OptionStyleAmerican)
	require.Error(t, err)
}

func TestNewOptionContract_PastExpiry(t *testing.T) {
	expiry := time.Date(2020, 1, 19, 0, 0, 0, 0, time.UTC)
	_, err := domain.NewOptionContract("AAPL", expiry, 190.0, domain.OptionRightCall, domain.OptionStyleAmerican)
	require.Error(t, err)
}

// ─────────────────────────────────────────────
// OrderIntent — backward compat
// ─────────────────────────────────────────────

func TestNewOrderIntent_BackwardCompat(t *testing.T) {
	id := uuid.New()
	sym, _ := domain.NewSymbol("AAPL")
	intent, err := domain.NewOrderIntent(
		id, "tenant1", domain.EnvModePaper, sym,
		domain.DirectionLong, 150.0, 145.0, 10, 10.0,
		"momentum", "test", 0.8, "key-1",
	)
	require.NoError(t, err)
	assert.Nil(t, intent.Instrument)
	assert.Equal(t, 0.0, intent.MaxLossUSD)
}

// ─────────────────────────────────────────────
// NewOptionOrderIntent
// ─────────────────────────────────────────────

func TestNewOptionOrderIntent_Valid(t *testing.T) {
	id := uuid.New()
	inst, _ := domain.NewInstrument(domain.InstrumentTypeOption, "AAPL240119C00190000", "AAPL")
	intent, err := domain.NewOptionOrderIntent(
		id, "tenant1", domain.EnvModePaper,
		inst, domain.DirectionLong,
		3.20, // limitPrice (premium)
		5,    // quantity (contracts)
		"momentum", "test", 0.8, "key-2",
		200.0, // MaxLossUSD
	)
	require.NoError(t, err)
	assert.NotNil(t, intent.Instrument)
	assert.Equal(t, domain.InstrumentTypeOption, intent.Instrument.Type)
	assert.Equal(t, 200.0, intent.MaxLossUSD)
}

func TestNewOptionOrderIntent_NilInstrument(t *testing.T) {
	id := uuid.New()
	_, err := domain.NewOptionOrderIntent(
		id, "tenant1", domain.EnvModePaper,
		domain.Instrument{}, domain.DirectionLong,
		3.20, 5,
		"momentum", "test", 0.8, "key-3",
		200.0,
	)
	require.Error(t, err)
}

func TestNewOptionOrderIntent_EquityInstrument(t *testing.T) {
	id := uuid.New()
	inst, _ := domain.NewInstrument(domain.InstrumentTypeEquity, "AAPL", "")
	_, err := domain.NewOptionOrderIntent(
		id, "tenant1", domain.EnvModePaper,
		inst, domain.DirectionLong,
		150.0, 5,
		"momentum", "test", 0.8, "key-4",
		200.0,
	)
	require.Error(t, err)
}

func TestNewOptionOrderIntent_ZeroMaxLoss(t *testing.T) {
	id := uuid.New()
	inst, _ := domain.NewInstrument(domain.InstrumentTypeOption, "AAPL240119C00190000", "AAPL")
	_, err := domain.NewOptionOrderIntent(
		id, "tenant1", domain.EnvModePaper,
		inst, domain.DirectionLong,
		3.20, 5,
		"momentum", "test", 0.8, "key-5",
		0.0,
	)
	require.Error(t, err)
}

// ─────────────────────────────────────────────
// Event types
// ─────────────────────────────────────────────

func TestOptionEventTypes(t *testing.T) {
	assert.Equal(t, domain.EventType("OptionChainReceived"), domain.EventOptionChainReceived)
	assert.Equal(t, domain.EventType("OptionContractSelected"), domain.EventOptionContractSelected)
}
