package simbroker_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/adapters/simbroker"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newIntent(sym domain.Symbol, dir domain.Direction, qty float64) domain.OrderIntent {
	return domain.OrderIntent{
		ID:             uuid.New(),
		TenantID:       "tenant-1",
		EnvMode:        domain.EnvModePaper,
		Symbol:         sym,
		Direction:      dir,
		Quantity:       qty,
		IdempotencyKey: "idem-" + uuid.NewString(),
	}
}

func slippage(lastPrice float64, bps int64) float64 {
	return lastPrice * float64(bps) / 10000.0
}

func TestNew_DefaultSlippageBPS(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 0}, log)

	sym := domain.Symbol("AAPL")
	price := 100.0
	barTime := time.Unix(1700000000, 0).UTC()
	b.UpdatePrice(sym, price, barTime)

	orderID, err := b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 1))
	require.NoError(t, err)
	require.NotEmpty(t, orderID)

	fill, ok := b.GetFillPrice(orderID)
	require.True(t, ok)
	assert.InEpsilon(t, price+slippage(price, 5), fill, 1e-12)
}

func TestNew_CustomSlippageBPS(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 12}, log)

	sym := domain.Symbol("MSFT")
	price := 200.0
	barTime := time.Unix(1700000000, 0).UTC()
	b.UpdatePrice(sym, price, barTime)

	orderID, err := b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 2))
	require.NoError(t, err)

	fill, ok := b.GetFillPrice(orderID)
	require.True(t, ok)
	assert.InEpsilon(t, price+slippage(price, 12), fill, 1e-12)
}

func TestUpdatePrice_GetPrice(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 7}, log)

	sym := domain.Symbol("BTC/USD")
	price := 43210.5
	b.UpdatePrice(sym, price, time.Unix(1700000001, 0).UTC())

	p, ok := b.GetPrice(sym)
	require.True(t, ok)
	assert.Equal(t, price, p)

	_, ok = b.GetPrice(domain.Symbol("UNKNOWN"))
	assert.False(t, ok)
}

func TestSubmitOrder_BuyAndSellFillPriceWithSlippage(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 0}, log)

	sym := domain.Symbol("ETH/USD")
	price := 100.0
	b.UpdatePrice(sym, price, time.Unix(1700000100, 0).UTC())

	buyID, err := b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 1))
	require.NoError(t, err)
	buyFill, ok := b.GetFillPrice(buyID)
	require.True(t, ok)
	assert.InEpsilon(t, price+slippage(price, 5), buyFill, 1e-12)

	sellID, err := b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionShort, 1))
	require.NoError(t, err)
	sellFill, ok := b.GetFillPrice(sellID)
	require.True(t, ok)
	assert.InEpsilon(t, price-slippage(price, 5), sellFill, 1e-12)
}

func TestSubmitOrder_ErrorsWithoutValidPrice(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 5}, log)

	sym := domain.Symbol("TSLA")
	_, err := b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no price available")

	b.UpdatePrice(sym, 0, time.Unix(1700000200, 0).UTC())
	_, err = b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 1))
	require.Error(t, err)

	b.UpdatePrice(sym, -1, time.Unix(1700000201, 0).UTC())
	_, err = b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 1))
	require.Error(t, err)
}

func TestGetOrderStatus_FilledAndUnknown(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 5}, log)

	sym := domain.Symbol("NVDA")
	b.UpdatePrice(sym, 10, time.Unix(1700000300, 0).UTC())

	orderID, err := b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 1))
	require.NoError(t, err)

	status, err := b.GetOrderStatus(context.Background(), orderID)
	require.NoError(t, err)
	assert.Equal(t, "filled", status)

	_, err = b.GetOrderStatus(context.Background(), "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCancelOrder_KnownNoOpAndUnknownErrors(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 5}, log)

	sym := domain.Symbol("SPY")
	b.UpdatePrice(sym, 50, time.Unix(1700000400, 0).UTC())

	orderID, err := b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 1))
	require.NoError(t, err)

	err = b.CancelOrder(context.Background(), orderID)
	require.NoError(t, err)

	err = b.CancelOrder(context.Background(), "unknown")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestGetFillPrice_KnownAndUnknown(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 5}, log)

	sym := domain.Symbol("QQQ")
	price := 300.0
	b.UpdatePrice(sym, price, time.Unix(1700000500, 0).UTC())

	orderID, err := b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 1))
	require.NoError(t, err)

	fill, ok := b.GetFillPrice(orderID)
	require.True(t, ok)
	assert.InEpsilon(t, price+slippage(price, 5), fill, 1e-12)

	fill, ok = b.GetFillPrice("missing")
	assert.False(t, ok)
	assert.Equal(t, 0.0, fill)
}

func TestStats_CountsOrdersAndSymbols(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 5}, log)

	orders, symbols := b.Stats()
	assert.Equal(t, 0, orders)
	assert.Equal(t, 0, symbols)

	sym := domain.Symbol("IWM")
	b.UpdatePrice(sym, 100, time.Unix(1700000600, 0).UTC())

	_, err := b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 0))
	require.NoError(t, err)

	orders, symbols = b.Stats()
	assert.Equal(t, 1, orders)
	assert.Equal(t, 1, symbols)

	pos, err := b.GetPositions(context.Background(), "tenant-1", domain.EnvModePaper)
	require.NoError(t, err)
	assert.Empty(t, pos)
}

