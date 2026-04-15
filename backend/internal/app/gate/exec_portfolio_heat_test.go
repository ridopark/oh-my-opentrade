package gate

import (
	"context"
	"errors"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockPortfolioHeatChecker struct{ err error }

func (m *mockPortfolioHeatChecker) Check(_ context.Context, _ domain.OrderIntent) error {
	return m.err
}

func TestPortfolioHeatGate(t *testing.T) {
	ctx := context.Background()

	t.Run("nil checker passes (disabled)", func(t *testing.T) {
		g := &portfolioHeatGate{}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("exit intent bypasses", func(t *testing.T) {
		// Even a failing checker is bypassed for exits — exits reduce heat,
		// not add to it, and must never be blocked by a risk-management gate.
		g := &portfolioHeatGate{checker: &mockPortfolioHeatChecker{err: errors.New("should not be called")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionCloseLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("passing checker returns nil", func(t *testing.T) {
		g := &portfolioHeatGate{checker: &mockPortfolioHeatChecker{}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("failing checker produces GateResult", func(t *testing.T) {
		g := &portfolioHeatGate{checker: &mockPortfolioHeatChecker{err: errors.New("heat 12% exceeds 10%")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		result := g.Check(ctx, gctx)
		require.NotNil(t, result)
		assert.Equal(t, "portfolio_heat_guard", result.GateName)
		assert.Contains(t, result.Reason, "heat 12%")
	})

	t.Run("factory wires checker from deps", func(t *testing.T) {
		deps := &ExecutionGateDeps{PortfolioHeatGuard: &mockPortfolioHeatChecker{}}
		g, err := newPortfolioHeatGate(nil, deps)
		require.NoError(t, err)
		assert.Equal(t, "portfolio_heat_guard", g.Name())
	})
}
