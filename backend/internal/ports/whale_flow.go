package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// Transfer represents a large on-chain transfer between wallets.
type Transfer struct {
	TxHash    string
	Asset     string       // "BTC", "ETH", "SOL"
	From      string       // address or tagged name
	FromTag   string       // "blackrock_custody", "binance_hot", etc.
	To        string
	ToTag     string
	AmountUSD float64
	Amount    float64      // native units
	Timestamp time.Time
	Venue     domain.Venue // inferred venue if exchange-related
}

// NetFlowResult is the aggregated net flow for an asset over a window.
type NetFlowResult struct {
	Asset      string
	WindowHrs  int
	NetFlowUSD float64   // positive = net inflow to exchanges (bearish), negative = net outflow (bullish)
	InFlowUSD  float64
	OutFlowUSD float64
	LargeCount int       // number of transfers > $1M
	Timestamp  time.Time // when this was computed
}

// WhaleFlowPort provides access to on-chain whale/custodian flow data for
// directional confluence scoring. Positive net flow indicates selling pressure
// (tokens moving to exchanges); negative indicates accumulation (withdrawals).
type WhaleFlowPort interface {
	// NetFlow returns the net exchange flow for an asset over the given window.
	// Positive = net inflow to exchanges (selling pressure), negative = outflow (accumulation).
	NetFlow(ctx context.Context, asset string, windowHrs int) (NetFlowResult, error)

	// LargeTransfers returns individual large transfers above minUSD in the window.
	LargeTransfers(ctx context.Context, asset string, windowHrs int, minUSD float64) ([]Transfer, error)
}
