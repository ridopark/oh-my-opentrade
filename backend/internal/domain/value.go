package domain

import (
	"errors"
	"fmt"
)

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
	DirectionLong  Direction = "LONG"
	DirectionShort Direction = "SHORT"
)

func (d Direction) String() string { return string(d) }

func NewDirection(d string) (Direction, error) {
	switch Direction(d) {
	case DirectionLong, DirectionShort:
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
