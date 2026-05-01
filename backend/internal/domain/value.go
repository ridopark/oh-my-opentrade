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

// Well-known symbols used across the codebase. Keeps raw-string
// references at a minimum so typos surface at compile time.
const (
	SymbolVIX Symbol = "VIX"
)

func (s Symbol) String() string { return string(s) }

// SymbolsToStrings converts a Symbol slice to a string slice. Pulled into
// domain because the same loop body was open-coded in three places
// (cmd/omo-replay, app/backtest, adapters/http) before consolidation.
func SymbolsToStrings(syms []Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = string(s)
	}
	return out
}

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

// Venue identifies the execution/market-data venue a symbol is traded on.
// Added for Gap 10 (MFT crypto): perpetual and cross-venue strategies need
// to distinguish "BTC/USD on Coinbase" from "BTC/USD on Binance" or
// "BTCUSDT-PERP on Hyperliquid". Equity code paths leave Venue zero (empty)
// and callers fall back to DefaultVenue(AssetClass) as the implicit venue,
// preserving existing behavior.
type Venue string

const (
	// VenueUnspecified is the zero value; treat as "use DefaultVenue(assetClass)".
	VenueUnspecified Venue = ""

	// Equity venues
	VenueAlpaca Venue = "alpaca"
	VenueIBKR   Venue = "ibkr"

	// Crypto spot venues
	VenueCoinbase Venue = "coinbase"
	VenueBinance  Venue = "binance"
	VenueKraken   Venue = "kraken"

	// Crypto derivatives venues (perps)
	VenueHyperliquid Venue = "hyperliquid"
	VenueBinanceFut  Venue = "binance-futures"
	VenueBybit       Venue = "bybit"
)

func (v Venue) String() string { return string(v) }

// IsUnspecified returns true when the venue field was never set. Callers
// should resolve via DefaultVenue(assetClass) before publishing to adapters
// that require a concrete venue.
func (v Venue) IsUnspecified() bool { return v == VenueUnspecified }

// NewVenue validates a venue string. Empty string is accepted as
// VenueUnspecified (implicit default venue for backward compat); any other
// value is accepted as-is so adapters can introduce new venues without a
// domain-layer change.
func NewVenue(v string) (Venue, error) {
	return Venue(v), nil
}

// DefaultVenue returns the implicit venue for a given asset class when an
// entity was constructed without an explicit Venue. Used so equity code
// paths built before Gap 10 keep working untouched.
func DefaultVenue(ac AssetClass) Venue {
	switch ac {
	case AssetClassEquity:
		return VenueAlpaca
	case AssetClassCrypto:
		return VenueCoinbase
	case AssetClassCryptoPerp:
		return VenueHyperliquid
	default:
		return VenueUnspecified
	}
}

// QualifiedSymbol is a venue-qualified symbol used by cross-venue strategies
// that need to refer to the same logical pair on different venues (e.g. a
// basis trade between Coinbase spot and Hyperliquid perp). It is a value
// object — adapters still persist Symbol and Venue as separate columns.
type QualifiedSymbol struct {
	Venue  Venue  `json:"venue"`
	Symbol Symbol `json:"symbol"`
}

// String returns "venue:symbol" (e.g. "coinbase:BTC/USD"). When Venue is
// unspecified the bare symbol is returned so legacy logs stay readable.
func (q QualifiedSymbol) String() string {
	if q.Venue.IsUnspecified() {
		return string(q.Symbol)
	}
	return string(q.Venue) + ":" + string(q.Symbol)
}

// QS is a constructor that defaults Venue from the asset class. Use this in
// call sites that previously passed a bare Symbol but now need to attach
// the implicit venue without hard-coding one.
func QS(sym Symbol, ac AssetClass) QualifiedSymbol {
	return QualifiedSymbol{Venue: DefaultVenue(ac), Symbol: sym}
}

// Timeframe represents a candle interval.
type Timeframe string

var validTimeframes = map[Timeframe]struct{}{
	"1m": {}, "5m": {}, "15m": {}, "30m": {}, "1h": {}, "1d": {},
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
	RegimeTrend     RegimeType = "TREND"
	RegimeTrendUp   RegimeType = "TREND_UP"
	RegimeTrendDown RegimeType = "TREND_DOWN"
	RegimeBalance   RegimeType = "BALANCE"
	RegimeReversal  RegimeType = "REVERSAL"
)

func (r RegimeType) String() string { return string(r) }

// IsTrend returns true if the regime is any trend variant (TREND, TREND_UP, TREND_DOWN).
func (r RegimeType) IsTrend() bool {
	return r == RegimeTrend || r == RegimeTrendUp || r == RegimeTrendDown
}

func NewRegimeType(r string) (RegimeType, error) {
	switch RegimeType(r) {
	case RegimeTrend, RegimeTrendUp, RegimeTrendDown, RegimeBalance, RegimeReversal:
		return RegimeType(r), nil
	default:
		return "", fmt.Errorf("invalid regime type: %q", r)
	}
}

// AssetClass represents the asset class for trading (EQUITY or CRYPTO).
type AssetClass string

const (
	AssetClassEquity     AssetClass = "EQUITY"
	AssetClassCrypto     AssetClass = "CRYPTO"
	AssetClassCryptoPerp AssetClass = "CRYPTO_PERP"
)

func (a AssetClass) String() string { return string(a) }

func NewAssetClass(a string) (AssetClass, error) {
	switch AssetClass(a) {
	case AssetClassEquity, AssetClassCrypto, AssetClassCryptoPerp:
		return AssetClass(a), nil
	default:
		return "", fmt.Errorf("invalid asset class: %q", a)
	}
}

// Is24x7 returns true if the asset class trades 24/7 (Crypto), false for traditional market hours (Equity).
func (a AssetClass) Is24x7() bool {
	return a == AssetClassCrypto || a == AssetClassCryptoPerp
}

// SupportsShort returns true if short selling is supported for this asset class.
// IBKR ZEROHASH only supports long spot crypto — no short selling.
func (a AssetClass) SupportsShort() bool {
	return a != AssetClassCrypto // crypto spot (ZEROHASH) has no short; perps do support short
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
