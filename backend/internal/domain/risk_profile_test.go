package domain_test

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
)

func defaultCfg() domain.DynamicRiskConfig {
	cfg := domain.DefaultDynamicRiskConfig()
	cfg.Enabled = true
	return cfg
}

func baseParams() domain.BaseRiskParams {
	return domain.BaseRiskParams{RiskPerTradeBPS: 10, StopBPS: 30}
}

func enrichmentOK(confidence float64, modifier domain.RiskModifier) domain.SignalEnrichment {
	return domain.SignalEnrichment{
		Status:       domain.EnrichmentOK,
		Confidence:   confidence,
		RiskModifier: modifier,
	}
}

func TestComputeRiskProfile(t *testing.T) {
	cfg := defaultCfg()
	base := baseParams()

	tests := []struct {
		name            string
		enrichment      domain.SignalEnrichment
		cfg             domain.DynamicRiskConfig
		wantGated       bool
		wantRiskBPS     int
		wantStopBPS     int
		wantScaleApprox float64
	}{
		{
			name:            "disabled config returns base unchanged",
			enrichment:      enrichmentOK(0.80, ""),
			cfg:             domain.DynamicRiskConfig{Enabled: false},
			wantRiskBPS:     10,
			wantStopBPS:     30,
			wantScaleApprox: 1.0,
		},
		{
			name: "enrichment timeout returns base unchanged",
			enrichment: domain.SignalEnrichment{
				Status:     domain.EnrichmentTimeout,
				Confidence: 0.80,
			},
			cfg:             cfg,
			wantRiskBPS:     10,
			wantStopBPS:     30,
			wantScaleApprox: 1.0,
		},
		{
			name: "enrichment error returns base unchanged",
			enrichment: domain.SignalEnrichment{
				Status:     domain.EnrichmentError,
				Confidence: 0.80,
			},
			cfg:             cfg,
			wantRiskBPS:     10,
			wantStopBPS:     30,
			wantScaleApprox: 1.0,
		},
		{
			name:       "confidence below min → gated",
			enrichment: enrichmentOK(0.50, ""),
			cfg:        cfg,
			wantGated:  true,
		},
		{
			name:       "confidence exactly at min → NOT gated (boundary)",
			enrichment: enrichmentOK(0.60, ""),
			cfg:        cfg,
			wantGated:  false,
			// t=0 → scale = RiskScaleMin (0.5), risk = round(10*0.5) = 5
			wantRiskBPS:     5,
			wantStopBPS:     30,
			wantScaleApprox: 0.5,
		},
		{
			name:       "confidence at max (1.0) → full scale",
			enrichment: enrichmentOK(1.0, ""),
			cfg:        cfg,
			// t=1 → scale = RiskScaleMax (1.0), risk = 10
			wantRiskBPS:     10,
			wantStopBPS:     30,
			wantScaleApprox: 1.0,
		},
		{
			name:       "confidence 0.80 → midpoint scale",
			enrichment: enrichmentOK(0.80, ""),
			cfg:        cfg,
			// t = (0.8-0.6)/(1.0-0.6) = 0.5 → scale = 0.5 + 0.5*(1.0-0.5) = 0.75
			// risk = round(10*0.75) = 8 (rounds to 8)
			wantRiskBPS:     8,
			wantStopBPS:     30,
			wantScaleApprox: 0.75,
		},
		{
			name:       "TIGHT modifier reduces stop and size",
			enrichment: enrichmentOK(1.0, domain.RiskModifierTight),
			cfg:        cfg,
			// confScale=1.0, sizeMult=0.7 → combined=0.7, stop=0.7
			// risk = round(10*0.7) = 7, stop = round(30*0.7) = 21
			wantRiskBPS:     7,
			wantStopBPS:     21,
			wantScaleApprox: 0.70,
		},
		{
			name:       "WIDE modifier widens stop, slight size increase",
			enrichment: enrichmentOK(1.0, domain.RiskModifierWide),
			cfg:        cfg,
			// confScale=1.0, sizeMult=1.2 → combined=1.2, stopMult=1.5
			// risk = round(10*1.2) = 12, stop = round(30*1.5) = 45
			wantRiskBPS:     12,
			wantStopBPS:     45,
			wantScaleApprox: 1.20,
		},
		{
			name:       "TIGHT + low confidence compounds reductions",
			enrichment: enrichmentOK(0.60, domain.RiskModifierTight),
			cfg:        cfg,
			// confScale=0.5, sizeMult=0.7 → combined=0.35, stopMult=0.7
			// risk = round(10*0.35) = 4 (rounds to 4), stop = round(30*0.7) = 21
			wantRiskBPS:     4,
			wantStopBPS:     21,
			wantScaleApprox: 0.35,
		},
		{
			name:            "NORMAL modifier same as empty string",
			enrichment:      enrichmentOK(0.80, domain.RiskModifierNormal),
			cfg:             cfg,
			wantRiskBPS:     8,
			wantStopBPS:     30,
			wantScaleApprox: 0.75,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := domain.ComputeRiskProfile(base, tt.enrichment, tt.cfg)

			if tt.wantGated {
				assert.True(t, result.Gated, "expected trade to be gated")
				assert.NotEmpty(t, result.GateReason)
				return
			}

			assert.False(t, result.Gated, "expected trade NOT to be gated")
			assert.Equal(t, tt.wantRiskBPS, result.RiskPerTradeBPS)
			assert.Equal(t, tt.wantStopBPS, result.StopBPS)
			assert.InDelta(t, tt.wantScaleApprox, result.ScaleFactor, 0.01)
		})
	}
}

