package domain_test

import (
	"encoding/json"
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

// TestEntryGatedPayload_RoundTripWithDiagnosticFields exercises the parity-
// indicator-diag schema additions: Tag, raw indicator inputs, and per-component
// SubScore/Inputs must all survive JSON marshal → unmarshal so a SQL diff on
// payload.indicators / payload.confluence.components can attribute a divergence
// to the specific raw input that disagreed.
func TestEntryGatedPayload_RoundTripWithDiagnosticFields(t *testing.T) {
	p := domain.EntryGatedPayload{
		Symbol:   "AAPL",
		Strategy: "avwap_v4",
		Tag:      "backtest_abc123",
		Indicators: domain.EntryGatedIndicators{
			RSI:           55,
			VolumeRatio:   1.5,
			Volume:        1234,
			VolumeSMA:     1000,
			MACDLine:      0.5,
			MACDSignal:    0.4,
			MACDHistogram: 0.1,
			EMA21:         100.5,
			EMA50:         99.5,
			StochK:        70,
			StochD:        65,
		},
		Confluence: domain.EntryGatedConfluence{
			Score:    7,
			MaxScore: 10,
			Components: []domain.EntryGatedComponent{
				{
					Name:     "darkpool",
					Group:    "microstructure",
					Weight:   10,
					Fired:    true,
					SubScore: 7,
					Inputs: map[string]float64{
						"dpRatio":         0.35,
						"dpRatioZScore":   1.8,
						"dpBuyRatio":      0.6,
						"dpLargePrintPct": 0.15,
					},
				},
			},
		},
	}

	raw, err := json.Marshal(p)
	require.NoError(t, err)
	str := string(raw)

	for _, sub := range []string{
		`"tag":"backtest_abc123"`,
		`"volumeSMA":1000`,
		`"macdLine":0.5`,
		`"ema21":100.5`,
		`"stochK":70`,
		`"subScore":7`,
		`"inputs":{`,
		`"dpRatioZScore":1.8`,
	} {
		assert.Contains(t, str, sub, "marshal must include %s", sub)
	}

	var got domain.EntryGatedPayload
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, p.Tag, got.Tag)
	assert.Equal(t, p.Indicators.Volume, got.Indicators.Volume)
	assert.Equal(t, p.Indicators.VolumeSMA, got.Indicators.VolumeSMA)
	assert.Equal(t, p.Indicators.MACDLine, got.Indicators.MACDLine)
	assert.Equal(t, p.Indicators.MACDSignal, got.Indicators.MACDSignal)
	assert.Equal(t, p.Indicators.MACDHistogram, got.Indicators.MACDHistogram)
	assert.Equal(t, p.Indicators.EMA21, got.Indicators.EMA21)
	assert.Equal(t, p.Indicators.EMA50, got.Indicators.EMA50)
	assert.Equal(t, p.Indicators.StochK, got.Indicators.StochK)
	assert.Equal(t, p.Indicators.StochD, got.Indicators.StochD)
	require.Len(t, got.Confluence.Components, 1)
	assert.Equal(t, 7, got.Confluence.Components[0].SubScore)
	assert.Equal(t, 0.35, got.Confluence.Components[0].Inputs["dpRatio"])
	assert.Equal(t, 1.8, got.Confluence.Components[0].Inputs["dpRatioZScore"])
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
	assert.Equal(t, "RiskDowngraded", domain.EventRiskDowngraded)
}
