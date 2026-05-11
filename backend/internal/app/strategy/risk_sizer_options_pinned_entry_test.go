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

// stubChainMarket returns a fixed chain on every GetOptionChain call so the
// risk sizer's forced-contract path can locate the pinned strike.
type stubChainMarket struct {
	chain []domain.OptionContractSnapshot
}

func (s stubChainMarket) GetOptionChain(_ context.Context, _ domain.Symbol, _ time.Time, _ domain.OptionRight, _, _ int) ([]domain.OptionContractSnapshot, error) {
	return s.chain, nil
}

func pinnedEntrySpec() *stratports.Spec {
	return &stratports.Spec{
		Params: map[string]any{
			"limit_offset_bps":   int64(5),
			"stop_bps":           int64(25),
			"risk_per_trade_bps": int64(100), // 1% so 1 contract @ $1 premium fits
		},
		Options: &domain.OptionsConfig{
			Enabled:      true,
			MaxContracts: 15,
			Defaults: domain.ContractSelectionConstraints{
				MinDTE: 0,
				MaxDTE: 60,
			},
		},
		Routing: stratports.RoutingConfig{AssetClasses: []string{"OPTION"}},
	}
}

func pinnedChain(expiry time.Time, strike, bid, ask float64) []domain.OptionContractSnapshot {
	return []domain.OptionContractSnapshot{{
		OptionContract: domain.OptionContract{
			ContractSymbol: domain.Symbol("AAPL260507C00190000"),
			Underlying:     domain.Symbol("AAPL"),
			Expiry:         expiry,
			Strike:         strike,
			Right:          domain.OptionRightCall,
			Multiplier:     100,
		},
		OptionQuote:  domain.OptionQuote{Bid: bid, Ask: ask, Last: (bid + ask) / 2},
		OpenInterest: 500,
	}}
}

func pinnedEntryEnrichment(t *testing.T, refPremium string) domain.SignalEnrichment {
	t.Helper()
	iid, _ := strat.NewInstanceID("copytrade_v1:1.0.0:__copytrade__")
	tags := map[string]string{
		"ref_price":           "1.20",
		strategy.TagForceExpiry: "2026-05-07",
		strategy.TagForceStrike: "190",
		strategy.TagForceRight:  "C",
	}
	if refPremium != "" {
		tags[strategy.TagForceRefPremium] = refPremium
	}
	return domain.SignalEnrichment{
		Signal: domain.SignalRef{
			StrategyInstanceID: string(iid),
			Symbol:             "AAPL",
			SignalType:         strat.SignalEntry.String(),
			Side:               strat.SideBuy.String(),
			Strength:           0.9,
			Tags:               tags,
		},
		Status:     domain.EnrichmentOK,
		Confidence: 0.9,
		Direction:  domain.DirectionLong,
	}
}

