package domain_test

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestEnrichmentStatus_Constants(t *testing.T) {
	assert.Equal(t, domain.EnrichmentStatus("ok"), domain.EnrichmentOK)
	assert.Equal(t, domain.EnrichmentStatus("timeout"), domain.EnrichmentTimeout)
	assert.Equal(t, domain.EnrichmentStatus("error"), domain.EnrichmentError)
	assert.Equal(t, domain.EnrichmentStatus("skipped"), domain.EnrichmentSkipped)
}

func TestSignalEnrichment_Populated(t *testing.T) {
	enrichment := domain.SignalEnrichment{
		Signal: domain.SignalRef{
			StrategyInstanceID: "avwap_v1:1.0.0:SPY",
			Symbol:             "SPY",
			SignalType:         "entry",
			Side:               "buy",
			Strength:           0.70,
			Tags: map[string]string{
				"setup":     "avwap_breakout",
				"ref_price": "450.25",
				"regime_5m": "BALANCE",
			},
		},
		Status:         domain.EnrichmentOK,
		Confidence:     0.85,
		Rationale:      "AVWAP reclaim validated by AI debate",
		Direction:      domain.DirectionLong,
		BullArgument:   "Strong volume breakout above anchored VWAP",
		BearArgument:   "Approaching resistance at prior day high",
		JudgeReasoning: "Momentum confirms entry — go long",
	}

	assert.Equal(t, domain.EnrichmentOK, enrichment.Status)
	assert.Equal(t, 0.85, enrichment.Confidence)
	assert.Equal(t, domain.DirectionLong, enrichment.Direction)
	assert.NotEmpty(t, enrichment.BullArgument)
	assert.NotEmpty(t, enrichment.BearArgument)
	assert.NotEmpty(t, enrichment.JudgeReasoning)
	assert.Equal(t, "SPY", enrichment.Signal.Symbol)
	assert.Equal(t, "avwap_breakout", enrichment.Signal.Tags["setup"])
}

func TestSignalEnrichment_Fallback(t *testing.T) {
	// When AI is unavailable, enrichment falls back to original signal values.
	enrichment := domain.SignalEnrichment{
		Signal: domain.SignalRef{
			StrategyInstanceID: "avwap_v1:1.0.0:SPY",
			Symbol:             "SPY",
			SignalType:         "entry",
			Side:               "buy",
			Strength:           0.70,
			Tags:               map[string]string{"ref_price": "450.25"},
		},
		Status:         domain.EnrichmentTimeout,
		Confidence:     0.70,
		Rationale:      "signal: entry buy strength=0.70",
		Direction:      domain.DirectionLong,
		BullArgument:   "",
		BearArgument:   "",
		JudgeReasoning: "",
	}

	assert.Equal(t, domain.EnrichmentTimeout, enrichment.Status)
	assert.Equal(t, 0.70, enrichment.Confidence)
	assert.Equal(t, "signal: entry buy strength=0.70", enrichment.Rationale)
	assert.Empty(t, enrichment.BullArgument)
	assert.Empty(t, enrichment.BearArgument)
	assert.Empty(t, enrichment.JudgeReasoning)
}

func TestSignalEnrichment_ExitSkipped(t *testing.T) {
	enrichment := domain.SignalEnrichment{
		Signal: domain.SignalRef{
			StrategyInstanceID: "avwap_v1:1.0.0:SPY",
			Symbol:             "SPY",
			SignalType:         "exit",
			Side:               "sell",
			Strength:           0.80,
			Tags:               map[string]string{"ref_price": "448.00", "setup": "avwap_exit"},
		},
		Status:     domain.EnrichmentSkipped,
		Confidence: 0.80,
		Rationale:  "signal: exit sell strength=0.80",
		Direction:  domain.DirectionCloseLong,
	}

	assert.Equal(t, domain.EnrichmentSkipped, enrichment.Status)
	assert.Equal(t, 0.80, enrichment.Confidence)
	assert.Equal(t, domain.DirectionCloseLong, enrichment.Direction)
}

func TestSignalEnrichment_EventRoundTrip(t *testing.T) {
	// Ensure SignalEnrichment can be used as event payload.
	envMode, _ := domain.NewEnvMode("Paper")
	enrichment := domain.SignalEnrichment{
		Signal: domain.SignalRef{
			StrategyInstanceID: "avwap_v1:1.0.0:SPY",
			Symbol:             "SPY",
			SignalType:         "entry",
			Side:               "buy",
			Strength:           0.70,
			Tags:               map[string]string{"ref_price": "450.25"},
		},
		Status:     domain.EnrichmentOK,
		Confidence: 0.85,
		Rationale:  "AI debate rationale",
		Direction:  domain.DirectionLong,
	}

	ev, err := domain.NewEvent(domain.EventSignalEnriched, "tenant-1", envMode, "enrich-1", enrichment)
	assert.NoError(t, err)
	assert.Equal(t, domain.EventSignalEnriched, ev.Type)

	payload, ok := ev.Payload.(domain.SignalEnrichment)
	assert.True(t, ok)
	assert.Equal(t, domain.EnrichmentOK, payload.Status)
	assert.Equal(t, 0.85, payload.Confidence)
}
