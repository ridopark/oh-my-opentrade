package gate

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// PDTChecker encapsulates pattern-day-trader enforcement. The concrete
// implementation lives in app/risk (PDTGuard wrapping a PDTTracker).
type PDTChecker interface {
	CheckIntent(ctx context.Context, intent domain.OrderIntent, now time.Time) error
}

// pdtGate wires PDTChecker into the execution gate chain.
type pdtGate struct {
	checker PDTChecker
	nowFn   func() time.Time
}

func newPDTGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
	return &pdtGate{checker: deps.PDTGuard, nowFn: time.Now}, nil
}

func (g *pdtGate) Name() string { return "pdt_guard" }

func (g *pdtGate) Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
	if g.checker == nil {
		return nil
	}
	if err := g.checker.CheckIntent(ctx, gctx.Intent, g.nowFn()); err != nil {
		return &GateResult{GateName: "pdt_guard", Reason: err.Error()}
	}
	return nil
}
