package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MarketBar represents a single OHLCV candle for a symbol and timeframe.
type MarketBar struct {
	Time      time.Time `json:"time"`
	Symbol    Symbol    `json:"symbol"`
	Timeframe Timeframe `json:"timeframe"`
	Open      float64   `json:"open"`
	High      float64   `json:"high"`
	Low       float64   `json:"low"`
	Close     float64   `json:"close"`
	Volume    float64   `json:"volume"`
	Suspect   bool      `json:"suspect,omitempty"`

	// Venue identifies the market-data venue this bar came from. Optional:
	// empty preserves pre-Gap-10 behavior (callers resolve implicit venue
	// from AssetClass via DefaultVenue). Cross-venue crypto strategies
	// populate this so they can distinguish same-symbol bars across venues.
	Venue Venue `json:"venue,omitempty"`

	// Microstructure metadata from broker feed.
	TradeCount uint64 `json:"tradeCount,omitempty"` // number of trades in this bar (0 if unavailable)

	// Spike repair metadata — populated by AdaptiveFilter when High/Low are clamped.
	Repaired     bool    `json:"repaired,omitempty"`     // true if High/Low were clamped by the adaptive spike filter
	OriginalHigh float64 `json:"originalHigh,omitempty"` // pre-repair High (0 if not repaired)
	OriginalLow  float64 `json:"originalLow,omitempty"`  // pre-repair Low (0 if not repaired)

	// Enriched indicator data — populated asynchronously from EnrichedBar events.
	// Zero values mean "not available". AVWAPs nil means "not available".
	EMA9   float64            `json:"ema9,omitempty"`
	EMA21  float64            `json:"ema21,omitempty"`
	EMA50  float64            `json:"ema50,omitempty"`
	EMA200 float64            `json:"ema200,omitempty"`
	AVWAPs map[string]float64 `json:"avwaps,omitempty"`
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
	// Venue is the execution venue this intent is routed to. Optional:
	// when empty, executors fall back to DefaultVenue(AssetClass). Perp
	// and cross-venue crypto strategies must set this explicitly.
	Venue Venue `json:"venue,omitempty"`

	// LegGroupID groups multiple OrderIntents that must be executed
	// atomically as a unit. Used by cross-venue strategies (e.g., basis
	// carry: buy spot + short perp). When non-empty, the execution layer
	// treats all intents with the same LegGroupID as a paired trade —
	// if any leg fails, remaining legs are rolled back.
	LegGroupID string `json:"legGroupId,omitempty"`

	// Sprint 5 combo BAG support. When Legs is non-empty this intent represents
	// a multi-leg BAG order and IsCombo() returns true. Symbol is the underlying
	// ticker, Quantity is the combo count (1 combo = full leg ratios). ComboType
	// classifies the structure (e.g. vertical_call_debit). Zero values preserve
	// pre-Sprint-5 behavior for every existing code path.
	Legs      []ComboLeg `json:"legs,omitempty"`
	ComboType ComboType  `json:"comboType,omitempty"`
}

// ResolvedVenue returns the explicit Venue when set, otherwise the implicit
// default derived from AssetClass. Use this at adapter boundaries so we
// never hand a broker an empty venue.
func (o OrderIntent) ResolvedVenue() Venue {
	if !o.Venue.IsUnspecified() {
		return o.Venue
	}
	return DefaultVenue(o.AssetClass)
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
	Broker         string            `json:"broker,omitempty"`
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
	EMA50    float64 `json:"ema50,omitempty"`
	EMA200   float64 `json:"ema200,omitempty"`
	Bias     string  `json:"bias,omitempty"`
	NR7      bool    `json:"nr7,omitempty"`      // prior day had narrowest range in 7 sessions
	DailyATR float64 `json:"dailyATR,omitempty"` // ATR(14) computed from daily bars
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
	BBUpper       float64                    `json:"bbUpper,omitempty"`
	BBMiddle      float64                    `json:"bbMiddle,omitempty"`
	BBLower       float64                    `json:"bbLower,omitempty"`
	BBPercentB    float64                    `json:"bbPercentB,omitempty"`
	BBBandwidth   float64                    `json:"bbBandwidth,omitempty"`
	MACDLine      float64                    `json:"macdLine,omitempty"`
	MACDSignal    float64                    `json:"macdSignal,omitempty"`
	MACDHistogram float64                    `json:"macdHistogram,omitempty"`
	ADX           float64                    `json:"adx,omitempty"`
	RegimeScore   float64                    `json:"regimeScore,omitempty"`
	AnchorRegimes map[Timeframe]MarketRegime `json:"anchorRegimes,omitempty"`
	HTF           map[Timeframe]HTFData      `json:"htf,omitempty"`
}

