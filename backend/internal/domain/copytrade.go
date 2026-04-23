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
}
