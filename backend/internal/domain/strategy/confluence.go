package strategy

import (
	"math"
	"strings"
)

// ConfluenceResult holds the computed confluence score and contributing factors.
type ConfluenceResult struct {
	Score   int
	Factors []string
}

// MergeConfluence combines multiple ConfluenceResults into one by summing scores
// and concatenating factor lists.
func MergeConfluence(results ...ConfluenceResult) ConfluenceResult {
	var merged ConfluenceResult
	for _, r := range results {
		merged.Score += r.Score
		merged.Factors = append(merged.Factors, r.Factors...)
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
		return ConfluenceResult{}
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
	switch count {
	case 3:
		return ConfluenceResult{Score: 15, Factors: []string{"ema_stack"}}
	case 2:
		return ConfluenceResult{Score: 10, Factors: []string{"ema_partial"}}
	case 1:
		return ConfluenceResult{Score: 5}
	default:
		return ConfluenceResult{}
	}
}

// ScoreADX evaluates trend strength via ADX (0-15).
func ScoreADX(ind IndicatorData) ConfluenceResult {
	if ind.ADX <= 0 {
		return ConfluenceResult{}
	}
	switch {
	case ind.ADX >= 30:
		return ConfluenceResult{Score: 15, Factors: []string{"adx_strong"}}
	case ind.ADX >= 25:
		return ConfluenceResult{Score: 12, Factors: []string{"adx_trend"}}
	case ind.ADX >= 20:
		return ConfluenceResult{Score: 8, Factors: []string{"adx_ok"}}
	case ind.ADX >= 15:
		return ConfluenceResult{Score: 4}
	default:
		return ConfluenceResult{}
	}
}

// ScoreRSI evaluates RSI position for directional quality (0-10).
// Long: 45-65 ideal. Short: 35-55 ideal.
func ScoreRSI(ind IndicatorData, isLong bool) ConfluenceResult {
	if ind.RSI <= 0 {
		return ConfluenceResult{}
	}
	if isLong {
		switch {
		case ind.RSI >= 45 && ind.RSI <= 65:
			return ConfluenceResult{Score: 10, Factors: []string{"rsi_ideal"}}
		case (ind.RSI >= 35 && ind.RSI < 45) || (ind.RSI > 65 && ind.RSI <= 75):
			return ConfluenceResult{Score: 5, Factors: []string{"rsi_ok"}}
		}
	} else {
		switch {
		case ind.RSI >= 35 && ind.RSI <= 55:
			return ConfluenceResult{Score: 10, Factors: []string{"rsi_ideal"}}
		case (ind.RSI > 55 && ind.RSI <= 65) || (ind.RSI >= 25 && ind.RSI < 35):
			return ConfluenceResult{Score: 5, Factors: []string{"rsi_ok"}}
		}
	}
	return ConfluenceResult{}
}

// ScoreVolume evaluates volume relative to its SMA (0-8).
func ScoreVolume(bar Bar, ind IndicatorData) ConfluenceResult {
	if ind.VolumeSMA <= 0 {
		return ConfluenceResult{}
	}
	ratio := bar.Volume / ind.VolumeSMA
	switch {
	case ratio >= 1.5:
		return ConfluenceResult{Score: 8, Factors: []string{"vol_surge"}}
	case ratio >= 1.2:
		return ConfluenceResult{Score: 6, Factors: []string{"vol_above_avg"}}
	case ratio >= 1.0:
		return ConfluenceResult{Score: 4}
	case ratio >= 0.8:
		return ConfluenceResult{Score: 2}
	default:
		return ConfluenceResult{}
	}
}

// ScoreCandle evaluates candle quality — body ratio and directional close (0-8).
func ScoreCandle(bar Bar, isLong bool) ConfluenceResult {
	barRange := bar.High - bar.Low
	if barRange <= 0 {
		return ConfluenceResult{}
	}
	bodyRatio := math.Abs(bar.Close-bar.Open) / barRange
	directional := (isLong && bar.Close > bar.Open) || (!isLong && bar.Close < bar.Open)
	switch {
	case bodyRatio > 0.7 && directional:
		return ConfluenceResult{Score: 8, Factors: []string{"candle_strong"}}
	case bodyRatio > 0.5 && directional:
		return ConfluenceResult{Score: 6, Factors: []string{"candle_ok"}}
	case directional:
		return ConfluenceResult{Score: 4, Factors: []string{"candle_dir"}}
	default:
		return ConfluenceResult{}
	}
}

// ScoreBB evaluates Bollinger Band %B position (0-7).
// Long: 0.5-0.8 ideal (trending, not overextended). Short: 0.2-0.5 ideal.
func ScoreBB(ind IndicatorData, isLong bool) ConfluenceResult {
	if isLong {
		switch {
		case ind.BBPercentB >= 0.5 && ind.BBPercentB <= 0.8:
			return ConfluenceResult{Score: 7, Factors: []string{"bb_trend"}}
		case ind.BBPercentB >= 0.3 && ind.BBPercentB < 0.5:
			return ConfluenceResult{Score: 4}
		}
	} else {
		switch {
		case ind.BBPercentB >= 0.2 && ind.BBPercentB <= 0.5:
			return ConfluenceResult{Score: 7, Factors: []string{"bb_trend"}}
		case ind.BBPercentB > 0.5 && ind.BBPercentB <= 0.7:
			return ConfluenceResult{Score: 4}
		}
	}
	return ConfluenceResult{}
}

// ScoreHTFBias evaluates higher-timeframe bias agreement (0-8).
func ScoreHTFBias(ind IndicatorData, isLong bool) ConfluenceResult {
	htf, ok := ind.HTF["1d"]
	if !ok || htf.Bias == "" {
		return ConfluenceResult{}
	}
	if (isLong && htf.Bias == "BULLISH") || (!isLong && htf.Bias == "BEARISH") {
		return ConfluenceResult{Score: 8, Factors: []string{"htf_agree"}}
	}
	if htf.Bias == "NEUTRAL" {
		return ConfluenceResult{Score: 4, Factors: []string{"htf_neutral"}}
	}
	return ConfluenceResult{} // opposing bias: 0
}

// ScoreVWAP evaluates VWAP alignment (0-7).
// Long: price above VWAP. Short: price below VWAP.
func ScoreVWAP(bar Bar, ind IndicatorData, isLong bool) ConfluenceResult {
	if ind.VWAP <= 0 {
		return ConfluenceResult{}
	}
	if (isLong && bar.Close > ind.VWAP) || (!isLong && bar.Close < ind.VWAP) {
		return ConfluenceResult{Score: 7, Factors: []string{"vwap_aligned"}}
	}
	return ConfluenceResult{}
}

// ScoreDarkPool evaluates dark pool activity as a confluence signal (0-10).
// Returns zero gracefully when no dark pool data is available.
func ScoreDarkPool(ind IndicatorData, isLong bool) ConfluenceResult {
	if ind.DPRatio <= 0 {
		return ConfluenceResult{}
	}
	score := 0
	var factors []string

	// Elevated DP ratio (baseline ~35%)
	if ind.DPRatio >= 0.50 {
		score += 3
		factors = append(factors, "dp_elevated")
	}

	// Directional pressure
	if isLong && ind.DPBuyRatio >= 0.60 {
		score += 4
		factors = append(factors, "dp_buy")
	} else if !isLong && ind.DPBuyRatio <= 0.40 {
		score += 4
		factors = append(factors, "dp_sell")
	}

	// Large block prints
	if ind.DPLargePrintPct >= 0.15 {
		score += 3
		factors = append(factors, "dp_blocks")
	}

	return ConfluenceResult{Score: score, Factors: factors}
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
