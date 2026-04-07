package gate

import (
	"context"

	"github.com/rs/zerolog"
)

// MonitorGateChain runs gates sequentially, short-circuiting on the first
// rejection. A nil return from Run means all gates passed.
type MonitorGateChain struct {
	gates []MonitorGate
	log   zerolog.Logger
}

// NewMonitorGateChain creates a chain that evaluates the given gates in order.
func NewMonitorGateChain(gates []MonitorGate, log zerolog.Logger) *MonitorGateChain {
	return &MonitorGateChain{
		gates: gates,
		log:   log,
	}
}

// Run evaluates every gate in order and returns the first rejection, or nil
// if all gates pass.
func (c *MonitorGateChain) Run(ctx context.Context, gctx *MonitorGateContext) *GateResult {
	for _, g := range c.gates {
		if result := g.Check(ctx, gctx); result != nil {
			c.log.Warn().
				Str("gate", result.GateName).
				Str("reason", result.Reason).
				Msg("setup blocked")
			return result
		}
	}
	return nil
}

// Names returns the ordered list of gate names for logging and diagnostics.
func (c *MonitorGateChain) Names() []string {
	names := make([]string, len(c.gates))
	for i, g := range c.gates {
		names[i] = g.Name()
	}
	return names
}

// ExecutionGateChain runs execution gates sequentially, short-circuiting on the
// first rejection. A nil return from Run means all gates passed.
type ExecutionGateChain struct {
	gates []ExecutionGate
	log   zerolog.Logger
}

// NewExecutionGateChain creates a chain that evaluates the given gates in order.
func NewExecutionGateChain(gates []ExecutionGate, log zerolog.Logger) *ExecutionGateChain {
	return &ExecutionGateChain{
		gates: gates,
		log:   log,
	}
}

// Run evaluates every gate in order and returns the first rejection, or nil
// if all gates pass.
func (c *ExecutionGateChain) Run(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
	for _, g := range c.gates {
		if result := g.Check(ctx, gctx); result != nil {
			c.log.Warn().
				Str("gate", result.GateName).
				Str("reason", result.Reason).
				Msg("order intent blocked")
			return result
		}
	}
	return nil
}

// Names returns the ordered list of gate names for logging and diagnostics.
func (c *ExecutionGateChain) Names() []string {
	names := make([]string, len(c.gates))
	for i, g := range c.gates {
		names[i] = g.Name()
	}
	return names
}