func TestGetPositions_LongLifecycle_WeightedAverage_ReduceAndFlip(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 5}, log)

	sym := domain.Symbol("AAPL")

	price1 := 100.0
	b.UpdatePrice(sym, price1, time.Unix(1700000700, 0).UTC())
	_, err := b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 1))
	require.NoError(t, err)
	fill1 := price1 + slippage(price1, 5)

	price2 := 110.0
	b.UpdatePrice(sym, price2, time.Unix(1700000701, 0).UTC())
	_, err = b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 3))
	require.NoError(t, err)
	fill2 := price2 + slippage(price2, 5)

	positions, err := b.GetPositions(context.Background(), "tenant-1", domain.EnvModePaper)
	require.NoError(t, err)
	require.Len(t, positions, 1)

	expectedAvg := (fill1*1 + fill2*3) / 4
	assert.Equal(t, sym, positions[0].Symbol)
	assert.Equal(t, "buy", positions[0].Side)
	assert.InEpsilon(t, 4.0, positions[0].Quantity, 1e-12)
	assert.InEpsilon(t, expectedAvg, positions[0].Price, 1e-12)
	assert.Equal(t, "open", positions[0].Status)

	price3 := 120.0
	b.UpdatePrice(sym, price3, time.Unix(1700000702, 0).UTC())
	_, err = b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionShort, 2))
	require.NoError(t, err)

	positions, err = b.GetPositions(context.Background(), "tenant-1", domain.EnvModePaper)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	assert.Equal(t, "buy", positions[0].Side)
	assert.InEpsilon(t, 2.0, positions[0].Quantity, 1e-12)
	assert.InEpsilon(t, expectedAvg, positions[0].Price, 1e-12)

	sellFill := price3 - slippage(price3, 5)
	_, err = b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionShort, 5))
	require.NoError(t, err)

	positions, err = b.GetPositions(context.Background(), "tenant-1", domain.EnvModePaper)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	assert.Equal(t, "sell", positions[0].Side)
	assert.InEpsilon(t, 3.0, positions[0].Quantity, 1e-12)
	assert.InEpsilon(t, sellFill, positions[0].Price, 1e-12)
}

func TestGetPositions_ShortLifecycle_WeightedAverage_CloseAndFlipToLong(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 5}, log)

	sym := domain.Symbol("META")

	price1 := 50.0
	b.UpdatePrice(sym, price1, time.Unix(1700000800, 0).UTC())
	_, err := b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionShort, 2))
	require.NoError(t, err)
	fill1 := price1 - slippage(price1, 5)

	price2 := 55.0
	b.UpdatePrice(sym, price2, time.Unix(1700000801, 0).UTC())
	_, err = b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionShort, 1))
	require.NoError(t, err)
	fill2 := price2 - slippage(price2, 5)

	positions, err := b.GetPositions(context.Background(), "tenant-1", domain.EnvModePaper)
	require.NoError(t, err)
	require.Len(t, positions, 1)

	expectedAvg := (fill1*2 + fill2*1) / 3
	assert.Equal(t, "sell", positions[0].Side)
	assert.InEpsilon(t, 3.0, positions[0].Quantity, 1e-12)
	assert.InEpsilon(t, expectedAvg, positions[0].Price, 1e-12)

	price3 := 60.0
	b.UpdatePrice(sym, price3, time.Unix(1700000802, 0).UTC())
	_, err = b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 2))
	require.NoError(t, err)

	positions, err = b.GetPositions(context.Background(), "tenant-1", domain.EnvModePaper)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	assert.Equal(t, "sell", positions[0].Side)
	assert.InEpsilon(t, 1.0, positions[0].Quantity, 1e-12)
	assert.InEpsilon(t, expectedAvg, positions[0].Price, 1e-12)

	buyFill := price3 + slippage(price3, 5)
	_, err = b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 5))
	require.NoError(t, err)

	positions, err = b.GetPositions(context.Background(), "tenant-1", domain.EnvModePaper)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	assert.Equal(t, "buy", positions[0].Side)
	assert.InEpsilon(t, 4.0, positions[0].Quantity, 1e-12)
	assert.InEpsilon(t, buyFill, positions[0].Price, 1e-12)
}

func TestGetPositions_CloseToZeroNotReturned(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 5}, log)

	sym := domain.Symbol("IBM")
	b.UpdatePrice(sym, 100, time.Unix(1700000900, 0).UTC())
	_, err := b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionLong, 2))
	require.NoError(t, err)

	_, err = b.SubmitOrder(context.Background(), newIntent(sym, domain.DirectionShort, 2))
	require.NoError(t, err)

	positions, err := b.GetPositions(context.Background(), "tenant-1", domain.EnvModePaper)
	require.NoError(t, err)
	assert.Empty(t, positions)

	orders, symbols := b.Stats()
	assert.Equal(t, 2, orders)
	assert.Equal(t, 1, symbols)
}

func TestSubmitOrder_ConcurrentDoesNotPanic(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{SlippageBPS: 5}, log)

	sym := domain.Symbol("CONC")
	b.UpdatePrice(sym, 123.45, time.Unix(1700001000, 0).UTC())

	const n = 200
	ctx := context.Background()

	var wg sync.WaitGroup
	ids := make(map[string]struct{}, n)
	var idsMu sync.Mutex

	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dir := domain.DirectionLong
			if i%2 == 1 {
				dir = domain.DirectionShort
			}

			id, err := b.SubmitOrder(ctx, newIntent(sym, dir, 1))
			if err != nil {
				errs <- err
				return
			}
			idsMu.Lock()
			ids[id] = struct{}{}
			idsMu.Unlock()
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	assert.Len(t, ids, n)
	orders, _ := b.Stats()
	assert.Equal(t, n, orders)
}
