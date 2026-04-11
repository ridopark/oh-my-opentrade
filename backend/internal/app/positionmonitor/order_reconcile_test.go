package positionmonitor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockIntentJournal is a minimal in-memory fake for the Sprint 2 reconciler tests.
// It records every method call so tests can assert the observable behavior.
type mockIntentJournal struct {
	mu              sync.Mutex
	openIntentsRows []domain.OrderIntentJournalRow
	openIntentsErr  error
	lostIDs         []uuid.UUID
	lostErr         error
	// Unused by the reconciler tests but needed to satisfy the full interface.
}

func (m *mockIntentJournal) SaveOrderIntent(ctx context.Context, intent domain.OrderIntent) error {
	return nil
}
func (m *mockIntentJournal) MarkIntentSubmitted(ctx context.Context, id uuid.UUID, brokerOrderID string, at time.Time) error {
	return nil
}
func (m *mockIntentJournal) MarkIntentSubmitFailed(ctx context.Context, id uuid.UUID, errMsg string, at time.Time) error {
	return nil
}
func (m *mockIntentJournal) MarkIntentTerminal(ctx context.Context, brokerOrderID string, status string, filledQty, filledAvgPrice float64, at time.Time) error {
	return nil
}
func (m *mockIntentJournal) OpenIntents(ctx context.Context, tenantID string, envMode domain.EnvMode, lookback time.Duration) ([]domain.OrderIntentJournalRow, error) {
	return m.openIntentsRows, m.openIntentsErr
}
func (m *mockIntentJournal) MarkIntentLost(ctx context.Context, id uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lostIDs = append(m.lostIDs, id)
	return m.lostErr
}

func newReconcileService(t *testing.T, broker *mockBroker, journal ports.OrderIntentJournal) *Service {
	t.Helper()
	s := &Service{
		broker:        broker,
		intentJournal: journal,
		tenantID:      "default",
		envMode:       domain.EnvModePaper,
		log:           zerolog.Nop(),
	}
	return s
}

func makeJournalRow(brokerOrderID, symbol, status string) domain.OrderIntentJournalRow {
	return domain.OrderIntentJournalRow{
		ID:            uuid.New(),
		BrokerOrderID: brokerOrderID,
		Symbol:        domain.Symbol(symbol),
		Status:        status,
		Strategy:      "test-strategy",
	}
}

func makeOpenOrder(brokerOrderID, symbol, side string) ports.OpenOrder {
	return ports.OpenOrder{
		BrokerOrderID: brokerOrderID,
		Symbol:        symbol,
		Side:          side,
		Quantity:      10,
		OrderType:     "limit",
		Status:        "accepted",
	}
}

func TestReconcileOpenOrders_EmptyBrokerAndJournal(t *testing.T) {
	broker := &mockBroker{}
	journal := &mockIntentJournal{}
	s := newReconcileService(t, broker, journal)

	matched, unmanaged, lost := s.reconcileOpenOrders(context.Background(), nil, nil)
	assert.Equal(t, 0, matched)
	assert.Equal(t, 0, unmanaged)
	assert.Equal(t, 0, lost)
	assert.Equal(t, 0, broker.cancelAllCalls, "should not cancel anything when both sides are empty")
}

func TestReconcileOpenOrders_AllMatched(t *testing.T) {
	broker := &mockBroker{}
	row1 := makeJournalRow("BRK-1", "AAPL", domain.OrderIntentJournalSubmitted)
	row2 := makeJournalRow("BRK-2", "MSFT", domain.OrderIntentJournalSubmitted)
	journal := &mockIntentJournal{}
	s := newReconcileService(t, broker, journal)

	brokerOrders := []ports.OpenOrder{
		makeOpenOrder("BRK-1", "AAPL", "sell"),
		makeOpenOrder("BRK-2", "MSFT", "sell"),
	}
	matched, unmanaged, lost := s.reconcileOpenOrders(context.Background(), brokerOrders, []domain.OrderIntentJournalRow{row1, row2})

	assert.Equal(t, 2, matched, "both broker orders have journal entries")
	assert.Equal(t, 0, unmanaged)
	assert.Equal(t, 0, lost)
	assert.Equal(t, 0, broker.cancelAllCalls, "matched orders must never be cancelled")
	assert.Len(t, journal.lostIDs, 0)
}

func TestReconcileOpenOrders_UnmanagedAlertNoCancel(t *testing.T) {
	broker := &mockBroker{}
	journal := &mockIntentJournal{}
	s := newReconcileService(t, broker, journal)

	brokerOrders := []ports.OpenOrder{makeOpenOrder("BRK-ghost", "TSLA", "buy")}
	matched, unmanaged, lost := s.reconcileOpenOrders(context.Background(), brokerOrders, nil)

	assert.Equal(t, 0, matched)
	assert.Equal(t, 1, unmanaged)
	assert.Equal(t, 0, lost)
	assert.Equal(t, 0, broker.cancelAllCalls, "unmanaged orders must NOT be auto-cancelled")
}

