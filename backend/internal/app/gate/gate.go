// Package gate implements the gate pattern for filtering trade setups.
// Gates are composable checks that run sequentially; the first rejection
// short-circuits the chain and blocks the setup.
package gate

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// SetupInput contains the setup fields gates need to inspect.
// Populated from monitor.SetupCondition before calling the chain,
// avoiding a circular import on the monitor package.
type SetupInput struct {
	Direction  domain.Direction
	Confidence float64
	RVOL       float64
	Trigger    string
	ORBHigh    float64
	ORBLow     float64
	Symbol     domain.Symbol
}

// MonitorGateContext bundles the state a monitor-level gate needs.
type MonitorGateContext struct {
	Setup         SetupInput
	Bar           domain.MarketBar
	Snapshot      domain.IndicatorSnapshot
	Regime        domain.MarketRegime
	VIXLevel      float64
	AnchorRegimes map[string]domain.MarketRegime // key: "AAPL:5m"
	ORBTimeframe  domain.Timeframe
	StrategyKey   string
}

// MonitorGate checks whether a detected setup should proceed.
// Check returns nil when the gate passes and a non-nil *GateResult
// when the setup is blocked.
type MonitorGate interface {
	Name() string
	Check(ctx context.Context, gctx *MonitorGateContext) *GateResult
}

// GateResult is returned by Check when a gate blocks a setup.
// It implements the error interface so callers can treat it as an error
// when convenient, but the structured fields are available for metrics
// and logging.
type GateResult struct {
	GateName string
	Reason   string
}

// Error implements the error interface.
func (r *GateResult) Error() string { return r.GateName + ": " + r.Reason }

// ExecutionGateContext bundles state an execution-level gate needs.
type ExecutionGateContext struct {
	Intent        domain.OrderIntent
	AccountEquity float64
	TenantID      string
	EnvMode       domain.EnvMode
}

// ExecutionGate checks whether an order intent should proceed to broker submission.
// Check returns nil when the gate passes and a non-nil *GateResult when the intent
// is blocked.
type ExecutionGate interface {
	Name() string
	Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult
}