// TestRiskSizer_PaperPinnedRef_BypassesAskBufferRejection is the Stage B fix
// guard: in Paper with force_ref_premium set, the sizer used to reject when
// live ask exceeded ref_premium * (1+bufferPct). The simbroker fills at the
// priceCap, so that rejection was gating on a price we never pay. After the
// fix, the sizer proceeds and emits an OrderIntent with LimitPrice=priceCap.
func TestRiskSizer_PaperPinnedRef_BypassesAskBufferRejection(t *testing.T) {
	bus := memory.NewBus()
	store := &fakeSpecStore{spec: pinnedEntrySpec()}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	expiry := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	// Live ask = 3.60 = 3x the author's $1.20 ref premium — well above the
	// 10% buffer cap (= $1.32). Pre-fix: rejected. Post-fix: accepted.
	rs.SetOptionsMarket(stubChainMarket{chain: pinnedChain(expiry, 190, 3.50, 3.60)})
	rs.SetNowFn(func() time.Time { return time.Date(2026, 5, 5, 14, 0, 0, 0, time.UTC) })

	ctx := context.Background()
	require.NoError(t, rs.Start(ctx))

	rejected := make(chan domain.Event, 1)
	require.NoError(t, bus.Subscribe(ctx, domain.EventOrderIntentRejected, func(_ context.Context, ev domain.Event) error {
		rejected <- ev
		return nil
	}))
	received := subscribeOrderIntentCreated(t, bus)

	publishSignalEnriched(t, bus, pinnedEntryEnrichment(t, "1.20"))

	evs := waitForEvents(t, received, 1)
	intent, ok := evs[0].Payload.(domain.OrderIntent)
	require.True(t, ok)
	// priceCap = 1.20 * (1 + 0.10) = 1.32
	assert.InDelta(t, 1.32, intent.LimitPrice, 1e-9, "limit pinned to author ref + buffer")
	assert.Equal(t, domain.Symbol("AAPL260507C00190000"), intent.Symbol)

	select {
	case <-rejected:
		t.Fatalf("unexpected price_buffer_exceeded rejection in Paper with pinned ref")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestRiskSizer_LivePinnedRef_PathUnchanged ensures the Paper-only bypass
// does not leak into Live: the Live path stays on the regular spread-based
// pricing and is not coerced into the priceCap branch.
func TestRiskSizer_LivePinnedRef_PathUnchanged(t *testing.T) {
	bus := memory.NewBus()
	store := &fakeSpecStore{spec: pinnedEntrySpec()}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	expiry := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	rs.SetOptionsMarket(stubChainMarket{chain: pinnedChain(expiry, 190, 3.50, 3.60)})
	rs.SetNowFn(func() time.Time { return time.Date(2026, 5, 5, 14, 0, 0, 0, time.UTC) })

	ctx := context.Background()
	require.NoError(t, rs.Start(ctx))

	received := subscribeOrderIntentCreated(t, bus)

	// Publish enrichment with EnvMode=Live.
	live, err := domain.NewEnvMode("Live")
	require.NoError(t, err)
	enrichment := pinnedEntryEnrichment(t, "1.20")
	ev, err := domain.NewEvent(domain.EventSignalEnriched, "t1", live, "enriched-live", enrichment)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *ev))

	evs := waitForEvents(t, received, 1)
	intent, ok := evs[0].Payload.(domain.OrderIntent)
	require.True(t, ok)
	// Live falls into the spread-based default: mid + 60% spread.
	// mid = 3.55, spread = 0.10 -> limit = 3.55 + 0.06 = 3.61
	assert.InDelta(t, 3.61, intent.LimitPrice, 1e-9, "Live keeps the spread-based limit; not the pinned-paper cap")
}

// TestRiskSizer_PaperNoForceRefPremium_UnchangedSpreadPath verifies the
// bypass only fires when the force_ref_premium tag is present.
func TestRiskSizer_PaperNoForceRefPremium_UnchangedSpreadPath(t *testing.T) {
	bus := memory.NewBus()
	store := &fakeSpecStore{spec: pinnedEntrySpec()}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	expiry := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	rs.SetOptionsMarket(stubChainMarket{chain: pinnedChain(expiry, 190, 3.50, 3.60)})
	rs.SetNowFn(func() time.Time { return time.Date(2026, 5, 5, 14, 0, 0, 0, time.UTC) })

	ctx := context.Background()
	require.NoError(t, rs.Start(ctx))

	received := subscribeOrderIntentCreated(t, bus)

	publishSignalEnriched(t, bus, pinnedEntryEnrichment(t, "")) // no force_ref_premium

	evs := waitForEvents(t, received, 1)
	intent, ok := evs[0].Payload.(domain.OrderIntent)
	require.True(t, ok)
	// Falls through to the spread-based default.
	assert.InDelta(t, 3.61, intent.LimitPrice, 1e-9)
}

// TestRiskSizer_PaperPinnedRef_WithinBuffer_UsesPriceCap covers the happy
// path: live ask is just slightly above ref premium, well within the 10%
// buffer. Result should be identical to the bypass path — limit = priceCap.
func TestRiskSizer_PaperPinnedRef_WithinBuffer_UsesPriceCap(t *testing.T) {
	bus := memory.NewBus()
	store := &fakeSpecStore{spec: pinnedEntrySpec()}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	expiry := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	// Live ask 1.26 ~ 1.05x ref premium 1.20.
	rs.SetOptionsMarket(stubChainMarket{chain: pinnedChain(expiry, 190, 1.24, 1.26)})
	rs.SetNowFn(func() time.Time { return time.Date(2026, 5, 5, 14, 0, 0, 0, time.UTC) })

	ctx := context.Background()
	require.NoError(t, rs.Start(ctx))

	received := subscribeOrderIntentCreated(t, bus)

	publishSignalEnriched(t, bus, pinnedEntryEnrichment(t, "1.20"))

	evs := waitForEvents(t, received, 1)
	intent, ok := evs[0].Payload.(domain.OrderIntent)
	require.True(t, ok)
	assert.InDelta(t, 1.32, intent.LimitPrice, 1e-9, "limit pinned to author ref + buffer regardless of ask")
}
