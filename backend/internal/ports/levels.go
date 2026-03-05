package ports

import (
	"context"
	"time"
)

// PriceLevels contains significant price levels for a symbol.
type PriceLevels struct {
	Symbol        string
	Date          time.Time
	PriorDayHigh  float64
	PriorDayLow   float64
	PriorDayClose float64
	OvernightHigh float64
	OvernightLow  float64
}

// LevelsProvider retrieves historical price levels for strategy anchoring.
type LevelsProvider interface {
	// GetLevels returns significant price levels for a symbol on the given date.
	// Returns a zero PriceLevels and nil error if no data is available.
	GetLevels(ctx context.Context, symbol string, date time.Time) (PriceLevels, error)
}
