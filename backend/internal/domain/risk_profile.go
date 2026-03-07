package domain

import "math"

type RiskProfile struct {
	RiskPerTradeBPS int
	StopBPS         int
	ScaleFactor     float64 // combined multiplier applied (for audit logging)
	Gated           bool
	GateReason      string
}

// DynamicRiskConfig is the per-strategy [dynamic_risk] TOML section.
// Zero-value means disabled. All multiplier fields use 0-1 scale for confidence,
// and fractional multipliers (e.g. 0.7 = 70%) for stop/size adjustments.
type DynamicRiskConfig struct {
	Enabled       bool
	MinConfidence float64 // 0-1; below → reject signal
	RiskScaleMin  float64 // risk multiplier at MinConfidence
	RiskScaleMax  float64 // risk multiplier at 1.0 confidence
	StopTightMult float64 // stop_bps multiplier for RiskModifierTight
	StopWideMult  float64 // stop_bps multiplier for RiskModifierWide
	SizeTightMult float64 // size multiplier for RiskModifierTight
	SizeWideMult  float64 // size multiplier for RiskModifierWide
}

func DefaultDynamicRiskConfig() DynamicRiskConfig {
	return DynamicRiskConfig{
		Enabled:       false,
		MinConfidence: 0.60,
		RiskScaleMin:  0.50,
		RiskScaleMax:  1.00,
		StopTightMult: 0.70,
		StopWideMult:  1.50,
		SizeTightMult: 0.70,
		SizeWideMult:  1.20,
	}
}

type BaseRiskParams struct {
	RiskPerTradeBPS int
	StopBPS         int
}

// ComputeRiskProfile adjusts risk parameters based on AI enrichment confidence and modifier.
//
// Algorithm:
//  1. Disabled or enrichment != OK → base params unchanged
//  2. Confidence < MinConfidence → gate (reject)
//  3. Linear interpolation: [MinConfidence,1.0] → [RiskScaleMin,RiskScaleMax]
//  4. RiskModifier (TIGHT/WIDE) multiplier on stop and size
//  5. Clamp all multipliers to [0.1, 2.0] safety bounds
func ComputeRiskProfile(base BaseRiskParams, enrichment SignalEnrichment, cfg DynamicRiskConfig) RiskProfile {
	if !cfg.Enabled || enrichment.Status != EnrichmentOK {
		return RiskProfile{
			RiskPerTradeBPS: base.RiskPerTradeBPS,
			StopBPS:         base.StopBPS,
			ScaleFactor:     1.0,
		}
	}

	if enrichment.Confidence < cfg.MinConfidence {
		return RiskProfile{
			RiskPerTradeBPS: base.RiskPerTradeBPS,
			StopBPS:         base.StopBPS,
			ScaleFactor:     0,
			Gated:           true,
			GateReason:      "confidence below minimum threshold",
		}
	}

	confRange := 1.0 - cfg.MinConfidence
	var confScale float64
	if confRange > 0 {
		t := (enrichment.Confidence - cfg.MinConfidence) / confRange
		confScale = cfg.RiskScaleMin + t*(cfg.RiskScaleMax-cfg.RiskScaleMin)
	} else {
		confScale = cfg.RiskScaleMax
	}

	stopMult := 1.0
	sizeMult := 1.0
	switch enrichment.RiskModifier {
	case RiskModifierTight:
		stopMult = cfg.StopTightMult
		sizeMult = cfg.SizeTightMult
	case RiskModifierWide:
		stopMult = cfg.StopWideMult
		sizeMult = cfg.SizeWideMult
	case RiskModifierNormal, "":
	}

	combinedScale := clamp(confScale*sizeMult, 0.1, 2.0)
	stopMult = clamp(stopMult, 0.1, 2.0)

	adjustedRisk := int(math.Round(float64(base.RiskPerTradeBPS) * combinedScale))
	adjustedStop := int(math.Round(float64(base.StopBPS) * stopMult))

	if adjustedRisk < 1 {
		adjustedRisk = 1
	}
	if adjustedStop < 1 {
		adjustedStop = 1
	}

	return RiskProfile{
		RiskPerTradeBPS: adjustedRisk,
		StopBPS:         adjustedStop,
		ScaleFactor:     combinedScale,
	}
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
