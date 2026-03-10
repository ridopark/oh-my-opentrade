package domain

import (
	"encoding/json"
	"errors"
	"time"
)

// TradeRealizedPayload is the event payload emitted when a position is fully or
// partially closed, carrying the realized P&L for the exit fill. It is used by
// the notification service to display P&L in messaging platforms.
type TradeRealizedPayload struct {
	Symbol       Symbol
	Side         string  // "SELL" (or "BUY" for short covers in the future)
	Quantity     float64 // quantity sold in this fill
	ExitPrice    float64 // fill price of the exit
	EntryPrice   float64 // weighted average entry price
	RealizedPnL  float64 // absolute P&L in dollars: (exit - entry) * qty
	PnLPct       float64 // percentage P&L: (exit - entry) / entry * 100
	Strategy     string
	HoldDuration time.Duration // time between entry and exit
}

// StrategyDailyPnL tracks per-strategy realized P&L for a single trading day.
// Stored in the strategy_daily_pnl table, keyed by (TenantID, EnvMode, Strategy, Day).
type StrategyDailyPnL struct {
	Day         time.Time
	TenantID    string
	EnvMode     EnvMode
	Strategy    string
	RealizedPnL float64
	Fees        float64
	TradeCount  int
	WinCount    int
	LossCount   int
	GrossProfit float64
	GrossLoss   float64
}

// NewStrategyDailyPnL creates a validated StrategyDailyPnL. TradeCount must not be negative.
func NewStrategyDailyPnL(day time.Time, tenantID string, envMode EnvMode, strategy string, realizedPnL, fees float64, tradeCount, winCount, lossCount int, grossProfit, grossLoss float64) (StrategyDailyPnL, error) {
	if tradeCount < 0 {
		return StrategyDailyPnL{}, errors.New("trade count cannot be negative")
	}
	if strategy == "" {
		return StrategyDailyPnL{}, errors.New("strategy is required")
	}
	return StrategyDailyPnL{
		Day:         day,
		TenantID:    tenantID,
		EnvMode:     envMode,
		Strategy:    strategy,
		RealizedPnL: realizedPnL,
		Fees:        fees,
		TradeCount:  tradeCount,
		WinCount:    winCount,
		LossCount:   lossCount,
		GrossProfit: grossProfit,
		GrossLoss:   grossLoss,
	}, nil
}

// StrategyEquityPoint represents a single point on a per-strategy equity curve.
// Stored in the strategy_equity_points hypertable.
type StrategyEquityPoint struct {
	Time              time.Time
	TenantID          string
	EnvMode           EnvMode
	Strategy          string
	Equity            float64
	RealizedPnLToDate float64
	FeesToDate        float64
	TradeCountToDate  int
}

// NewStrategyEquityPoint creates a validated StrategyEquityPoint.
func NewStrategyEquityPoint(t time.Time, tenantID string, envMode EnvMode, strategy string, equity, realizedPnLToDate, feesToDate float64, tradeCountToDate int) (StrategyEquityPoint, error) {
	if strategy == "" {
		return StrategyEquityPoint{}, errors.New("strategy is required")
	}
	return StrategyEquityPoint{
		Time:              t,
		TenantID:          tenantID,
		EnvMode:           envMode,
		Strategy:          strategy,
		Equity:            equity,
		RealizedPnLToDate: realizedPnLToDate,
		FeesToDate:        feesToDate,
		TradeCountToDate:  tradeCountToDate,
	}, nil
}

// SignalStatus represents the lifecycle status of a strategy signal.
type SignalStatus string

const (
	SignalStatusGenerated      SignalStatus = "generated"
	SignalStatusValidated      SignalStatus = "validated"
	SignalStatusExecuted       SignalStatus = "executed"
	SignalStatusSuppressed     SignalStatus = "suppressed"
	SignalStatusRejected       SignalStatus = "rejected"
	SignalStatusDebateOverride SignalStatus = "debate_override"
)