func TestReconcileOpenOrders_LostJournalIntent(t *testing.T) {
	broker := &mockBroker{}
	row := makeJournalRow("BRK-vanished", "NVDA", domain.OrderIntentJournalSubmitted)
	journal := &mockIntentJournal{}
	s := newReconcileService(t, broker, journal)

	matched, unmanaged, lost := s.reconcileOpenOrders(context.Background(), nil, []domain.OrderIntentJournalRow{row})

	assert.Equal(t, 0, matched)
	assert.Equal(t, 0, unmanaged)
	assert.Equal(t, 1, lost)
	require.Len(t, journal.lostIDs, 1)
	assert.Equal(t, row.ID, journal.lostIDs[0])
}

func TestReconcileOpenOrders_PendingSubmitRowNotLost(t *testing.T) {
	broker := &mockBroker{}
	// Row in pending_submit means we crashed between journaling and broker
	// submission — it has no broker_order_id, so the reconciler should leave
	// it alone (no lost marking, no alert).
	row := makeJournalRow("", "AMD", domain.OrderIntentJournalPendingSubmit)
	journal := &mockIntentJournal{}
	s := newReconcileService(t, broker, journal)

	matched, unmanaged, lost := s.reconcileOpenOrders(context.Background(), nil, []domain.OrderIntentJournalRow{row})
	assert.Equal(t, 0, matched)
	assert.Equal(t, 0, unmanaged)
	assert.Equal(t, 0, lost)
	assert.Len(t, journal.lostIDs, 0)
}

func TestReconcileOpenOrders_MixedMatchedUnmanagedLost(t *testing.T) {
	broker := &mockBroker{}
	matchedRow := makeJournalRow("BRK-1", "AAPL", domain.OrderIntentJournalSubmitted)
	lostRow := makeJournalRow("BRK-vanished", "NVDA", domain.OrderIntentJournalSubmitted)
	journal := &mockIntentJournal{}
	s := newReconcileService(t, broker, journal)

	brokerOrders := []ports.OpenOrder{
		makeOpenOrder("BRK-1", "AAPL", "sell"),         // matched
		makeOpenOrder("BRK-unmanaged", "META", "buy"),  // unmanaged
	}
	rows := []domain.OrderIntentJournalRow{matchedRow, lostRow}
	matched, unmanaged, lost := s.reconcileOpenOrders(context.Background(), brokerOrders, rows)

	assert.Equal(t, 1, matched)
	assert.Equal(t, 1, unmanaged)
	assert.Equal(t, 1, lost)
	assert.Equal(t, 0, broker.cancelAllCalls)
	require.Len(t, journal.lostIDs, 1)
	assert.Equal(t, lostRow.ID, journal.lostIDs[0])
}

func TestReconcileOpenOrdersOnBoot_BrokerQueryError_FallbackCancelAll(t *testing.T) {
	broker := &mockBroker{openOrdersErr: errors.New("broker down")}
	journal := &mockIntentJournal{}
	s := newReconcileService(t, broker, journal)

	s.reconcileOpenOrdersOnBoot(context.Background())

	assert.Equal(t, 1, broker.cancelAllCalls, "broker query error must trip cancel-all fallback")
}

func TestReconcileOpenOrdersOnBoot_JournalQueryError_FallbackCancelAll(t *testing.T) {
	broker := &mockBroker{openOrders: []ports.OpenOrder{makeOpenOrder("BRK-1", "AAPL", "sell")}}
	journal := &mockIntentJournal{openIntentsErr: errors.New("db down")}
	s := newReconcileService(t, broker, journal)

	s.reconcileOpenOrdersOnBoot(context.Background())

	assert.Equal(t, 1, broker.cancelAllCalls, "journal query error must trip cancel-all fallback")
}

func TestReconcileOpenOrdersOnBoot_FlagDisabled_PreservesLegacyCancelAll(t *testing.T) {
	broker := &mockBroker{openOrders: []ports.OpenOrder{makeOpenOrder("BRK-1", "AAPL", "sell")}}
	// With the flag disabled upstream, services.go leaves intentJournal nil.
	// reconcileOpenOrdersOnBoot must fall back to the legacy cancel-all path
	// whenever it sees a nil journal, regardless of what broker returned.
	s := newReconcileService(t, broker, nil)

	s.reconcileOpenOrdersOnBoot(context.Background())

	assert.Equal(t, 1, broker.cancelAllCalls, "nil journal (flag disabled) must keep legacy cancel-all behavior")
}

func TestReconcileOpenOrdersOnBoot_HappyPath(t *testing.T) {
	broker := &mockBroker{openOrders: []ports.OpenOrder{makeOpenOrder("BRK-1", "AAPL", "sell")}}
	row := makeJournalRow("BRK-1", "AAPL", domain.OrderIntentJournalSubmitted)
	journal := &mockIntentJournal{openIntentsRows: []domain.OrderIntentJournalRow{row}}
	s := newReconcileService(t, broker, journal)

	s.reconcileOpenOrdersOnBoot(context.Background())

	assert.Equal(t, 0, broker.cancelAllCalls, "matched order must NOT be cancelled on happy path")
	assert.Len(t, journal.lostIDs, 0)
}
