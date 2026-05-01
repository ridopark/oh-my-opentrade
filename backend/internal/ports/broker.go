package ports

import (
	"context"
	"errors"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// ErrOrderNotFound is returned by GetOrderDetails when the broker has no record
// of the order (e.g. it was canceled when the previous session disconnected).
var ErrOrderNotFound = errors.New("order not found at broker")

// ErrUnsupportedModify is returned by OrderModifier.ModifyOrder when the
// broker either does not implement modify-in-place at all (simbroker, alpaca
// stub) or cannot apply the modify because the order has already gone
// terminal at the broker. Callers MUST treat this as a soft signal to fall
// back to the cancel+place flow, never as a hard error.
var ErrUnsupportedModify = errors.New("broker does not support order modification")

// BrokerPort is the abstraction strategy code uses to submit orders
// and read account state. Multiple adapters satisfy it (SimBroker for
// backtest, IBKR for equities, Hyperliquid for crypto). LSP requires
// strategies that work against the port abstraction to behave the same
// way against any conforming implementation; the contract below names
// the invariants the brokerporttest harness enforces today.
//
// Enforced invariants (run on every adapter via brokerporttest.RunBrokerPortContract):
//
//   - SubmitOrder: with a valid OrderIntent (non-empty Symbol, positive
//     Quantity, set Direction, OrderType="market", non-zero LimitPrice),
//     returns a non-empty broker orderID and nil error.
//   - GetPosition: for a symbol with no position, returns (0, nil) —
//     not an error. Callers depend on this to compute deltas without
//     special-casing the "no position yet" path.
//   - GetPositions: on a fresh adapter (no orders submitted), returns
//     an empty slice and nil error. Reconciliation logic relies on
//     "empty list = no positions" being a valid post-startup state.
//   - GetOrderStatus: idempotent on terminal orders. Repeat calls
//     return the same status; the broker MUST NOT mutate the
//     observable status of an order between reads. Strategies poll
//     this for fill reconciliation; non-idempotent reads cause
//     double-reconciliation bugs.
//
// Known unconstrained (DO NOT rely on these in strategy code; they
// vary across adapters and the harness does not yet enforce parity):
//
//   - Fill timing. SimBroker emits OrderEventFill synchronously on
//     SubmitOrder's call stack; IBKR fills arrive asynchronously via
//     execReconciler polling at 2-second cadence. Strategies that
//     gate on PositionSide observe the transition on different bars.
//   - Partial-fill ordering. IBKR streams OrderEventPartialFill for
//     each leg before the terminal OrderEventFill; SimBroker emits a
//     single fill carrying full quantity. FilledQty monotonicity
//     across the stream is not yet asserted.
//   - Rejection semantics. SimBroker fails synchronously at SubmitOrder
//     return; IBKR succeeds at SubmitOrder and emits OrderEventRejected
//     asynchronously. Whether OrderEventRejected is terminal (i.e., no
//     subsequent events fire for that BrokerOrderID) is not yet
//     asserted.
//   - Slippage model. SimBroker applies a configurable bps offset;
//     IBKR returns whatever the venue reports; Hyperliquid uses a 5%
//     hard collar.
//
// These gaps trace to audit H5 (parity_live_vs_backtest_divergence_audit.md).
// Future contract assertions and a stream-aware mockIB extension will
// close them; until then, strategies cannot assume cross-adapter parity
// on these surfaces.
type BrokerPort interface {
	SubmitOrder(ctx context.Context, intent domain.OrderIntent) (orderID string, err error)
	CancelOrder(ctx context.Context, orderID string) error
	CancelOpenOrders(ctx context.Context, symbol domain.Symbol, side string) (int, error)
	GetOrderStatus(ctx context.Context, orderID string) (string, error)
	GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error)
	// GetPosition returns the current quantity held for a single symbol.
	// Returns (0, nil) if no position exists — this is not an error.
	GetPosition(ctx context.Context, symbol domain.Symbol) (qty float64, err error)
	// CloseAtMarket liquidates any remaining position for a symbol via a
	// broker-native MKT order. Guaranteed-fill primitive; pays the spread.
	// Strategy exits and dust-sweeps use priced paths instead — this is the
	// last-resort/emergency primitive for operator-initiated liquidation.
	// Returns ("", nil) if the position was already fully closed (broker returns 404/422).
	CloseAtMarket(ctx context.Context, symbol domain.Symbol) (orderID string, err error)
	// GetOrderDetails returns full order details from the broker including cumulative fill info.
	GetOrderDetails(ctx context.Context, orderID string) (OrderDetails, error)
	// CancelAllOpenOrders cancels every open order on the broker account.
	// Used at startup to clear stale orders from a prior session.
	// Returns the number of orders for which cancellation was requested —
	// NOT the number confirmed terminal. Some brokers (IBKR ReqGlobalCancel)
	// issue the cancel asynchronously; callers should reconcile via
	// GetOpenOrders if confirmation is required.
	CancelAllOpenOrders(ctx context.Context) (int, error)
	// GetOpenOrders returns the broker's view of every currently-working
	// order on the account. Used by startup reconciliation to cross-reference
	// broker state against the intent journal (Sprint 2 Phase B).
	// Implementations MUST filter to working states only (Submitted, Accepted,
	// PreSubmitted, Working) — terminal orders are not interesting here.
	GetOpenOrders(ctx context.Context) ([]OpenOrder, error)
}

