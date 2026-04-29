package strategy

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// ComponentScore captures a single confluence scorer's contribution.
type ComponentScore struct {
	Name     string             // e.g. "ema_stack", "adx_strong"
	Group    string             // e.g. "trend", "momentum", "volume"
	Weight   int                // max points this scorer can award
	Value    float64            // optional numeric input (e.g. ADX value, RSI value)
	Fired    bool               // true if the scorer contributed > 0 points
	SubScore int // points awarded by this scorer (matches r.Score for single-component results)
	// Inputs holds the raw numeric inputs the scorer consumed (diagnostic;
	// mirrored to EntryGatedComponent.Inputs by reference, not deep-copied).
	// Treat as immutable post-return: scorers must not mutate it after
	// constructing the ConfluenceResult.
	Inputs map[string]float64
}

// ConfluenceResult holds the computed confluence score and contributing factors.
type ConfluenceResult struct {
	Score      int
	Factors    []string
	Components []ComponentScore
}

// MergeConfluence combines multiple ConfluenceResults into one by summing scores
// and concatenating factor lists and component slices.
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

// ComponentsJSON returns the Components serialized as a compact JSON string
// for inclusion in signal tags. Returns "" if no components.
func (cr ConfluenceResult) ComponentsJSON() string {
	if len(cr.Components) == 0 {
		return ""
	}
	type comp struct {
		Name   string  `json:"n"`
		Group  string  `json:"g"`
		Weight int     `json:"w"`
		Value  float64 `json:"v,omitempty"`
		Fired  bool    `json:"f"`
	}
	// Explicit field copy (not a struct conversion). The diagnostic fields
	// SubScore and Inputs on ComponentScore are intentionally excluded from
	// this compact tag-string JSON — they are surfaced via EntryGated payloads
	// instead, where they belong. Including them here would bloat every fired
	// signal's tag column in strategy_signal_events.
	out := make([]comp, len(cr.Components))
	for i, c := range cr.Components {
		out[i] = comp{Name: c.Name, Group: c.Group, Weight: c.Weight, Value: c.Value, Fired: c.Fired}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// ────────────────────────────────────────────────────────────────────
// Universal confluence factors — applicable to any strategy.
// Each returns a partial ConfluenceResult with its score and factor name.
// ────────────────────────────────────────────────────────────────────

// ScoreEMAStack evaluates EMA alignment (0-15).
// Long: Close > EMA9 > EMA21 > EMA50. Short: reversed.
func ScoreEMAStack(bar Bar, ind IndicatorData, isLong bool) ConfluenceResult {
	inputs := map[string]float64{
		"close": bar.Close,
		"ema9":  ind.EMA9,
		"ema21": ind.EMA21,
		"ema50": ind.EMA50,
		"count": 0,
	}
	if ind.EMA9 <= 0 || ind.EMA21 <= 0 || ind.EMA50 <= 0 {
		return ConfluenceResult{Components: []ComponentScore{{Name: "ema_stack", Group: "technical", Value: 0, Inputs: inputs}}}
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
	inputs["count"] = float64(count)
	comp := ComponentScore{Name: "ema_stack", Group: "trend", Weight: 15, Value: float64(count), Inputs: inputs}
	switch count {
	case 3:
		comp.Fired = true
		comp.SubScore = 15
		return ConfluenceResult{Score: 15, Factors: []string{"ema_stack"}, Components: []ComponentScore{comp}}
	case 2:
		comp.Fired = true
		comp.SubScore = 10
		return ConfluenceResult{Score: 10, Factors: []string{"ema_partial"}, Components: []ComponentScore{comp}}
	case 1:
		comp.Fired = true
		comp.SubScore = 5
		return ConfluenceResult{Score: 5, Components: []ComponentScore{comp}}
	default:
		return ConfluenceResult{Components: []ComponentScore{comp}}
	}
}

// ScoreADX evaluates trend strength via ADX (0-15).
func ScoreADX(ind IndicatorData) ConfluenceResult {
	comp := ComponentScore{
		Name: "adx", Group: "trend", Weight: 15, Value: ind.ADX,
		Inputs: map[string]float64{"adx": ind.ADX},
	}
	if ind.ADX <= 0 {
		return ConfluenceResult{Components: []ComponentScore{comp}}
	}
	switch {
	case ind.ADX >= 30:
		comp.Fired = true
		comp.SubScore = 15
		return ConfluenceResult{Score: 15, Factors: []string{"adx_strong"}, Components: []ComponentScore{comp}}
	case ind.ADX >= 25:
		comp.Fired = true
		comp.SubScore = 12
		return ConfluenceResult{Score: 12, Factors: []string{"adx_trend"}, Components: []ComponentScore{comp}}
	case ind.ADX >= 20:
		comp.Fired = true
		comp.SubScore = 8
		return ConfluenceResult{Score: 8, Factors: []string{"adx_ok"}, Components: []ComponentScore{comp}}
	case ind.ADX >= 15:
		comp.Fired = true
		comp.SubScore = 4
		return ConfluenceResult{Score: 4, Components: []ComponentScore{comp}}
	default:
		return ConfluenceResult{Components: []ComponentScore{comp}}
	}
}

// ScoreRSI evaluates RSI position for directional quality (0-10).
// Long: 45-65 ideal. Short: 35-55 ideal.
func ScoreRSI(ind IndicatorData, isLong bool) ConfluenceResult {
	comp := ComponentScore{
		Name: "rsi", Group: "momentum", Weight: 10, Value: ind.RSI,
		Inputs: map[string]float64{"rsi": ind.RSI},
	}
	if ind.RSI <= 0 {
		return ConfluenceResult{Components: []ComponentScore{comp}}
	}
	if isLong {
		switch {
		case ind.RSI >= 45 && ind.RSI <= 65:
			comp.Fired = true
			comp.SubScore = 10
			return ConfluenceResult{Score: 10, Factors: []string{"rsi_ideal"}, Components: []ComponentScore{comp}}
		case (ind.RSI >= 35 && ind.RSI < 45) || (ind.RSI > 65 && ind.RSI <= 75):
			comp.Fired = true
			comp.SubScore = 5
			return ConfluenceResult{Score: 5, Factors: []string{"rsi_ok"}, Components: []ComponentScore{comp}}
		}
	} else {
		switch {
		case ind.RSI >= 35 && ind.RSI <= 55:
			comp.Fired = true
			comp.SubScore = 10
			return ConfluenceResult{Score: 10, Factors: []string{"rsi_ideal"}, Components: []ComponentScore{comp}}
		case (ind.RSI > 55 && ind.RSI <= 65) || (ind.RSI >= 25 && ind.RSI < 35):
			comp.Fired = true
			comp.SubScore = 5
			return ConfluenceResult{Score: 5, Factors: []string{"rsi_ok"}, Components: []ComponentScore{comp}}
		}
	}
	return ConfluenceResult{Components: []ComponentScore{comp}}
}

// ScoreVolume evaluates volume relative to its SMA (0-8).
func ScoreVolume(bar Bar, ind IndicatorData) ConfluenceResult {
	comp := ComponentScore{
		Name: "volume", Group: "volume", Weight: 8,
		Inputs: map[string]float64{"volume": bar.Volume, "volumeSMA": ind.VolumeSMA, "ratio": 0},
	}
	if ind.VolumeSMA <= 0 {
		return ConfluenceResult{Components: []ComponentScore{comp}}
	}
	ratio := bar.Volume / ind.VolumeSMA
	comp.Value = ratio
	comp.Inputs["ratio"] = ratio
	switch {
	case ratio >= 1.5:
		comp.Fired = true
		comp.SubScore = 8
		return ConfluenceResult{Score: 8, Factors: []string{"vol_surge"}, Components: []ComponentScore{comp}}
	case ratio >= 1.2:
		comp.Fired = true
		comp.SubScore = 6
		return ConfluenceResult{Score: 6, Factors: []string{"vol_above_avg"}, Components: []ComponentScore{comp}}
	case ratio >= 1.0:
		comp.Fired = true
		comp.SubScore = 4
		return ConfluenceResult{Score: 4, Components: []ComponentScore{comp}}
	case ratio >= 0.8:
		comp.Fired = true
		comp.SubScore = 2
		return ConfluenceResult{Score: 2, Components: []ComponentScore{comp}}
	default:
		return ConfluenceResult{Components: []ComponentScore{comp}}
	}
}

// ScoreCandle evaluates candle quality — body ratio and directional close (0-8).
func ScoreCandle(bar Bar, isLong bool) ConfluenceResult {
	barRange := bar.High - bar.Low
	if barRange <= 0 {
		return ConfluenceResult{Components: []ComponentScore{{
			Name: "candle", Group: "price_action", Weight: 8,
			Inputs: map[string]float64{"bodyRatio": 0, "range": 0, "directional": 0},
		}}}
	}
	bodyRatio := math.Abs(bar.Close-bar.Open) / barRange
	directional := (isLong && bar.Close > bar.Open) || (!isLong && bar.Close < bar.Open)
	directionalFlag := 0.0
	if directional {
		directionalFlag = 1
	}
	comp := ComponentScore{
		Name: "candle", Group: "price_action", Weight: 8, Value: bodyRatio,
		Inputs: map[string]float64{"bodyRatio": bodyRatio, "range": barRange, "directional": directionalFlag},
	}
	switch {
	case bodyRatio > 0.7 && directional:
		comp.Fired = true
		comp.SubScore = 8
		return ConfluenceResult{Score: 8, Factors: []string{"candle_strong"}, Components: []ComponentScore{comp}}
	case bodyRatio > 0.5 && directional:
		comp.Fired = true
		comp.SubScore = 6
		return ConfluenceResult{Score: 6, Factors: []string{"candle_ok"}, Components: []ComponentScore{comp}}
	case directional:
		comp.Fired = true
		comp.SubScore = 4
		return ConfluenceResult{Score: 4, Factors: []string{"candle_dir"}, Components: []ComponentScore{comp}}
	default:
		return ConfluenceResult{Components: []ComponentScore{comp}}
	}
}

// ScoreBB evaluates Bollinger Band %B position (0-7).
// Long: 0.5-0.8 ideal (trending, not overextended). Short: 0.2-0.5 ideal.
func ScoreBB(ind IndicatorData, isLong bool) ConfluenceResult {
	comp := ComponentScore{
		Name: "bb", Group: "volatility", Weight: 7, Value: ind.BBPercentB,
		Inputs: map[string]float64{"percentB": ind.BBPercentB},
	}
	if isLong {
		switch {
		case ind.BBPercentB >= 0.5 && ind.BBPercentB <= 0.8:
			comp.Fired = true
			comp.SubScore = 7
			return ConfluenceResult{Score: 7, Factors: []string{"bb_trend"}, Components: []ComponentScore{comp}}
		case ind.BBPercentB >= 0.3 && ind.BBPercentB < 0.5:
			comp.Fired = true
			comp.SubScore = 4
			return ConfluenceResult{Score: 4, Components: []ComponentScore{comp}}
		}
	} else {
		switch {
		case ind.BBPercentB >= 0.2 && ind.BBPercentB <= 0.5:
			comp.Fired = true
			comp.SubScore = 7
			return ConfluenceResult{Score: 7, Factors: []string{"bb_trend"}, Components: []ComponentScore{comp}}
		case ind.BBPercentB > 0.5 && ind.BBPercentB <= 0.7:
			comp.Fired = true
			comp.SubScore = 4
			return ConfluenceResult{Score: 4, Components: []ComponentScore{comp}}
		}
	}
	return ConfluenceResult{Components: []ComponentScore{comp}}
}

// ScoreHTFBias evaluates higher-timeframe bias agreement (0-8).
func ScoreHTFBias(ind IndicatorData, isLong bool) ConfluenceResult {
	// Encode bias as 2=BULLISH, 1=NEUTRAL, 0=BEARISH/missing.
	htf, ok := ind.HTF["1d"]
	biasCode := 0.0
	if ok {
		switch htf.Bias {
		case "BULLISH":
			biasCode = 2
		case "NEUTRAL":
			biasCode = 1
		}
	}
	comp := ComponentScore{
		Name: "htf_bias", Group: "trend", Weight: 8,
		Inputs: map[string]float64{"bias": biasCode},
	}
	if !ok || htf.Bias == "" {
		return ConfluenceResult{Components: []ComponentScore{comp}}
	}
	if (isLong && htf.Bias == "BULLISH") || (!isLong && htf.Bias == "BEARISH") {
		comp.Fired = true
		comp.SubScore = 8
		return ConfluenceResult{Score: 8, Factors: []string{"htf_agree"}, Components: []ComponentScore{comp}}
	}
	if htf.Bias == "NEUTRAL" {
		comp.Fired = true
		comp.SubScore = 4
		return ConfluenceResult{Score: 4, Factors: []string{"htf_neutral"}, Components: []ComponentScore{comp}}
	}
	return ConfluenceResult{Components: []ComponentScore{comp}} // opposing bias: 0
}

// ScoreVWAP evaluates VWAP alignment (0-7).
// Long: price above VWAP. Short: price below VWAP.
func ScoreVWAP(bar Bar, ind IndicatorData, isLong bool) ConfluenceResult {
	comp := ComponentScore{
		Name: "vwap", Group: "price_action", Weight: 7, Value: ind.VWAP,
		Inputs: map[string]float64{"close": bar.Close, "vwap": ind.VWAP, "distancePct": 0},
	}
	if ind.VWAP <= 0 {
		return ConfluenceResult{Components: []ComponentScore{comp}}
	}
	distancePct := (bar.Close - ind.VWAP) / ind.VWAP
	comp.Value = distancePct
	comp.Inputs["distancePct"] = distancePct
	if (isLong && bar.Close > ind.VWAP) || (!isLong && bar.Close < ind.VWAP) {
		comp.Fired = true
		comp.SubScore = 7
		return ConfluenceResult{Score: 7, Factors: []string{"vwap_aligned"}, Components: []ComponentScore{comp}}
	}
	return ConfluenceResult{Components: []ComponentScore{comp}}
}

// ScoreDarkPool evaluates dark pool activity as a confluence signal (0-10).
// Returns zero gracefully when no dark pool data is available.
// Uses Z-score normalization for the elevated ratio check when available,
// falling back to a static 0.50 threshold when Z-score is zero.
func ScoreDarkPool(ind IndicatorData, isLong bool) ConfluenceResult {
	comp := ComponentScore{
		Name: "darkpool", Group: "flow", Weight: 10, Value: ind.DPRatio,
		Inputs: map[string]float64{
			"dpRatio":         ind.DPRatio,
			"dpRatioZScore":   ind.DPRatioZScore,
			"dpBuyRatio":      ind.DPBuyRatio,
			"dpLargePrintPct": ind.DPLargePrintPct,
		},
	}
	if ind.DPRatio <= 0 {
		return ConfluenceResult{Components: []ComponentScore{comp}}
	}
	score := 0
	var factors []string

	if ind.DPRatioZScore >= 1.5 {
		score += 3
		factors = append(factors, "dp_elevated")
	} else if ind.DPRatioZScore == 0 && ind.DPRatio >= 0.50 {
		score += 3
		factors = append(factors, "dp_elevated")
	}

	if isLong && ind.DPBuyRatio >= 0.60 {
		score += 4
		factors = append(factors, "dp_buy")
	} else if !isLong && ind.DPBuyRatio <= 0.40 {
		score += 4
		factors = append(factors, "dp_sell")
	}

	if ind.DPLargePrintPct >= 0.15 {
		score += 3
		factors = append(factors, "dp_blocks")
	}

	comp.Fired = score > 0
	comp.SubScore = score
	return ConfluenceResult{Score: score, Factors: factors, Components: []ComponentScore{comp}}
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

	switch {
	case retestBarCount <= 3:
		score += 5
		factors = append(factors, "rq_speed")
	case retestBarCount <= 6:
		score += 2
	}

	switch {
	case pullbackDepthPct < 0.382:
		score += 5
		factors = append(factors, "rq_shallow")
	case pullbackDepthPct < 0.50:
		score += 3
	}

	volRatio := 0.0
	if breakoutVolume > 0 {
		volRatio = retestAvgVolume / breakoutVolume
		switch {
		case volRatio < 0.60:
			score += 3
			factors = append(factors, "rq_dryup")
		case volRatio < 0.80:
			score++
		}
	}

	if confirmBodyRatio > 0.6 && confirmDirectional {
		score += 3
		factors = append(factors, "rq_confirm")
	}

	comp := ComponentScore{
		Name: "retest_quality", Group: "setup", Weight: 16, Value: pullbackDepthPct,
		Fired: score > 0, SubScore: score,
		Inputs: map[string]float64{
			"retestBarCount":   float64(retestBarCount),
			"pullbackDepthPct": pullbackDepthPct,
			"volRatio":         volRatio,
			"confirmBodyRatio": confirmBodyRatio,
		},
	}
	return ConfluenceResult{Score: score, Factors: factors, Components: []ComponentScore{comp}}
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
