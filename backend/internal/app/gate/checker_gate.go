package gate

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// intentChecker is the common shape shared by PortfolioHeatChecker,
// SectorExposureChecker, and DirectionalBiasChecker.
type intentChecker interface {
	Check(ctx context.Context, intent domain.OrderIntent) error
}

// checkerGate is the shared skeleton for entry-only execution gates that
// delegate to a single Check(ctx, intent) method. It handles the two
// common fast-paths: exits always pass, and a nil checker disables the gate.
func checkerGate(ctx context.Context, name string, checker intentChecker, intent domain.OrderIntent) *GateResult {
	if intent.Direction.IsExit() {
		return nil
	}
	if checker == nil {
		return nil
	}
	if err := checker.Check(ctx, intent); err != nil {
		return &GateResult{GateName: name, Reason: err.Error()}
	}
	return nil
}
