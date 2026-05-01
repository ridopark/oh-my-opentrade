package monitor

import "github.com/oh-my-opentrade/backend/internal/domain"

func (ic *IndicatorCalculator) SnapshotForTest(sym domain.Symbol, tf domain.Timeframe) (domain.IndicatorSnapshot, bool) {
	state, ok := ic.states[stateKey{Symbol: sym, Timeframe: tf}]
	if !ok {
		return domain.IndicatorSnapshot{}, false
	}
	return state.lastSnap, true
}
