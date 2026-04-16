package simbroker_test

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/simbroker"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPerpIntent creates an OrderIntent for a crypto perp position.
func newPerpIntent(sym domain.Symbol, dir domain.Direction, qty float64) domain.OrderIntent {
	i := newIntent(sym, dir, qty)
	i.AssetClass = domain.AssetClassCryptoPerp
	return i
}

// cashBalance is a helper that reads the current cash from the broker.
func cashBalance(t *testing.T, b *simbroker.Broker) float64 {
	t.Helper()
	bp, err := b.GetAccountBuyingPower(context.Background())
	require.NoError(t, err)
	return bp.DayTradingBuyingPower
}

func TestAccrueFunding_LongPaysPositiveRate(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 1, InitialEquity: 100_000}, log)

	sym := domain.Symbol("BTC/USD")
	price := 50_000.0
	ts := time.Unix(1700000000, 0).UTC()
	b.UpdatePrice(sym, price, ts)

	_, err := b.SubmitOrder(context.Background(), newPerpIntent(sym, domain.DirectionLong, 1))
	require.NoError(t, err)

	cashAfterFill := cashBalance(t, b)

	rate := 0.0001 // 1 bps
	fundingTS := ts.Add(8 * time.Hour)
	b.AccrueFunding(sym, rate, fundingTS)

	// Payment = 1 * 50000 * 0.0001 = 5.0; long pays, cash decreases by 5
	assert.InDelta(t, cashAfterFill-5.0, cashBalance(t, b), 0.01)

	assert.InDelta(t, -5.0, b.FundingPnL(), 0.001)
	paid, received := b.FundingStats()
	assert.InDelta(t, 5.0, paid, 0.001)
	assert.InDelta(t, 0.0, received, 0.001)
}

func TestAccrueFunding_ShortReceivesPositiveRate(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 1, InitialEquity: 100_000}, log)

	sym := domain.Symbol("BTC/USD")
	price := 50_000.0
	ts := time.Unix(1700000000, 0).UTC()
	b.UpdatePrice(sym, price, ts)

	_, err := b.SubmitOrder(context.Background(), newPerpIntent(sym, domain.DirectionShort, 1))
	require.NoError(t, err)

	cashAfterFill := cashBalance(t, b)

	rate := 0.0001
	b.AccrueFunding(sym, rate, ts.Add(8*time.Hour))

	// Payment = 1 * 50000 * 0.0001 = 5.0; short receives, cash increases by 5
	assert.InDelta(t, cashAfterFill+5.0, cashBalance(t, b), 0.01)

	assert.InDelta(t, 5.0, b.FundingPnL(), 0.001)
	paid, received := b.FundingStats()
	assert.InDelta(t, 0.0, paid, 0.001)
	assert.InDelta(t, 5.0, received, 0.001)
}

func TestAccrueFunding_NegativeRate_LongReceives(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 1, InitialEquity: 100_000}, log)

	sym := domain.Symbol("ETH/USD")
	price := 3_000.0
	ts := time.Unix(1700000000, 0).UTC()
	b.UpdatePrice(sym, price, ts)

	_, err := b.SubmitOrder(context.Background(), newPerpIntent(sym, domain.DirectionLong, 2))
	require.NoError(t, err)

	cashAfterFill := cashBalance(t, b)

	rate := -0.0002 // negative = shorts pay longs
	b.AccrueFunding(sym, rate, ts.Add(8*time.Hour))

	// Payment = 2 * 3000 * (-0.0002) = -1.2; long: cash -= (-1.2) => cash increases by 1.2
	assert.InDelta(t, cashAfterFill+1.2, cashBalance(t, b), 0.01)

	assert.InDelta(t, 1.2, b.FundingPnL(), 0.001)
	paid, received := b.FundingStats()
	assert.InDelta(t, 0.0, paid, 0.001)
	assert.InDelta(t, 1.2, received, 0.001)
}

func TestAccrueFunding_NoPosition_Noop(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 1, InitialEquity: 100_000}, log)

	sym := domain.Symbol("SOL/USD")
	b.UpdatePrice(sym, 100, time.Unix(1700000000, 0).UTC())

	cashBefore := cashBalance(t, b)

	b.AccrueFunding(sym, 0.0001, time.Unix(1700000000, 0).UTC())

	assert.Equal(t, cashBefore, cashBalance(t, b))
	assert.InDelta(t, 0.0, b.FundingPnL(), 0.001)
}

