package gate

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

type shortDirectionGate struct{}

func newShortDirectionGate(_ map[string]any, _ *ExecutionGateDeps) (ExecutionGate, error) {
	return &shortDirectionGate{}, nil
}

func (g *shortDirectionGate) Name() string { return "short_direction" }

func (g *shortDirectionGate) Check(_ context.Context, gctx *ExecutionGateContext) *GateResult {
	if gctx.Intent.Direction.IsExit() {
		return nil
	}
	if gctx.Intent.Direction == domain.DirectionShort && !gctx.Intent.AssetClass.SupportsShort() {
		return &GateResult{
			GateName: "short_direction",
			Reason:   "SHORT not supported for " + gctx.Intent.AssetClass.String(),
		}
	}
	return nil
}
