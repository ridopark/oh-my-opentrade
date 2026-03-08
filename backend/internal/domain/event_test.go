package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEvent(t *testing.T) {
	tenantID := "tenant-1"
	envMode, _ := domain.NewEnvMode("Paper")
	idempotencyKey := "req-12345"
	payload := map[string]string{"foo": "bar"}

	t.Run("valid creation", func(t *testing.T) {
		before := time.Now()
		event, err := domain.NewEvent(
			domain.EventMarketBarSanitized,
			tenantID,
			envMode,
			idempotencyKey,
			payload,
		)
		after := time.Now()

		require.NoError(t, err)

		// Assert ID is set and is a valid UUID
		assert.NotEmpty(t, event.ID)
		_, uuidErr := uuid.Parse(event.ID)
		assert.NoError(t, uuidErr)

		// Assert OccurredAt is set properly
		assert.False(t, event.OccurredAt.IsZero())
		assert.True(t, event.OccurredAt.After(before) || event.OccurredAt.Equal(before))
		assert.True(t, event.OccurredAt.Before(after) || event.OccurredAt.Equal(after))

		// Assert other fields
		assert.Equal(t, domain.EventMarketBarSanitized, event.Type)
		assert.Equal(t, tenantID, event.TenantID)
		assert.Equal(t, envMode, event.EnvMode)
		assert.Equal(t, idempotencyKey, event.IdempotencyKey)
		assert.Equal(t, payload, event.Payload)
	})

	t.Run("invalid - missing event type", func(t *testing.T) {
		_, err := domain.NewEvent("", tenantID, envMode, idempotencyKey, payload)
		assert.ErrorContains(t, err, "event type is required")
	})

	t.Run("invalid - missing idempotency key", func(t *testing.T) {
		_, err := domain.NewEvent(domain.EventMarketBarSanitized, tenantID, envMode, "", payload)
		assert.ErrorContains(t, err, "idempotency key is required")
	})
}

func TestEventTypeConstants(t *testing.T) {
	// Assert all required event type constants are defined with correct string values
	assert.Equal(t, "MarketBarReceived", domain.EventMarketBarReceived)
	assert.Equal(t, "MarketBarSanitized", domain.EventMarketBarSanitized)
	assert.Equal(t, "MarketBarRejected", domain.EventMarketBarRejected)
	assert.Equal(t, "StateUpdated", domain.EventStateUpdated)
	assert.Equal(t, "RegimeShifted", domain.EventRegimeShifted)
	assert.Equal(t, "SetupDetected", domain.EventSetupDetected)
	assert.Equal(t, "DebateRequested", domain.EventDebateRequested)
	assert.Equal(t, "DebateCompleted", domain.EventDebateCompleted)
	assert.Equal(t, "OrderIntentCreated", domain.EventOrderIntentCreated)
	assert.Equal(t, "OrderIntentValidated", domain.EventOrderIntentValidated)
	assert.Equal(t, "OrderIntentRejected", domain.EventOrderIntentRejected)
	assert.Equal(t, "OrderSubmitted", domain.EventOrderSubmitted)
	assert.Equal(t, "OrderAccepted", domain.EventOrderAccepted)
	assert.Equal(t, "OrderRejected", domain.EventOrderRejected)
	assert.Equal(t, "FillReceived", domain.EventFillReceived)
	assert.Equal(t, "PositionUpdated", domain.EventPositionUpdated)
	assert.Equal(t, "KillSwitchEngaged", domain.EventKillSwitchEngaged)
	assert.Equal(t, "CircuitBreakerTripped", domain.EventCircuitBreakerTripped)
	assert.Equal(t, "SignalDebateRequested", domain.EventSignalDebateRequested)
	assert.Equal(t, "SignalEnriched", domain.EventSignalEnriched)
	assert.Equal(t, "ExitOrderTerminal", domain.EventExitOrderTerminal)
}
