package strategy

import (
	"fmt"
	"math"
	"strings"
)

// ComponentScore captures a single confluence scorer's output for attribution.
// No json tags — this is an in-memory value object. Use ThesisConfluence for
// serialization to JSONB.
type ComponentScore struct {
	Name   string  // "ema_stack", "dp_buy", "rq_speed", etc.
	Group  string  // "technical", "darkpool", "retest", "options"
	Weight int     // points contributed (0 if not fired)
	Value  float64 // raw metric value (ADX=28.3, RSI=62, etc.)
	Fired  bool    // crossed threshold
}

// ConfluenceResult holds the computed confluence score and contributing factors.
type ConfluenceResult struct {
	Score      int
	Factors    []string
	Components []ComponentScore
}

// MergeConfluence combines multiple ConfluenceResults into one by summing scores
// and concatenating factor lists.
func MergeConfluence(results ...ConfluenceResult) ConfluenceResult {
	var merged ConfluenceResult
	for _, r := range results {
		merged.Score += r.Score
		merged.Factors = append(merged.Factors, r.Factors...)
		merged.Components = append(merged.Components, r.Components...)
	}
	return merged
}

// FormatDetail returns the confluence factors as a "+"-joined string
// suitable for signal tags (e.g., "ema_stack+adx_strong+vwap_aligned").
func (cr ConfluenceResult) FormatDetail() string {
	return strings.Join(cr.Factors, "+")
}

// ────────────────────────────────────────────────────────────────────
// Universal confluence factors — applicable to any strategy.
// Each returns a partial ConfluenceResult with its score and factor name.
// ────────────────────────────────────────────────────────────────────

// ScoreEMAStack evaluates EMA alignment (0-15).
// Long: Close > EMA9 > EMA21 > EMA50. Short: reversed.
func ScoreEMAStack(bar Bar, ind IndicatorData, isLong bool) ConfluenceResult {
	if ind.EMA9 <= 0 || ind.EMA21 <= 0 || ind.EMA50 <= 0 {
		return ConfluenceResult{Components: []ComponentScore{{Name: "ema_stack", Group: "technical", Value: 0}}}
	}
	count := 0
	if isLong {
		if bar.Close > ind.EMA9 {
			count++
		}
		if ind.EMA9 > ind.EMA21 {
			count++
		}
		if ind.EMA21 > ind.EMA50 {
			count++
		}
	} else {
		if bar.Close < ind.EMA9 {
			count++
		}
		if ind.EMA9 < ind.EMA21 {
			count++
		}
		if ind.EMA21 < ind.EMA50 {
			count++
		}
	}
	cs := ComponentScore{Name: "ema_stack", Group: "technical", Value: float64(count)}
	switch count {
	case 3:
		cs.Weight = 15
		cs.Fired = true
		return ConfluenceResult{Score: 15, Factors: []string{"ema_stack"}, Components: []ComponentScore{cs}}
	case 2:
		cs.Name = "ema_partial"
		cs.Weight = 10
		cs.Fired = true
		return ConfluenceResult{Score: 10, Factors: []string{"ema_partial"}, Components: []ComponentScore{cs}}
	case 1:
		cs.Weight = 5
		return ConfluenceResult{Score: 5, Components: []ComponentScore{cs}}
	default:
		return ConfluenceResult{Components: []ComponentScore{cs}}
	}
}

// ScoreADX evaluates trend strength via ADX (0-15).
func ScoreADX(ind IndicatorData) ConfluenceResult {
	cs := ComponentScore{Name: "adx", Group: "technical", Value: ind.ADX}
	if ind.ADX <= 0 {
		return ConfluenceResult{Components: []ComponentScore{cs}}
	}
	switch {
	case ind.ADX >= 30:
		cs.Name = "adx_strong"
		cs.Weight = 15
		cs.Fired = true
		return ConfluenceResult{Score: 15, Factors: []string{"adx_strong"}, Components: []ComponentScore{cs}}
	case ind.ADX >= 25:
		cs.Name = "adx_trend"
		cs.Weight = 12
		cs.Fired = true
		return ConfluenceResult{Score: 12, Factors: []string{"adx_trend"}, Components: []ComponentScore{cs}}
	case ind.ADX >= 20:
		cs.Name = "adx_ok"
		cs.Weight = 8
		cs.Fired = true
		return ConfluenceResult{Score: 8, Factors: []string{"adx_ok"}, Components: []ComponentScore{cs}}
	case ind.ADX >= 15:
		cs.Weight = 4
		return ConfluenceResult{Score: 4, Components: []ComponentScore{cs}}
	default:
		return ConfluenceResult{Components: []ComponentScore{cs}}
	}
}

