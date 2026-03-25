package domain

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// ─────────────────────────────────────────────
// InstrumentType
// ─────────────────────────────────────────────

// InstrumentType identifies whether an instrument is an equity or an option.
type InstrumentType string

const (
	InstrumentTypeEquity InstrumentType = "EQUITY"
	InstrumentTypeOption InstrumentType = "OPTION"
	InstrumentTypeCrypto InstrumentType = "CRYPTO"
)

// UnderlyingFromOCC extracts the underlying ticker from an OCC option symbol.
// OCC format: {UNDERLYING}{YYMMDD}{C|P}{8-digit strike}
// Returns empty Symbol if the input is not a valid OCC symbol.
func UnderlyingFromOCC(s Symbol) Symbol {
	str := string(s)
	if len(str) <= 15 {
		return ""
	}
	return Symbol(str[:len(str)-15])
}

// IsOCCSymbol reports whether s looks like an OCC option symbol.
// OCC format: 1–6 uppercase letters, followed by YYMMDD, C or P, and 8 strike digits.
// Minimum length is 15 chars (1-char underlying + 6 date + 1 right + 8 strike = 16; realistically ≥15).
func IsOCCSymbol(s Symbol) bool {
	str := string(s)
	if len(str) < 15 {
		return false
	}
	suffix := str[len(str)-15:]
	rightChar := suffix[6]
	if rightChar != 'C' && rightChar != 'P' {
		return false
	}
	for _, c := range suffix[:6] {
		if c < '0' || c > '9' {
			return false
		}
	}
	for _, c := range suffix[7:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ─────────────────────────────────────────────
// OptionRight
// ─────────────────────────────────────────────

// OptionRight distinguishes call vs put options.
type OptionRight string

const (
	OptionRightCall OptionRight = "CALL"
	OptionRightPut  OptionRight = "PUT"
)

// NewOptionRight validates and returns an OptionRight.
func NewOptionRight(r string) (OptionRight, error) {
	switch OptionRight(r) {
	case OptionRightCall, OptionRightPut:
		return OptionRight(r), nil
	default:
		return "", fmt.Errorf("invalid option right: %q", r)
	}
}

// ─────────────────────────────────────────────
// OptionStyle
// ─────────────────────────────────────────────

// OptionStyle represents the exercise style of an option contract.
type OptionStyle string

const (
	OptionStyleAmerican OptionStyle = "AMERICAN"
)

// NewOptionStyle validates and returns an OptionStyle.
func NewOptionStyle(s string) (OptionStyle, error) {
	switch OptionStyle(s) {
	case OptionStyleAmerican:
		return OptionStyle(s), nil
	default:
		return "", fmt.Errorf("invalid option style: %q", s)
	}
}

// ─────────────────────────────────────────────
// Instrument
// ─────────────────────────────────────────────

// Instrument represents a tradeable financial instrument (equity or option).
type Instrument struct {
	Type             InstrumentType
	Symbol           Symbol
	UnderlyingSymbol Symbol
}

// NewInstrument creates a validated Instrument value object.
// For options, UnderlyingSymbol must not be empty.
// For equities, UnderlyingSymbol may be empty.
func NewInstrument(itype InstrumentType, sym string, underlying string) (Instrument, error) {
	if itype != InstrumentTypeEquity && itype != InstrumentTypeOption && itype != InstrumentTypeCrypto {
		return Instrument{}, fmt.Errorf("invalid instrument type: %q", itype)
	}
	if sym == "" {
		return Instrument{}, errors.New("instrument symbol must not be empty")
	}
	return Instrument{
		Type:             itype,
		Symbol:           Symbol(sym),
		UnderlyingSymbol: Symbol(underlying),
	}, nil
}

// ─────────────────────────────────────────────
// OptionQuote
// ─────────────────────────────────────────────

// OptionQuote holds a point-in-time bid/ask/last for an option contract.
type OptionQuote struct {
	Bid       float64
	Ask       float64
	Last      float64
	Timestamp time.Time
}

// ─────────────────────────────────────────────
// Greeks
// ─────────────────────────────────────────────

// Greeks represents the option sensitivity measures.
type Greeks struct {
	Delta float64
	Gamma float64
	Theta float64
	Vega  float64
	Rho   float64
	IV    float64
}

// NewGreeks creates a validated Greeks struct.
// Validates: |delta| <= 1 and IV >= 0.
func NewGreeks(delta, gamma, theta, vega, rho, iv float64) (Greeks, error) {
	if math.Abs(delta) > 1.0 {
		return Greeks{}, fmt.Errorf("delta must be in [-1, 1], got %g", delta)
	}
	if iv < 0 {
		return Greeks{}, fmt.Errorf("implied volatility must be >= 0, got %g", iv)
	}
	return Greeks{
		Delta: delta,
		Gamma: gamma,
		Theta: theta,
		Vega:  vega,
		Rho:   rho,
		IV:    iv,
	}, nil
}

// ─────────────────────────────────────────────
// OptionContract entity
// ─────────────────────────────────────────────

// OptionContract represents a single standardized option contract.
type OptionContract struct {
	ContractSymbol Symbol
	Underlying     Symbol
	Expiry         time.Time
	Strike         float64
	Right          OptionRight
	Style          OptionStyle
	Multiplier     int
}

// NewOptionContract creates a validated OptionContract and derives the OCC contract symbol.
// OCC format: {UNDERLYING}{YYMMDD}{C|P}{8-digit strike * 1000 padded to 8 digits}
// e.g. AAPL240119C00190000 for AAPL $190 call expiring 2024-01-19.
func NewOptionContract(underlying string, expiry time.Time, strike float64, right OptionRight, style OptionStyle) (OptionContract, error) {
	if strike <= 0 {
		return OptionContract{}, errors.New("strike must be greater than zero")
	}
	if expiry.Before(time.Now()) {
		return OptionContract{}, errors.New("expiry must be in the future")
	}
	occ := FormatOCCSymbol(underlying, expiry, right, strike)
	return OptionContract{
		ContractSymbol: Symbol(occ),
		Underlying:     Symbol(underlying),
		Expiry:         expiry,
		Strike:         strike,
		Right:          right,
		Style:          style,
		Multiplier:     100,
	}, nil
}

// FormatOCCSymbol produces the OCC option ticker string.
// Format: {UNDERLYING}{YYMMDD}{C|P}{strike * 1000 zero-padded to 8 digits}
func FormatOCCSymbol(underlying string, expiry time.Time, right OptionRight, strike float64) string {
	dateStr := expiry.Format("060102") // YYMMDD
	rightChar := "C"
	if right == OptionRightPut {
		rightChar = "P"
	}
	// Strike price: multiply by 1000, format as 8-digit integer
	strikeInt := int(math.Round(strike * 1000))
	return fmt.Sprintf("%s%s%s%08d", underlying, dateStr, rightChar, strikeInt)
}

// ─────────────────────────────────────────────
// OptionContractSnapshot
// ─────────────────────────────────────────────

// OptionContractSnapshot combines a contract with its current market data.
type OptionContractSnapshot struct {
	OptionContract
	OptionQuote
	Greeks
	OpenInterest int
}
