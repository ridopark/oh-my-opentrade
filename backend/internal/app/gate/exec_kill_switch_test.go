package gate

import (
	"context"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubKillSwitchChecker struct{ state risk.KillSwitchState }

func (s *stubKillSwitchChecker) State() risk.KillSwitchState { return s.state }

func TestKillSwitchGate(t *testing.T) {
	ctx := context.Background()

	long := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
	short := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionShort}}
	exitLong := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionCloseLong}}
	exitShort := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionCloseShort}}

	t.Run("nil checker passes (disabled)", func(t *testing.T) {
		g := &killSwitchGate{}
		assert.Nil(t, g.Check(ctx, long))
	})

	t.Run("ACTIVE passes all intents", func(t *testing.T) {
		g := &killSwitchGate{checker: &stubKillSwitchChecker{state: risk.KillSwitchActive}}
		for _, gctx := range []*ExecutionGateContext{long, short, exitLong, exitShort} {
			assert.Nil(t, g.Check(ctx, gctx))
		}
	})

	t.Run("REDUCING blocks entries, allows exits", func(t *testing.T) {
		g := &killSwitchGate{checker: &stubKillSwitchChecker{state: risk.KillSwitchReducing}}

		res := g.Check(ctx, long)
		require.NotNil(t, res)
		assert.Equal(t, "kill_switch", res.GateName)
		assert.Contains(t, res.Reason, "REDUCING")
		assert.Contains(t, res.Reason, "new entries blocked")

		res = g.Check(ctx, short)
		require.NotNil(t, res)
		assert.Contains(t, res.Reason, "REDUCING")

		assert.Nil(t, g.Check(ctx, exitLong))
		assert.Nil(t, g.Check(ctx, exitShort))
	})

	t.Run("HALTED blocks everything including exits", func(t *testing.T) {
		g := &killSwitchGate{checker: &stubKillSwitchChecker{state: risk.KillSwitchHalted}}
		for _, gctx := range []*ExecutionGateContext{long, short, exitLong, exitShort} {
			res := g.Check(ctx, gctx)
			require.NotNil(t, res, "direction=%s should be blocked", gctx.Intent.Direction)
			assert.Equal(t, "kill_switch", res.GateName)
			assert.Contains(t, res.Reason, "HALTED")
		}
	})

	t.Run("factory wires checker from deps", func(t *testing.T) {
		deps := &ExecutionGateDeps{KillSwitchGuard: &stubKillSwitchChecker{state: risk.KillSwitchActive}}
		g, err := newKillSwitchGate(nil, deps)
		require.NoError(t, err)
		assert.Equal(t, "kill_switch", g.Name())
	})

	t.Run("registry has kill_switch", func(t *testing.T) {
		r := NewDefaultExecutionRegistry()
		_, ok := r.factories["kill_switch"]
		assert.True(t, ok, "default registry must register kill_switch")
	})
}
