package compliance

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubBuys struct {
	byWindow []BuyRecord
}

func (s *stubBuys) BuysInWindow(_ context.Context, _ string, _ time.Time, _ time.Time) ([]BuyRecord, error) {
	return s.byWindow, nil
}

type captureSink struct {
	rows []WashSaleRow
}

func (c *captureSink) RecordWashSale(_ context.Context, row WashSaleRow) error {
	c.rows = append(c.rows, row)
	return nil
}

func TestWashSale_NoLossIgnored(t *testing.T) {
	sink := &captureSink{}
	j := NewWashSaleJournal(&stubBuys{}, sink, zerolog.Nop())
	n, err := j.OnRealizedLoss(context.Background(), LossEvent{Symbol: "AAPL", Amount: 0})
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Empty(t, sink.rows)
}

func TestWashSale_LossTriggersScan_Match(t *testing.T) {
	loss := LossEvent{
		TradeID:    "loss-1",
		Symbol:     "AAPL",
		RealizedAt: time.Date(2026, 4, 15, 15, 0, 0, 0, time.UTC),
		Amount:     200,
	}
	buys := &stubBuys{byWindow: []BuyRecord{
		{ID: "buy-1", Symbol: "AAPL", At: loss.RealizedAt.Add(-5 * 24 * time.Hour), Amount: 1000},
	}}
	sink := &captureSink{}
	j := NewWashSaleJournal(buys, sink, zerolog.Nop())

	n, err := j.OnRealizedLoss(context.Background(), loss)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	require.Len(t, sink.rows, 1)
	assert.Equal(t, "loss-1", sink.rows[0].LossTradeID)
	assert.Equal(t, "buy-1", sink.rows[0].TriggeringBuyID)
}

func TestWashSale_NoMatchInWindow(t *testing.T) {
	loss := LossEvent{
		TradeID:    "loss-1",
		Symbol:     "AAPL",
		RealizedAt: time.Date(2026, 4, 15, 15, 0, 0, 0, time.UTC),
		Amount:     200,
	}
	// Repo returns empty slice — no matching buys.
	sink := &captureSink{}
	j := NewWashSaleJournal(&stubBuys{}, sink, zerolog.Nop())
	n, err := j.OnRealizedLoss(context.Background(), loss)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Empty(t, sink.rows)
}

func TestWashSale_WindowBoundary30DaysExact(t *testing.T) {
	loss := LossEvent{
		TradeID:    "loss-1",
		Symbol:     "AAPL",
		RealizedAt: time.Date(2026, 4, 15, 15, 0, 0, 0, time.UTC),
		Amount:     200,
	}
	// Buy at exactly -30d should match (inclusive boundary).
	onEdge := loss.RealizedAt.Add(-WashSaleWindow)
	// Buy at -31d should NOT match — outside the window.
	outside := loss.RealizedAt.Add(-WashSaleWindow - 24*time.Hour)

	buys := &stubBuys{byWindow: []BuyRecord{
		{ID: "buy-edge", Symbol: "AAPL", At: onEdge},
		{ID: "buy-outside", Symbol: "AAPL", At: outside},
	}}
	sink := &captureSink{}
	j := NewWashSaleJournal(buys, sink, zerolog.Nop())

	n, err := j.OnRealizedLoss(context.Background(), loss)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	require.Len(t, sink.rows, 1)
	assert.Equal(t, "buy-edge", sink.rows[0].TriggeringBuyID)
}

func TestWashSale_ExcludesLossTradeItself(t *testing.T) {
	loss := LossEvent{
		TradeID:    "loss-1",
		Symbol:     "AAPL",
		RealizedAt: time.Date(2026, 4, 15, 15, 0, 0, 0, time.UTC),
		Amount:     200,
	}
	buys := &stubBuys{byWindow: []BuyRecord{
		{ID: "loss-1", Symbol: "AAPL", At: loss.RealizedAt.Add(-1 * time.Hour)},
	}}
	sink := &captureSink{}
	j := NewWashSaleJournal(buys, sink, zerolog.Nop())
	n, err := j.OnRealizedLoss(context.Background(), loss)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}