func TestComputeRiskProfile_ClampSafety(t *testing.T) {
	base := baseParams()

	t.Run("extreme multipliers clamped to 2.0 max", func(t *testing.T) {
		cfg := defaultCfg()
		cfg.SizeWideMult = 10.0
		cfg.StopWideMult = 10.0

		result := domain.ComputeRiskProfile(base, enrichmentOK(1.0, domain.RiskModifierWide), cfg)
		assert.LessOrEqual(t, result.ScaleFactor, 2.0)
		assert.LessOrEqual(t, result.StopBPS, 60) // 30 * 2.0
	})

	t.Run("near-zero multipliers clamped to 0.1 min", func(t *testing.T) {
		cfg := defaultCfg()
		cfg.SizeTightMult = 0.01
		cfg.StopTightMult = 0.01

		result := domain.ComputeRiskProfile(base, enrichmentOK(1.0, domain.RiskModifierTight), cfg)
		assert.GreaterOrEqual(t, result.ScaleFactor, 0.1)
		assert.GreaterOrEqual(t, result.StopBPS, 1)
		assert.GreaterOrEqual(t, result.RiskPerTradeBPS, 1)
	})
}

func TestComputeRiskProfile_FloorOneBPS(t *testing.T) {
	base := domain.BaseRiskParams{RiskPerTradeBPS: 1, StopBPS: 1}
	cfg := defaultCfg()

	result := domain.ComputeRiskProfile(base, enrichmentOK(0.60, domain.RiskModifierTight), cfg)
	assert.GreaterOrEqual(t, result.RiskPerTradeBPS, 1, "risk should never go below 1 BPS")
	assert.GreaterOrEqual(t, result.StopBPS, 1, "stop should never go below 1 BPS")
}

func TestNewRiskModifier(t *testing.T) {
	assert.Equal(t, domain.RiskModifierTight, domain.NewRiskModifier("TIGHT"))
	assert.Equal(t, domain.RiskModifierNormal, domain.NewRiskModifier("NORMAL"))
	assert.Equal(t, domain.RiskModifierWide, domain.NewRiskModifier("WIDE"))
	assert.Equal(t, domain.RiskModifierNormal, domain.NewRiskModifier("garbage"))
	assert.Equal(t, domain.RiskModifierNormal, domain.NewRiskModifier(""))
}
