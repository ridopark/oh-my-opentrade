package notify_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/notify"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockNotifier struct {
	mu       sync.Mutex
	messages []notifyCall
}

type notifyCall struct {
	TenantID string
	Message  string
}

func (m *mockNotifier) Notify(_ context.Context, tenantID, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, notifyCall{TenantID: tenantID, Message: message})
	return nil
}

func (m *mockNotifier) getMessages() []notifyCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]notifyCall, len(m.messages))
	copy(out, m.messages)
	return out
}

func (m *mockNotifier) waitForMessages(n int, timeout time.Duration) []notifyCall {
	deadline := time.After(timeout)
	for {
		msgs := m.getMessages()
		if len(msgs) >= n {
			return msgs
		}
		select {
		case <-deadline:
			return msgs
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestService_SubscribesToOrderEvents(t *testing.T) {
	bus := memory.NewBus()
	notifier := &mockNotifier{}
	svc, err := notify.NewService(bus, notifier, zerolog.Nop())
	require.NoError(t, err)

	err = svc.Start(context.Background())
	require.NoError(t, err)
	defer svc.Stop()

	intent := createTestOrderIntent(t)
	payload := domain.NewOrderIntentEventPayload(intent, domain.OrderIntentStatusSubmitted)
	ev, err := domain.NewEvent(domain.EventOrderSubmitted, "tenant-1", domain.EnvModePaper, "key-1", payload)
	require.NoError(t, err)

	err = bus.Publish(context.Background(), *ev)
	require.NoError(t, err)

	msgs := notifier.waitForMessages(1, 5*time.Second)
	require.Len(t, msgs, 1)
	assert.Equal(t, "tenant-1", msgs[0].TenantID)
	assert.Contains(t, msgs[0].Message, "Order Submitted")
	assert.Contains(t, msgs[0].Message, "AAPL")
	assert.Contains(t, msgs[0].Message, "Strategy: test")
	assert.Contains(t, msgs[0].Message, "Rationale: test rationale")
	assert.Contains(t, msgs[0].Message, "80%")
}

func TestService_KillSwitchNotification(t *testing.T) {
	bus := memory.NewBus()
	notifier := &mockNotifier{}
	svc, err := notify.NewService(bus, notifier, zerolog.Nop())
	require.NoError(t, err)

	err = svc.Start(context.Background())
	require.NoError(t, err)
	defer svc.Stop()

	ev, err := domain.NewEvent(domain.EventKillSwitchEngaged, "tenant-1", domain.EnvModePaper, "ks-1", nil)
	require.NoError(t, err)

	err = bus.Publish(context.Background(), *ev)
	require.NoError(t, err)

	msgs := notifier.waitForMessages(1, 5*time.Second)
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Message, "KILL SWITCH")
}

func TestService_CircuitBreakerNotification(t *testing.T) {
	bus := memory.NewBus()
	notifier := &mockNotifier{}
	svc, err := notify.NewService(bus, notifier, zerolog.Nop())
	require.NoError(t, err)

	err = svc.Start(context.Background())
	require.NoError(t, err)
	defer svc.Stop()

	ev, err := domain.NewEvent(domain.EventCircuitBreakerTripped, "tenant-1", domain.EnvModePaper, "cb-1", "3 stops in 2 minutes")
	require.NoError(t, err)

	err = bus.Publish(context.Background(), *ev)
	require.NoError(t, err)

	msgs := notifier.waitForMessages(1, 5*time.Second)
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Message, "CIRCUIT BREAKER")
	assert.Contains(t, msgs[0].Message, "3 stops in 2 minutes")
}

func TestService_MultipleEventsNotifyAll(t *testing.T) {
	bus := memory.NewBus()
	notifier := &mockNotifier{}
	svc, err := notify.NewService(bus, notifier, zerolog.Nop())
	require.NoError(t, err)

	err = svc.Start(context.Background())
	require.NoError(t, err)
	defer svc.Stop()

	events := []struct {
		eventType string
		payload   any
	}{
		{domain.EventKillSwitchEngaged, nil},
		{domain.EventCircuitBreakerTripped, "test"},
		{domain.EventFeedDegraded, domain.FeedDegradedPayload{Feed: "alpaca", Reason: "timeout"}},
	}

	for i, e := range events {
		ev, err := domain.NewEvent(e.eventType, "tenant-1", domain.EnvModePaper, fmt.Sprintf("key-%d", i), e.payload)
		require.NoError(t, err)
		err = bus.Publish(context.Background(), *ev)
		require.NoError(t, err)
	}

	msgs := notifier.waitForMessages(3, 2*time.Second)
	assert.Len(t, msgs, 3, "should receive notifications for all 3 event types")
}

func TestService_SignalEnrichedExitSuppressed(t *testing.T) {
	bus := memory.NewBus()
	notifier := &mockNotifier{}
	svc, err := notify.NewService(bus, notifier, zerolog.Nop())
	require.NoError(t, err)

	err = svc.Start(context.Background())
	require.NoError(t, err)
	defer svc.Stop()

	enrichment := domain.SignalEnrichment{
		Signal: domain.SignalRef{
			StrategyInstanceID: "avwap_v1:1.0.0:BTC/USD",
			Symbol:             "BTC/USD",
			SignalType:         "exit",
			Side:               "sell",
			Strength:           0.80,
		},
		Status:           domain.EnrichmentSkipped,
		Confidence:       0.80,
		Direction:        domain.DirectionCloseLong,
		Rationale:        "exit signal: sell strength=0.80",
		HasPnL:           true,
		EntryPrice:       90000.0,
		UnrealizedPnLPct: 0.05,
		UnrealizedPnLUSD: 500.0,
	}

	ev, err := domain.NewEvent(domain.EventSignalEnriched, "tenant-1", domain.EnvModePaper, "exit-1", enrichment)
	require.NoError(t, err)

	err = bus.Publish(context.Background(), *ev)
	require.NoError(t, err)

	time.Sleep(500 * time.Millisecond)
	msgs := notifier.getMessages()
	assert.Len(t, msgs, 0, "exit signal enrichment should not produce a notification")
}

func TestService_OrderSubmittedWithMeta(t *testing.T) {
	bus := memory.NewBus()
	notifier := &mockNotifier{}
	svc, err := notify.NewService(bus, notifier, zerolog.Nop())
	require.NoError(t, err)

	err = svc.Start(context.Background())
	require.NoError(t, err)
	defer svc.Stop()

	intent := createTestOrderIntent(t)
	intent.Meta = map[string]string{
		"bull":  "RSI reset + trend continuation",
		"bear":  "Earnings gap risk",
		"judge": "Edge positive; size appropriately",
	}

	payload := domain.NewOrderIntentEventPayload(intent, domain.OrderIntentStatusSubmitted)
	ev, err := domain.NewEvent(domain.EventOrderSubmitted, "tenant-1", domain.EnvModePaper, "key-meta-1", payload)
	require.NoError(t, err)

	err = bus.Publish(context.Background(), *ev)
	require.NoError(t, err)

	msgs := notifier.waitForMessages(1, 5*time.Second)
	require.Len(t, msgs, 1)
	assert.NotContains(t, msgs[0].Message, "Bull:")
	assert.NotContains(t, msgs[0].Message, "Bear:")
	assert.NotContains(t, msgs[0].Message, "Judge:")
	assert.Contains(t, msgs[0].Message, "Order Submitted")
	assert.Contains(t, msgs[0].Message, "AAPL")
	assert.Contains(t, msgs[0].Message, "Strategy: test")
}

func createTestOrderIntent(t *testing.T) domain.OrderIntent {
	t.Helper()
	intent, err := domain.NewOrderIntent(
		uuid.New(),
		"tenant-1",
		domain.EnvModePaper,
		"AAPL",
		domain.DirectionLong,
		150.0, // limit price
		145.0, // stop loss
		10,    // max slippage bps
		10.0,  // quantity
		"test",
		"test rationale",
		0.8,
		"idempotency-key-1",
	)
	require.NoError(t, err)
	return intent
}
