package domain

import (
	"errors"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// backtestSeq is a monotonic counter used by NewBacktestEvent to generate
// cheap, unique event IDs without UUID allocation overhead.
var backtestSeq atomic.Uint64

// fastEventIDs, when set, makes NewEvent use the same monotonic counter as
// NewBacktestEvent instead of uuid.NewString(). uuid.NewString() draws from
// crypto/rand, which showed up as ~10% of backtest CPU (one getrandom syscall
// per event, 400k+ events per run). Only enable this from batch/offline
// binaries that don't need cryptographically unique IDs — live omo-core
// leaves it off so the production pipeline still gets real UUIDs.
var fastEventIDs atomic.Bool

// UseFastEventIDs toggles the fast ID path for NewEvent. Intended for
// backtest/replay binaries where event IDs are ephemeral. Safe to call at any
// time (atomic load per NewEvent call), but typically set once at startup.
func UseFastEventIDs(enabled bool) {
	fastEventIDs.Store(enabled)
}

// fastClockNano, when non-zero and fastEventIDs is on, is used as the
// OccurredAt source for NewEvent instead of time.Now(). Replay binaries
// call SetFastClock at the top of each bar so multiple per-bar events share
// one timestamp and we save ~500k time.Now() syscalls per backtest.
var fastClockNano atomic.Int64

// SetFastClock sets the clock the fast-ID NewEvent path reads. Pass 0 to
// revert to time.Now().
func SetFastClock(t time.Time) {
	fastClockNano.Store(t.UnixNano())
}

// EventType identifies the kind of domain event.
type EventType = string

// Domain event type constants covering the full trading pipeline.
const (
	EventMarketBarReceived       EventType = "MarketBarReceived"
	EventMarketBarSanitized      EventType = "MarketBarSanitized"
	EventMarketBarRejected       EventType = "MarketBarRejected"
	EventStateUpdated            EventType = "StateUpdated"
	EventRegimeShifted           EventType = "RegimeShifted"
	EventSetupDetected           EventType = "SetupDetected"
	EventDNAVersionDetected      EventType = "DNAVersionDetected"
	EventDNAApprovalRequested    EventType = "DNAApprovalRequested"
	EventDNAApproved             EventType = "DNAApproved"
	EventDNARejected             EventType = "DNARejected"
	EventActiveDNAChanged        EventType = "ActiveDNAChanged"
	EventDebateRequested         EventType = "DebateRequested"
	EventDebateCompleted         EventType = "DebateCompleted"
	EventOrderIntentCreated      EventType = "OrderIntentCreated"
	EventOrderIntentValidated    EventType = "OrderIntentValidated"
	EventOrderIntentRejected     EventType = "OrderIntentRejected"
	EventOrderSubmitted          EventType = "OrderSubmitted"
	EventOrderAccepted           EventType = "OrderAccepted"
	EventOrderRejected           EventType = "OrderRejected"
	EventFillReceived            EventType = "FillReceived"
	EventPositionUpdated         EventType = "PositionUpdated"
	EventKillSwitchEngaged       EventType = "KillSwitchEngaged"
	EventCircuitBreakerTripped   EventType = "CircuitBreakerTripped"
	EventOptionChainReceived     EventType = "OptionChainReceived"
	EventOptionContractSelected  EventType = "OptionContractSelected"
	EventSignalCreated           EventType = "SignalCreated"
	EventSignalDebateRequested   EventType = "SignalDebateRequested"
	EventSignalEnriched          EventType = "SignalEnriched"
	EventScreenerTicked          EventType = "ScreenerTicked"
	EventScreenerCompleted       EventType = "ScreenerCompleted"
	EventAIScreenerCompleted     EventType = "AIScreenerCompleted"
	EventEffectiveSymbolsUpdated EventType = "EffectiveSymbolsUpdated"
	EventStrategySignalLifecycle EventType = "StrategySignalLifecycle"
	EventStrategyStateSnapshot   EventType = "StrategyStateSnapshot"
	EventStrategyEvaluation      EventType = "StrategyEvaluation"
	EventExitTriggered           EventType = "ExitTriggered"
	EventExitOrderTerminal       EventType = "ExitOrderTerminal"
	EventRiskRevaluated          EventType = "RiskRevaluated"
	EventRiskDowngraded          EventType = "RiskDowngraded"
	EventSignalGated             EventType = "SignalGated"
	EventTradeReceived           EventType = "TradeReceived"
	EventTradeRealized           EventType = "TradeRealized"
	EventFormingBar              EventType = "FormingBar"
	EventFeedDegraded            EventType = "FeedDegraded"
	EventExitCircuitBroken       EventType = "ExitCircuitBroken"
	EventORBRangeSet             EventType = "ORBRangeSet"
	EventEnrichedBar             EventType = "EnrichedBar"
	EventEntryGated              EventType = "EntryGated"
	EventORBPhaseUpdate          EventType = "ORBPhaseUpdate"
	EventAuctionImbalance        EventType = "AuctionImbalance"

	// Connectivity & system events.
	EventBrokerAPIError          EventType = "BrokerAPIError"
	EventWSCircuitBreakerTripped EventType = "WSCircuitBreakerTripped"
	EventFillPollTimeout         EventType = "FillPollTimeout"
	EventStaleOrderCancelled     EventType = "StaleOrderCancelled"
	EventSystemStarted           EventType = "SystemStarted"
	EventIBKRConnected           EventType = "IBKRConnected"
	EventSymbolsActivated        EventType = "SymbolsActivated"

	// Copytrade: a Discord signal arrived from the discord-copytrade sidecar.
	EventCopytradeSignalReceived EventType = "CopytradeSignalReceived"

	// Copytrade: a strategy requests the position monitor to externally-arm
	// a CHANDELIER_TRAIL rule on an existing option position. Fired by the
	// copytrade strategy when the author's first STC-partial is processed.
	EventChandelierTrailArm EventType = "ChandelierTrailArm"

	// Copytrade: the copytrade strategy requests the position monitor to close
	// a fraction of an existing option position. Fired on each STC keyword
	// match. Routing is by OCC contract symbol; the fraction is stashed in
	// pos.CustomState and consumed by positionmonitor.triggerExit.
	EventCopytradeExitRequest EventType = "CopytradeExitRequest"

	// Copytrade: the position monitor refused an exit request because a prior
	// exit is already in flight for the target position. The strategy consumes
	// this event to roll RemainingFrac back so its view matches the broker.
	EventCopytradeExitRejected EventType = "CopytradeExitRejected"

	// Copytrade: the strategy swept a Pending position whose BTO never filled
	// within the configured TTL. Execution subscribes and cancels the matching
	// outstanding broker order so the slot is actually freed at the broker.
	EventCopytradeEntryExpired EventType = "CopytradeEntryExpired"

	// Copytrade: a BUY fill arrived for a contract with no matching Pending
	// position in the strategy (race: TTL sweep freed the slot just before the
	// broker fill). notify.Service subscribes and alerts operators via Discord.
	EventCopytradeOrphanFill EventType = "CopytradeOrphanFill"
)

type SymbolsActivatedPayload struct {
	Symbols []string
	Source  string
}

type FeedDegradedPayload struct {
	Feed   string
	Reason string
}

type BrokerAPIErrorPayload struct {
	Endpoint   string
	StatusCode int
	Message    string
}

type WSCircuitBreakerTrippedPayload struct {
	Feed              string
	ConsecutiveFails  int
	BlockedForSeconds float64
}

type FillPollTimeoutPayload struct {
	Symbol        Symbol
	BrokerOrderID string
	Strategy      string
	Direction     string
	Quantity      float64
}

type StaleOrderCancelledPayload struct {
	Symbol        Symbol
	BrokerOrderID string
	Strategy      string
	Direction     string
	AgeSeconds    float64
}

type ExitCircuitBrokenPayload struct {
	Symbol       Symbol
	Failures     int
	CooldownSecs float64
}

type SystemStartedPayload struct {
	Version         string
	EnvMode         string
	Broker          string
	StreamingSource string
	Symbols         []string
	EquityCount     int
	CryptoCount     int
	IBKRConnected   bool
	IBKRPaperMode   bool
	EMA50Succeeded  int
	EMA50Failed     []string
	EMA200Succeeded int
	EMA200Failed    []string
	Strategies      []string
	StrategySymbols map[string][]string
}

type IBKRConnectedPayload struct {
	Host           string
	Port           int
	ClientID       int
	PaperMode      bool
	MarketDataType int // 1=live, 3=delayed
	AccountID      string
}

type ORBRangeSetPayload struct {
	Symbol  Symbol
	High    float64
	Low     float64
	Bars    int
	HTFBias string
	ATRPct  float64
	NR7     bool
}

// EnrichedBarPayload bundles bar OHLCV with computed indicator values so
// SSE consumers receive a single event with price + indicators.
type EnrichedBarPayload struct {
	Time      int64              `json:"time"`
	Symbol    string             `json:"symbol"`
	Timeframe string             `json:"timeframe"`
	Open      float64            `json:"open"`
	High      float64            `json:"high"`
	Low       float64            `json:"low"`
	Close     float64            `json:"close"`
	Volume    float64            `json:"volume"`
	EMA9      float64            `json:"ema9,omitempty"`
	EMA21     float64            `json:"ema21,omitempty"`
	EMA50     float64            `json:"ema50,omitempty"`
	EMA200    float64            `json:"ema200,omitempty"`
	AVWAPs    map[string]float64 `json:"avwaps,omitempty"`
}

// BarSnapshot is a compact OHLCV snapshot embedded in signal progress events.
type BarSnapshot struct {
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

// EntryGatedPayload is emitted when a strategy evaluates an entry but a gate blocks it.
// It provides visibility into how close a signal is to triggering.
type EntryGatedPayload struct {
	Symbol        string                `json:"symbol"`
	Strategy      string                `json:"strategy"`
	SetupType     string                `json:"setupType"`
	GatesPassed   int                   `json:"gatesPassed"`
	GatesTotal    int                   `json:"gatesTotal"`
	BlockingGate  string                `json:"blockingGate"`
	BlockingDetail string               `json:"blockingDetail"`
	EntryChecks   []EntryCheckResult    `json:"entryChecks,omitempty"`
	Confluence    EntryGatedConfluence  `json:"confluence"`
	Indicators    EntryGatedIndicators  `json:"indicators"`
	AVWAPState    EntryGatedAVWAPState  `json:"avwapState,omitempty"`
	Bar           BarSnapshot           `json:"bar"`
}

type EntryGatedConfluence struct {
	Score          int    `json:"score"`
	MaxScore       int    `json:"maxScore"`
	Fib            bool   `json:"fib"`
	FibDetail      string `json:"fibDetail,omitempty"`
	KeyLevel       bool   `json:"keyLevel"`
	KeyLevelDetail string `json:"keyLevelDetail,omitempty"`
	Candle         bool   `json:"candle"`
	CandleDetail   string `json:"candleDetail,omitempty"`
	Band           bool   `json:"band"`
	// Components is the per-factor breakdown (fib, key_level, candle,
	// band, dp, inducement, whale, ...) the confluence scorer returned
	// for this evaluation. Each entry mirrors strategy.ComponentScore;
	// the field is a JSON DTO so domain/event.go avoids depending on
	// domain/strategy. Phase 2 of the parity plan: this is what makes
	// blocked rows comparable between live and backtest.
	Components []EntryGatedComponent `json:"components,omitempty"`
}

// EntryGatedComponent is one row of the confluence-factor breakdown
// captured at an EntryGated evaluation. Mirror of strategy.ComponentScore.
type EntryGatedComponent struct {
	Name   string  `json:"name"`
	Group  string  `json:"group"`
	Weight int     `json:"weight"`
	Value  float64 `json:"value,omitempty"`
	Fired  bool    `json:"fired"`
}

// EntryCheckResult describes the outcome of a single entry type evaluation.
// When Passed is false, Reason explains why the entry type did not fire.
// Proximity is a 0.0..1.0 "how close to passing" value for UI progress
// indicators; only populated for checks with a meaningful numeric signal
// (breakout hold_bars, pinch gap). Disabled / binary checks leave it at 0.
type EntryCheckResult struct {
	Name      string  `json:"name"` // "pinch", "cap_reclaim", "gap_reclaim", "pullback", "handoff", "breakout", "bounce"
	Passed    bool    `json:"passed"`
	Reason    string  `json:"reason"` // short human-readable reason
	Proximity float64 `json:"proximity,omitempty"`
}

type EntryGatedIndicators struct {
	RSI         float64            `json:"rsi"`
	VolumeRatio float64            `json:"volumeRatio"`
	AVWAPBias   string             `json:"avwapBias"`
	SlopeBPS    float64            `json:"slopeBPS"`
	AboveCount  map[string]int     `json:"aboveCount,omitempty"`
	BelowCount  map[string]int     `json:"belowCount,omitempty"`
}

// EntryGatedAVWAPState captures the AVWAP calc state at the bar an
// EntryGated event was emitted. Empty for strategies that don't use
// AnchoredVWAPCalc (e.g. MACD). Phase 2 of the parity plan: lets a
// SQL diff between live and backtest show whether an AVWAP anchor
// disagreed on slope or bar count at the same evaluation moment.
type EntryGatedAVWAPState struct {
	LastBarTime time.Time                 `json:"lastBarTime,omitempty"`
	AnchorCount int                       `json:"anchorCount"`
	Anchors     map[string]EntryGatedAnchor `json:"anchors,omitempty"`
}

// EntryGatedAnchor is one anchor's view at evaluation time.
type EntryGatedAnchor struct {
	VWAP      float64 `json:"vwap"`
	SlopeBPS  float64 `json:"slopeBPS"`
	BarCount  int     `json:"barCount"`
	VWAPCount int     `json:"vwapCount"`
	Active    bool    `json:"active"`
}

// ORBPhaseUpdatePayload is emitted on ORB state machine transitions.
type ORBPhaseUpdatePayload struct {
	Symbol     string            `json:"symbol"`
	Phase      string            `json:"phase"`
	Range      ORBPhaseRange     `json:"range"`
	Breakout   ORBPhaseBreakout  `json:"breakout"`
	Retest     ORBPhaseRetest    `json:"retest"`
	Confidence float64           `json:"confidence"`
	FVG        ORBPhaseFVG       `json:"fvg"`
	Bar        BarSnapshot       `json:"bar"`
}

type ORBPhaseRange struct {
	High          float64 `json:"high"`
	Low           float64 `json:"low"`
	Valid         bool    `json:"valid"`
	BarCount      int     `json:"barCount"`
	ExpectedBars  int     `json:"expectedBars"`
	WindowMinutes int     `json:"windowMinutes"`
}

type ORBPhaseBreakout struct {
	Direction  string  `json:"direction"`
	RVOL       float64 `json:"rvol"`
	BreakClose float64 `json:"breakClose"`
	BreakTime  string  `json:"breakTime,omitempty"`
}

type ORBPhaseRetest struct {
	Touched        bool    `json:"touched"`
	TouchPrice     float64 `json:"touchPrice"`
	BarsSinceBreak int     `json:"barsSinceBreak"`
	MaxRetestBars  int     `json:"maxRetestBars"`
	HoldConfirmed  bool    `json:"holdConfirmed"`
}

type ORBPhaseFVG struct {
	Active bool    `json:"active"`
	High   float64 `json:"high,omitempty"`
	Low    float64 `json:"low,omitempty"`
}

// Event represents a domain event in the trading pipeline.
// Events are immutable once created and carry an idempotency key
// to support exactly-once processing semantics.
type Event struct {
	ID             string
	Type           EventType
	TenantID       string
	EnvMode        EnvMode
	OccurredAt     time.Time
	IdempotencyKey string
	Payload        any
}

// NewEvent creates an Event with auto-generated ID and timestamp.
func NewEvent(eventType EventType, tenantID string, envMode EnvMode, idempotencyKey string, payload any) (*Event, error) {
	if eventType == "" {
		return nil, errors.New("event type is required")
	}
	if idempotencyKey == "" {
		return nil, errors.New("idempotency key is required")
	}
	var id string
	var occurredAt time.Time
	if fastEventIDs.Load() {
		// Drop the "bt-" prefix to save the concat alloc — event IDs are
		// opaque strings consumed only by log lines and handler-internal
		// dedup (which uses IdempotencyKey instead).
		id = strconv.FormatUint(backtestSeq.Add(1), 36)
		if cn := fastClockNano.Load(); cn != 0 {
			occurredAt = time.Unix(0, cn)
		} else {
			occurredAt = time.Now()
		}
	} else {
		id = uuid.NewString()
		occurredAt = time.Now()
	}
	return &Event{
		ID:             id,
		Type:           eventType,
		TenantID:       tenantID,
		EnvMode:        envMode,
		OccurredAt:     occurredAt,
		IdempotencyKey: idempotencyKey,
		Payload:        payload,
	}, nil
}

// NewBacktestEvent creates an Event optimized for high-throughput backtest
// replay. It replaces uuid.NewString() with a monotonic counter and accepts
// an explicit occurredAt time (the bar timestamp) instead of calling
// time.Now(). The produced Event is identical in structure and fully
// compatible with every handler that consumes domain.Event.
func NewBacktestEvent(eventType EventType, tenantID string, envMode EnvMode, idempotencyKey string, payload any, occurredAt time.Time) Event {
	return Event{
		ID:             "bt-" + strconv.FormatUint(backtestSeq.Add(1), 36),
		Type:           eventType,
		TenantID:       tenantID,
		EnvMode:        envMode,
		OccurredAt:     occurredAt,
		IdempotencyKey: idempotencyKey,
		Payload:        payload,
	}
}
