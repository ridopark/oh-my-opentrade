package risk

import (
	"context"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testMetadata covers the symbols used in the table-driven cases below.
func testMetadata() config.SymbolMetadata {
	return config.SymbolMetadata{
		"AAPL": {Sector: "Technology", Industry: "Consumer Electronics"},
		"MSFT": {Sector: "Technology", Industry: "Software"},
		"NVDA": {Sector: "Technology", Industry: "Semiconductors"},
		"AMD":  {Sector: "Technology", Industry: "Semiconductors"},
		"AVGO": {Sector: "Technology", Industry: "Semiconductors"},
		"MRVL": {Sector: "Technology", Industry: "Semiconductors"},
		"MU":   {Sector: "Technology", Industry: "Semiconductors"},
	}
}

func makePos(symbol string, entry, qty float64) domain.MonitoredPosition {
	return domain.MonitoredPosition{
		Symbol:     domain.Symbol(symbol),
		EntryPrice: entry,
		Quantity:   qty,
	}
}

func makeOptionPos(symbol string, entry, qty float64) domain.MonitoredPosition {
	return domain.MonitoredPosition{
		Symbol:         domain.Symbol(symbol),
		EntryPrice:     entry,
		Quantity:       qty,
		InstrumentType: domain.InstrumentTypeOption,
	}
}

func TestSectorExposure_Check(t *testing.T) {
	ctx := context.Background()

	t.Run("disabled when both caps are zero", func(t *testing.T) {
		s := NewSectorExposure(0, 0, testMetadata(), &stubPositions{}, &stubEquity{equity: 100000}, zerolog.Nop())
		err := s.Check(ctx, domain.OrderIntent{Symbol: "AAPL", LimitPrice: 100, Quantity: 100})
		assert.NoError(t, err)
	})

	t.Run("empty portfolio + AAPL intent allowed", func(t *testing.T) {
		s := NewSectorExposure(0.30, 0.20, testMetadata(), &stubPositions{}, &stubEquity{equity: 100000}, zerolog.Nop())
		// 100 * 100 = 10000 notional -> 10% sector / 10% industry; under both caps.
		err := s.Check(ctx, domain.OrderIntent{Symbol: "AAPL", LimitPrice: 100, Quantity: 100})
		assert.NoError(t, err)
	})

	t.Run("AAPL pos + MSFT intent allowed (same sector under cap)", func(t *testing.T) {
		// AAPL Technology/Consumer Electronics, MSFT Technology/Software.
		// Shared sector only; industries differ.
		positions := []domain.MonitoredPosition{makePos("AAPL", 100, 100)} // 10000
		s := NewSectorExposure(0.30, 0.20, testMetadata(), &stubPositions{positions: positions}, &stubEquity{equity: 100000}, zerolog.Nop())
		// New MSFT 10000 -> Technology sector 20% (under 30%), Software industry 10% (under 20%).
		err := s.Check(ctx, domain.OrderIntent{Symbol: "MSFT", LimitPrice: 100, Quantity: 100})
		assert.NoError(t, err)
	})

	t.Run("5 semi positions + NVDA intent rejected on industry", func(t *testing.T) {
		// Each existing semi position = 100*40 = 4000 notional. 5 of them = 20000 (20%).
		// Industry cap is 20%, so the new NVDA intent (even 1 share) pushes it over.
		positions := []domain.MonitoredPosition{
			makePos("NVDA", 100, 40),
			makePos("AMD", 100, 40),
			makePos("AVGO", 100, 40),
			makePos("MRVL", 100, 40),
			makePos("MU", 100, 40),
		}
		s := NewSectorExposure(0.50, 0.20, testMetadata(), &stubPositions{positions: positions}, &stubEquity{equity: 100000}, zerolog.Nop())
		err := s.Check(ctx, domain.OrderIntent{Symbol: "NVDA", LimitPrice: 100, Quantity: 50}) // 5000 more
		require.Error(t, err)
		assert.Contains(t, err.Error(), "industry")
		assert.Contains(t, err.Error(), "Semiconductors")
		assert.Contains(t, err.Error(), "%")
	})

	t.Run("sector rejection fires before industry", func(t *testing.T) {
		// Technology sector: AAPL (CE) + MSFT (Software) = 30000, already 30%.
		positions := []domain.MonitoredPosition{
			makePos("AAPL", 100, 150), // 15000
			makePos("MSFT", 100, 150), // 15000
		}
		s := NewSectorExposure(0.30, 0.20, testMetadata(), &stubPositions{positions: positions}, &stubEquity{equity: 100000}, zerolog.Nop())
		// New NVDA 5000 -> Technology sector projected 35% > 30% cap.
		err := s.Check(ctx, domain.OrderIntent{Symbol: "NVDA", LimitPrice: 100, Quantity: 50})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sector")
		assert.Contains(t, err.Error(), "Technology")
	})

	t.Run("symbol not in metadata fail-open", func(t *testing.T) {
		s := NewSectorExposure(0.30, 0.20, testMetadata(), &stubPositions{}, &stubEquity{equity: 100000}, zerolog.Nop())
		err := s.Check(ctx, domain.OrderIntent{Symbol: "ZZZZ", LimitPrice: 100, Quantity: 100})
		assert.NoError(t, err)
	})

	t.Run("positions missing from metadata are skipped", func(t *testing.T) {
		// An unknown ticker in positions must not count toward sector/industry
		// notional — we have no idea where to put it.
		positions := []domain.MonitoredPosition{makePos("ZZZZ", 1000, 100)} // 100000 notional, but unclassified
		s := NewSectorExposure(0.30, 0.20, testMetadata(), &stubPositions{positions: positions}, &stubEquity{equity: 100000}, zerolog.Nop())
		err := s.Check(ctx, domain.OrderIntent{Symbol: "AAPL", LimitPrice: 100, Quantity: 100})
		assert.NoError(t, err)
	})

	t.Run("option positions skipped in notional sum", func(t *testing.T) {
		// A big NVDA option premium notional must NOT block a new NVDA equity
		// intent — Sprint 4 defers options delta-notional to Sprint 5.
		positions := []domain.MonitoredPosition{
			makeOptionPos("NVDA", 100, 500), // 50000 premium, would otherwise blow the industry cap
		}
		s := NewSectorExposure(0.30, 0.20, testMetadata(), &stubPositions{positions: positions}, &stubEquity{equity: 100000}, zerolog.Nop())
		err := s.Check(ctx, domain.OrderIntent{Symbol: "NVDA", LimitPrice: 100, Quantity: 100}) // 10k only
		assert.NoError(t, err)
	})

	t.Run("zero equity errors", func(t *testing.T) {
		s := NewSectorExposure(0.30, 0.20, testMetadata(), &stubPositions{}, &stubEquity{equity: 0}, zerolog.Nop())
		err := s.Check(ctx, domain.OrderIntent{Symbol: "AAPL", LimitPrice: 100, Quantity: 100})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid equity")
	})

	t.Run("only industry cap active triggers industry check", func(t *testing.T) {
		// Sector cap disabled (0), industry cap enforces.
		positions := []domain.MonitoredPosition{
			makePos("NVDA", 100, 100), // 10000
			makePos("AMD", 100, 100),  // 10000
		}
		s := NewSectorExposure(0, 0.20, testMetadata(), &stubPositions{positions: positions}, &stubEquity{equity: 100000}, zerolog.Nop())
		// new AVGO 10000 -> Semiconductors industry 30% > 20%.
		err := s.Check(ctx, domain.OrderIntent{Symbol: "AVGO", LimitPrice: 100, Quantity: 100})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "industry")
	})
}
