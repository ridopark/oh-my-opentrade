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

// CrossSectionalStrategy extends Strategy with a batch-bar callback.
// Instead of receiving bars one symbol at a time via OnBar, the runner
// buffers bars until the full universe is observed at a given timestamp,
// then dispatches the complete cross-section via OnCrossSectionalBar.
//
// Strategies implementing this interface opt into the xsec runner path
// automatically. The single-symbol OnBar method should return (state, nil, nil)
// as a no-op — all logic lives in OnCrossSectionalBar.
type CrossSectionalStrategy interface {
	Strategy
	// OnCrossSectionalBar is called once per timestamp with the complete
	// universe of bars at that time. bars is keyed by symbol string.
	// The strategy can rank, score, and emit signals for any subset of symbols.
	OnCrossSectionalBar(ctx Context, ts time.Time, bars map[string]Bar, st State) (State, []Signal, error)

	// Universe returns the set of symbols this strategy covers.
	// Used by the runner to know when a cross-section is complete.
	Universe() []string
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

// SignalProgressEmitter is an optional interface that State implementations
// can satisfy to provide signal progress snapshots after warmup.
type SignalProgressEmitter interface {
	// EmitSignalProgress returns domain events (EntryGated, ORBPhaseUpdate)
	// representing the current signal formation state. Called once after warmup
	// to seed the SSE cache so the dashboard has immediate data.
	EmitSignalProgress() []any // returns payload values (not domain.Event)
}

// EnvMode is defined locally (shadowing domain.EnvMode) to keep the
// strategy contract package free of domain imports.
type EnvMode string

const (
	EnvModePaper EnvMode = "Paper"
	EnvModeLive  EnvMode = "Live"
)

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

	// ProgressEventsSuppressed returns true when the runner drops
	// EntryGated/ORBPhaseUpdate telemetry events (offline replay/backtest).
	// Strategies can use it to skip building the payload structs entirely,
	// saving both allocations and gate-evaluation work in the common case
	// where no SSE consumer is listening.
	ProgressEventsSuppressed() bool

	// EnvMode returns the execution environment the strategy is running in.
	// Backtests run as EnvModePaper with tenantID="backtest".
	EnvMode() EnvMode

	// IsBacktest reports whether the strategy is running inside the backtest
	// harness (sharded slice replay or legacy heap dispatch). Strategies that
	// rely on a PendingEntry -> PositionSide handshake driven by broker fill
	// confirmations need this signal: in backtest the simbroker fills at bar
	// close and the FillReceived event may not reach OnEvent before subsequent
	// OnBars (sharded path defers signal publication until replayFlat). When
	// IsBacktest is true the strategy may transition optimistically on emit
	// and rely on EntryRejection to roll back. Live and paper trading return
	// false so live broker semantics are preserved.
	//
	// This is a tactical fix (Option B in
	// _workspace/whale_pullback_v1_backtest_fill_event_plan.md). The
	// architecturally clean fix is Section 4 of that plan — synchronous
	// in-shard fill loop in the backtest engine — at which point this flag
	// becomes unnecessary and strategies no longer need to know which
	// harness runs them.
	IsBacktest() bool
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
	EMA200        float64
	BBUpper       float64
	BBMiddle      float64
	BBLower       float64
	BBPercentB    float64
	BBBandwidth   float64
	MACDLine      float64
	MACDSignal    float64
	MACDHistogram float64
	ADX           float64
	RegimeScore   float64
	AnchorRegimes map[string]AnchorRegime
	HTF           map[string]HTFIndicator

	// Dark pool microstructure (populated from darkpool_bars when available)
	DPRatio            float64 // dark pool volume / total volume (0-1)
	DPBuyRatio         float64 // DP buy volume / DP total volume
	DPLargePrintPct    float64 // large print volume / DP volume
	DPRatioZScore      float64 // (current DP ratio - mean) / std over rolling lookback
	DPSupportLevel     float64 // nearest DP volume shelf below price (DPVWAP)
	DPResistanceLevel  float64 // nearest DP volume shelf above price (DPVWAP)

	// Late-session dark pool Z-score: yesterday's 14:00-15:30 ET buy_ratio
	// normalized over 20 trading days. Negative = abnormally low DP buying
	// (bullish reversal for mean-reversion strategies like AVWAP).
	LateSessionDPZ float64

	// Late-session large print imbalance Z: institutional block flow
	// direction (large_print_volume * (2*buy_ratio - 1)) Z-normalized.
	LateSessionLPZ float64

	// Late-session net flow Z: (buy_volume - sell_volume) Z-normalized.
	// Preserves absolute volume magnitude unlike the ratio formulation.
	LateSessionNetFlowZ float64

	// Late-session DP volume ratio Z: dp_volume/(dp_volume+lit_volume) Z-normalized.
	// Signing-free — avoids buy/sell misclassification bias.
	LateSessionDPVolRatioZ float64

	// Whale accumulation score (populated from whale_accumulation when available)
	WhaleScore int // aggregate 13F accumulation score (0 = no data)
}

type HTFIndicator struct {
	EMA50    float64
	EMA200   float64
	Bias     string
	DailyATR float64 // ATR(14) from daily bars — available at bar #1
	NR7      bool    // prior day had narrowest range in 7 sessions
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

// AuctionImbalanceUpdate is forwarded to strategies when NYSE auction imbalance data arrives.
type AuctionImbalanceUpdate struct {
	Symbol    string
	Volume    float64
	Price     float64
	Imbalance float64 // positive = buy imbalance, negative = sell imbalance
}

// TradeTick is forwarded to strategies when a trade tick (MarketTrade) arrives
// for one of their subscribed symbols. Strategies that care about aggressor-
// side microstructure (e.g. crypto TFI gating) handle it in OnEvent; others
// can ignore it. Intentionally decoupled from domain.MarketTrade so the
// strategy package stays free of domain imports.
type TradeTick struct {
	Symbol    string
	Time      time.Time
	Price     float64
	Size      float64
	TakerSide string // "buy", "sell", or "" if unknown
	Venue     string
}

// CopytradeExitRejection is forwarded to the copytrade strategy when the
// position monitor refuses an exit request (e.g. prior exit already in flight).
// The strategy rolls RemainingFrac back by the original Fraction so its view
// matches the broker's actual closing position.
type CopytradeExitRejection struct {
	ContractSymbol string
	Fraction       float64
	Reason         string
}

// CopytradeSignal is forwarded to the copytrade strategy when the sidecar
// posts a parsed Discord message. Kept string-typed so this package does not
// import domain.
type CopytradeSignal struct {
	SignalID  string
	MessageID string
	Author    string
	PostedAt  time.Time
	Action    string // "BTO" | "STC" | "AVG"
	Ticker    string
	Expiry    time.Time
	Strike    float64
	Right     string // "C" | "P"
	Price     float64
	Tail      string
	RawLine   string
}
