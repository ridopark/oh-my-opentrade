package risk

import (
	"context"
	"fmt"
	"math"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// SectorExposure caps notional concentration within a single GICS sector and
// a single industry as a fraction of account equity.
//
// Rationale: portfolio_heat catches aggregate *risk* but not *concentration*.
// Six semiconductor positions during an AI rally may individually pass every
// per-trade and heat check yet collectively represent 80% sector exposure.
// When the sector rolls over those six positions move together — the
// concentrated drawdown is far worse than the sum of independent drawdowns.
//
// The gate rejects a new intent whose projected sector-notional or
// industry-notional share of equity would exceed the configured caps.
//
// Design choices:
//   - Symbols not in the metadata table fail open (log warn, allow) so a
//     stale metadata file doesn't halt trading. Operators can tighten to
//     fail-closed later by editing this checker.
//   - Option positions are intentionally skipped in the notional sum. Sprint 4
//     defers proper options delta-notional accounting to Sprint 5.
//   - Exits bypass the gate at the gate layer (see exec_sector_exposure.go);
//     this type has no exit-aware branch.
type SectorExposure struct {
	maxSectorPct   float64
	maxIndustryPct float64
	metadata       config.SymbolMetadata
	posSource      PositionSource
	equitySource   EquitySource
	log            zerolog.Logger
}

// NewSectorExposure constructs a SectorExposure guard. If both caps are <= 0,
// the returned guard treats all Check calls as non-gating.
func NewSectorExposure(
	maxSectorPct, maxIndustryPct float64,
	metadata config.SymbolMetadata,
	posSource PositionSource,
	equitySource EquitySource,
	log zerolog.Logger,
) *SectorExposure {
	return &SectorExposure{
		maxSectorPct:   maxSectorPct,
		maxIndustryPct: maxIndustryPct,
		metadata:       metadata,
		posSource:      posSource,
		equitySource:   equitySource,
		log:            log.With().Str("component", "sector_exposure").Logger(),
	}
}

// Check implements gate.SectorExposureChecker. Returns a descriptive error
// when the projected sector or industry notional share exceeds the configured
// caps.
func (s *SectorExposure) Check(_ context.Context, intent domain.OrderIntent) error {
	if s.maxSectorPct <= 0 && s.maxIndustryPct <= 0 {
		return nil
	}
	meta, ok := s.metadata[string(intent.Symbol)]
	if !ok {
		s.log.Warn().
			Str("symbol", string(intent.Symbol)).
			Msg("symbol not in metadata table, allowing (fail-open)")
		return nil
	}
	if s.equitySource == nil {
		return fmt.Errorf("sector_exposure: nil equity source")
	}
	equity := s.equitySource.AccountEquity()
	if equity <= 0 {
		return fmt.Errorf("sector_exposure: invalid equity %.2f", equity)
	}

	newNotional := intentNotional(intent)
	sectorNotional := newNotional
	industryNotional := newNotional

	if s.posSource != nil {
		for _, pos := range s.posSource.ListPositions() {
			// Sprint 4 defers options delta-notional accounting to Sprint 5:
			// skip option positions rather than count their premium notional
			// as if it were underlying exposure.
			if pos.InstrumentType == domain.InstrumentTypeOption {
				continue
			}
			posMeta, ok := s.metadata[string(pos.Symbol)]
			if !ok {
				continue
			}
			posN := positionNotional(pos)
			if posMeta.Sector == meta.Sector {
				sectorNotional += posN
			}
			if posMeta.Industry == meta.Industry {
				industryNotional += posN
			}
		}
	}

	if s.maxSectorPct > 0 {
		if pct := sectorNotional / equity; pct > s.maxSectorPct {
			return fmt.Errorf(
				"sector_exposure: sector %q projected %.2f%% exceeds %.2f%% max",
				meta.Sector, pct*100, s.maxSectorPct*100,
			)
		}
	}
	if s.maxIndustryPct > 0 {
		if pct := industryNotional / equity; pct > s.maxIndustryPct {
			return fmt.Errorf(
				"sector_exposure: industry %q projected %.2f%% exceeds %.2f%% max",
				meta.Industry, pct*100, s.maxIndustryPct*100,
			)
		}
	}
	return nil
}

// intentNotional returns the absolute dollar notional of the proposed intent.
// Option intents are excluded (handled separately in Sprint 5).
func intentNotional(intent domain.OrderIntent) float64 {
	if intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeOption {
		return 0
	}
	return math.Abs(intent.LimitPrice * intent.Quantity)
}

// positionNotional returns |EntryPrice * Quantity| for an open position.
// MonitoredPosition does not carry a CurrentPrice field — the monitor tracks
// high/low watermarks, not the live last — so entry price is the honest
// baseline. Using entry means the gate compares apples to apples with the
// intent's LimitPrice and does not depend on ticker freshness.
func positionNotional(pos domain.MonitoredPosition) float64 {
	return math.Abs(pos.EntryPrice * pos.Quantity)
}
