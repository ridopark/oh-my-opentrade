package gate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockPDTChecker struct {
	err error
}

func (m *mockPDTChecker) CheckIntent(_ context.Context, _ domain.OrderIntent, _ time.Time) error {
	return m.err
}

func TestPDTGate(t *testing.T) {
	ctx := context.Background()

	t.Run("nil checker passes (disabled)", func(t *testing.T) {
		g := &pdtGate{nowFn: time.Now}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionCloseLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("passing checker returns nil", func(t *testing.T) {
		g := &pdtGate{checker: &mockPDTChecker{}, nowFn: time.Now}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionCloseLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("failing checker produces GateResult", func(t *testing.T) {
		g := &pdtGate{checker: &mockPDTChecker{err: errors.New("4th day trade blocked")}, nowFn: time.Now}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionCloseLong}}
		result := g.Check(ctx, gctx)
		require.NotNil(t, result)
		assert.Equal(t, "pdt_guard", result.GateName)
		assert.Contains(t, result.Reason, "4th day trade")
	})

	t.Run("factory wires checker from deps", func(t *testing.T) {
		deps := &ExecutionGateDeps{PDTGuard: &mockPDTChecker{}}
		g, err := newPDTGate(nil, deps)
		require.NoError(t, err)
		assert.Equal(t, "pdt_guard", g.Name())
	})
}