// ScoreRSI evaluates RSI position for directional quality (0-10).
// Long: 45-65 ideal. Short: 35-55 ideal.
func ScoreRSI(ind IndicatorData, isLong bool) ConfluenceResult {
	cs := ComponentScore{Name: "rsi", Group: "technical", Value: ind.RSI}
	if ind.RSI <= 0 {
		return ConfluenceResult{Components: []ComponentScore{cs}}
	}
	if isLong {
		switch {
		case ind.RSI >= 45 && ind.RSI <= 65:
			cs.Name = "rsi_ideal"
			cs.Weight = 10
			cs.Fired = true
			return ConfluenceResult{Score: 10, Factors: []string{"rsi_ideal"}, Components: []ComponentScore{cs}}
		case (ind.RSI >= 35 && ind.RSI < 45) || (ind.RSI > 65 && ind.RSI <= 75):
			cs.Name = "rsi_ok"
			cs.Weight = 5
			cs.Fired = true
			return ConfluenceResult{Score: 5, Factors: []string{"rsi_ok"}, Components: []ComponentScore{cs}}
		}
	} else {
		switch {
		case ind.RSI >= 35 && ind.RSI <= 55:
			cs.Name = "rsi_ideal"
			cs.Weight = 10
			cs.Fired = true
			return ConfluenceResult{Score: 10, Factors: []string{"rsi_ideal"}, Components: []ComponentScore{cs}}
		case (ind.RSI > 55 && ind.RSI <= 65) || (ind.RSI >= 25 && ind.RSI < 35):
			cs.Name = "rsi_ok"
			cs.Weight = 5
			cs.Fired = true
			return ConfluenceResult{Score: 5, Factors: []string{"rsi_ok"}, Components: []ComponentScore{cs}}
		}
	}
	return ConfluenceResult{Components: []ComponentScore{cs}}
}

// ScoreVolume evaluates volume relative to its SMA (0-8).
func ScoreVolume(bar Bar, ind IndicatorData) ConfluenceResult {
	if ind.VolumeSMA <= 0 {
		return ConfluenceResult{Components: []ComponentScore{{Name: "volume", Group: "technical", Value: 0}}}
	}
	ratio := bar.Volume / ind.VolumeSMA
	cs := ComponentScore{Name: "volume", Group: "technical", Value: ratio}
	switch {
	case ratio >= 1.5:
		cs.Name = "vol_surge"
		cs.Weight = 8
		cs.Fired = true
		return ConfluenceResult{Score: 8, Factors: []string{"vol_surge"}, Components: []ComponentScore{cs}}
	case ratio >= 1.2:
		cs.Name = "vol_above_avg"
		cs.Weight = 6
		cs.Fired = true
		return ConfluenceResult{Score: 6, Factors: []string{"vol_above_avg"}, Components: []ComponentScore{cs}}
	case ratio >= 1.0:
		cs.Weight = 4
		return ConfluenceResult{Score: 4, Components: []ComponentScore{cs}}
	case ratio >= 0.8:
		cs.Weight = 2
		return ConfluenceResult{Score: 2, Components: []ComponentScore{cs}}
	default:
		return ConfluenceResult{Components: []ComponentScore{cs}}
	}
}

