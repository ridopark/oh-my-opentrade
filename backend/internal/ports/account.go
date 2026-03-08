package ports

import "context"

// BuyingPower holds broker account buying power information.
type BuyingPower struct {
	DayTradingBuyingPower    float64
	EffectiveBuyingPower     float64
	NonMarginableBuyingPower float64 // crypto buying power (non-marginable cash)
	PatternDayTrader         bool
}

// AccountPort provides access to broker account information.
type AccountPort interface {
	GetAccountBuyingPower(ctx context.Context) (BuyingPower, error)
}
