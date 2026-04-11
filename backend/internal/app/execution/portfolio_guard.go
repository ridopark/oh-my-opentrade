package execution

import (
	"context"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// PositionCountFunc returns current open positions for a tenant/env pair.
// The caller decides the source (broker, position monitor, etc.).
type PositionCountFunc func(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error)

// PortfolioGuard limits the total number of simultaneous positions and the
// number of positions within each sector group (as classified by
// domain.ClassifySector). An optional per-strategy map caps each strategy's
// concurrent positions independently, so multi-strategy portfolios don't
// starve slower strategies when a faster one would otherwise squat on slots.
// Any of the three limits is optional — a zero value disables that check.
type PortfolioGuard struct {
	positionsFn    PositionCountFunc
	maxPositions   int
	maxPerGroup    int
	perStrategyMax map[string]int
	log            zerolog.Logger
}

// NewPortfolioGuard creates a PortfolioGuard.
// maxPositions=0 disables the total position cap.
// maxPerGroup=0 disables the per-sector-group cap.
// perStrategyMax=nil or an empty map disables per-strategy enforcement.
func NewPortfolioGuard(fn PositionCountFunc, maxPositions, maxPerGroup int, log zerolog.Logger) *PortfolioGuard {
	return &PortfolioGuard{
		positionsFn:  fn,
		maxPositions: maxPositions,
		maxPerGroup:  maxPerGroup,
		log:          log.With().Str("guard", "portfolio").Logger(),
	}
}

// SetPerStrategyMax installs a per-strategy position budget. Safe to call
// at wiring time before any concurrent calls to Check.
func (g *PortfolioGuard) SetPerStrategyMax(limits map[string]int) {
	g.perStrategyMax = limits
}

// Check rejects entry intents that would breach position limits.
// Exit intents always pass through.
func (g *PortfolioGuard) Check(ctx context.Context, intent domain.OrderIntent) error {
	if intent.Direction.IsExit() {
		return nil
	}

	// All limits disabled — nothing to check.
	if g.maxPositions <= 0 && g.maxPerGroup <= 0 && len(g.perStrategyMax) == 0 {
		return nil
	}

	positions, err := g.positionsFn(ctx, intent.TenantID, intent.EnvMode)
	if err != nil {
		g.log.Error().Err(err).Msg("portfolio guard: failed to fetch positions — allowing order through")
		return nil
	}

	// --- total position cap ---
	if g.maxPositions > 0 && len(positions) >= g.maxPositions {
		reason := fmt.Sprintf("portfolio_guard: %d open positions reached max %d",
			len(positions), g.maxPositions)
		g.log.Warn().
			Int("open", len(positions)).
			Int("max", g.maxPositions).
			Str("symbol", string(intent.Symbol)).
			Msg(reason)
		return fmt.Errorf("%s", reason)
	}

	// --- per-strategy cap ---
	// When a per-strategy budget is configured and the intent carries a
	// strategy tag, count only open positions attributed to that strategy.
	// Positions without a strategy tag (live broker that doesn't know)
	// are counted under "" — callers should not set an "" key.
	if len(g.perStrategyMax) > 0 && intent.Strategy != "" {
		if cap, ok := g.perStrategyMax[intent.Strategy]; ok && cap > 0 {
			stratCount := 0
			for _, p := range positions {
				if p.Strategy == intent.Strategy {
					stratCount++
				}
			}
			if stratCount >= cap {
				reason := fmt.Sprintf("portfolio_guard: strategy %s has %d open positions, cap %d",
					intent.Strategy, stratCount, cap)
				g.log.Warn().
					Str("strategy", intent.Strategy).
					Int("open", stratCount).
					Int("cap", cap).
					Str("symbol", string(intent.Symbol)).
					Msg(reason)
				return fmt.Errorf("%s", reason)
			}
		}
	}

	// --- per sector-group cap ---
	if g.maxPerGroup > 0 {
		intentGroup := domain.ClassifySector(intent.Symbol)
		groupCount := 0
		for _, p := range positions {
			if domain.ClassifySector(p.Symbol) == intentGroup {
				groupCount++
			}
		}
		if groupCount >= g.maxPerGroup {
			reason := fmt.Sprintf("portfolio_guard: %d positions in group %s reached max %d",
				groupCount, intentGroup, g.maxPerGroup)
			g.log.Warn().
				Int("groupCount", groupCount).
				Int("maxPerGroup", g.maxPerGroup).
				Str("group", string(intentGroup)).
				Str("symbol", string(intent.Symbol)).
				Msg(reason)
			return fmt.Errorf("%s", reason)
		}

		g.log.Debug().
			Str("group", string(intentGroup)).
			Int("groupCount", groupCount).
			Int("maxPerGroup", g.maxPerGroup).
			Str("symbol", string(intent.Symbol)).
			Msg("portfolio guard passed")
	}

	return nil
}