// StrategySignalEvent is an append-only record tracking a signal's lifecycle.
// Stored in the strategy_signal_events hypertable.
type StrategySignalEvent struct {
	TS         time.Time
	TenantID   string
	EnvMode    EnvMode
	Strategy   string
	SignalID   string
	Symbol     string
	Kind       string // entry, exit, scale_in, scale_out
	Side       string // BUY, SELL
	Status     SignalStatus
	Reason     string
	Confidence float64
	Payload    json.RawMessage
}

// NewStrategySignalEvent creates a validated StrategySignalEvent.
func NewStrategySignalEvent(ts time.Time, tenantID string, envMode EnvMode, strategy, signalID, symbol, kind, side string, status SignalStatus, reason string, confidence float64, payload json.RawMessage) (StrategySignalEvent, error) {
	if strategy == "" {
		return StrategySignalEvent{}, errors.New("strategy is required")
	}
	if signalID == "" {
		return StrategySignalEvent{}, errors.New("signal ID is required")
	}
	if symbol == "" {
		return StrategySignalEvent{}, errors.New("symbol is required")
	}
	return StrategySignalEvent{
		TS:         ts,
		TenantID:   tenantID,
		EnvMode:    envMode,
		Strategy:   strategy,
		SignalID:   signalID,
		Symbol:     symbol,
		Kind:       kind,
		Side:       side,
		Status:     status,
		Reason:     reason,
		Confidence: confidence,
		Payload:    payload,
	}, nil
}

// StateSnapshot is a point-in-time snapshot of a strategy's internal state
// for a single symbol. Used for the strategy state dashboard.
type StateSnapshot struct {
	Strategy string          `json:"strategy"`
	Symbol   string          `json:"symbol"`
	Kind     string          `json:"kind"` // e.g., "orb_session", "avwap_state", "ai_scalper_state"
	AsOf     time.Time       `json:"asOf"`
	Payload  json.RawMessage `json:"payload"`
}

// StrategySummaryRow holds aggregated metrics for one strategy in a comparison list.
type StrategySummaryRow struct {
	Strategy    string  `json:"strategy"`
	RealizedPnL float64 `json:"realized_pnl"`
	Fees        float64 `json:"fees"`
	TotalTrades int     `json:"total_trades"`
	WinCount    int     `json:"win_count"`
	LossCount   int     `json:"loss_count"`
	GrossProfit float64 `json:"gross_profit"`
	GrossLoss   float64 `json:"gross_loss"`
}

// StrategyDashboard aggregates per-strategy performance metrics for the API.
type StrategyDashboard struct {
	Strategy    string                `json:"strategy"`
	Summary     StrategyPerfSummary   `json:"summary"`
	DailyPnL    []StrategyDailyPnL    `json:"dailyPnl"`
	EquityCurve []StrategyEquityPoint `json:"equityCurve"`
	BySymbol    []SymbolAttribution   `json:"bySymbol"`
}

// StrategyPerfSummary holds computed summary metrics for a strategy.
type StrategyPerfSummary struct {
	TotalRealizedPnL float64  `json:"totalRealizedPnl"`
	TotalFees        float64  `json:"totalFees"`
	TotalTrades      int      `json:"totalTrades"`
	WinCount         int      `json:"winCount"`
	LossCount        int      `json:"lossCount"`
	WinRate          float64  `json:"winRate"`
	ProfitFactor     float64  `json:"profitFactor"`
	GrossProfit      float64  `json:"grossProfit"`
	GrossLoss        float64  `json:"grossLoss"`
	MaxDrawdown      *float64 `json:"maxDrawdown,omitempty"`
	Sharpe           *float64 `json:"sharpe,omitempty"`
}

// SymbolAttribution breaks down P&L by symbol within a strategy.
type SymbolAttribution struct {
	Symbol      string  `json:"symbol"`
	RealizedPnL float64 `json:"realizedPnl"`
	TradeCount  int     `json:"tradeCount"`
	WinCount    int     `json:"winCount"`
	LossCount   int     `json:"lossCount"`
}
