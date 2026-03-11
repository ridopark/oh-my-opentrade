package screener

import (
	"math"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

func Pass0Filter(snapshots map[string]ports.Snapshot, cfg config.AIScreenerConfig) []string {
	var passed []string
	for sym, snap := range snapshots {
		price := pickPrice(snap)
		if price <= cfg.Pass0MinPrice {
			continue
		}

		// Pre-market volume: reject if nil (no pre-market activity) or below threshold.
		if snap.PreMarketVolume == nil || *snap.PreMarketVolume <= cfg.Pass0MinVolume {
			continue
		}

		// Average daily volume: reject if nil or below threshold.
		if cfg.Pass0MinADV > 0 {
			if snap.PrevDailyVolume == nil || *snap.PrevDailyVolume < cfg.Pass0MinADV {
				continue
			}
		}

		gapPct := computeGapPct(snap.PrevClose, price)
		if math.Abs(gapPct) < cfg.Pass0MinGapPct {
			continue
		}

		passed = append(passed, sym)
	}
	return passed
}

func pickPrice(snap ports.Snapshot) float64 {
	if snap.PreMarketPrice != nil && *snap.PreMarketPrice > 0 {
		return *snap.PreMarketPrice
	}
	if snap.LastTradePrice != nil && *snap.LastTradePrice > 0 {
		return *snap.LastTradePrice
	}
	return 0
}

func computeGapPct(prevClose *float64, price float64) float64 {
	if prevClose == nil || *prevClose == 0 {
		return 0
	}
	return (price - *prevClose) / *prevClose * 100
}