// ScoreCandle evaluates candle quality — body ratio and directional close (0-8).
func ScoreCandle(bar Bar, isLong bool) ConfluenceResult {
	barRange := bar.High - bar.Low
	if barRange <= 0 {
		return ConfluenceResult{Components: []ComponentScore{{Name: "candle", Group: "technical", Value: 0}}}
	}
	bodyRatio := math.Abs(bar.Close-bar.Open) / barRange
	directional := (isLong && bar.Close > bar.Open) || (!isLong && bar.Close < bar.Open)
	cs := ComponentScore{Name: "candle", Group: "technical", Value: bodyRatio}
	switch {
	case bodyRatio > 0.7 && directional:
		cs.Name = "candle_strong"
		cs.Weight = 8
		cs.Fired = true
		return ConfluenceResult{Score: 8, Factors: []string{"candle_strong"}, Components: []ComponentScore{cs}}
	case bodyRatio > 0.5 && directional:
		cs.Name = "candle_ok"
		cs.Weight = 6
		cs.Fired = true
		return ConfluenceResult{Score: 6, Factors: []string{"candle_ok"}, Components: []ComponentScore{cs}}
	case directional:
		cs.Name = "candle_dir"
		cs.Weight = 4
		cs.Fired = true
		return ConfluenceResult{Score: 4, Factors: []string{"candle_dir"}, Components: []ComponentScore{cs}}
	default:
		return ConfluenceResult{Components: []ComponentScore{cs}}
	}
}

// ScoreBB evaluates Bollinger Band %B position (0-7).
// Long: 0.5-0.8 ideal (trending, not overextended). Short: 0.2-0.5 ideal.
func ScoreBB(ind IndicatorData, isLong bool) ConfluenceResult {
	cs := ComponentScore{Name: "bb", Group: "technical", Value: ind.BBPercentB}
	if isLong {
		switch {
		case ind.BBPercentB >= 0.5 && ind.BBPercentB <= 0.8:
			cs.Name = "bb_trend"
			cs.Weight = 7
			cs.Fired = true
			return ConfluenceResult{Score: 7, Factors: []string{"bb_trend"}, Components: []ComponentScore{cs}}
		case ind.BBPercentB >= 0.3 && ind.BBPercentB < 0.5:
			cs.Weight = 4
			return ConfluenceResult{Score: 4, Components: []ComponentScore{cs}}
		}
	} else {
		switch {
		case ind.BBPercentB >= 0.2 && ind.BBPercentB <= 0.5:
			cs.Name = "bb_trend"
			cs.Weight = 7
			cs.Fired = true
			return ConfluenceResult{Score: 7, Factors: []string{"bb_trend"}, Components: []ComponentScore{cs}}
		case ind.BBPercentB > 0.5 && ind.BBPercentB <= 0.7:
			cs.Weight = 4
			return ConfluenceResult{Score: 4, Components: []ComponentScore{cs}}
		}
	}
	return ConfluenceResult{Components: []ComponentScore{cs}}
}

// ScoreHTFBias evaluates higher-timeframe bias agreement (0-8).
func ScoreHTFBias(ind IndicatorData, isLong bool) ConfluenceResult {
	htf, ok := ind.HTF["1d"]
	if !ok || htf.Bias == "" {
		return ConfluenceResult{Components: []ComponentScore{{Name: "htf_bias", Group: "technical", Value: 0}}}
	}
	cs := ComponentScore{Name: "htf_bias", Group: "technical"}
	if (isLong && htf.Bias == "BULLISH") || (!isLong && htf.Bias == "BEARISH") {
		cs.Name = "htf_agree"
		cs.Weight = 8
		cs.Fired = true
		cs.Value = 1
		return ConfluenceResult{Score: 8, Factors: []string{"htf_agree"}, Components: []ComponentScore{cs}}
	}
	if htf.Bias == "NEUTRAL" {
		cs.Name = "htf_neutral"
		cs.Weight = 4
		cs.Fired = true
		cs.Value = 0.5
		return ConfluenceResult{Score: 4, Factors: []string{"htf_neutral"}, Components: []ComponentScore{cs}}
	}
	return ConfluenceResult{Components: []ComponentScore{cs}}
}

// ScoreVWAP evaluates VWAP alignment (0-7).
// Long: price above VWAP. Short: price below VWAP.
func ScoreVWAP(bar Bar, ind IndicatorData, isLong bool) ConfluenceResult {
	cs := ComponentScore{Name: "vwap", Group: "technical"}
	if ind.VWAP <= 0 {
		return ConfluenceResult{Components: []ComponentScore{cs}}
	}
	cs.Value = (bar.Close - ind.VWAP) / ind.VWAP
	if (isLong && bar.Close > ind.VWAP) || (!isLong && bar.Close < ind.VWAP) {
		cs.Name = "vwap_aligned"
		cs.Weight = 7
		cs.Fired = true
		return ConfluenceResult{Score: 7, Factors: []string{"vwap_aligned"}, Components: []ComponentScore{cs}}
	}
	return ConfluenceResult{Components: []ComponentScore{cs}}
}

