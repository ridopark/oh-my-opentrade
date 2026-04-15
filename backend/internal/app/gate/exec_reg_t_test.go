package gate

import (
	"context"
	"errors"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockRegTChecker struct{ err error }

func (m *mockRegTChecker) Check(_ context.Context, _ domain.OrderIntent) error {
	return m.err
}

func TestRegTGate(t *testing.T) {
	ctx := context.Background()

	t.Run("nil checker passes (disabled)", func(t *testing.T) {
		g := &regTGate{}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("exit intent bypasses", func(t *testing.T) {
		g := &regTGate{checker: &mockRegTChecker{err: errors.New("should not be called")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionCloseLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("passing checker returns nil", func(t *testing.T) {
		g := &regTGate{checker: &mockRegTChecker{}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("failing checker produces GateResult", func(t *testing.T) {
		g := &regTGate{checker: &mockRegTChecker{err: errors.New("required 5000 exceeds 4000")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		result := g.Check(ctx, gctx)
		require.NotNil(t, result)
		assert.Equal(t, "reg_t_guard", result.GateName)
	})

	t.Run("factory wires checker from deps", func(t *testing.T) {
		deps := &ExecutionGateDeps{RegTGuard: &mockRegTChecker{}}
		g, err := newRegTGate(nil, deps)
		require.NoError(t, err)
		assert.Equal(t, "reg_t_guard", g.Name())
	})
}
