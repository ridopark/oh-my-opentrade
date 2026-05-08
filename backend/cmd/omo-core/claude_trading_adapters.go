package main

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/adapters/ibkr"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/positionmonitor"
	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

// claudeBudgetAdapter satisfies omhttp.budgetReader by composing the
// existing risk breaker, broker, position monitor, and config-derived
// risk fraction. Wired only in registerRoutes.
type claudeBudgetAdapter struct {
	breaker     *risk.DailyLossBreaker
	broker      *ibkr.Adapter
	posMonitor  *positionmonitor.Service
	maxRiskFrac float64
}

func (a *claudeBudgetAdapter) KillSwitchState() string {
	return a.breaker.State().String()
}

func (a *claudeBudgetAdapter) AccountEquity(ctx context.Context) float64 {
	if a.broker == nil {
		return 0
	}
	eq, err := a.broker.GetAccountEquity(ctx)
	if err != nil {
		return 0
	}
	return eq
}

func (a *claudeBudgetAdapter) DailyLossUsedUSD(_ context.Context, tenantID string, envMode domain.EnvMode, equity float64) float64 {
	lossUSD, _, _, err := a.breaker.Inspect(tenantID, envMode, equity)
	if err != nil {
		return 0
	}
	return lossUSD
}

func (a *claudeBudgetAdapter) MaxLossUSD() float64       { return a.breaker.MaxLossUSD() }
func (a *claudeBudgetAdapter) MaxLossPct() float64       { return a.breaker.MaxLossPct() }
func (a *claudeBudgetAdapter) MaxRiskPctPerIntent() float64 { return a.maxRiskFrac }

// OpenPositionsCount returns live position count from the monitor cache.
// Cap is a v1 placeholder until a config knob lands.
func (a *claudeBudgetAdapter) OpenPositionsCount(_ context.Context) (int, int) {
	count := 0
	if a.posMonitor != nil {
		count = a.posMonitor.PositionCount()
	}
	return count, 10 // v1 placeholder cap pending wiring
}

// InflightIntents is a v1 placeholder; PositionGate inflight counter is
// not exposed publicly today.
func (a *claudeBudgetAdapter) InflightIntents() int { return 0 }

// Equity is the proposalSnapshotReader name for AccountEquity. The two
// interfaces overlap by every other field, so the same adapter satisfies
// both via this small alias method.
func (a *claudeBudgetAdapter) Equity(ctx context.Context) float64 {
	return a.AccountEquity(ctx)
}

// OpenPositions is the proposalSnapshotReader name for the position
// count. Drops the cap return.
func (a *claudeBudgetAdapter) OpenPositions(ctx context.Context) int {
	count, _ := a.OpenPositionsCount(ctx)
	return count
}

// claudeIntentValidator wraps execution.Service.ValidateIntent and
// flattens *GateError into the (gate, reason, blocked) shape expected by
// omhttp.intentValidator.
type claudeIntentValidator struct {
	exec *execution.Service
}

func (v *claudeIntentValidator) ValidateIntent(ctx context.Context, intent domain.OrderIntent) (string, string, bool) {
	if err := v.exec.ValidateIntent(ctx, intent); err != nil {
		return err.Gate, err.Reason, true
	}
	return "", "", false
}
