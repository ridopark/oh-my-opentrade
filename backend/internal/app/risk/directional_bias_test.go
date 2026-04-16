package risk

import (
	"context"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeLongPos / makeShortPos build MonitoredPositions with the fields the
// DirectionalBias checker reads: EntryPrice, Quantity, and Side (which
// IsShort() consults). Symbol is set for readability in failure output.
func makeLongPos(symbol string, entry, qty float64) domain.MonitoredPosition {
	return domain.MonitoredPosition{
		Symbol:     domain.Symbol(symbol),
		EntryPrice: entry,
		Quantity:   qty,
		Side:       "BUY",
	}
}

func makeShortPos(symbol string, entry, qty float64) domain.MonitoredPosition {
	return domain.MonitoredPosition{
		Symbol:     domain.Symbol(symbol),
		EntryPrice: entry,
		Quantity:   qty,
		Side:       "SELL",
	}
}

func makeOptionLongPos(symbol string, entry, qty float64) domain.MonitoredPosition {
	return domain.MonitoredPosition{
		Symbol:         domain.Symbol(symbol),
		EntryPrice:     entry,
		Quantity:       qty,
		Side:           "BUY",
		InstrumentType: domain.InstrumentTypeOption,
	}
}

func TestDirectionalBias_Check(t *testing.T) {
	ctx := context.Background()
	equity := 100000.0

	t.Run("disabled when maxBiasPct <= 0", func(t *testing.T) {
		d := NewDirectionalBias(0, &stubPositions{}, &stubEquity{equity: equity}, zerolog.Nop())
		err := d.Check(ctx, domain.OrderIntent{Symbol: "AAPL", Direction: domain.DirectionLong, LimitPrice: 1000, Quantity: 1000})
		assert.NoError(t, err)
	})

	t.Run("empty portfolio + new long under cap allowed", func(t *testing.T) {
		d := NewDirectionalBias(0.70, &stubPositions{}, &stubEquity{equity: equity}, zerolog.Nop())
		// 100 * 100 = 10000 notional -> 10% net long; well under 70%.
		err := d.Check(ctx, domain.OrderIntent{Symbol: "AAPL", Direction: domain.DirectionLong, LimitPrice: 100, Quantity: 100})
		assert.NoError(t, err)
	})

	t.Run("balanced portfolio + either direction allowed", func(t *testing.T) {
		// 30k long + 30k short = 0 net bias.
		positions := []domain.MonitoredPosition{
			makeLongPos("AAPL", 100, 300),  // 30000 long
			makeShortPos("MSFT", 100, 300), // 30000 short
		}
		d := NewDirectionalBias(0.70, &stubPositions{positions: positions}, &stubEquity{equity: equity}, zerolog.Nop())
		// New 20k long -> projected net = +20k = 20% < 70%.
		errLong := d.Check(ctx, domain.OrderIntent{Symbol: "NVDA", Direction: domain.DirectionLong, LimitPrice: 100, Quantity: 200})
		assert.NoError(t, errLong)
		errShort := d.Check(ctx, domain.OrderIntent{Symbol: "NVDA", Direction: domain.DirectionShort, LimitPrice: 100, Quantity: 200})
		assert.NoError(t, errShort)
	})

	t.Run("60% net long + new 20% long rejected", func(t *testing.T) {
		// 60000 net long (60%).
		positions := []domain.MonitoredPosition{makeLongPos("AAPL", 100, 600)}
		d := NewDirectionalBias(0.70, &stubPositions{positions: positions}, &stubEquity{equity: equity}, zerolog.Nop())
		// New 20k long -> 80k net (80%) > 70% AND further from neutral.
		err := d.Check(ctx, domain.OrderIntent{Symbol: "MSFT", Direction: domain.DirectionLong, LimitPrice: 100, Quantity: 200})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "directional_bias")
		assert.Contains(t, err.Error(), "long")
		assert.Contains(t, err.Error(), "%")
	})

	t.Run("60% net long + new 20% short allowed (reduces bias)", func(t *testing.T) {
		positions := []domain.MonitoredPosition{makeLongPos("AAPL", 100, 600)}
		d := NewDirectionalBias(0.70, &stubPositions{positions: positions}, &stubEquity{equity: equity}, zerolog.Nop())
		// New 20k short -> 40k net long (40%) — further from current 60%
		// toward neutral; always allowed.
		err := d.Check(ctx, domain.OrderIntent{Symbol: "MSFT", Direction: domain.DirectionShort, LimitPrice: 100, Quantity: 200})
		assert.NoError(t, err)
	})

	t.Run("net short past threshold + new long allowed (reduces bias)", func(t *testing.T) {
		// 80k net short already exceeds the 70% cap — but an intent that
		// *reduces* the bias must always be allowed regardless.
		positions := []domain.MonitoredPosition{makeShortPos("AAPL", 100, 800)}
		d := NewDirectionalBias(0.70, &stubPositions{positions: positions}, &stubEquity{equity: equity}, zerolog.Nop())
		err := d.Check(ctx, domain.OrderIntent{Symbol: "MSFT", Direction: domain.DirectionLong, LimitPrice: 100, Quantity: 300})
		assert.NoError(t, err)
	})

	t.Run("net short past threshold + new short rejected (worsens)", func(t *testing.T) {
		positions := []domain.MonitoredPosition{makeShortPos("AAPL", 100, 800)}
		d := NewDirectionalBias(0.70, &stubPositions{positions: positions}, &stubEquity{equity: equity}, zerolog.Nop())
		err := d.Check(ctx, domain.OrderIntent{Symbol: "MSFT", Direction: domain.DirectionShort, LimitPrice: 100, Quantity: 100})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "short")
	})

	t.Run("option positions skipped in notional sum", func(t *testing.T) {
		// A big option position must not count as directional underlying
		// exposure — Sprint 4 defers options delta-notional to Sprint 5.
		positions := []domain.MonitoredPosition{makeOptionLongPos("NVDA", 100, 10000)} // 1M ignored
		d := NewDirectionalBias(0.70, &stubPositions{positions: positions}, &stubEquity{equity: equity}, zerolog.Nop())
		err := d.Check(ctx, domain.OrderIntent{Symbol: "AAPL", Direction: domain.DirectionLong, LimitPrice: 100, Quantity: 100})
		assert.NoError(t, err)
	})

	t.Run("zero equity errors", func(t *testing.T) {
		d := NewDirectionalBias(0.70, &stubPositions{}, &stubEquity{equity: 0}, zerolog.Nop())
		err := d.Check(ctx, domain.OrderIntent{Symbol: "AAPL", Direction: domain.DirectionLong, LimitPrice: 100, Quantity: 100})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid equity")
	})
}