// ScoreDarkPool evaluates dark pool activity as a confluence signal (0-10).
// Returns zero gracefully when no dark pool data is available.
// Uses Z-score normalization for the elevated ratio check when available,
// falling back to a static 0.50 threshold when Z-score is zero.
func ScoreDarkPool(ind IndicatorData, isLong bool) ConfluenceResult {
	if ind.DPRatio <= 0 {
		return ConfluenceResult{Components: []ComponentScore{
			{Name: "dp_elevated", Group: "darkpool", Value: ind.DPRatio},
			{Name: "dp_direction", Group: "darkpool", Value: ind.DPBuyRatio},
			{Name: "dp_blocks", Group: "darkpool", Value: ind.DPLargePrintPct},
		}}
	}
	score := 0
	var factors []string
	components := make([]ComponentScore, 0, 3)

	// Elevated DP ratio
	csElevated := ComponentScore{Name: "dp_elevated", Group: "darkpool", Value: ind.DPRatioZScore}
	if ind.DPRatioZScore >= 1.5 {
		score += 3
		factors = append(factors, "dp_elevated")
		csElevated.Weight = 3
		csElevated.Fired = true
	} else if ind.DPRatioZScore == 0 && ind.DPRatio >= 0.50 {
		score += 3
		factors = append(factors, "dp_elevated")
		csElevated.Value = ind.DPRatio
		csElevated.Weight = 3
		csElevated.Fired = true
	}
	components = append(components, csElevated)

	// Directional pressure
	csDir := ComponentScore{Name: "dp_direction", Group: "darkpool", Value: ind.DPBuyRatio}
	if isLong && ind.DPBuyRatio >= 0.60 {
		score += 4
		factors = append(factors, "dp_buy")
		csDir.Name = "dp_buy"
		csDir.Weight = 4
		csDir.Fired = true
	} else if !isLong && ind.DPBuyRatio <= 0.40 {
		score += 4
		factors = append(factors, "dp_sell")
		csDir.Name = "dp_sell"
		csDir.Weight = 4
		csDir.Fired = true
	}
	components = append(components, csDir)

	// Large block prints
	csBlocks := ComponentScore{Name: "dp_blocks", Group: "darkpool", Value: ind.DPLargePrintPct}
	if ind.DPLargePrintPct >= 0.15 {
		score += 3
		factors = append(factors, "dp_blocks")
		csBlocks.Weight = 3
		csBlocks.Fired = true
	}
	components = append(components, csBlocks)

	return ConfluenceResult{Score: score, Factors: factors, Components: components}
}

// DPVeto returns true (blocked) when dark pool flow opposes the trade direction.
// When no DP data is available (DPRatio <= 0), the veto does not fire.
func DPVeto(ind IndicatorData, isLong bool, buyRatioMin, sellRatioMax float64) (blocked bool, reason string) {
	if ind.DPRatio <= 0 {
		return false, "" // no data = no veto
	}
	if isLong && ind.DPBuyRatio < buyRatioMin {
		return true, fmt.Sprintf("dp_veto_long: buy_ratio %.2f < %.2f", ind.DPBuyRatio, buyRatioMin)
	}
	if !isLong && ind.DPBuyRatio > sellRatioMax {
		return true, fmt.Sprintf("dp_veto_short: buy_ratio %.2f > %.2f", ind.DPBuyRatio, sellRatioMax)
	}
	return false, ""
}

// DPSizingMultiplier returns a position sizing multiplier (1.0-maxBoost) based on DP alignment.
// Returns 1.0 when no DP data, Z-score is not positive, or direction is not aligned.
func DPSizingMultiplier(ind IndicatorData, isLong bool, maxBoost float64) float64 {
	if ind.DPRatioZScore <= 0 {
		return 1.0
	}
	aligned := (isLong && ind.DPBuyRatio >= 0.60) || (!isLong && ind.DPBuyRatio <= 0.40)
	if !aligned {
		return 1.0
	}
	if ind.DPRatioZScore >= 2.0 {
		return maxBoost
	}
	if ind.DPRatioZScore >= 1.5 {
		return 1.0 + (maxBoost-1.0)*0.5
	}
	return 1.0
}

