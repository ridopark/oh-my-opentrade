package domain

import "time"

// CopytradeAction enumerates the author's intent on a parsed Discord message.
type CopytradeAction string

const (
	CopytradeActionBTO CopytradeAction = "BTO" // buy to open
	CopytradeActionSTC CopytradeAction = "STC" // sell to close
	CopytradeActionAVG CopytradeAction = "AVG" // retrospective average-note (informational)
)

// CopytradeSignalPayload is the domain-event payload for a parsed Discord
// trade message arriving from the discord-copytrade sidecar. The HTTP handler
// constructs this after authenticating the shared secret, and publishes it on
// the event bus keyed by EventCopytradeSignalReceived.
type CopytradeSignalPayload struct {
	SignalID  string          // dedupe key: "<message_id>:<line_index>"
	MessageID string          // source Discord message id
	Author    string          // Discord username that posted the alert
	PostedAt  time.Time       // when the message was posted (UTC)
	Action    CopytradeAction // BTO | STC | AVG
	Ticker    Symbol          // underlying equity
	Expiry    time.Time       // option expiry at 00:00 UTC of that calendar date
	Strike    float64
	Right     OptionRight // CALL | PUT
	Price     float64     // author's stated fill price (premium)
	Tail      string      // raw trailing text (keywords like "half out", "partial")
	RawLine   string      // the line as parsed, for audit
}

// ChandelierTrailArmPayload is published by a strategy when it wants the
// position monitor to externally-arm a CHANDELIER_TRAIL rule attached to an
// existing option position. The strategy supplies the peak premium at arm
// time; the evaluator then tracks running peak and fires when current premium
// falls below peak * (1 - giveback_pct).
type ChandelierTrailArmPayload struct {
	TenantID       string
	EnvMode        string
	Strategy       string
	Symbol         string // underlying ticker (maps to position routing key)
	ContractSymbol string // OCC contract symbol of the specific option leg
	PeakPremium    float64
}

// CopytradeExitRequestPayload is published by the copytrade strategy when a
// parsed STC message should reduce or close an existing option position. The
// position monitor looks up the position by ContractSymbol, stashes Fraction
// in pos.CustomState["copytrade_exit_qty_frac"], and dispatches the existing
// triggerExit path which computes qty = ceil(pos.Quantity * fraction) and
// emits a correctly-shaped option OrderIntent. Fraction = 1.0 closes fully.
type CopytradeExitRequestPayload struct {
	TenantID       string
	EnvMode        string
	Strategy       string
	Symbol         string  // underlying ticker (for logging and routing lookup)
	ContractSymbol string  // OCC contract symbol of the specific option leg
	Fraction       float64 // fraction of REMAINING quantity to close, (0, 1]
	Reason         string  // audit trail: the keyword that matched, e.g. "half out"
	Author         string  // Discord author name — surfaced in exit intent Rationale
	RawLine        string  // raw Discord line — surfaced in exit intent Rationale
}

// CopytradeExitRejectedPayload tells the strategy its exit request was rejected
// by the position monitor (e.g. because a prior exit is already in flight).
// Fraction is the exact value from the original request so the strategy can roll
// its RemainingFrac back via RemainingFrac /= (1 - Fraction).
type CopytradeExitRejectedPayload struct {
	TenantID       string
	EnvMode        string
	Strategy       string
	ContractSymbol string
	Fraction       float64
	Reason         string // why it was rejected, e.g. "exit_in_flight"
}

// CopytradeEntryExpiredPayload is emitted by the strategy when it sweeps a
// Pending position whose BTO never filled within the TTL. Execution subscribes
// and cancels the matching outstanding broker order. Strategy leaves tenant/env
// blank on the envelope; the runner stamps them.
type CopytradeEntryExpiredPayload struct {
	StrategyID     string    `json:"strategyID"`
	ContractSymbol string    `json:"contractSymbol"`
	PositionKey    string    `json:"positionKey"`
	ExpiredAt      time.Time `json:"expiredAt"`
	AgeSeconds     float64   `json:"ageSeconds"`
}

// CopytradeOrphanFillPayload is emitted when a BUY fill arrives for a contract
// with no matching Pending strategy position — typically the TTL sweep just
// freed the slot a beat before the broker acknowledged the fill. notify.Service
// subscribes and pages operators via Discord; no auto-remediation is attempted.
type CopytradeOrphanFillPayload struct {
	StrategyID     string    `json:"strategyID"`
	ContractSymbol string    `json:"contractSymbol"`
	BrokerOrderID  string    `json:"brokerOrderID"`
	FillPrice      float64   `json:"fillPrice"`
	Qty            float64   `json:"qty"`
	ObservedAt     time.Time `json:"observedAt"`
}
