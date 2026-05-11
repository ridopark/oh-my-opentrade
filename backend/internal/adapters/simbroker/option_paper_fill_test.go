package simbroker

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makePaperPinnedIntent mirrors a risk-sizer paper-pinned BTO intent.
// limit is the 10%-buffer cap; liveAsk (when >0) becomes the
// intent.Meta["live_ask"] tag stamped by risk_sizer when
// [options].paper_fill_at_ask is true. liveAsk=0 omits the tag entirely so
// the broker keeps the legacy cap-fill path.
func makePaperPinnedIntent(limit, liveAsk float64) domain.OrderIntent {
	intent := makeOptionEntryIntent(limit, false)
	if liveAsk > 0 {
		intent.Meta["live_ask"] = strconv.FormatFloat(liveAsk, 'f', -1, 64)
	}
	return intent
}

// TestOptionPaperFill_LiveAskBelowCapAnchorsFill: live ask < cap → fill at
// live_ask + tiered half-spread, NOT at cap. This is the fix's hot path —
// the 11.3% backtest-only slippage collapses to the live spread.
func TestOptionPaperFill_LiveAskBelowCapAnchorsFill(t *testing.T) {
	b := newEntrySpreadBroker()

	cap := 6.60   // 10% buffer over ref_premium=6.00 (the legacy fill price)
	liveAsk := 6.05 // realistic live ask sits well below the cap
	intent := makePaperPinnedIntent(cap, liveAsk)

	price, err := b.computeOptionEntryPrice(intent, false)
	require.NoError(t, err)

	// liveAsk falls in the 5-10 premium tier => 0.005 half-spread.
	expected := liveAsk + liveAsk*0.005
	assert.InDelta(t, expected, price, 1e-9,
		"paper-pinned BTO must fill at live_ask + half_spread when live_ask < cap")
	assert.Less(t, price, cap, "fill must be cheaper than the cap")
}

// TestOptionPaperFill_LiveAskEqualToCapUsesCap: live ask == cap → fill at
// cap + half_spread (the legacy outcome). Boundary case.
func TestOptionPaperFill_LiveAskEqualToCapUsesCap(t *testing.T) {
	b := newEntrySpreadBroker()

	cap := 6.60
	intent := makePaperPinnedIntent(cap, cap) // live_ask == limit

	price, err := b.computeOptionEntryPrice(intent, false)
	require.NoError(t, err)

	expected := cap + cap*0.005 // premium 6.60 -> 0.5% tier
	assert.InDelta(t, expected, price, 1e-9,
		"paper-pinned BTO must fill at cap + half_spread when live_ask == cap")
}

// TestOptionPaperFill_LiveAskAboveCapRejects: live ask > cap → broker
// returns ErrLimitNotMarketable and refuses to fabricate a fill at cap.
// In live, IBKR/Alpaca would leave the order working unfilled.
func TestOptionPaperFill_LiveAskAboveCapRejects(t *testing.T) {
	b := newEntrySpreadBroker()

	cap := 6.60
	liveAsk := 7.20 // 9% above cap
	intent := makePaperPinnedIntent(cap, liveAsk)

	_, err := b.computeOptionEntryPrice(intent, false)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrLimitNotMarketable),
		"live_ask > cap must surface ErrLimitNotMarketable")
}

// TestOptionPaperFill_MetaAbsentPreservesCapFill: no live_ask Meta tag
// (paper_fill_at_ask=false at the strategy level, or non-copytrade
// strategies that don't go through the paper-pinned branch) → fill at
// intent.LimitPrice + half_spread, byte-identical to pre-fix behavior.
func TestOptionPaperFill_MetaAbsentPreservesCapFill(t *testing.T) {
	b := newEntrySpreadBroker()

	cap := 6.60
	intent := makePaperPinnedIntent(cap, 0) // no live_ask Meta

	price, err := b.computeOptionEntryPrice(intent, false)
	require.NoError(t, err)

	expected := cap + cap*0.005
	assert.InDelta(t, expected, price, 1e-9,
		"absent live_ask must preserve legacy cap-anchored fill")
}

// TestOptionPaperFill_SubmitOrderEndToEnd_LiveAskUsed: full SubmitOrder
// path with live_ask < cap. Verifies the wiring from the Meta tag through
// computeOptionEntryPrice → fillPrice → recorded order.
func TestOptionPaperFill_SubmitOrderEndToEnd_LiveAskUsed(t *testing.T) {
	b := newEntrySpreadBroker()
	b.UpdatePrice(domain.Symbol("AAPL"), 150.0, time.Now())

	cap := 6.60
	liveAsk := 6.05
	intent := makePaperPinnedIntent(cap, liveAsk)

	_, err := b.SubmitOrder(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, b.orders, 1)

	// SubmitOrder applies the default 5bps slippage AFTER computeOptionEntryPrice.
	expectedPreSlippage := liveAsk + liveAsk*0.005
	expected := expectedPreSlippage * (1.0 + 5.0/10000.0)
	for _, o := range b.orders {
		assert.InDelta(t, expected, o.fillPrice, 1e-6,
			"end-to-end fill must anchor on live_ask, not on cap")
	}
}

// TestOptionPaperFill_SubmitOrderEndToEnd_LiveAskRejection: full
// SubmitOrder rejects when live_ask > cap. The order must not be recorded
// and the intent must not occupy a position slot.
func TestOptionPaperFill_SubmitOrderEndToEnd_LiveAskRejection(t *testing.T) {
	b := newEntrySpreadBroker()
	b.UpdatePrice(domain.Symbol("AAPL"), 150.0, time.Now())

	cap := 6.60
	liveAsk := 7.20
	intent := makePaperPinnedIntent(cap, liveAsk)

	_, err := b.SubmitOrder(context.Background(), intent)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrLimitNotMarketable),
		"end-to-end rejection must surface ErrLimitNotMarketable")
	assert.Empty(t, b.orders, "rejected paper-pin entry must not be recorded")
}

// TestOptionPaperFill_LegacyFlagDisabledIgnoresMeta: when
// OptionEntrySpreadEnabled=false (legacy byte-identical mode), the
// paper-pinned live_ask Meta is ignored — fill stays at intent.LimitPrice.
// Defensive: no strategy should be combining legacy mode with live_ask,
// but if it did, behaviour falls through to the documented legacy path.
func TestOptionPaperFill_LegacyFlagDisabledIgnoresMeta(t *testing.T) {
	b := New(Config{
		SlippageBPS:              0,
		DisableFillChan:          true,
		OptionEntrySpreadEnabled: false,
	}, zerolog.Nop())

	cap := 6.60
	liveAsk := 6.05
	intent := makePaperPinnedIntent(cap, liveAsk)

	price, err := b.computeOptionEntryPrice(intent, false)
	require.NoError(t, err)
	assert.Equal(t, cap, price,
		"legacy mode must ignore live_ask Meta and stay at intent.LimitPrice")
}