// OpenOrder is the broker's view of a working order that existed before
// this process started. It carries just enough information for startup
// reconciliation to match it against the intent journal.
type OpenOrder struct {
	BrokerOrderID string
	Symbol        string
	Side          string // "buy" / "sell"
	Quantity      float64
	OrderType     string // "limit" / "market" / "stop" / "stop_limit"
	LimitPrice    float64
	StopPrice     float64
	Status        string // "submitted" / "accepted" / "working"
	CreatedAt     time.Time
}

// FilledOrder is the broker's view of a terminal (filled) order from the
// current session's trade list. Used by startup backfill to restore orders
// the DB never recorded (session crashed before the orders row was written).
type FilledOrder struct {
	BrokerOrderID  string
	Symbol         string
	Side           string  // "BUY" / "SELL" (as reported by broker)
	Quantity       float64 // original order quantity
	FilledQty      float64 // cumulative filled quantity
	FilledAvgPrice float64
	FilledAt       time.Time
	Status         string // should be "filled"
}

// FilledOrderLister is an optional broker capability for backfill-on-boot.
// Brokers that can enumerate filled orders from the current session implement
// this; others (simbroker, alpaca stub) skip the backfill path. Kept off
// BrokerPort so adding broker adapters stays cheap.
type FilledOrderLister interface {
	// GetFilledOrders returns every terminal (filled) order currently visible
	// on the broker session. Implementations MUST filter to status=="filled"
	// with FilledQty>0; open/canceled/rejected orders belong elsewhere.
	GetFilledOrders(ctx context.Context) ([]FilledOrder, error)
}

// FillRecord is one execution leg as reported by the broker. Multi-fill
// orders produce multiple FillRecords sharing the same BrokerOrderID but
// carrying distinct ExecutionIDs. CumQty/AvgPrice are the running totals
// reported by the broker as of THIS exec; treating them as authoritative
// (vs. recomputing locally) makes the fill recorder race-free under
// out-of-order delivery.
type FillRecord struct {
	BrokerOrderID string
	ExecutionID   string
	Symbol        string
	Side          string
	Qty           float64
	Price         float64
	CumQty        float64
	AvgPrice      float64
	FilledAt      time.Time
}

// FillLister is an optional broker capability used by boot reconciliation
// to recover fills missed during crashes, disconnects, or stream gaps. The
// returned slice MUST include every fill the broker holds for the current
// session; the caller dedups against trades.execution_id before inserting.
type FillLister interface {
	GetAllFills(ctx context.Context) ([]FillRecord, error)
}

// OrderModifier is an optional broker capability for atomic in-place order
// modification (no cancel + resubmit). Used by the position monitor's exit
// re-peg flow to eliminate the cancel-fill race that produces duplicate
// SELL legs when the cancel loses to a fill.
//
// Implementations MUST preserve the broker's order id across the modify so
// downstream FillReceived events still attribute to the same pendingOrder.
// On any condition that prevents in-place modification (broker doesn't
// support it, the order is already terminal, the order is unknown to the
// broker), implementations MUST return ErrUnsupportedModify so the caller
// can fall back to cancel+place safely.
type OrderModifier interface {
	ModifyOrder(ctx context.Context, brokerOrderID string, newLimit, newQty float64) error
}

