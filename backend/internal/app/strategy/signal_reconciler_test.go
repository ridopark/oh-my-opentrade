package strategy

import (
	"log/slog"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func longPosition(symbol string) domain.MonitoredPosition {
	return domain.MonitoredPosition{
		Symbol:     domain.Symbol(symbol),
		EntryPrice: 100.0,
		EntryTime:  time.Now().Add(-time.Hour),
		Quantity:   10,
		Strategy:   "test_strategy",
	}
}

func lookupWith(positions map[string]domain.MonitoredPosition) PositionLookupFunc {
	return func(symbol string) (domain.MonitoredPosition, bool) {
		pos, ok := positions[symbol]
		return pos, ok
	}
}

func testSignal(symbol string, sigType start.SignalType, side start.Side) start.Signal {
	sig, _ := start.NewSignal(
		start.InstanceID("test:1.0.0:"+symbol),
		symbol,
		sigType,
		side,
		0.75,
		map[string]string{"reason": "test"},
	)
	return sig
}

func TestReconcile_ShortEntryOnLong_ConvertsToExit(t *testing.T) {
	lookup := lookupWith(map[string]domain.MonitoredPosition{
		"META": longPosition("META"),
	})

	signals := []start.Signal{
		testSignal("META", start.SignalEntry, start.SideSell),
	}

	result := ReconcileSignals(signals, lookup, slog.Default())

	require.Len(t, result, 1)
	assert.Equal(t, start.SignalExit, result[0].Type, "should convert to exit")
	assert.Equal(t, start.SideSell, result[0].Side, "side should remain sell")
	assert.Equal(t, 0.75, result[0].Strength, "strength preserved")
	assert.Equal(t, "entry_short_to_close_long", result[0].Tags["reconciled"])
	assert.Equal(t, "entry", result[0].Tags["original_type"])
}

func TestReconcile_LongEntryOnFlat_Passthrough(t *testing.T) {
	lookup := lookupWith(map[string]domain.MonitoredPosition{})

	signals := []start.Signal{
		testSignal("AAPL", start.SignalEntry, start.SideBuy),
	}

	result := ReconcileSignals(signals, lookup, slog.Default())

	require.Len(t, result, 1)
	assert.Equal(t, start.SignalEntry, result[0].Type, "no position = passthrough")
	assert.Equal(t, start.SideBuy, result[0].Side)
}

func TestReconcile_ShortEntryOnFlat_Passthrough(t *testing.T) {
	lookup := lookupWith(map[string]domain.MonitoredPosition{})

	signals := []start.Signal{
		testSignal("TSLA", start.SignalEntry, start.SideSell),
	}

	result := ReconcileSignals(signals, lookup, slog.Default())

	require.Len(t, result, 1)
	assert.Equal(t, start.SignalEntry, result[0].Type, "no position = passthrough")
}

func TestReconcile_ExitSignal_Passthrough(t *testing.T) {
	lookup := lookupWith(map[string]domain.MonitoredPosition{
		"META": longPosition("META"),
	})

	signals := []start.Signal{
		testSignal("META", start.SignalExit, start.SideSell),
	}

	result := ReconcileSignals(signals, lookup, slog.Default())

	require.Len(t, result, 1)
	assert.Equal(t, start.SignalExit, result[0].Type, "exit signals pass through unchanged")
	_, hasReconciled := result[0].Tags["reconciled"]
	assert.False(t, hasReconciled, "should not be tagged as reconciled")
}

func TestReconcile_LongEntryOnLong_Passthrough(t *testing.T) {
	lookup := lookupWith(map[string]domain.MonitoredPosition{
		"AAPL": longPosition("AAPL"),
	})

	signals := []start.Signal{
		testSignal("AAPL", start.SignalEntry, start.SideBuy),
	}

	result := ReconcileSignals(signals, lookup, slog.Default())

	require.Len(t, result, 1)
	assert.Equal(t, start.SignalEntry, result[0].Type, "same-direction entry passes through (position gate will handle)")
}

func TestReconcile_NilLookup_Passthrough(t *testing.T) {
	signals := []start.Signal{
		testSignal("META", start.SignalEntry, start.SideSell),
	}

	result := ReconcileSignals(signals, nil, slog.Default())

	require.Len(t, result, 1)
	assert.Equal(t, start.SignalEntry, result[0].Type, "nil lookup = no reconciliation")
}

func TestReconcile_MultipleSignals_MixedReconciliation(t *testing.T) {
	lookup := lookupWith(map[string]domain.MonitoredPosition{
		"META": longPosition("META"),
	})

	signals := []start.Signal{
		testSignal("META", start.SignalEntry, start.SideSell), // conflict → exit
		testSignal("AAPL", start.SignalEntry, start.SideBuy),  // no position → passthrough
		testSignal("META", start.SignalExit, start.SideSell),  // already exit → passthrough
	}

	result := ReconcileSignals(signals, lookup, slog.Default())

	require.Len(t, result, 3)
	assert.Equal(t, start.SignalExit, result[0].Type, "META entry sell → exit")
	assert.Equal(t, "entry_short_to_close_long", result[0].Tags["reconciled"])
	assert.Equal(t, start.SignalEntry, result[1].Type, "AAPL unchanged")
	assert.Equal(t, start.SignalExit, result[2].Type, "META exit unchanged")
}

func TestReconcile_FlatSignal_Passthrough(t *testing.T) {
	lookup := lookupWith(map[string]domain.MonitoredPosition{
		"SPY": longPosition("SPY"),
	})

	signals := []start.Signal{
		testSignal("SPY", start.SignalFlat, start.SideSell),
	}

	result := ReconcileSignals(signals, lookup, slog.Default())

	require.Len(t, result, 1)
	assert.Equal(t, start.SignalFlat, result[0].Type, "flat signals pass through")
}
