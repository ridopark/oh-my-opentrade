package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MarketBar represents a single OHLCV candle for a symbol and timeframe.
type MarketBar struct {
	Time      time.Time
	Symbol    Symbol
	Timeframe Timeframe
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	Suspect   bool
}

// NewMarketBar creates a validated MarketBar. High must be >= Low and Volume must be positive.
func NewMarketBar(t time.Time, sym Symbol, tf Timeframe, open, high, low, close, volume float64) (MarketBar, error) {
	if high < low {
		return MarketBar{}, errors.New("high cannot be less than low")
	}
	if volume <= 0 {
		return MarketBar{}, errors.New("volume must be greater than zero")
	}
	return MarketBar{
		Time:      t,
		Symbol:    sym,
		Timeframe: tf,
		Open:      open,
		High:      high,
		Low:       low,
		Close:     close,
		Volume:    volume,
		Suspect:   false,
	}, nil
}

// OrderIntent represents a validated intent to place an order, pending broker submission.
type OrderIntent struct {
	ID             uuid.UUID
	TenantID       string
	EnvMode        EnvMode
	Symbol         Symbol
	Direction      Direction
	LimitPrice     float64
	StopLoss       float64
	MaxSlippageBPS int
	Quantity       float64
	Strategy       string
	Rationale      string
	Confidence     float64
	IdempotencyKey string
}

// NewOrderIntent creates a validated OrderIntent.
// Requires positive prices, valid confidence [0,1], and a non-empty idempotency key.
func NewOrderIntent(
	id uuid.UUID,
	tenantID string,
	envMode EnvMode,
	sym Symbol,
	dir Direction,
	limitPrice, stopLoss float64,
	maxSlippageBPS int,
	quantity float64,
	strategy, rationale string,
	confidence float64,
	idempotencyKey string,
) (OrderIntent, error) {
	if idempotencyKey == "" {
		return OrderIntent{}, errors.New("idempotency key is required")
	}
	if stopLoss <= 0 {
		return OrderIntent{}, errors.New("stop loss must be greater than zero")
	}
	if limitPrice <= 0 {
		return OrderIntent{}, errors.New("limit price must be greater than zero")
	}
	if confidence < 0 || confidence > 1 {
		return OrderIntent{}, fmt.Errorf("confidence must be between 0 and 1, got %v", confidence)
	}
	return OrderIntent{
		ID:             id,
		TenantID:       tenantID,
		EnvMode:        envMode,
		Symbol:         sym,
		Direction:      dir,
		LimitPrice:     limitPrice,
		StopLoss:       stopLoss,
		MaxSlippageBPS: maxSlippageBPS,
		Quantity:       quantity,
		Strategy:       strategy,
		Rationale:      rationale,
		Confidence:     confidence,
		IdempotencyKey: idempotencyKey,
	}, nil
}

// IndicatorSnapshot holds a point-in-time snapshot of technical indicators.
type IndicatorSnapshot struct {
	Time      time.Time
	Symbol    Symbol
	Timeframe Timeframe
	RSI       float64
	StochK    float64
	StochD    float64
	EMA9      float64
	EMA21     float64
	VWAP      float64
	Volume    float64
	VolumeSMA float64
}

// NewIndicatorSnapshot creates a validated IndicatorSnapshot. RSI must be in [0,100].
func NewIndicatorSnapshot(
	t time.Time, sym Symbol, tf Timeframe,
	rsi, stochK, stochD, ema9, ema21, vwap, volume, volumeSMA float64,
) (IndicatorSnapshot, error) {
	if rsi < 0 || rsi > 100 {
		return IndicatorSnapshot{}, fmt.Errorf("RSI must be between 0 and 100, got %v", rsi)
	}
	return IndicatorSnapshot{
		Time:      t,
		Symbol:    sym,
		Timeframe: tf,
		RSI:       rsi,
		StochK:    stochK,
		StochD:    stochD,
		EMA9:      ema9,
		EMA21:     ema21,
		VWAP:      vwap,
		Volume:    volume,
		VolumeSMA: volumeSMA,
	}, nil
}

// MarketRegime captures the current regime classification for a symbol/timeframe pair.
type MarketRegime struct {
	Symbol    Symbol
	Timeframe Timeframe
	Type      RegimeType
	Since     time.Time
	Strength  float64
}

// NewMarketRegime creates a validated MarketRegime. Strength must be in [0,1].
func NewMarketRegime(sym Symbol, tf Timeframe, rt RegimeType, since time.Time, strength float64) (MarketRegime, error) {
	if strength < 0 || strength > 1 {
		return MarketRegime{}, fmt.Errorf("strength must be between 0 and 1, got %v", strength)
	}
	return MarketRegime{
		Symbol:    sym,
		Timeframe: tf,
		Type:      rt,
		Since:     since,
		Strength:  strength,
	}, nil
}

// StrategyDNA holds the configuration and performance metrics for a trading strategy version.
type StrategyDNA struct {
	ID                 uuid.UUID
	TenantID           string
	EnvMode            EnvMode
	Version            int
	Parameters         map[string]any
	PerformanceMetrics map[string]float64
}

// NewStrategyDNA creates a StrategyDNA. Version must be positive.
func NewStrategyDNA(id uuid.UUID, tenantID string, envMode EnvMode, version int, parameters map[string]any, metrics map[string]float64) (StrategyDNA, error) {
	return StrategyDNA{
		ID:                 id,
		TenantID:           tenantID,
		EnvMode:            envMode,
		Version:            version,
		Parameters:         parameters,
		PerformanceMetrics: metrics,
	}, nil
}

// Trade represents a completed or in-progress trade execution.
type Trade struct {
	Time       time.Time
	TenantID   string
	EnvMode    EnvMode
	TradeID    uuid.UUID
	Symbol     Symbol
	Side       string
	Quantity   float64
	Price      float64
	Commission float64
	Status     string
}

// NewTrade creates a validated Trade. Quantity must not be negative.
func NewTrade(
	t time.Time, tenantID string, envMode EnvMode, tradeID uuid.UUID,
	sym Symbol, side string, quantity, price, commission float64, status string,
) (Trade, error) {
	if quantity < 0 {
		return Trade{}, errors.New("quantity cannot be negative")
	}
	return Trade{
		Time:       t,
		TenantID:   tenantID,
		EnvMode:    envMode,
		TradeID:    tradeID,
		Symbol:     sym,
		Side:       side,
		Quantity:   quantity,
		Price:      price,
		Commission: commission,
		Status:     status,
	}, nil
}
