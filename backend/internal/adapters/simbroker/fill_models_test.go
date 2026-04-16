package simbroker_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/simbroker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptimisticFillModel(t *testing.T) {
	m := simbroker.OptimisticFillModel{}
	now := time.Unix(1700000000, 0).UTC()
	cur := simbroker.Bar{Time: now, Open: 99, High: 101, Low: 98, Close: 100}

	t.Run("MKT BUY applies +slippage on current close", func(t *testing.T) {
		res, err := m.FillPrice(simbroker.FillContext{
			Symbol: "AAPL", Side: "BUY", Qty: 1, OrderType: "MKT",
			CurrentBar: cur, SlippageBPS: 10,
		})
		require.NoError(t, err)
		require.True(t, res.Filled)
		assert.InEpsilon(t, 100+100*10.0/10000.0, res.Price, 1e-12)
	})

	t.Run("MKT SELL applies -slippage on current close", func(t *testing.T) {
		res, err := m.FillPrice(simbroker.FillContext{
			Symbol: "AAPL", Side: "SELL", Qty: 1, OrderType: "MKT",
			CurrentBar: cur, SlippageBPS: 10,
		})
		require.NoError(t, err)
		require.True(t, res.Filled)
		assert.InEpsilon(t, 100-100*10.0/10000.0, res.Price, 1e-12)
	})

	t.Run("LMT fills at limit", func(t *testing.T) {
		res, err := m.FillPrice(simbroker.FillContext{
			Symbol: "AAPL", Side: "BUY", Qty: 1, OrderType: "LMT",
			LimitPrice: 99.5, CurrentBar: cur, SlippageBPS: 10,
		})
		require.NoError(t, err)
		require.True(t, res.Filled)
		assert.Equal(t, 99.5, res.Price)
	})

	t.Run("error on zero close", func(t *testing.T) {
		_, err := m.FillPrice(simbroker.FillContext{
			Symbol: "X", Side: "BUY", OrderType: "MKT",
			CurrentBar: simbroker.Bar{Time: now},
		})
		require.Error(t, err)
	})
}

func TestRealisticFillModel(t *testing.T) {
	m := simbroker.RealisticFillModel{}
	t0 := time.Unix(1700000000, 0).UTC()
	t1 := t0.Add(time.Minute)
	cur := simbroker.Bar{Time: t0, Close: 100}
	next := &simbroker.Bar{Time: t1, Open: 101, High: 102, Low: 100.5, Close: 101.5}

	t.Run("MKT BUY fills at next-bar open plus slip", func(t *testing.T) {
		res, err := m.FillPrice(simbroker.FillContext{
			Symbol: "AAPL", Side: "BUY", Qty: 1, OrderType: "MKT",
			CurrentBar: cur, NextBar: next, SlippageBPS: 10,
		})
		require.NoError(t, err)
		require.True(t, res.Filled)
		assert.InEpsilon(t, 101+101*10.0/10000.0, res.Price, 1e-12)
		assert.Equal(t, t1, res.At)
	})

	t.Run("LMT BUY fills when next bar prints through", func(t *testing.T) {
		// limit 101 — next bar low 100.5 triggers
		res, err := m.FillPrice(simbroker.FillContext{
			Symbol: "AAPL", Side: "BUY", Qty: 1, OrderType: "LMT",
			LimitPrice: 101, CurrentBar: cur, NextBar: next, SlippageBPS: 10,
		})
		require.NoError(t, err)
		require.True(t, res.Filled)
		assert.Equal(t, 101.0, res.Price)
	})

	t.Run("LMT BUY not filled when next bar does not trade through", func(t *testing.T) {
		// limit 100 — next bar low 100.5 does not trigger, open 101 is not favorable
		res, err := m.FillPrice(simbroker.FillContext{
			Symbol: "AAPL", Side: "BUY", Qty: 1, OrderType: "LMT",
			LimitPrice: 100, CurrentBar: cur, NextBar: next, SlippageBPS: 10,
		})
		require.NoError(t, err)
		assert.False(t, res.Filled)
		assert.NotEmpty(t, res.Reason)
	})

	t.Run("LMT SELL fills when next bar prints through on high", func(t *testing.T) {
		res, err := m.FillPrice(simbroker.FillContext{
			Symbol: "AAPL", Side: "SELL", Qty: 1, OrderType: "LMT",
			LimitPrice: 101.8, CurrentBar: cur, NextBar: next, SlippageBPS: 10,
		})
		require.NoError(t, err)
		require.True(t, res.Filled)
		assert.Equal(t, 101.8, res.Price)
	})

	t.Run("NextBar nil fallback uses current close", func(t *testing.T) {
		res, err := m.FillPrice(simbroker.FillContext{
			Symbol: "AAPL", Side: "BUY", Qty: 1, OrderType: "MKT",
			CurrentBar: cur, NextBar: nil, SlippageBPS: 10,
		})
		require.NoError(t, err)
		require.True(t, res.Filled)
		assert.InEpsilon(t, 100+100*10.0/10000.0, res.Price, 1e-12)
	})
}

