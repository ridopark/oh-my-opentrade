package gate

import (
	"context"
	"errors"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockSectorExposureChecker struct{ err error }

func (m *mockSectorExposureChecker) Check(_ context.Context, _ domain.OrderIntent) error {
	return m.err
}

func TestSectorExposureGate(t *testing.T) {
	ctx := context.Background()

	t.Run("nil checker passes (disabled)", func(t *testing.T) {
		g := &sectorExposureGate{}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("exit intent bypasses", func(t *testing.T) {
		g := &sectorExposureGate{checker: &mockSectorExposureChecker{err: errors.New("should not be called")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionCloseLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("passing checker returns nil", func(t *testing.T) {
		g := &sectorExposureGate{checker: &mockSectorExposureChecker{}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("failing checker produces GateResult", func(t *testing.T) {
		g := &sectorExposureGate{checker: &mockSectorExposureChecker{err: errors.New("sector \"Technology\" projected 35.00% exceeds 30.00%")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		result := g.Check(ctx, gctx)
		require.NotNil(t, result)
		assert.Equal(t, "sector_exposure_guard", result.GateName)
		assert.Contains(t, result.Reason, "Technology")
	})

	t.Run("factory wires checker from deps", func(t *testing.T) {
		deps := &ExecutionGateDeps{SectorExposureGuard: &mockSectorExposureChecker{}}
		g, err := newSectorExposureGate(nil, deps)
		require.NoError(t, err)
		assert.Equal(t, "sector_exposure_guard", g.Name())
	})

	t.Run("registered under sector_exposure_guard name", func(t *testing.T) {
		r := NewDefaultExecutionRegistry()
		deps := &ExecutionGateDeps{SectorExposureGuard: &mockSectorExposureChecker{}}
		_, ok := r.factories["sector_exposure_guard"]
		require.True(t, ok, "sector_exposure_guard must be registered in the default execution registry")
		g, err := r.factories["sector_exposure_guard"](nil, deps)
		require.NoError(t, err)
		assert.Equal(t, "sector_exposure_guard", g.Name())
	})
}