// HTFDailyATR returns the daily ATR from HTF data, or 0 if not available.
func (s IndicatorSnapshot) HTFDailyATR() float64 {
	if htf, ok := s.HTF[Timeframe("1d")]; ok {
		return htf.DailyATR
	}
	return 0
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
	Venue       Venue // execution venue for this fill; empty => DefaultVenue(AssetClass)
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

// SignedQuantity returns the position-signed quantity: positive for long,
// negative for short, derived from Side. The canonical Trade contract is
// non-negative Quantity + Side — NewTrade enforces this. Use this helper
// whenever you need arithmetic that must preserve direction (net positions,
// short detection, exposure sign), rather than reading Quantity directly.
func (t Trade) SignedQuantity() float64 {
	q := math.Abs(t.Quantity)
	switch strings.ToLower(t.Side) {
	case "sell", "short":
		return -q
	default:
		return q
	}
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
	Venue         Venue // execution venue; empty => inferred from asset class

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
// Persisted to market_trades by the AsyncTradeWriter (Phase 0); also feeds
// formingbar candle construction and the strategy runner's tick handler in
// flight.
type MarketTrade struct {
	Time   time.Time `json:"time"`
	Symbol Symbol    `json:"symbol"`
	Price  float64   `json:"price"`
	Size   float64   `json:"size"`
	// TakerSide indicates which side was the aggressor in the trade.
	// Values: "buy" (taker bought, aggressive buyer lifted the offer),
	//         "sell" (taker sold, aggressive seller hit the bid),
	//         ""    (unknown / not provided by the feed).
	// Required for microstructure gating (e.g. Trade-Flow Imbalance / TFI)
	// on crypto strategies where venues publish taker side explicitly.
	TakerSide string `json:"taker_side,omitempty"`
	// Venue identifies where this trade tick originated. Optional; crypto
	// cross-venue strategies set it to disambiguate the same pair across
	// feeds. Equity paths leave it empty.
	Venue Venue `json:"venue,omitempty"`
	// Exchange is the equity-tape exchange code. "D" identifies FINRA ADF
	// (dark-pool / off-exchange) prints, which the live DP aggregator
	// (Phase 4) keys on. Empty for crypto.
	Exchange string `json:"exchange,omitempty"`
	// Conditions are SIP trade condition codes (Form-T late prints,
	// regular-way, odd-lot, etc). Persisted so the audit path can replay
	// the same exclusion logic the live DP aggregator applies.
	Conditions []string `json:"conditions,omitempty"`
	// Tape identifies the listing tape ("A"=NYSE, "B"=AMEX/regional,
	// "C"=Nasdaq). Useful for cross-feed reconciliation.
	Tape string `json:"tape,omitempty"`
}

// AuctionImbalanceSnapshot represents NYSE closing auction imbalance data from IBKR tick type 225.
type AuctionImbalanceSnapshot struct {
	Time      time.Time
	Symbol    Symbol
	Volume    float64 // Auction volume (tick 29)
	Price     float64 // Auction indicative price (tick 30)
	Imbalance float64 // Signed imbalance: positive=buy, negative=sell (tick 31)
}

// DarkPoolBar holds aggregated dark pool metrics for a 5-minute window.
// Built from individual trade ticks where exchange == "D" (FINRA ADF).
type DarkPoolBar struct {
	Time             time.Time
	Symbol           Symbol
	Timeframe        Timeframe
	DPVolume         float64 // dark pool volume (shares)
	DPTrades         int     // count of DP prints
	DPVWAP           float64 // DP-only VWAP
	LitVolume        float64 // lit exchange volume
	TotalVolume      float64 // all exchanges
	DPRatio          float64 // DPVolume / TotalVolume
	BuyVolume        float64 // inferred buys (price >= running VWAP)
	SellVolume       float64 // inferred sells (price < running VWAP)
	LargePrintVolume float64 // prints with notional > $200K
	LargePrintCount  int     // count of large prints
	MaxPrintSize     float64 // largest single DP print (shares)
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

// WhaleFiling represents a single holding row from a 13F-HR filing.
type WhaleFiling struct {
	FilingDate      time.Time
	FilerCIK        string
	FilerName       string
	CUSIP           string
	Ticker          string
	IssuerName      string
	ShareCount      int64
	MarketValue1000 int64  // in thousands (SEC native format)
	PutCall         string // "PUT", "CALL", or ""
	FilerTier       int    // 1=high-conviction, 2=standard
}

// WhaleAccumulation holds the pre-computed accumulation score for a symbol in a quarter.
type WhaleAccumulation struct {
	QuarterEnd     time.Time
	Ticker         string
	Score          int
	NewPositions   int
	Additions50Pct int
	Additions25Pct int
	Reductions     int
	TotalFilers    int
	TopFilerDetail []byte // JSON: [{cik, name, action, pct_change}]
}

// CUSIPMapping caches a CUSIP-to-ticker resolution from OpenFIGI.
type CUSIPMapping struct {
	CUSIP      string
	Ticker     string
	FIGI       string
	ResolvedAt time.Time
}