// ScoreRetestQuality evaluates ORB retest quality on 4 factors (max 16 points).
// Parameters are pre-computed by the caller from retest bar data:
//   - retestBarCount: number of bars between breakout and retest confirmation
//   - pullbackDepthPct: how deep into ORB range the retest pulled back (0.0-1.0)
//   - retestAvgVolume: average volume of retest bars (excluding confirm bar)
//   - breakoutVolume: volume of the breakout bar
//   - confirmBodyRatio: abs(close-open)/(high-low) of the confirmation bar (0.0-1.0)
//   - confirmDirectional: true if confirm bar closed in the trade direction
func ScoreRetestQuality(retestBarCount int, pullbackDepthPct float64, retestAvgVolume float64, breakoutVolume float64, confirmBodyRatio float64, confirmDirectional bool) ConfluenceResult {
	var score int
	var factors []string
	components := make([]ComponentScore, 0, 4)

	// Factor 1: Speed (0-5)
	csSpeed := ComponentScore{Name: "rq_speed", Group: "retest", Value: float64(retestBarCount)}
	switch {
	case retestBarCount <= 3:
		score += 5
		factors = append(factors, "rq_speed")
		csSpeed.Weight = 5
		csSpeed.Fired = true
	case retestBarCount <= 6:
		score += 2
		csSpeed.Weight = 2
	}
	components = append(components, csSpeed)

	// Factor 2: Pullback depth (0-5)
	csDepth := ComponentScore{Name: "rq_shallow", Group: "retest", Value: pullbackDepthPct}
	switch {
	case pullbackDepthPct < 0.382:
		score += 5
		factors = append(factors, "rq_shallow")
		csDepth.Weight = 5
		csDepth.Fired = true
	case pullbackDepthPct < 0.50:
		score += 3
		csDepth.Weight = 3
	}
	components = append(components, csDepth)

	// Factor 3: Volume dry-up (0-3)
	csVol := ComponentScore{Name: "rq_dryup", Group: "retest"}
	if breakoutVolume > 0 {
		volRatio := retestAvgVolume / breakoutVolume
		csVol.Value = volRatio
		switch {
		case volRatio < 0.60:
			score += 3
			factors = append(factors, "rq_dryup")
			csVol.Weight = 3
			csVol.Fired = true
		case volRatio < 0.80:
			score++
			csVol.Weight = 1
		}
	}
	components = append(components, csVol)

	// Factor 4: Confirm candle quality (0-3)
	csConfirm := ComponentScore{Name: "rq_confirm", Group: "retest", Value: confirmBodyRatio}
	if confirmBodyRatio > 0.6 && confirmDirectional {
		score += 3
		factors = append(factors, "rq_confirm")
		csConfirm.Weight = 3
		csConfirm.Fired = true
	}
	components = append(components, csConfirm)

	return ConfluenceResult{Score: score, Factors: factors, Components: components}
}

// ────────────────────────────────────────────────────────────────────
// ComputeBaseConfluence evaluates all 8 universal factors.
// Max score: 78 (15+15+10+8+8+7+8+7).
// Strategies add their own factors on top via MergeConfluence.
// ────────────────────────────────────────────────────────────────────

// ComputeBaseConfluence evaluates the 8 universal confluence factors
// that apply to any strategy receiving a Bar and IndicatorData.
func ComputeBaseConfluence(bar Bar, ind IndicatorData, isLong bool) ConfluenceResult {
	return MergeConfluence(
		ScoreEMAStack(bar, ind, isLong),
		ScoreADX(ind),
		ScoreRSI(ind, isLong),
		ScoreVolume(bar, ind),
		ScoreCandle(bar, isLong),
		ScoreBB(ind, isLong),
		ScoreHTFBias(ind, isLong),
		ScoreVWAP(bar, ind, isLong),
	)
}