func TestAccrueFunding_EquityPosition_Ignored(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 1, InitialEquity: 100_000}, log)

	sym := domain.Symbol("AAPL")
	price := 150.0
	ts := time.Unix(1700000000, 0).UTC()
	b.UpdatePrice(sym, price, ts)

	// Submit equity order (default AssetClass is empty/EQUITY)
	_, err := b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 10))
	require.NoError(t, err)

	cashAfterFill := cashBalance(t, b)

	b.AccrueFunding(sym, 0.001, ts.Add(8*time.Hour))

	assert.Equal(t, cashAfterFill, cashBalance(t, b))
	assert.InDelta(t, 0.0, b.FundingPnL(), 0.001)
}

func TestAccrueFunding_AccumulatesOverMultiplePeriods(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 1, InitialEquity: 100_000}, log)

	sym := domain.Symbol("BTC/USD")
	price := 40_000.0
	ts := time.Unix(1700000000, 0).UTC()
	b.UpdatePrice(sym, price, ts)

	_, err := b.SubmitOrder(context.Background(), newPerpIntent(sym, domain.DirectionLong, 0.5))
	require.NoError(t, err)

	cashAfterFill := cashBalance(t, b)

	// 3 funding periods at different rates
	rates := []float64{0.0001, 0.0002, -0.00005}
	var totalPayment float64
	for i, rate := range rates {
		fundTS := ts.Add(time.Duration(i+1) * 8 * time.Hour)
		b.AccrueFunding(sym, rate, fundTS)
		totalPayment += 0.5 * price * rate // payment from long's perspective
	}

	// totalPayment = 2.0 + 4.0 - 1.0 = 5.0 (paid by long)
	assert.InDelta(t, -totalPayment, b.FundingPnL(), 0.001)

	// Cash should decrease by totalPayment from the post-fill level
	assert.InDelta(t, cashAfterFill-totalPayment, cashBalance(t, b), 0.01)
}

func TestAccrueFunding_EmitsFundingEvent(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 1, InitialEquity: 100_000}, log)

	ch, err := b.SubscribeOrderUpdates(context.Background())
	require.NoError(t, err)

	sym := domain.Symbol("BTC/USD")
	price := 50_000.0
	ts := time.Unix(1700000000, 0).UTC()
	b.UpdatePrice(sym, price, ts)

	_, err = b.SubmitOrder(context.Background(), newPerpIntent(sym, domain.DirectionLong, 1))
	require.NoError(t, err)

	// Drain the fill event from SubmitOrder
	select {
	case <-ch:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected fill event")
	}

	fundTS := ts.Add(8 * time.Hour)
	b.AccrueFunding(sym, 0.0001, fundTS)

	// Should receive a funding event
	select {
	case update := <-ch:
		assert.Equal(t, "funding", update.Event)
		assert.Equal(t, fundTS, update.FilledAt)
		assert.InDelta(t, 5.0, update.Price, 0.001) // payment amount
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected funding event on fill channel")
	}
}

func TestFundingPnL_NetCalculation(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 1, InitialEquity: 100_000}, log)

	sym := domain.Symbol("BTC/USD")
	price := 50_000.0
	ts := time.Unix(1700000000, 0).UTC()
	b.UpdatePrice(sym, price, ts)

	_, err := b.SubmitOrder(context.Background(), newPerpIntent(sym, domain.DirectionLong, 1))
	require.NoError(t, err)

	// Pay funding (positive rate, long pays)
	b.AccrueFunding(sym, 0.0001, ts.Add(8*time.Hour))
	// Receive funding (negative rate, long receives)
	b.AccrueFunding(sym, -0.00005, ts.Add(16*time.Hour))

	// paid = 5.0, received = 2.5, net = -2.5
	paid, received := b.FundingStats()
	assert.InDelta(t, 5.0, paid, 0.001)
	assert.InDelta(t, 2.5, received, 0.001)
	assert.InDelta(t, -2.5, b.FundingPnL(), 0.001)
}
