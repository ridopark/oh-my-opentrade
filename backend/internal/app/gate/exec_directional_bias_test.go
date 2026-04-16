package gate

import (
	"context"
	"errors"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDirectionalBiasChecker struct{ err error }

func (m *mockDirectionalBiasChecker) Check(_ context.Context, _ domain.OrderIntent) error {
	return m.err
}

func TestDirectionalBiasGate(t *testing.T) {
	ctx := context.Background()

	t.Run("nil checker passes (disabled)", func(t *testing.T) {
		g := &directionalBiasGate{}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("exit intent bypasses", func(t *testing.T) {
		g := &directionalBiasGate{checker: &mockDirectionalBiasChecker{err: errors.New("should not be called")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionCloseLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("passing checker returns nil", func(t *testing.T) {
		g := &directionalBiasGate{checker: &mockDirectionalBiasChecker{}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("failing checker produces GateResult", func(t *testing.T) {
		g := &directionalBiasGate{checker: &mockDirectionalBiasChecker{err: errors.New("net long 80.00% projected exceeds 70.00%")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		result := g.Check(ctx, gctx)
		require.NotNil(t, result)
		assert.Equal(t, "directional_bias_guard", result.GateName)
		assert.Contains(t, result.Reason, "net long")
	})

	t.Run("factory wires checker from deps", func(t *testing.T) {
		deps := &ExecutionGateDeps{DirectionalBiasGuard: &mockDirectionalBiasChecker{}}
		g, err := newDirectionalBiasGate(nil, deps)
		require.NoError(t, err)
		assert.Equal(t, "directional_bias_guard", g.Name())
	})

	t.Run("registered under directional_bias_guard name", func(t *testing.T) {
		r := NewDefaultExecutionRegistry()
		deps := &ExecutionGateDeps{DirectionalBiasGuard: &mockDirectionalBiasChecker{}}
		_, ok := r.factories["directional_bias_guard"]
		require.True(t, ok, "directional_bias_guard must be registered in the default execution registry")
		g, err := r.factories["directional_bias_guard"](nil, deps)
		require.NoError(t, err)
		assert.Equal(t, "directional_bias_guard", g.Name())
	})
}
