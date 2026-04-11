package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseOCC(t *testing.T) {
	etLoc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err, "America/New_York timezone must be loadable")

	t.Run("valid call", func(t *testing.T) {
		// AAPL, March 20 2026, Call, $150.00
		sym := Symbol("AAPL260320C00150000")
		underlying, expiry, right, strike, ok := ParseOCC(sym)
		require.True(t, ok)
		assert.Equal(t, "AAPL", underlying)
		assert.Equal(t, "CALL", right)
		assert.InDelta(t, 150.0, strike, 0.001)
		assert.Equal(t, 2026, expiry.Year())
		assert.Equal(t, time.March, expiry.Month())
		assert.Equal(t, 20, expiry.Day())
	})

	t.Run("valid put", func(t *testing.T) {
		// SPY, March 13 2026, Put, $550.00
		sym := Symbol("SPY260313P00550000")
		underlying, expiry, right, strike, ok := ParseOCC(sym)
		require.True(t, ok)
		assert.Equal(t, "SPY", underlying)
		assert.Equal(t, "PUT", right)
		assert.InDelta(t, 550.0, strike, 0.001)
		assert.Equal(t, 2026, expiry.Year())
		assert.Equal(t, time.March, expiry.Month())
		assert.Equal(t, 13, expiry.Day())
	})

	t.Run("fractional strike", func(t *testing.T) {
		// AAPL, Jan 19 2024, Call, $152.50 -> strike*1000 = 152500 -> 00152500
		sym := Symbol("AAPL240119C00152500")
		underlying, _, right, strike, ok := ParseOCC(sym)
		require.True(t, ok)
		assert.Equal(t, "AAPL", underlying)
		assert.Equal(t, "CALL", right)
		assert.InDelta(t, 152.5, strike, 0.001)
	})

	t.Run("short symbol (1 char)", func(t *testing.T) {
		// X (US Steel), Dec 20 2024, Put, $40.00
		sym := Symbol("X241220P00040000")
		underlying, _, right, strike, ok := ParseOCC(sym)
		require.True(t, ok)
		assert.Equal(t, "X", underlying)
		assert.Equal(t, "PUT", right)
		assert.InDelta(t, 40.0, strike, 0.001)
	})

	t.Run("expiry has 16:00 ET", func(t *testing.T) {
		sym := Symbol("AAPL260320C00150000")
		_, expiry, _, _, ok := ParseOCC(sym)
		require.True(t, ok)
		// Should be 16:00:00 ET
		expiryET := expiry.In(etLoc)
		assert.Equal(t, 16, expiryET.Hour())
		assert.Equal(t, 0, expiryET.Minute())
		assert.Equal(t, 0, expiryET.Second())
	})

	t.Run("invalid: too short", func(t *testing.T) {
		_, _, _, _, ok := ParseOCC(Symbol("AAPL26032C001"))
		assert.False(t, ok)
	})

	t.Run("invalid: wrong right character", func(t *testing.T) {
		// Replace C with X
		_, _, _, _, ok := ParseOCC(Symbol("AAPL260320X00150000"))
		assert.False(t, ok)
	})

	t.Run("invalid: non-numeric strike", func(t *testing.T) {
		_, _, _, _, ok := ParseOCC(Symbol("AAPL260320C0015ABCD"))
		assert.False(t, ok)
	})

	t.Run("invalid: non-numeric date", func(t *testing.T) {
		_, _, _, _, ok := ParseOCC(Symbol("AAPL26AB20C00150000"))
		assert.False(t, ok)
	})

	t.Run("invalid: empty underlying", func(t *testing.T) {
		// 15 chars = no underlying (0-char prefix)
		_, _, _, _, ok := ParseOCC(Symbol("260320C00150000"))
		assert.False(t, ok)
	})

	t.Run("roundtrip with FormatOCCSymbol", func(t *testing.T) {
		original := Symbol("MSFT260620C00400000")
		underlying, expiry, right, strike, ok := ParseOCC(original)
		require.True(t, ok)

		rightEnum := OptionRightCall
		if right == "PUT" {
			rightEnum = OptionRightPut
		}
		reconstructed := FormatOCCSymbol(underlying, expiry, rightEnum, strike)
		assert.Equal(t, string(original), reconstructed)
	})
}

func TestIsOCCSymbol(t *testing.T) {
	t.Run("valid OCC", func(t *testing.T) {
		assert.True(t, IsOCCSymbol(Symbol("AAPL260320C00150000")))
		assert.True(t, IsOCCSymbol(Symbol("SPY260313P00550000")))
		assert.True(t, IsOCCSymbol(Symbol("X241220P00040000")))
	})

	t.Run("not OCC", func(t *testing.T) {
		assert.False(t, IsOCCSymbol(Symbol("AAPL")))
		assert.False(t, IsOCCSymbol(Symbol("SPY")))
		assert.False(t, IsOCCSymbol(Symbol("")))
	})
}

func TestUnderlyingFromOCC(t *testing.T) {
	t.Run("extracts underlying", func(t *testing.T) {
		assert.Equal(t, Symbol("AAPL"), UnderlyingFromOCC(Symbol("AAPL260320C00150000")))
		assert.Equal(t, Symbol("SPY"), UnderlyingFromOCC(Symbol("SPY260313P00550000")))
		assert.Equal(t, Symbol("X"), UnderlyingFromOCC(Symbol("X241220P00040000")))
	})

	t.Run("returns empty for non-OCC", func(t *testing.T) {
		assert.Equal(t, Symbol(""), UnderlyingFromOCC(Symbol("AAPL")))
		assert.Equal(t, Symbol(""), UnderlyingFromOCC(Symbol("SHORT")))
	})
}
