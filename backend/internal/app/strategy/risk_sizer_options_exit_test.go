package strategy_test

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	stratports "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOptionsMarket satisfies ports.OptionsMarketDataPort. The exit path
// does not call GetOptionChain (only entry does), so a no-op suffices.
type fakeOptionsMarket struct{}

func (fakeOptionsMarket) GetOptionChain(_ context.Context, _ domain.Symbol, _ time.Time, _ domain.OptionRight, _, _ int) ([]domain.OptionContractSnapshot, error) {
	return nil, nil
}

func optionsExitSpec() *stratports.Spec {
	return &stratports.Spec{
		Params: map[string]any{
			"limit_offset_bps":   int64(5),
			"stop_bps":           int64(25),
			"risk_per_trade_bps": int64(10),
		},
		Options: &domain.OptionsConfig{Enabled: true},
		Routing: stratports.RoutingConfig{AssetClasses: []string{"OPTION"}},
	}
}

// TestRiskSizer_OptionsExit_TranslatesUnderlyingToContract verifies the core
// production bug: a strategy-emitted exit signal keyed by underlying (MRVL)
// must translate to a CloseLong intent keyed by the open contract symbol so
// position_gate's symbol-equality filter matches.
func TestRiskSizer_OptionsExit_TranslatesUnderlyingToContract(t *testing.T) {
	bus := memory.NewBus()
	store := &fakeSpecStore{spec: optionsExitSpec()}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	rs.SetOptionsMarket(fakeOptionsMarket{})

	openContract := domain.MonitoredPosition{
		Symbol:         domain.Symbol("MRVL260508C00162500"),
		EntryPrice:     2.10,
		EntryTime:      time.Now(),
		Quantity:       3,
		Strategy:       "macd_only_v1",
		AssetClass:     domain.AssetClassEquity,
		TenantID:       "t1",
		EnvMode:        mustEnvMode(t),
		Side:           "BUY",
		InstrumentType: domain.InstrumentTypeOption,
		OptionRight:    "CALL",
		OptionExpiry:   time.Date(2026, 5, 8, 16, 0, 0, 0, time.UTC),
	}
	rs.SetOpenOptionContractsLookup(func(underlying string) []domain.MonitoredPosition {
		if underlying == "MRVL" {
			return []domain.MonitoredPosition{openContract}
		}
		return nil
	})

	ctx := context.Background()
	require.NoError(t, rs.Start(ctx))

	received := subscribeOrderIntentCreated(t, bus)

	iid, _ := strat.NewInstanceID("macd_only_v1:1.0.0:MRVL")
	enrichment := domain.SignalEnrichment{
		Signal: domain.SignalRef{
			StrategyInstanceID: string(iid),
			Symbol:             "MRVL",
			SignalType:         strat.SignalExit.String(),
			Side:               strat.SideSell.String(),
			Tags: map[string]string{
				"ref_price": "67.50",
				"reason":    "bb_macd_stop_hit",
				"bar_ts":    "2026-05-01T14:30:00Z",
			},
		},
		Status:     domain.EnrichmentOK,
		Direction:  domain.DirectionShort,
		Confidence: 0.7,
	}
	publishSignalEnriched(t, bus, enrichment)

	events := waitForEvents(t, received, 1)
	require.Len(t, events, 1)

	intent, ok := events[0].Payload.(domain.OrderIntent)
	require.True(t, ok, "expected OrderIntent payload, got %T", events[0].Payload)

	assert.Equal(t, domain.Symbol("MRVL260508C00162500"), intent.Symbol,
		"intent must be keyed by contract symbol, not underlying")
	assert.Equal(t, domain.DirectionCloseLong, intent.Direction,
		"options always close as CloseLong (broker-LONG semantics)")
	assert.Equal(t, "market", intent.OrderType,
		"option exit defers pricing to broker via market order")
	assert.Equal(t, float64(3), intent.Quantity, "quantity must match the open position")
	assert.Equal(t, "strategy:bb_macd_stop_hit", intent.Rationale,
		"rationale tag must distinguish from exit_monitor:* origins")
	assert.Equal(t, string(domain.InstrumentTypeOption), intent.Meta["instrument_type"],
		"meta must mark this as an option exit so downstream consumers route it correctly")
	assert.Equal(t, "MRVL", intent.Meta["underlying"])
	assert.Equal(t, "strategy", intent.Meta["exit_origin"])
}

