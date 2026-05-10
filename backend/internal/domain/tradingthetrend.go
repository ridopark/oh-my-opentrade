package domain

import "time"

// TradingTheTrendSignalPayload is the domain-event payload for a parsed
// TradingTheTrend Discord watchlist line arriving from the
// discord-tradingthetrend sidecar. Unlike CopytradeSignalPayload, the source
// message carries no expiry, fill price, or action — those are resolved by
// the strategy (nearest weekly Friday for expiry, break-and-retest entry
// triggered by underlying crossing Trigger).
//
// The HTTP handler constructs this after authenticating the shared secret
// and publishes it on the event bus keyed by EventTradingTheTrendSignalReceived.
type TradingTheTrendSignalPayload struct {
	SignalID  string      // dedupe key: "tradingthetrend:<message_id>:<line_index>"
	MessageID string      // source Discord message id
	Author    string      // Discord username that posted the watchlist
	PostedAt  time.Time   // when the message was posted (UTC)
	Ticker    Symbol      // underlying equity
	Strike    float64     // option strike price
	Right     OptionRight // CALL | PUT
	Trigger   float64     // breakout level on the underlying — buy when price >= Trigger
	RawLine   string      // the line as parsed, for audit
}