func TestPessimisticFillModel(t *testing.T) {
	m := simbroker.PessimisticFillModel{SlippageMultiplier: 2.0}
	t0 := time.Unix(1700000000, 0).UTC()
	t1 := t0.Add(time.Minute)
	cur := simbroker.Bar{Time: t0, Close: 100}
	next := &simbroker.Bar{Time: t1, Open: 101, High: 102, Low: 100.5, Close: 101.5}

	t.Run("MKT BUY doubles slippage over realistic", func(t *testing.T) {
		res, err := m.FillPrice(simbroker.FillContext{
			Symbol: "AAPL", Side: "BUY", Qty: 1, OrderType: "MKT",
			CurrentBar: cur, NextBar: next, SlippageBPS: 10,
		})
		require.NoError(t, err)
		require.True(t, res.Filled)
		// 2x slippage on next-bar open
		assert.InEpsilon(t, 101+101*10.0/10000.0*2.0, res.Price, 1e-12)
	})

	t.Run("default multiplier is 2.0 when zero", func(t *testing.T) {
		m0 := simbroker.PessimisticFillModel{}
		res, err := m0.FillPrice(simbroker.FillContext{
			Symbol: "AAPL", Side: "BUY", Qty: 1, OrderType: "MKT",
			CurrentBar: cur, NextBar: next, SlippageBPS: 10,
		})
		require.NoError(t, err)
		assert.InEpsilon(t, 101+101*10.0/10000.0*2.0, res.Price, 1e-12)
	})

	t.Run("LMT delegates to realistic", func(t *testing.T) {
		res, err := m.FillPrice(simbroker.FillContext{
			Symbol: "AAPL", Side: "BUY", Qty: 1, OrderType: "LMT",
			LimitPrice: 101, CurrentBar: cur, NextBar: next, SlippageBPS: 10,
		})
		require.NoError(t, err)
		require.True(t, res.Filled)
		assert.Equal(t, 101.0, res.Price)
	})
}

func TestFillModelByName(t *testing.T) {
	t.Run("optimistic", func(t *testing.T) {
		fm, err := simbroker.FillModelByName("optimistic", 0)
		require.NoError(t, err)
		assert.Equal(t, "optimistic", fm.Name())
	})
	t.Run("realistic", func(t *testing.T) {
		fm, err := simbroker.FillModelByName("realistic", 0)
		require.NoError(t, err)
		assert.Equal(t, "realistic", fm.Name())
	})
	t.Run("pessimistic uses multiplier", func(t *testing.T) {
		fm, err := simbroker.FillModelByName("pessimistic", 3.0)
		require.NoError(t, err)
		assert.Equal(t, "pessimistic", fm.Name())
		pm, ok := fm.(simbroker.PessimisticFillModel)
		require.True(t, ok)
		assert.Equal(t, 3.0, pm.SlippageMultiplier)
	})
	t.Run("empty defaults to optimistic", func(t *testing.T) {
		fm, err := simbroker.FillModelByName("", 0)
		require.NoError(t, err)
		assert.Equal(t, "optimistic", fm.Name())
	})
	t.Run("unknown returns error", func(t *testing.T) {
		_, err := simbroker.FillModelByName("mystery", 0)
		require.Error(t, err)
	})
}
