package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

// ErrDuplicateIntent is returned by OrderIntentJournal.SaveOrderIntent when
// the (tenant_id, env_mode, idempotency_key) tuple already exists. The caller
// should treat this as a deduplication signal — the original attempt is
// already persisted and another code path is responsible for completing it.
var ErrDuplicateIntent = errors.New("order intent already journaled (duplicate idempotency key)")

// OrderIntentJournal is the write-ahead journal for order intents. It lives
// on its own interface (not embedded in RepositoryPort) so that execution
// and position-monitor services can depend on the narrow contract, and so
// that existing RepositoryPort mocks across the test suite don't require
// updates just because a new journaling method was added.
//
// Feature-flagged: when OMO_ORDER_JOURNAL_ENABLED is unset/false, the
// execution pipeline bypasses this interface entirely and behaves as it
// did before Sprint 2.
type OrderIntentJournal interface {
	// SaveOrderIntent writes a new intent row with status=pending_submit.
	// Must succeed before broker.SubmitOrder is called. Returns
	// ErrDuplicateIntent if idempotency_key already exists for the
	// (tenant, env) pair.
	SaveOrderIntent(ctx context.Context, intent domain.OrderIntent) error

	// MarkIntentSubmitted records that broker.SubmitOrder returned
	// successfully with the given brokerOrderID. Transitions
	// pending_submit -> submitted.
	MarkIntentSubmitted(ctx context.Context, intentID uuid.UUID, brokerOrderID string, at time.Time) error

	// MarkIntentSubmitFailed records a broker-rejected submission.
	// Transitions pending_submit -> rejected and stores the error.
	MarkIntentSubmitFailed(ctx context.Context, intentID uuid.UUID, errMsg string, at time.Time) error

	// MarkIntentTerminal records a terminal lifecycle event (fill, cancel,
	// reject, expired) driven by the broker order stream. Looks up by
	// brokerOrderID since that is what the broker stream carries.
	MarkIntentTerminal(ctx context.Context, brokerOrderID string, status string, filledQty, filledAvgPrice float64, at time.Time) error

	// OpenIntents returns all journal rows in non-terminal status created
	// within the given lookback window. Used by startup reconciliation
	// to match against broker-reported open orders.
	OpenIntents(ctx context.Context, tenantID string, envMode domain.EnvMode, lookback time.Duration) ([]domain.OrderIntentJournalRow, error)

	// MarkIntentLost records that startup reconciliation could not find
	// a broker match for a journal row in submitted state. Transitions
	// submitted -> lost. The existing fill-reconciliation path handles
	// any actual fills that occurred during downtime.
	MarkIntentLost(ctx context.Context, intentID uuid.UUID, at time.Time) error
}
