// Package brokerporttest provides a shared contract-test harness for any
// adapter that satisfies ports.BrokerPort. The harness asserts invariants
// every conforming adapter must hold (Liskov substitution): if a strategy
// can run against the port abstraction, swapping one BrokerPort
// implementation for another should not change the strategy's observable
// behavior on the invariants the contract names.
//
// First-pass scope (this package): four invariants every current adapter
// (SimBroker, IBKR, Hyperliquid) satisfies today. The harness is the
// vehicle for adding stricter assertions over time; the audit's H5
// findings (sync vs async fill timing, partial-fill ordering, async
// rejection events, slippage model) are NOT covered by these starter
// invariants — those are PR 2+ once the harness pattern is established.
//
// Test scope:
//   - REST surface only (SubmitOrder, GetPosition, GetPositions,
//     GetOrderStatus). OrderStreamPort coverage is a follow-up that
//     introduces stream-specific invariants (FilledQty monotonicity,
//     terminal-event idempotency, etc.).
//   - Adapter-internal state: the harness runs subtests in a fixed order
//     against a single broker instance, so each subtest assumes the state
//     left by the previous one. Currently: positions empty → position
//     unknown → submit one order → status idempotent.
package brokerporttest

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// Env carries fixture data and adapter-specific lifecycle hooks for the
// harness. Adapters supply the symbol set and prices their tests use,
// plus a Setup hook to prime adapter-internal state (e.g., SimBroker
// requires UpdatePrice before SubmitOrder will return a fill).
type Env struct {
	// TestSymbols is the symbol fixture the harness uses for its
	// SubmitOrder / GetPosition probes. Must be non-empty; the harness
	// uses TestSymbols[0] as the order target.
	TestSymbols []domain.Symbol

	// InitialPrice provides a last-trade price per symbol so adapters
	// that need it (SimBroker fills against last trade) can prime
	// state. Populated by the adapter's test, consumed by Setup.
	InitialPrice map[domain.Symbol]float64

	// TestTenantID and TestEnvMode are forwarded into OrderIntents
	// the harness submits.
	TestTenantID string
	TestEnvMode  domain.EnvMode

	// Setup is called once before any subtest runs. Adapters use it to
	// prime internal state (e.g., SimBroker.UpdatePrice for every entry
	// in InitialPrice). May be nil.
	Setup func(t *testing.T) error

	// SkipFreshPositionsCheck disables the "GetPositions returns empty
	// on a fresh adapter" subtest. Required for adapters whose mock
	// fixtures pre-seed positions for other tests in the same file
	// (e.g., the IBKR mock retains positions across the package's
	// tests). Default false (the assertion runs).
	SkipFreshPositionsCheck bool
}
