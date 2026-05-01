package indicator

import "github.com/oh-my-opentrade/backend/internal/domain"

type Updater interface {
	Update(bar domain.MarketBar) domain.IndicatorSnapshot
}

type Reader interface {
	LastSnapshot(sym domain.Symbol, tf domain.Timeframe) (domain.IndicatorSnapshot, bool)
}

type Warmer interface {
	WarmUp(bars []domain.MarketBar)
}

type Calculator interface {
	Updater
	Reader
	Warmer
}
