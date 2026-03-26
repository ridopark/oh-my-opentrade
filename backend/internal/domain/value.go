package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// PriceLevel represents a horizontal price line to overlay on a chart.
type PriceLevel struct {
	Label     string
	Price     float64
	Color     string    // semantic color: "green", "red", "blue"
	StartTime time.Time // if non-zero, line starts at this time instead of chart xMin
	EndTime   time.Time // if non-zero, line ends at this time instead of chart xMax
}

// TimeMarker represents a vertical line at a specific timestamp on the chart,
// used to mark when a trade entry or exit occurred on the time axis.
type TimeMarker struct {
	Time  time.Time
	Label string
	Color string
}

// EnvMode represents the execution environment (paper trading vs live).
type EnvMode string

const (
	EnvModePaper EnvMode = "Paper"
	EnvModeLive  EnvMode = "Live"
)

func (m EnvMode) String() string { return string(m) }

func NewEnvMode(m string) (EnvMode, error) {
	switch EnvMode(m) {
	case EnvModePaper, EnvModeLive:
		return EnvMode(m), nil
	default:
		return "", fmt.Errorf("invalid env mode: %q", m)
	}
}

// Direction represents the trade direction.
type Direction string

const (
	DirectionLong       Direction = "LONG"
	DirectionShort      Direction = "SHORT"
	DirectionCloseLong  Direction = "CLOSE_LONG"
	DirectionCloseShort Direction = "CLOSE_SHORT"
)

func (d Direction) String() string { return string(d) }

// IsExit returns true if the direction represents closing an existing position.
func (d Direction) IsExit() bool {
	return d == DirectionCloseLong || d == DirectionCloseShort
}

func NewDirection(d string) (Direction, error) {
	switch Direction(d) {
	case DirectionLong, DirectionShort, DirectionCloseLong, DirectionCloseShort:
		return Direction(d), nil
	default:
		return "", fmt.Errorf("invalid direction: %q", d)
	}
}

// Symbol represents a trading pair identifier (e.g. "BTC/USD").
type Symbol string

func (s Symbol) String() string { return string(s) }

func NewSymbol(s string) (Symbol, error) {
	if s == "" {
		return "", errors.New("invalid symbol: must not be empty")
	}
	return Symbol(s), nil
}

// ToSlashFormat converts a no-slash crypto symbol to slash format.
// "BTCUSD" → "BTC/USD". If already has slash or is not a crypto symbol, returns as-is.
func (s Symbol) ToSlashFormat() Symbol {
	str := string(s)
	if strings.Contains(str, "/") {
		return s
	}
	if len(str) >= 6 && strings.HasSuffix(str, "USD") {
		return Symbol(str[:len(str)-3] + "/" + "USD")
	}
	return s
}

// ToNoSlashFormat removes slashes from a symbol. "BTC/USD" → "BTCUSD".
func (s Symbol) ToNoSlashFormat() Symbol {
	return Symbol(strings.ReplaceAll(string(s), "/", ""))
}

// IsCryptoSymbol returns true if the symbol is in crypto format (contains "/" and ends with "/USD").
func (s Symbol) IsCryptoSymbol() bool {
	str := string(s)
	return strings.Contains(str, "/") && strings.HasSuffix(str, "/USD")
}

// Timeframe represents a candle interval.
type Timeframe string

var validTimeframes = map[Timeframe]struct{}{
	"1m": {}, "5m": {}, "15m": {}, "1h": {}, "1d": {},
}

func (t Timeframe) String() string { return string(t) }

func NewTimeframe(t string) (Timeframe, error) {
	tf := Timeframe(t)
	if _, ok := validTimeframes[tf]; !ok {
		return "", fmt.Errorf("invalid timeframe: %q", t)
	}
	return tf, nil
}

// RegimeType classifies the current market regime.
type RegimeType string

const (
	RegimeTrend    RegimeType = "TREND"
	RegimeBalance  RegimeType = "BALANCE"
	RegimeReversal RegimeType = "REVERSAL"
)

func (r RegimeType) String() string { return string(r) }

func NewRegimeType(r string) (RegimeType, error) {
	switch RegimeType(r) {
	case RegimeTrend, RegimeBalance, RegimeReversal:
		return RegimeType(r), nil
	default:
		return "", fmt.Errorf("invalid regime type: %q", r)
	}
}

// AssetClass represents the asset class for trading (EQUITY or CRYPTO).
type AssetClass string

const (
	AssetClassEquity AssetClass = "EQUITY"
	AssetClassCrypto AssetClass = "CRYPTO"
)

func (a AssetClass) String() string { return string(a) }

func NewAssetClass(a string) (AssetClass, error) {
	switch AssetClass(a) {
	case AssetClassEquity, AssetClassCrypto:
		return AssetClass(a), nil
	default:
		return "", fmt.Errorf("invalid asset class: %q", a)
	}
}

// Is24x7 returns true if the asset class trades 24/7 (Crypto), false for traditional market hours (Equity).
func (a AssetClass) Is24x7() bool {
	return a == AssetClassCrypto
}

// SupportsShort returns true if short selling is supported for this asset class.
// Empty/unset asset class defaults to true (only crypto is explicitly blocked).
func (a AssetClass) SupportsShort() bool {
	return a != AssetClassCrypto
}

// FmtPrice formats a price with appropriate decimal precision based on magnitude.
// Sub-penny assets (e.g., PEPE at $0.000012) show up to 8 decimals;
// normal assets use 2 decimals.
func FmtPrice(v float64) string {
	abs := v
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs == 0:
		return "0"
	case abs >= 1.0:
		return fmt.Sprintf("%.2f", v)
	case abs >= 0.01:
		return fmt.Sprintf("%.4f", v)
	case abs >= 0.0001:
		return fmt.Sprintf("%.6f", v)
	default:
		return fmt.Sprintf("%.8f", v)
	}
}
