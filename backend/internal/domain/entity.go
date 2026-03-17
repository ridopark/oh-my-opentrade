package domain

import (
	"encoding/json"
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

	// Microstructure metadata from broker feed.
	TradeCount uint64 // number of trades in this bar (0 if unavailable)

	// Spike repair metadata — populated by AdaptiveFilter when High/Low are clamped.
	Repaired     bool    // true if High/Low were clamped by the adaptive spike filter
	OriginalHigh float64 // pre-repair High (0 if not repaired)
	OriginalLow  float64 // pre-repair Low (0 if not repaired)
}

// NewMarketBar creates a validated MarketBar. High must be >= Low and Volume must be non-negative.
// Crypto markets legitimately emit zero-volume bars during low-activity periods.
func NewMarketBar(t time.Time, sym Symbol, tf Timeframe, open, high, low, close, volume float64) (MarketBar, error) {
	if high < low {
		return MarketBar{}, errors.New("high cannot be less than low")
	}
	if volume < 0 {
		return MarketBar{}, errors.New("volume must not be negative")
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
	ID             uuid.UUID `json:"id"`
	TenantID       string    `json:"tenantId"`
	EnvMode        EnvMode   `json:"envMode"`
	Symbol         Symbol    `json:"symbol"`
	Direction      Direction `json:"direction"`
	LimitPrice     float64   `json:"limitPrice"`
	StopLoss       float64   `json:"stopLoss"`
	MaxSlippageBPS int       `json:"maxSlippageBPS"`
	Quantity       float64   `json:"quantity"`
	Strategy       string    `json:"strategy"`
	Rationale      string    `json:"rationale"`
	Confidence     float64   `json:"confidence"`
	IdempotencyKey string    `json:"idempotencyKey"`
	// Execution control: override broker order type and time-in-force.
	// Empty values fall back to adapter defaults ("limit" / "gtc").
	OrderType   string `json:"orderType,omitempty"`   // "limit", "market", "stop_limit"
	TimeInForce string `json:"timeInForce,omitempty"` // "gtc", "ioc", "day"
	// Options-specific fields (nil/zero for equity orders)
	Instrument *Instrument       `json:"instrument,omitempty"`
	AssetClass AssetClass        `json:"assetClass"`
	MaxLossUSD float64           `json:"maxLossUSD,omitempty"`
	Meta       map[string]string `json:"meta,omitempty"`
}

// OrderIntentStatus indicates where in the pipeline an order intent currently sits.
type OrderIntentStatus = string

const (
	OrderIntentStatusCreated   OrderIntentStatus = "created"
	OrderIntentStatusValidated OrderIntentStatus = "validated"
	OrderIntentStatusRejected  OrderIntentStatus = "rejected"
	OrderIntentStatusSubmitted OrderIntentStatus = "submitted"
)

// OrderIntentEventPayload is the SSE wire shape for all order-intent events.
// It embeds the intent fields and adds a Status so the frontend can derive
// the current lifecycle stage from a single payload.
type OrderIntentEventPayload struct {
	ID             string            `json:"id"`
	Symbol         string            `json:"symbol"`
	Direction      string            `json:"direction"`
	LimitPrice     float64           `json:"limitPrice"`
	StopLoss       float64           `json:"stopLoss"`
	MaxSlippageBPS int               `json:"maxSlippageBPS"`
	Quantity       float64           `json:"quantity"`
	Strategy       string            `json:"strategy"`
	Rationale      string            `json:"rationale"`
	Confidence     float64           `json:"confidence"`
	Status         string            `json:"status"`
	Reason         string            `json:"reason,omitempty"`
	BrokerOrderID  string            `json:"brokerOrderId,omitempty"`
	Meta           map[string]string `json:"meta,omitempty"`
}

// NewOrderIntentEventPayload converts an OrderIntent into the SSE wire shape.
func NewOrderIntentEventPayload(intent OrderIntent, status OrderIntentStatus) OrderIntentEventPayload {
	return OrderIntentEventPayload{
		ID:             intent.ID.String(),
		Symbol:         string(intent.Symbol),
		Direction:      string(intent.Direction),
		LimitPrice:     intent.LimitPrice,
		StopLoss:       intent.StopLoss,
		MaxSlippageBPS: intent.MaxSlippageBPS,
		Quantity:       intent.Quantity,
		Strategy:       intent.Strategy,
		Rationale:      intent.Rationale,
		Confidence:     intent.Confidence,
		Status:         status,
		Meta:           intent.Meta,
	}
}

// NewOrderIntentRejectedPayload is like NewOrderIntentEventPayload but includes
// the specific reason the intent was rejected (risk, slippage, position gate, etc.).
func NewOrderIntentRejectedPayload(intent OrderIntent, reason string) OrderIntentEventPayload {
	p := NewOrderIntentEventPayload(intent, OrderIntentStatusRejected)
	p.Reason = reason
	return p
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
	if stopLoss <= 0 && !dir.IsExit() {
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

// NewOptionOrderIntent creates an OrderIntent specifically for an options trade.
// It requires a non-nil Instrument of type InstrumentTypeOption and MaxLossUSD > 0.
// StopLoss is set to LimitPrice (premium) as a placeholder; risk is controlled via MaxLossUSD.
func NewOptionOrderIntent(
	id uuid.UUID,
	tenantID string,
	envMode EnvMode,
	inst Instrument,
	dir Direction,
	limitPrice float64,
	quantity float64,
	strategy, rationale string,
	confidence float64,
	idempotencyKey string,
	maxLossUSD float64,
) (OrderIntent, error) {
	if inst.Type != InstrumentTypeOption {
		return OrderIntent{}, errors.New("instrument must be of type OPTION for NewOptionOrderIntent")
	}
	if maxLossUSD <= 0 {
		return OrderIntent{}, errors.New("MaxLossUSD must be > 0 for option orders")
	}
	if confidence < 0 || confidence > 1 {
		return OrderIntent{}, fmt.Errorf("confidence must be between 0 and 1, got %v", confidence)
	}
	if idempotencyKey == "" {
		return OrderIntent{}, errors.New("idempotency key is required")
	}
	if limitPrice <= 0 {
		return OrderIntent{}, errors.New("limit price must be greater than zero")
	}
	instCopy := inst
	return OrderIntent{
		ID:             id,
		TenantID:       tenantID,
		EnvMode:        envMode,
		Symbol:         inst.Symbol,
		Direction:      dir,
		LimitPrice:     limitPrice,
		StopLoss:       limitPrice, // premium as reference; risk enforced via MaxLossUSD
		MaxSlippageBPS: 0,
		Quantity:       quantity,
		Strategy:       strategy,
		Rationale:      rationale,
		Confidence:     confidence,
		IdempotencyKey: idempotencyKey,
		Instrument:     &instCopy,
		MaxLossUSD:     maxLossUSD,
	}, nil
}

// HTFData holds higher-timeframe indicator values attached to a lower-timeframe snapshot.
type HTFData struct {
	EMA50  float64 `json:"ema50,omitempty"`
	EMA200 float64 `json:"ema200,omitempty"`
	Bias   string  `json:"bias,omitempty"`
}

// IndicatorSnapshot holds a point-in-time snapshot of technical indicators.
type IndicatorSnapshot struct {
	Time          time.Time
	Symbol        Symbol
	Timeframe     Timeframe
	RSI           float64
	StochK        float64
	StochD        float64
	EMA9          float64
	EMA21         float64
	EMA50         float64
	EMA200        float64
	EMAFast       float64
	EMASlow       float64
	EMAFastPeriod int
	EMASlowPeriod int
	VWAP          float64
	Volume        float64
	VolumeSMA     float64
	ATR           float64
	VWAPSD        float64                    `json:"vwapSD,omitempty"`
	AnchorRegimes map[Timeframe]MarketRegime `json:"anchorRegimes,omitempty"`
	HTF           map[Timeframe]HTFData      `json:"htf,omitempty"`
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
	Time        time.Time
	TenantID    string
	EnvMode     EnvMode
	TradeID     uuid.UUID
	ExecutionID string // broker fill execution ID for WS dedup (empty for reconciliation/sweep trades)
	Symbol      Symbol
	Side        string
	Quantity    float64
	Price       float64
	Commission  float64
	Status      string
	Strategy    string
	Rationale   string
	AssetClass  AssetClass
	Thesis      json.RawMessage

	InstrumentType InstrumentType
	OptionSymbol   string
	Underlying     string
	Strike         float64
	Expiry         time.Time
	OptionRight    string
	Premium        float64
	DeltaAtEntry   float64
	IVAtEntry      float64
}

// NewTrade creates a validated Trade. Quantity must not be negative.
func NewTrade(
	t time.Time, tenantID string, envMode EnvMode, tradeID uuid.UUID,
	sym Symbol, side string, quantity, price, commission float64, status string,
	strategy, rationale string,
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
		Strategy:   strategy,
		Rationale:  rationale,
	}, nil
}

// BrokerOrder represents a submitted order tracked until fill or cancellation.
type BrokerOrder struct {
	Time          time.Time
	TenantID      string
	EnvMode       EnvMode
	IntentID      uuid.UUID
	BrokerOrderID string
	Symbol        Symbol
	Side          string
	Quantity      float64
	LimitPrice    float64
	StopLoss      float64
	Status        string // submitted | filled | canceled | expired
	FilledAt      *time.Time
	FilledPrice   float64
	FilledQty     float64
	Strategy      string
	Rationale     string
	Confidence    float64

	InstrumentType InstrumentType
	OptionSymbol   string
	Underlying     string
	Strike         float64
	Expiry         time.Time
	OptionRight    string
}

// DailyPnL tracks realized and unrealized P&L for a single trading day.
type DailyPnL struct {
	Date          time.Time
	TenantID      string
	EnvMode       EnvMode
	RealizedPnL   float64
	UnrealizedPnL float64
	TradeCount    int
	MaxDrawdown   float64
}

// NewDailyPnL creates a validated DailyPnL. TradeCount must not be negative.
func NewDailyPnL(date time.Time, tenantID string, envMode EnvMode, realizedPnL, unrealizedPnL float64, tradeCount int, maxDrawdown float64) (DailyPnL, error) {
	if tradeCount < 0 {
		return DailyPnL{}, errors.New("trade count cannot be negative")
	}
	return DailyPnL{
		Date:          date,
		TenantID:      tenantID,
		EnvMode:       envMode,
		RealizedPnL:   realizedPnL,
		UnrealizedPnL: unrealizedPnL,
		TradeCount:    tradeCount,
		MaxDrawdown:   maxDrawdown,
	}, nil
}

// EquityPoint represents a single point on the equity curve.
type EquityPoint struct {
	Time     time.Time
	TenantID string
	EnvMode  EnvMode
	Equity   float64
	Cash     float64
	Drawdown float64
}

// NewEquityPoint creates a validated EquityPoint. Equity must not be negative.
func NewEquityPoint(t time.Time, tenantID string, envMode EnvMode, equity, cash, drawdown float64) (EquityPoint, error) {
	if equity < 0 {
		return EquityPoint{}, errors.New("equity cannot be negative")
	}
	return EquityPoint{
		Time:     t,
		TenantID: tenantID,
		EnvMode:  envMode,
		Equity:   equity,
		Cash:     cash,
		Drawdown: drawdown,
	}, nil
}

// ThoughtLog represents an AI debate reasoning record persisted for historical audit.
type ThoughtLog struct {
	Time           time.Time
	TenantID       string
	EnvMode        EnvMode
	Symbol         Symbol
	EventType      string
	Direction      string
	Confidence     float64
	BullArgument   string
	BearArgument   string
	JudgeReasoning string
	Rationale      string
	IntentID       string // stored in payload JSONB
}

// MarketTrade represents a single trade tick from the exchange.
// Used for real-time chart candle formation only — not persisted.
type MarketTrade struct {
	Time   time.Time `json:"time"`
	Symbol Symbol    `json:"symbol"`
	Price  float64   `json:"price"`
	Size   float64   `json:"size"`
}

// FormingBar represents a partial (in-progress) OHLCV candle for the current bucket.
// Sent to the frontend via SSE so the chart can show a forming candle in real-time.
type FormingBar struct {
	Time      time.Time `json:"time"`
	Symbol    Symbol    `json:"symbol"`
	Timeframe Timeframe `json:"timeframe"`
	Open      float64   `json:"open"`
	High      float64   `json:"high"`
	Low       float64   `json:"low"`
	Close     float64   `json:"close"`
	Volume    float64   `json:"volume"`
}
