package indicator

import (
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// SnapshotFn returns a closure that drives Service.Update for each bar and
// projects the resulting domain.IndicatorSnapshot onto strategy.IndicatorData.
// Used by the strategy runner's WarmUp / WarmUpTF entry points so warmup
// reuses the same per-context indicator state as live and backtest paths.
func SnapshotFn(svc *Service) func(bar domain.MarketBar) start.IndicatorData {
	return func(bar domain.MarketBar) start.IndicatorData {
		snap := svc.Update(bar)
		return start.IndicatorData{
			RSI:           snap.RSI,
			StochK:        snap.StochK,
			StochD:        snap.StochD,
			EMA9:          snap.EMA9,
			EMA21:         snap.EMA21,
			EMA50:         snap.EMA50,
			EMAFast:       snap.EMAFast,
			EMASlow:       snap.EMASlow,
			EMAFastPeriod: snap.EMAFastPeriod,
			EMASlowPeriod: snap.EMASlowPeriod,
			VWAP:          snap.VWAP,
			Volume:        snap.Volume,
			VolumeSMA:     snap.VolumeSMA,
			ATR:           snap.ATR,
			VWAPSD:        snap.VWAPSD,
			EMA200:        snap.EMA200,
			BBUpper:       snap.BBUpper,
			BBMiddle:      snap.BBMiddle,
			BBLower:       snap.BBLower,
			BBPercentB:    snap.BBPercentB,
			BBBandwidth:   snap.BBBandwidth,
			MACDLine:      snap.MACDLine,
			MACDSignal:    snap.MACDSignal,
			MACDHistogram: snap.MACDHistogram,
			ADX:           snap.ADX,
			RegimeScore:   snap.RegimeScore,
		}
	}
}
