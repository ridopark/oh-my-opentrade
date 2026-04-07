package gate

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

type riskGate struct {
	equity  RiskValidator
	options OptionsRiskValidator
}

func newRiskGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
	return &riskGate{equity: deps.RiskEngine, options: deps.OptionsRiskEngine}, nil
}

func (g *riskGate) Name() string { return "risk_engine" }

func (g *riskGate) Check(_ context.Context, gctx *ExecutionGateContext) *GateResult {
	if gctx.Intent.Direction.IsExit() {
		return nil
	}

	isOption := gctx.Intent.Instrument != nil && gctx.Intent.Instrument.Type == domain.InstrumentTypeOption
	if isOption {
		if g.options == nil {
			return nil
		}
		if err := g.options.ValidateOptionIntent(gctx.Intent, gctx.AccountEquity); err != nil {
			return &GateResult{GateName: "risk_engine", Reason: err.Error()}
		}
		return nil
	}

	if g.equity == nil {
		return nil
	}
	if err := g.equity.Validate(gctx.Intent, gctx.AccountEquity); err != nil {
		return &GateResult{GateName: "risk_engine", Reason: err.Error()}
	}
	return nil
}
