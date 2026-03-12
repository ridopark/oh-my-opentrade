package strategy

import (
	"log/slog"
	"time"
)

// Strategy is the core interface that all trading strategies must implement.
// Strategies are pure decision engines: they receive market data and produce
// signals. They never directly access brokers, databases, or other adapters.
type Strategy interface {
	// Meta returns immutable metadata about the strategy.
	Meta() Meta

	// WarmupBars returns the number of historical bars needed before the
	// strategy can produce meaningful signals.
	WarmupBars() int

	// Init initializes the strategy for a specific symbol with the given
	// parameters. If prior is non-nil, the strategy should attempt to
	// resume from that state (e.g., after a restart or blue/green swap).
	// Returns the initial state and any error.
	Init(ctx Context, symbol string, params map[string]any, prior State) (State, error)

	// OnBar is the main decision step. Given a bar and current state, it
	// produces zero or more signals and the next state. This method must
	// be deterministic given the same inputs.
	OnBar(ctx Context, symbol string, bar Bar, st State) (next State, signals []Signal, err error)

	// OnEvent handles non-bar events (fills, halts, risk events, etc.).
	// Strategies that don't need event handling can return (st, nil, nil).
	OnEvent(ctx Context, symbol string, evt any, st State) (next State, signals []Signal, err error)
}

// ReplayableStrategy is an opt-in interface for strategies that support
// replay-aware warmup. When implemented, the runner calls ReplayOnBar
// during warmup instead of OnBar, allowing the strategy to pass replay=true
// to its internal state machine. This prevents replayed historical bars
// from firing live signals while still reconstructing internal state.
type ReplayableStrategy interface {
	Strategy
	// ReplayOnBar processes a historical bar for state recovery.
	// It updates internal state but never produces signals.
	// The indicators parameter provides pre-computed indicator data.
	ReplayOnBar(ctx Context, symbol string, bar Bar, st State, indicators IndicatorData) (State, error)
}

// Meta holds immutable metadata about a strategy implementation.
type Meta struct {
	ID          StrategyID
	Version     Version
	Name        string
	Description string
	Author      string
}

// Bar represents OHLCV data passed to strategies. This is a strategy-layer
// type decoupled from domain.MarketBar to keep the strategy package independent.
type Bar struct {
	Time   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

// Signal represents a strategy's trading intent. Signals are NOT orders —
// they express what the strategy wants to do. The application-layer RiskSizer
// converts signals into OrderIntents after position sizing and risk checks.
type Signal struct {
	StrategyInstanceID InstanceID
	Symbol             string
	Type               SignalType
	Side               Side
	Strength           float64           // 0.0–1.0 confidence/conviction score
	Tags               map[string]string // reason codes, regime info, etc.
}

// NewSignal creates a validated Signal. Strength must be in [0,1].
func NewSignal(instanceID InstanceID, symbol string, signalType SignalType, side Side, strength float64, tags map[string]string) (Signal, error) {
	if strength < 0 || strength > 1 {
		return Signal{}, ErrInvalidStrength
	}
	if symbol == "" {
		return Signal{}, ErrEmptySymbol
	}
	if tags == nil {
		tags = make(map[string]string)
	}
	return Signal{
		StrategyInstanceID: instanceID,
		Symbol:             symbol,
		Type:               signalType,
		Side:               side,
		Strength:           strength,
		Tags:               tags,
	}, nil
}

// State is an opaque interface for strategy-managed internal state.
// Each strategy defines its own concrete state type. State must be
// serializable for persistence and recovery on restart.
type State interface {
	// Marshal serializes the state to bytes for persistence.
	Marshal() ([]byte, error)

	// Unmarshal deserializes the state from bytes.
	// Called on the zero value of the concrete type.
	Unmarshal(data []byte) error
}

// Context provides strategies with controlled access to the environment.
// Strategies must not import adapters or infrastructure directly.
type Context interface {
	// Now returns the current time (or simulated time in backtesting).
	Now() time.Time

	// Logger returns a structured logger scoped to this strategy instance.
	Logger() *slog.Logger

	// EmitDomainEvent publishes a domain event without giving the strategy
	// direct access to the event bus or any adapter.
	EmitDomainEvent(evt any) error
}

// IndicatorData provides pre-computed technical indicators alongside a bar.
// Strategies receive this from the central indicator computation pipeline.
// This is optional — strategies that compute their own indicators can ignore it.
type IndicatorData struct {
	RSI           float64
	StochK        float64
	StochD        float64
	EMA9          float64
	EMA21         float64
	EMA50         float64
	EMAFast       float64
	EMASlow       float64
	EMAFastPeriod int
	EMASlowPeriod int
	VWAP          float64
	Volume        float64
	VolumeSMA     float64
	ATR           float64
	VWAPSD        float64
	AnchorRegimes map[string]AnchorRegime
	HTF           map[string]HTFIndicator
}

type HTFIndicator struct {
	EMA50  float64
	EMA200 float64
	Bias   string
}

type AnchorRegime struct {
	Type     string
	Strength float64
}

// FillConfirmation is sent to a strategy when its entry order is filled.
// The strategy should transition from PendingEntry to confirmed PositionSide.
type FillConfirmation struct {
	Symbol   string
	Side     Side // SideBuy or SideSell — the side that was filled
	Quantity float64
	Price    float64
}

// EntryRejection is sent to a strategy when its entry signal is rejected
// downstream (risk, position gate, broker, etc.). The strategy should clear
// its PendingEntry state so it can re-evaluate on the next bar.
type EntryRejection struct {
	Symbol string
	Side   Side // the side that was rejected
	Reason string
}
