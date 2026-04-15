package domain

import (
	"time"

	"github.com/google/uuid"
)

// OrderIntentJournalStatus constants for the order_intents journal table.
// Kept as plain string constants (not a Go enum) so adding a new state
// doesn't require a schema migration.
const (
	OrderIntentJournalPendingSubmit = "pending_submit"
	OrderIntentJournalSubmitted     = "submitted"
	OrderIntentJournalFilled        = "filled"
	OrderIntentJournalCanceled      = "canceled"
	OrderIntentJournalRejected      = "rejected"
	OrderIntentJournalExpired       = "expired"
	OrderIntentJournalLost          = "lost"
)

// OrderIntentJournalRow is the persisted shape of a row in the order_intents
// write-ahead journal. It carries enough context for startup reconciliation
// to decide whether a broker-side working order should be resumed, alerted
// on as unmanaged, or marked lost.
//
// This is separate from OrderIntent itself because it includes lifecycle
// state owned by the journal (status, timestamps, broker_order_id) that
// does not belong on the immutable intent domain object.
type OrderIntentJournalRow struct {
	ID              uuid.UUID
	IdempotencyKey  string
	TenantID        string
	EnvMode         EnvMode
	Symbol          Symbol
	Direction       Direction
	AssetClass      AssetClass
	Venue           Venue // execution venue; empty => DefaultVenue(AssetClass)
	OrderType       string
	TimeInForce     string
	Quantity        float64
	LimitPrice      float64
	StopLoss        float64
	MaxSlippageBPS  int
	Strategy        string
	Confidence      float64
	MaxLossUSD      float64
	InstrumentKind  string
	InstrumentJSON  []byte // raw JSONB bytes (null for equity)
	Status          string
	BrokerOrderID   string
	SubmitError     string
	FilledQty       float64
	FilledAvgPrice  float64
	CreatedAt       time.Time
	SubmittedAt     *time.Time
	TerminalAt      *time.Time
	Meta            []byte // raw JSONB metadata
}
