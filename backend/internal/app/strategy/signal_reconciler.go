package strategy

import (
	"log/slog"

	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// PositionLookupFunc returns a monitored position for a symbol, if one exists.
type PositionLookupFunc func(symbol string) (domain.MonitoredPosition, bool)

// ReconcileSignals converts conflicting entry signals into exit signals when
// an existing position is detected. This prevents wasted LLM calls on signals
// that would be rejected by the position gate, and instead routes the strategy's
// bearish/bullish conviction into a proper exit intent.
//
// Reconciliation rules:
//   - entry sell + existing LONG  → exit sell (CLOSE_LONG)
//   - entry buy  + existing SHORT → exit buy  (CLOSE_SHORT — future)
//   - entry on flat position      → passthrough (no change)
//   - exit/adjust/flat signals    → passthrough (no change)
func ReconcileSignals(signals []start.Signal, lookup PositionLookupFunc, logger *slog.Logger) []start.Signal {
	if lookup == nil {
		return signals
	}

	out := make([]start.Signal, 0, len(signals))
	for _, sig := range signals {
		out = append(out, reconcileOne(sig, lookup, logger))
	}
	return out
}

func reconcileOne(sig start.Signal, lookup PositionLookupFunc, logger *slog.Logger) start.Signal {
	if sig.Type != start.SignalEntry {
		return sig
	}

	pos, exists := lookup(sig.Symbol)
	if !exists {
		return sig
	}

	isLong := pos.Side == "BUY" || pos.Side == "long"
	isShort := pos.Side == "SELL" || pos.Side == "short"

	// Entry sell on a long position → CLOSE_LONG exit.
	if sig.Side == start.SideSell && isLong {
		logger.Info("reconciler: converting SHORT entry → CLOSE_LONG exit",
			"symbol", sig.Symbol,
			"strategy", sig.StrategyInstanceID.String(),
			"strength", sig.Strength,
			"entry_price", pos.EntryPrice,
		)
		converted := sig
		converted.Type = start.SignalExit
		if converted.Tags == nil {
			converted.Tags = make(map[string]string)
		}
		converted.Tags["reconciled"] = "entry_short_to_close_long"
		converted.Tags["exit_direction"] = string(domain.DirectionCloseLong)
		converted.Tags["original_type"] = string(start.SignalEntry)
		return converted
	}

	// Entry buy on a short position → CLOSE_SHORT exit (buy to cover).
	if sig.Side == start.SideBuy && isShort {
		logger.Info("reconciler: converting LONG entry → CLOSE_SHORT exit",
			"symbol", sig.Symbol,
			"strategy", sig.StrategyInstanceID.String(),
			"strength", sig.Strength,
			"entry_price", pos.EntryPrice,
		)
		converted := sig
		converted.Type = start.SignalExit
		if converted.Tags == nil {
			converted.Tags = make(map[string]string)
		}
		converted.Tags["reconciled"] = "entry_long_to_close_short"
		converted.Tags["exit_direction"] = string(domain.DirectionCloseShort)
		converted.Tags["original_type"] = string(start.SignalEntry)
		return converted
	}

	// Same-direction entry on existing position: let it pass through.
	// The position gate will block the duplicate.
	return sig
}