// TestRiskSizer_OptionsExit_NoOpenContractsIsLegitimate verifies the stale-
// exit case: the lookup returns no positions, so no intent is emitted and
// the call returns cleanly (single info log, no error).
func TestRiskSizer_OptionsExit_NoOpenContractsIsLegitimate(t *testing.T) {
	bus := memory.NewBus()
	store := &fakeSpecStore{spec: optionsExitSpec()}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	rs.SetOptionsMarket(fakeOptionsMarket{})
	rs.SetOpenOptionContractsLookup(func(string) []domain.MonitoredPosition { return nil })

	ctx := context.Background()
	require.NoError(t, rs.Start(ctx))

	received := subscribeOrderIntentCreated(t, bus)

	iid, _ := strat.NewInstanceID("macd_only_v1:1.0.0:MRVL")
	enrichment := domain.SignalEnrichment{
		Signal: domain.SignalRef{
			StrategyInstanceID: string(iid),
			Symbol:             "MRVL",
			SignalType:         strat.SignalExit.String(),
			Side:               strat.SideSell.String(),
			Tags: map[string]string{
				"ref_price": "67.50",
				"reason":    "bb_macd_stop_hit",
			},
		},
		Status:     domain.EnrichmentOK,
		Direction:  domain.DirectionShort,
		Confidence: 0.7,
	}
	publishSignalEnriched(t, bus, enrichment)

	select {
	case ev := <-received:
		t.Fatalf("did not expect an intent for stale exit; got %+v", ev)
	case <-time.After(100 * time.Millisecond):
		// Expected silence.
	}
}

// TestRiskSizer_OptionsExit_ClosesAllContractsUnderUnderlying verifies the
// plan default (Q1: "close all"): when multiple option positions are open
// under the same underlying, every one gets a close intent.
func TestRiskSizer_OptionsExit_ClosesAllContractsUnderUnderlying(t *testing.T) {
	bus := memory.NewBus()
	store := &fakeSpecStore{spec: optionsExitSpec()}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	rs.SetOptionsMarket(fakeOptionsMarket{})

	envMode := mustEnvMode(t)
	contracts := []domain.MonitoredPosition{
		{
			Symbol:         domain.Symbol("MRVL260508C00162500"),
			EntryPrice:     2.10,
			EntryTime:      time.Now(),
			Quantity:       3,
			Strategy:       "macd_only_v1",
			TenantID:       "t1",
			EnvMode:        envMode,
			Side:           "BUY",
			InstrumentType: domain.InstrumentTypeOption,
			OptionRight:    "CALL",
		},
		{
			Symbol:         domain.Symbol("MRVL260515C00170000"),
			EntryPrice:     1.85,
			EntryTime:      time.Now(),
			Quantity:       5,
			Strategy:       "macd_only_v1",
			TenantID:       "t1",
			EnvMode:        envMode,
			Side:           "BUY",
			InstrumentType: domain.InstrumentTypeOption,
			OptionRight:    "CALL",
		},
	}
	rs.SetOpenOptionContractsLookup(func(underlying string) []domain.MonitoredPosition {
		if underlying == "MRVL" {
			return contracts
		}
		return nil
	})

	ctx := context.Background()
	require.NoError(t, rs.Start(ctx))

	received := subscribeOrderIntentCreated(t, bus)

	iid, _ := strat.NewInstanceID("macd_only_v1:1.0.0:MRVL")
	enrichment := domain.SignalEnrichment{
		Signal: domain.SignalRef{
			StrategyInstanceID: string(iid),
			Symbol:             "MRVL",
			SignalType:         strat.SignalExit.String(),
			Side:               strat.SideSell.String(),
			Tags: map[string]string{
				"ref_price": "67.50",
				"reason":    "bb_macd_stop_hit",
			},
		},
		Status:     domain.EnrichmentOK,
		Direction:  domain.DirectionShort,
		Confidence: 0.7,
	}
	publishSignalEnriched(t, bus, enrichment)

	events := waitForEvents(t, received, 2)
	require.Len(t, events, 2)

	syms := make(map[domain.Symbol]bool)
	for _, ev := range events {
		intent, ok := ev.Payload.(domain.OrderIntent)
		require.True(t, ok)
		syms[intent.Symbol] = true
	}
	assert.True(t, syms["MRVL260508C00162500"], "expected close intent for first contract")
	assert.True(t, syms["MRVL260515C00170000"], "expected close intent for second contract")
}
