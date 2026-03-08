package ports

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// RiskAssessorPort evaluates the health of an open position against current market conditions.
type RiskAssessorPort interface {
	AssessPosition(ctx context.Context, position domain.MonitoredPosition, indicators domain.IndicatorSnapshot, regime domain.MarketRegime) (*domain.RiskRevaluation, error)
}