// OrderDetails contains full order information from the broker, including
// cumulative fill data needed for fill reconciliation.
type OrderDetails struct {
	BrokerOrderID  string
	Status         string
	FilledQty      float64
	FilledAvgPrice float64
	FilledAt       time.Time
	Symbol         string
	Side           string
	Qty            float64
}

// OrderUpdate.Event values emitted by broker adapters. Terminal "fill" is
// the canonical "this order is done" signal; "partial_fill" means more
// execs still coming for the same BrokerOrderID.
const (
	OrderEventFill        = "fill"
	OrderEventPartialFill = "partial_fill"
	OrderEventCanceled    = "canceled"
	OrderEventExpired     = "expired"
	OrderEventRejected    = "rejected"
	OrderEventNew         = "new"
	OrderEventAccepted    = "accepted"
)

// OrderUpdate represents a real-time order status change received from the
// broker's streaming API. It carries enough information for the execution
// service to correlate, persist, and emit fill events without additional
// REST calls.
type OrderUpdate struct {
	BrokerOrderID  string
	ExecutionID    string  // unique fill execution ID from broker (Alpaca WS "execution_id")
	Event          string  // "fill", "partial_fill", "canceled", "canceled", "expired", "rejected", "new", "accepted"
	Qty            float64 // incremental: quantity filled in THIS specific fill
	Price          float64 // incremental: execution price for THIS specific fill
	FilledQty      float64 // cumulative: total quantity filled so far across all fills
	FilledAvgPrice float64 // cumulative: volume-weighted average price across all fills
	FilledAt       time.Time
	// Commission, regulatory, and exchange fees attributed to THIS fill.
	// Populated by adapters that price fees internally (e.g. simbroker with a
	// FeeSchedule). Live adapters that receive fees out-of-band from the
	// broker leave these at 0.
	Commission     float64
	RegulatoryFee  float64
	ExchangeFee    float64
	FeesTotal      float64
}

// OrderStreamPort is the push-based surface for real-time order
// updates. Implementations handle connection lifecycle (auth, reconnect)
// internally and deliver events on the returned channel until ctx is
// canceled. Optional capability — adapters that don't stream
// (SimBroker, Hyperliquid today) simply don't implement this.
//
// Enforced invariants (brokerporttest.RunOrderStreamPortContract):
//
//   - SubscribeOrderUpdates: returns a non-nil channel and nil error
//     when the broker is connected. Callers can immediately range over
//     the channel; nil-channel return on success is a contract violation.
//   - Channel close on ctx cancel: when the caller cancels ctx, the
//     returned channel closes within a bounded time (currently 6s in
//     the harness; couples to IBKR's 2s poll cadence). Implementations
//     MAY drain in-flight events before closing.
//
// Known unconstrained (NOT yet asserted by the harness; require a
// stream-aware mock that can simulate fill emissions):
//
//   - FilledQty monotonicity per BrokerOrderID across the event
//     stream. IBKR enforces this internally via ibsync's CumQty;
//     SimBroker would too if it ever emitted partials. Strategies
//     correlating fills should not yet assume monotonicity holds for
//     every conforming implementation.
//   - Partial-fill ordering. If an adapter emits OrderEventPartialFill,
//     the contract intent is that all partials precede the terminal
//     OrderEventFill for the same BrokerOrderID — but no test enforces
//     this today.
//   - Terminal-event idempotency. Once OrderEventRejected /
//     OrderEventCanceled / OrderEventExpired fires, no subsequent
//     events for the same BrokerOrderID should be emitted. Not yet
//     asserted.
//
// These gaps pair with the BrokerPort gaps above and trace to audit
// H5. A future PR adds a stream-aware mockIB and the strict assertions
// together.
type OrderStreamPort interface {
	SubscribeOrderUpdates(ctx context.Context) (<-chan OrderUpdate, error)
}
