package positionmonitor

import (
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// CryptoSessionMultiplier returns a stop-widening multiplier based on the
// current UTC hour and day of week. Crypto order books thin dramatically
// during off-peak hours, making tight stops vulnerable to flash wicks.
//
//	Peak   (13:00–18:00 UTC weekdays) → 1.0x  (full liquidity)
//	Medium (08:00–13:00, 18:00–22:00) → 1.2x  (moderate liquidity)
//	Low    (22:00–08:00 + weekends)   → 1.5x  (thin books, wick risk)
func CryptoSessionMultiplier(now time.Time) float64 {
	hour := now.UTC().Hour()
	wd := now.UTC().Weekday()
	isWeekend := wd == time.Saturday || wd == time.Sunday

	if isWeekend {
		return 1.5
	}

	switch {
	case hour >= 13 && hour < 18:
		return 1.0
	case hour >= 8 && hour < 13, hour >= 18 && hour < 22:
		return 1.2
	default:
		return 1.5
	}
}

func sessionAdjustRule(rule domain.ExitRule, assetClass domain.AssetClass, now time.Time) domain.ExitRule {
	if assetClass != domain.AssetClassCrypto {
		return rule
	}

	mult := CryptoSessionMultiplier(now)
	if mult == 1.0 {
		return rule
	}

	switch rule.Type {
	case domain.ExitRuleTrailingStop, domain.ExitRuleMaxLoss:
		if rule.Params["pct"] > 0 {
			adjusted := copyRuleParams(rule)
			adjusted.Params["pct"] = rule.Params["pct"] * mult
			return adjusted
		}
	case domain.ExitRuleVolatilityStop:
		if rule.Params["atr_multiplier"] > 0 {
			adjusted := copyRuleParams(rule)
			adjusted.Params["atr_multiplier"] = rule.Params["atr_multiplier"] * mult
			return adjusted
		}
	}

	return rule
}

func copyRuleParams(rule domain.ExitRule) domain.ExitRule {
	params := make(map[string]float64, len(rule.Params))
	for k, v := range rule.Params {
		params[k] = v
	}
	return domain.ExitRule{Type: rule.Type, Params: params}
}
