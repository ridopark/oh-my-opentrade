package risk

import (
	"context"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

type stubPositions struct{ positions []domain.MonitoredPosition }

func (s *stubPositions) ListPositions() []domain.MonitoredPosition { return s.positions }

type stubEquity struct{ equity float64 }

func (s *stubEquity) AccountEquity() float64 { return s.equity }

// makePosWithStop creates a MonitoredPosition whose implied stop-loss
// (via a TRAILING_STOP rule carrying stop_price) yields |entry - stop|
// per-share risk for the heat calculation.
func makePosWithStop(entry, stop, qty float64) domain.MonitoredPosition {
	rules := []domain.ExitRule{{
		Type:   domain.ExitRuleTrailingStop,
		Params: map[string]float64{"stop_price": stop},
	}}
	return domain.MonitoredPosition{
		EntryPrice:       entry,
		Quantity:         qty,
		InitialExitRules: rules,
	}
}

func makePosNoStop(entry, qty float64) domain.MonitoredPosition {
	return domain.MonitoredPosition{EntryPrice: entry, Quantity: qty}
}

// ---------------------------------------------------------------------------
// PortfolioHeat.Check
// ---------------------------------------------------------------------------

func TestPortfolioHeat_Check(t *testing.T) {
	ctx := context.Background()

	t.Run("disabled when maxHeatPct <= 0", func(t *testing.T) {
		p := NewPortfolioHeat(0, &stubPositions{}, &stubEquity{equity: 100000}, zerolog.Nop())
		err := p.Check(ctx, domain.OrderIntent{LimitPrice: 100, StopLoss: 90, Quantity: 100})
		assert.NoError(t, err)
	})

	t.Run("empty portfolio + new intent allowed", func(t *testing.T) {
		p := NewPortfolioHeat(0.10, &stubPositions{}, &stubEquity{equity: 100000}, zerolog.Nop())
		// 10 * 100 = 1000 new risk; 1000/100000 = 1% well below 10%.
		err := p.Check(ctx, domain.OrderIntent{LimitPrice: 100, StopLoss: 90, Quantity: 100})
		assert.NoError(t, err)
	})

	t.Run("existing positions under limit allowed", func(t *testing.T) {
		positions := []domain.MonitoredPosition{
			makePosWithStop(100, 95, 100), // 5 * 100 = 500 risk
			makePosWithStop(50, 48, 200),  // 2 * 200 = 400 risk
		}
		p := NewPortfolioHeat(0.10, &stubPositions{positions: positions}, &stubEquity{equity: 100000}, zerolog.Nop())
		// current 900; new 500; total 1400 / 100000 = 1.4% < 10%
		err := p.Check(ctx, domain.OrderIntent{LimitPrice: 200, StopLoss: 195, Quantity: 100})
		assert.NoError(t, err)
	})

	t.Run("over-limit rejected with details", func(t *testing.T) {
		positions := []domain.MonitoredPosition{
			makePosWithStop(100, 90, 500), // 10 * 500 = 5000 risk (5%)
			makePosWithStop(200, 190, 300), // 10 * 300 = 3000 risk (3%)
		}
		p := NewPortfolioHeat(0.10, &stubPositions{positions: positions}, &stubEquity{equity: 100000}, zerolog.Nop())
		// current 8000 (8%); new intent 3000 (3%) -> 11% > 10%
		err := p.Check(ctx, domain.OrderIntent{LimitPrice: 50, StopLoss: 40, Quantity: 300})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "portfolio_heat")
		assert.Contains(t, err.Error(), "%")
	})

	t.Run("intent with no stop contributes 0", func(t *testing.T) {
		p := NewPortfolioHeat(0.10, &stubPositions{}, &stubEquity{equity: 100000}, zerolog.Nop())
		// StopLoss=0 — risk_sizer normally sets this, but be defensive.
		err := p.Check(ctx, domain.OrderIntent{LimitPrice: 100, StopLoss: 0, Quantity: 100})
		assert.NoError(t, err)
	})

	t.Run("existing position with no stop contributes 0", func(t *testing.T) {
		positions := []domain.MonitoredPosition{makePosNoStop(100, 1000)}
		p := NewPortfolioHeat(0.10, &stubPositions{positions: positions}, &stubEquity{equity: 100000}, zerolog.Nop())
		err := p.Check(ctx, domain.OrderIntent{LimitPrice: 100, StopLoss: 90, Quantity: 100})
		assert.NoError(t, err)
	})

	t.Run("zero equity errors", func(t *testing.T) {
		p := NewPortfolioHeat(0.10, &stubPositions{}, &stubEquity{equity: 0}, zerolog.Nop())
		err := p.Check(ctx, domain.OrderIntent{LimitPrice: 100, StopLoss: 90, Quantity: 100})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid equity")
	})

	t.Run("negative equity errors", func(t *testing.T) {
		p := NewPortfolioHeat(0.10, &stubPositions{}, &stubEquity{equity: -1}, zerolog.Nop())
		err := p.Check(ctx, domain.OrderIntent{LimitPrice: 100, StopLoss: 90, Quantity: 100})
		require.Error(t, err)
	})

	t.Run("exactly at limit allowed", func(t *testing.T) {
		positions := []domain.MonitoredPosition{makePosWithStop(100, 90, 900)} // 9000 / 100000 = 9%
		p := NewPortfolioHeat(0.10, &stubPositions{positions: positions}, &stubEquity{equity: 100000}, zerolog.Nop())
		// new intent exactly brings to 10% (1000 more)
		err := p.Check(ctx, domain.OrderIntent{LimitPrice: 100, StopLoss: 90, Quantity: 100})
		assert.NoError(t, err)
	})
}
