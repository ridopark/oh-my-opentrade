package simbroker

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newEntrySpreadBroker returns a simbroker with OptionEntrySpreadEnabled=true
// so the entry-side tiered half-spread is charged on fills.
func newEntrySpreadBroker() *Broker {
	return New(Config{
		SlippageBPS:              0, // isolate spread — no bps slippage in these tests
		DisableFillChan:          true,
		OptionEntrySpreadEnabled: true,
	}, zerolog.Nop())
}

func makeOptionEntryIntent(limit float64, short bool) domain.OrderIntent {
	inst := &domain.Instrument{
		Type:             domain.InstrumentTypeOption,
		Symbol:           domain.Symbol("AAPL260410C00150000"),
		UnderlyingSymbol: domain.Symbol("AAPL"),
	}
	dir := domain.DirectionLong
	if short {
		dir = domain.DirectionShort
	}
	return domain.OrderIntent{
		Symbol:     domain.Symbol("AAPL260410C00150000"),
		Direction:  dir,
		Quantity:   1,
		LimitPrice: limit,
		Instrument: inst,
		Meta: map[string]string{
			"strike":       "150.0",
			"expiry":       "2026-04-10",
			"option_right": "CALL",
			"underlying":   "AAPL",
		},
	}
}

func TestComputeOptionEntryPrice_BuyPaysAskPlusHalfSpread(t *testing.T) {
	b := newEntrySpreadBroker()

	// Premium tier >=5.0 && <10.0 => 0.005 half-spread per side.
	mid := 6.00
	intent := makeOptionEntryIntent(mid, false)

	price := b.computeOptionEntryPrice(intent, false)
	expected := mid + mid*0.005
	assert.InDelta(t, expected, price, 1e-9,
		"BUY entry must pay mid + tiered half-spread (taker hits the ask)")
	assert.Greater(t, price, mid, "buyer must pay more than mid")
}

func TestComputeOptionEntryPrice_SellReceivesBidMinusHalfSpread(t *testing.T) {
	b := newEntrySpreadBroker()

	mid := 6.00
	intent := makeOptionEntryIntent(mid, true)

	price := b.computeOptionEntryPrice(intent, true)
	expected := mid - mid*0.005
	assert.InDelta(t, expected, price, 1e-9,
		"SELL entry must receive mid - tiered half-spread (taker hits the bid)")
	assert.Less(t, price, mid, "seller must receive less than mid")
}

func TestComputeOptionEntryPrice_TieredSpreadWidensOTM(t *testing.T) {
	b := newEntrySpreadBroker()

	// Cheap OTM option (<2.0) gets the widest tier at 1.5%.
	cheapMid := 0.50
	cheap := makeOptionEntryIntent(cheapMid, false)
	cheapPrice := b.computeOptionEntryPrice(cheap, false)
	cheapSpread := (cheapPrice - cheapMid) / cheapMid

	// Deep ITM (>=10.0) gets the tightest tier at 0.3%.
	richMid := 15.00
	rich := makeOptionEntryIntent(richMid, false)
	richPrice := b.computeOptionEntryPrice(rich, false)
	richSpread := (richPrice - richMid) / richMid

	assert.InDelta(t, 0.015, cheapSpread, 1e-9, "cheap OTM must land in the widest tier")
	assert.InDelta(t, 0.003, richSpread, 1e-9, "rich ITM must land in the tightest tier")
	assert.Greater(t, cheapSpread, richSpread, "cheaper premiums must carry wider half-spread")
}

// mockOptionLiveDataBA exposes both a Bid and an Ask so we can verify the
// entry path picks Ask on BUY and Bid on SELL. mockOptionLiveData in the
// sibling test file only lets us set Bid (Ask = Bid + 0.05), which is also
// fine for an independent spread.
type mockOptionLiveDataBA struct {
	bid float64
	ask float64
}

func (m *mockOptionLiveDataBA) Quote(_ context.Context, _ string, _ time.Time, _ float64, _ string) (ports.OptionQuote, error) {
	return ports.OptionQuote{Bid: m.bid, Ask: m.ask}, nil
}

func (m *mockOptionLiveDataBA) Greeks(_ context.Context, _ string, _ time.Time, _ float64, _ string) (ports.OptionGreeks, error) {
	return ports.OptionGreeks{}, nil
}

func TestComputeOptionEntryPrice_LivePortBuyUsesAsk(t *testing.T) {
	b := newEntrySpreadBroker()
	b.SetOptionLiveData(&mockOptionLiveDataBA{bid: 6.20, ask: 6.45})

	mid := 6.30 // intentionally different from both bid and ask
	intent := makeOptionEntryIntent(mid, false)

	price := b.computeOptionEntryPrice(intent, false)
	assert.Equal(t, 6.45, price, "buyer must pay live Ask when quote is available")
}

func TestComputeOptionEntryPrice_LivePortSellUsesBid(t *testing.T) {
	b := newEntrySpreadBroker()
	b.SetOptionLiveData(&mockOptionLiveDataBA{bid: 6.20, ask: 6.45})

	mid := 6.30
	intent := makeOptionEntryIntent(mid, true)

	price := b.computeOptionEntryPrice(intent, true)
	assert.Equal(t, 6.20, price, "short seller must receive live Bid when quote is available")
}

func TestComputeOptionEntryPrice_FlagDisabledReturnsMid(t *testing.T) {
	b := New(Config{
		SlippageBPS:              0,
		DisableFillChan:          true,
		OptionEntrySpreadEnabled: false, // legacy byte-identical path
	}, zerolog.Nop())

	mid := 6.00
	intent := makeOptionEntryIntent(mid, false)

	price := b.computeOptionEntryPrice(intent, false)
	assert.Equal(t, mid, price, "flag-disabled entry must fill at mid (legacy behavior)")

	// Short side too.
	priceShort := b.computeOptionEntryPrice(makeOptionEntryIntent(mid, true), true)
	assert.Equal(t, mid, priceShort, "flag-disabled SELL entry must also fill at mid")
}

// End-to-end via SubmitOrder: the entry-spread flag must propagate through
// the real fill path. Confirms the config wiring, not just the helper.
func TestSubmitOrder_OptionEntrySpreadAppliedOnBuy(t *testing.T) {
	b := newEntrySpreadBroker()

	// Underlying price must be present so SubmitOrder can price the intent.
	b.UpdatePrice(domain.Symbol("AAPL"), 150.0, time.Now())

	mid := 6.00
	intent := makeOptionEntryIntent(mid, false)

	_, err := b.SubmitOrder(context.Background(), intent)
	require.NoError(t, err)

	// Inspect recorded fill.
	b.mu.RLock()
	defer b.mu.RUnlock()
	require.Len(t, b.orders, 1)
	var fill float64
	for _, o := range b.orders {
		fill = o.fillPrice
	}
	// simbroker.New defaults SlippageBPS to 5 when zero, so the recorded
	// fill includes (mid + half_spread) * (1 + 5bps).
	base := mid + mid*0.005 // premium>=5 tier
	expected := base * (1 + 5.0/10000.0)
	assert.InDelta(t, expected, fill, 1e-9, "SubmitOrder must charge entry half-spread on BUY")
}
