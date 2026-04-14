package gate

import (
	"context"
	"errors"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

type mockExposureChecker struct{ err error }

func (m *mockExposureChecker) Check(_ context.Context, _ domain.OrderIntent) error { return m.err }

type mockPortfolioChecker struct{ err error }

func (m *mockPortfolioChecker) Check(_ context.Context, _ domain.OrderIntent) error { return m.err }

type mockRiskValidator struct{ err error }

func (m *mockRiskValidator) Validate(_ domain.OrderIntent, _ float64) error { return m.err }

type mockOptionsRiskValidator struct{ err error }

func (m *mockOptionsRiskValidator) ValidateOptionIntent(_ domain.OrderIntent, _ float64) error {
	return m.err
}

type mockSlippageChecker struct{ err error }

func (m *mockSlippageChecker) Check(_ context.Context, _ domain.OrderIntent) error { return m.err }

type mockTradingWindowChecker struct{ err error }

func (m *mockTradingWindowChecker) Check(_ domain.OrderIntent) error { return m.err }

type mockSpreadChecker struct{ err error }

func (m *mockSpreadChecker) Check(_ context.Context, _ domain.OrderIntent) error { return m.err }

type mockBuyingPowerChecker struct{ err error }

func (m *mockBuyingPowerChecker) Check(_ context.Context, _ domain.OrderIntent) error { return m.err }

// ---------------------------------------------------------------------------
// Short direction gate
// ---------------------------------------------------------------------------

func TestShortDirectionGate(t *testing.T) {
	g := &shortDirectionGate{}
	ctx := context.Background()

	t.Run("long passes", func(t *testing.T) {
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong, AssetClass: domain.AssetClassCrypto}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("short equity passes", func(t *testing.T) {
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionShort, AssetClass: domain.AssetClassEquity}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("short crypto blocks", func(t *testing.T) {
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionShort, AssetClass: domain.AssetClassCrypto}}
		result := g.Check(ctx, gctx)
		require.NotNil(t, result)
		assert.Equal(t, "short_direction", result.GateName)
		assert.Contains(t, result.Reason, "CRYPTO")
	})

	t.Run("exit skipped", func(t *testing.T) {
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionCloseLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})
}

// ---------------------------------------------------------------------------
// Exposure gate
// ---------------------------------------------------------------------------

func TestExposureGate(t *testing.T) {
	ctx := context.Background()

	t.Run("nil checker passes", func(t *testing.T) {
		g := &exposureGate{}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("checker passes", func(t *testing.T) {
		g := &exposureGate{checker: &mockExposureChecker{}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("checker blocks", func(t *testing.T) {
		g := &exposureGate{checker: &mockExposureChecker{err: errors.New("too much exposure")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		result := g.Check(ctx, gctx)
		require.NotNil(t, result)
		assert.Equal(t, "exposure_guard", result.GateName)
	})

	t.Run("exit skipped", func(t *testing.T) {
		g := &exposureGate{checker: &mockExposureChecker{err: errors.New("would block")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionCloseLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})
}

// ---------------------------------------------------------------------------
// Portfolio gate
// ---------------------------------------------------------------------------

func TestPortfolioGate(t *testing.T) {
	ctx := context.Background()

	t.Run("nil checker passes", func(t *testing.T) {
		g := &portfolioGate{}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("checker blocks", func(t *testing.T) {
		g := &portfolioGate{checker: &mockPortfolioChecker{err: errors.New("max positions")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		result := g.Check(ctx, gctx)
		require.NotNil(t, result)
		assert.Equal(t, "portfolio_guard", result.GateName)
	})
}

// ---------------------------------------------------------------------------
// Risk engine gate
// ---------------------------------------------------------------------------

func TestRiskGate(t *testing.T) {
	ctx := context.Background()

	t.Run("nil validators pass", func(t *testing.T) {
		g := &riskGate{}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}, AccountEquity: 100000}
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("equity risk blocks", func(t *testing.T) {
		g := &riskGate{equity: &mockRiskValidator{err: errors.New("risk limit")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}, AccountEquity: 100000}
		result := g.Check(ctx, gctx)
		require.NotNil(t, result)
		assert.Equal(t, "risk_engine", result.GateName)
	})

	t.Run("options risk blocks", func(t *testing.T) {
		inst := &domain.Instrument{Type: domain.InstrumentTypeOption}
		g := &riskGate{options: &mockOptionsRiskValidator{err: errors.New("option risk")}}
		gctx := &ExecutionGateContext{
			Intent:        domain.OrderIntent{Direction: domain.DirectionLong, Instrument: inst},
			AccountEquity: 100000,
		}
		result := g.Check(ctx, gctx)
		require.NotNil(t, result)
		assert.Equal(t, "risk_engine", result.GateName)
	})

	t.Run("options with nil validator passes", func(t *testing.T) {
		inst := &domain.Instrument{Type: domain.InstrumentTypeOption}
		g := &riskGate{equity: &mockRiskValidator{err: errors.New("should not reach")}}
		gctx := &ExecutionGateContext{
			Intent:        domain.OrderIntent{Direction: domain.DirectionLong, Instrument: inst},
			AccountEquity: 100000,
		}
		// No options validator => pass.
		assert.Nil(t, g.Check(ctx, gctx))
	})

	t.Run("exit skipped", func(t *testing.T) {
		g := &riskGate{equity: &mockRiskValidator{err: errors.New("would block")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionCloseShort}}
		assert.Nil(t, g.Check(ctx, gctx))
	})
}

// ---------------------------------------------------------------------------
// Slippage gate
// ---------------------------------------------------------------------------

func TestSlippageGate(t *testing.T) {
	ctx := context.Background()

	t.Run("checker blocks", func(t *testing.T) {
		g := &slippageGate{checker: &mockSlippageChecker{err: errors.New("slippage too high")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		result := g.Check(ctx, gctx)
		require.NotNil(t, result)
		assert.Equal(t, "slippage_guard", result.GateName)
	})

	t.Run("nil passes", func(t *testing.T) {
		g := &slippageGate{}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})
}

// ---------------------------------------------------------------------------
// Trading window gate
// ---------------------------------------------------------------------------

func TestTradingWindowGate(t *testing.T) {
	ctx := context.Background()

	t.Run("checker blocks", func(t *testing.T) {
		g := &tradingWindowGate{checker: &mockTradingWindowChecker{err: errors.New("outside hours")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		result := g.Check(ctx, gctx)
		require.NotNil(t, result)
		assert.Equal(t, "trading_window", result.GateName)
	})
}

// ---------------------------------------------------------------------------
// Spread gate
// ---------------------------------------------------------------------------

func TestSpreadGate(t *testing.T) {
	ctx := context.Background()

	t.Run("checker blocks", func(t *testing.T) {
		g := &spreadGate{checker: &mockSpreadChecker{err: errors.New("spread too wide")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		result := g.Check(ctx, gctx)
		require.NotNil(t, result)
		assert.Equal(t, "spread_guard", result.GateName)
	})
}

// ---------------------------------------------------------------------------
// Buying power gate
// ---------------------------------------------------------------------------

func TestBuyingPowerGate(t *testing.T) {
	ctx := context.Background()

	t.Run("checker blocks", func(t *testing.T) {
		g := &buyingPowerGate{checker: &mockBuyingPowerChecker{err: errors.New("insufficient BP")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionLong}}
		result := g.Check(ctx, gctx)
		require.NotNil(t, result)
		assert.Equal(t, "buying_power_guard", result.GateName)
	})

	t.Run("exit skipped", func(t *testing.T) {
		g := &buyingPowerGate{checker: &mockBuyingPowerChecker{err: errors.New("would block")}}
		gctx := &ExecutionGateContext{Intent: domain.OrderIntent{Direction: domain.DirectionCloseLong}}
		assert.Nil(t, g.Check(ctx, gctx))
	})
}

// ---------------------------------------------------------------------------
// Execution gate chain
// ---------------------------------------------------------------------------

type execPassGate struct{ name string }

func (g *execPassGate) Name() string                                                  { return g.name }
func (g *execPassGate) Check(_ context.Context, _ *ExecutionGateContext) *GateResult { return nil }

type execBlockGate struct {
	name   string
	reason string
}

func (g *execBlockGate) Name() string { return g.name }
func (g *execBlockGate) Check(_ context.Context, _ *ExecutionGateContext) *GateResult {
	return &GateResult{GateName: g.name, Reason: g.reason}
}

func TestExecutionChain_AllPass(t *testing.T) {
	chain := NewExecutionGateChain(
		[]ExecutionGate{&execPassGate{"a"}, &execPassGate{"b"}},
		zerolog.Nop(),
	)
	result := chain.Run(context.Background(), &ExecutionGateContext{})
	assert.Nil(t, result)
}

func TestExecutionChain_ShortCircuits(t *testing.T) {
	chain := NewExecutionGateChain(
		[]ExecutionGate{
			&execPassGate{"a"},
			&execBlockGate{"b", "blocked"},
			&execPassGate{"c"},
		},
		zerolog.Nop(),
	)
	result := chain.Run(context.Background(), &ExecutionGateContext{})
	require.NotNil(t, result)
	assert.Equal(t, "b", result.GateName)
	assert.Equal(t, "blocked", result.Reason)
}

func TestExecutionChain_Names(t *testing.T) {
	chain := NewExecutionGateChain(
		[]ExecutionGate{&execPassGate{"x"}, &execPassGate{"y"}},
		zerolog.Nop(),
	)
	assert.Equal(t, []string{"x", "y"}, chain.Names())
}

// ---------------------------------------------------------------------------
// Default execution configs + registry
// ---------------------------------------------------------------------------

func TestDefaultExecutionGateConfigs(t *testing.T) {
	configs := DefaultExecutionGateConfigs()
	assert.Len(t, configs, 8)
	assert.Equal(t, "short_direction", configs[0].Name)
	assert.Equal(t, "buying_power_guard", configs[7].Name)
}

func TestExecutionRegistry_BuildChain(t *testing.T) {
	r := NewDefaultExecutionRegistry()
	deps := &ExecutionGateDeps{}
	chain, err := r.BuildChain(DefaultExecutionGateConfigs(), deps, zerolog.Nop())
	require.NoError(t, err)
	require.NotNil(t, chain)
	assert.Len(t, chain.Names(), 8)
}

func TestExecutionRegistry_UnknownGate(t *testing.T) {
	r := NewDefaultExecutionRegistry()
	_, err := r.BuildChain([]GateConfig{{Name: "nonexistent"}}, &ExecutionGateDeps{}, zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown execution gate")
}
