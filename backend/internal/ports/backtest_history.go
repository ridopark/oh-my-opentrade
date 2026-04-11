package ports

import (
	"context"
	"time"
)

// BacktestHistoryPort is the durable journal of completed backtest runs. It
// is written to once per run (at completion) and read by the dashboard history
// page for list/compare/drill-in views. Lives on its own narrow interface so
// the backtest hot path can depend on just the Save method via a function
// callback if desired, and the HTTP read layer can depend on the full surface.
type BacktestHistoryPort interface {
	Save(ctx context.Context, row BacktestRunRow, trades []BacktestTradeRow) error
	List(ctx context.Context, filter BacktestListFilter) ([]BacktestRunSummary, int, error)
	Get(ctx context.Context, id string) (*BacktestRunDetail, error)
	SetTags(ctx context.Context, id string, tags []string) error
	SetPinned(ctx context.Context, id string, pinned bool) error
}

// BacktestEquityPoint is a single daily sample on the equity curve.
type BacktestEquityPoint struct {
	T  int64   `json:"t"`  // unix seconds, end of trading day
	Eq float64 `json:"eq"` // equity at that point
}

// BacktestRunRow is the full write payload for the parent backtest_runs row.
type BacktestRunRow struct {
	ID             string
	RanAt          time.Time
	Strategies     []string
	Symbols        []string
	PeriodStart    time.Time
	PeriodEnd      time.Time
	InitialEquity  float64
	SlippageBPS    int
	NoAI           bool
	PF             float64
	WinRate        float64
	Expectancy     float64
	MaxDrawdown    float64
	Sharpe         float64
	TradeCount     int
	WinCount       int
	LossCount      int
	NetPnL         float64
	TotalReturn    float64
	FinalEquity    float64
	EquityCurve    []BacktestEquityPoint
	DNASnapshot    map[string]any // strategyID -> params map
	Tags           []string
}

// BacktestTradeRow is one fill stored in backtest_run_trades. Mirrors
// collector.TradeRecord. Used for both write (Save) and read (Get) paths.
type BacktestTradeRow struct {
	Seq           int       `json:"seq"`
	Symbol        string    `json:"symbol"`
	Side          string    `json:"side"`
	Direction     string    `json:"direction,omitempty"`
	Quantity      float64   `json:"quantity"`
	Price         float64   `json:"price"`
	FilledAt      time.Time `json:"filled_at"`
	PnL           float64   `json:"pnl,omitempty"`
	StrategyID    string    `json:"strategy_id,omitempty"`
	Rationale     string    `json:"rationale,omitempty"`
	Regime        string    `json:"regime,omitempty"`
	VIXBucket     string    `json:"vix_bucket,omitempty"`
	MarketContext string    `json:"market_context,omitempty"`
}

// BacktestListFilter drives GET /backtest/history. All fields are optional;
// zero value means "no filter on this axis".
type BacktestListFilter struct {
	Strategies []string
	Symbols    []string
	From       time.Time
	To         time.Time
	MinPF      float64
	Tags       []string
	PinnedOnly bool
	Search     string
	Limit      int
	Offset     int
	OrderBy    string // ran_at | pf | win_rate | expectancy | max_drawdown | trade_count | net_pnl
	OrderDir   string // asc | desc
}

// BacktestRunSummary is the lightweight list-view payload (no equity curve,
// no trades, no DNA).
type BacktestRunSummary struct {
	ID           string                `json:"id"`
	RanAt        time.Time             `json:"ran_at"`
	Strategies   []string              `json:"strategies"`
	Symbols      []string              `json:"symbols"`
	PeriodStart  time.Time             `json:"period_start"`
	PeriodEnd    time.Time             `json:"period_end"`
	PF           float64               `json:"pf"`
	WinRate      float64               `json:"win_rate"`
	Expectancy   float64               `json:"expectancy"`
	MaxDrawdown  float64               `json:"max_drawdown"`
	Sharpe       float64               `json:"sharpe"`
	TradeCount   int                   `json:"trade_count"`
	NetPnL       float64               `json:"net_pnl"`
	TotalReturn  float64               `json:"total_return"`
	EquityCurve  []BacktestEquityPoint `json:"equity_curve"` // full daily series — small enough to include in list
	Tags         []string              `json:"tags"`
	Pinned       bool                  `json:"pinned"`
}

// BacktestRunDetail is the drill-in payload.
type BacktestRunDetail struct {
	Summary       BacktestRunSummary    `json:"summary"`
	InitialEquity float64               `json:"initial_equity"`
	FinalEquity   float64               `json:"final_equity"`
	SlippageBPS   int                   `json:"slippage_bps"`
	NoAI          bool                  `json:"no_ai"`
	WinCount      int                   `json:"win_count"`
	LossCount     int                   `json:"loss_count"`
	DNASnapshot   map[string]any        `json:"dna_snapshot"`
	Notes         string                `json:"notes"`
	Trades        []BacktestTradeRow    `json:"trades"`
}
