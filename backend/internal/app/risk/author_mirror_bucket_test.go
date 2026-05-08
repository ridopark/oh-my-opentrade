package risk

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tttBucketCfg() AuthorMirrorConfig {
	return AuthorMirrorConfig{
		Members:    []string{"copytrade_v1", "tradingthetrend_v1"},
		CapMult:    1.3,
		FireWindow: 5 * time.Minute,
		MaxFires:   2,
		MaxRiskPct: 0.02,
	}
}

func optionIntent(strategy, occ, underlying string, maxLossUSD float64) domain.OrderIntent {
	inst, _ := domain.NewInstrument(domain.InstrumentTypeOption, occ, underlying)
	return domain.OrderIntent{
		Symbol:     domain.Symbol(occ),
		Strategy:   strategy,
		Direction:  domain.DirectionLong,
		LimitPrice: 1.0,
		Quantity:   1,
		MaxLossUSD: maxLossUSD,
		Instrument: &inst,
	}
}

func equityIntent(strategy, sym string, qty, price float64) domain.OrderIntent {
	return domain.OrderIntent{
		Symbol:     domain.Symbol(sym),
		Strategy:   strategy,
		Direction:  domain.DirectionLong,
		LimitPrice: price,
		Quantity:   qty,
	}
}

func optionPosition(strategy, occ string, qty, entryPrice float64) domain.MonitoredPosition {
	return domain.MonitoredPosition{
		Symbol:         domain.Symbol(occ),
		Strategy:       strategy,
		EntryPrice:     entryPrice,
		Quantity:       qty,
		InstrumentType: domain.InstrumentTypeOption,
	}
}

func TestAuthorMirrorBucket_Disabled(t *testing.T) {
	ctx := context.Background()

	t.Run("CapMult zero → pass", func(t *testing.T) {
		cfg := tttBucketCfg()
		cfg.CapMult = 0
		b := NewAuthorMirrorBucket(cfg, &stubPositions{}, &stubEquity{equity: 100000}, nil, zerolog.Nop())
		err := b.Check(ctx, optionIntent("copytrade_v1", "AAPL251031C00190000", "AAPL", 500))
		assert.NoError(t, err)
	})

	t.Run("empty members → pass", func(t *testing.T) {
		cfg := tttBucketCfg()
		cfg.Members = nil
		b := NewAuthorMirrorBucket(cfg, &stubPositions{}, &stubEquity{equity: 100000}, nil, zerolog.Nop())
		err := b.Check(ctx, optionIntent("copytrade_v1", "AAPL251031C00190000", "AAPL", 500))
		assert.NoError(t, err)
	})

	t.Run("non-member strategy → pass even at cap", func(t *testing.T) {
		b := NewAuthorMirrorBucket(tttBucketCfg(), &stubPositions{}, &stubEquity{equity: 100000}, nil, zerolog.Nop())
		err := b.Check(ctx, optionIntent("avwap_v1", "AAPL251031C00190000", "AAPL", 999999))
		assert.NoError(t, err)
	})
}

func TestAuthorMirrorBucket_NotionalCap_AggregatesByUnderlying(t *testing.T) {
	ctx := context.Background()
	// equity 100k, max_risk 0.02, cap_mult 1.3 → cap = 100000 * 0.02 * 1.3 = $2,600
	positions := []domain.MonitoredPosition{
		// Two existing positions on RKLB from the peer strategy: same underlying, different OCCs.
		optionPosition("copytrade_v1", "RKLB251031C00090000", 5, 200), // notional 1000
		optionPosition("copytrade_v1", "RKLB251031C00092000", 5, 240), // notional 1200
	}
	b := NewAuthorMirrorBucket(tttBucketCfg(), &stubPositions{positions: positions}, &stubEquity{equity: 100000}, nil, zerolog.Nop())

	// Current bucket notional on RKLB = 2200. cap = 2600. Intent of 500 fits (2700 > 2600 — should reject).
	err := b.Check(ctx, optionIntent("tradingthetrend_v1", "RKLB251031C00091000", "RKLB", 500))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RKLB")
	assert.Contains(t, err.Error(), "exceed cap")

	// Smaller intent fits (2200 + 300 = 2500 < 2600).
	err = b.Check(ctx, optionIntent("tradingthetrend_v1", "RKLB251031C00091000", "RKLB", 300))
	assert.NoError(t, err)
}

func TestAuthorMirrorBucket_NotionalCap_DifferentUnderlyingsDoNotAggregate(t *testing.T) {
	ctx := context.Background()
	// Position is on AAPL; intent on RKLB.
	positions := []domain.MonitoredPosition{
		optionPosition("copytrade_v1", "AAPL251031C00190000", 100, 999), // huge AAPL notional
	}
	b := NewAuthorMirrorBucket(tttBucketCfg(), &stubPositions{positions: positions}, &stubEquity{equity: 100000}, nil, zerolog.Nop())

	err := b.Check(ctx, optionIntent("tradingthetrend_v1", "RKLB251031C00091000", "RKLB", 500))
	assert.NoError(t, err, "AAPL position must not affect RKLB bucket")
}

func TestAuthorMirrorBucket_FireRate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 8, 14, 30, 0, 0, time.UTC)
	currentTime := now
	clock := func() time.Time { return currentTime }

	b := NewAuthorMirrorBucket(tttBucketCfg(), &stubPositions{}, &stubEquity{equity: 100000}, clock, zerolog.Nop())

	// First two fires within window pass.
	require.NoError(t, b.Check(ctx, optionIntent("copytrade_v1", "AAPL251031C00190000", "AAPL", 500)))
	currentTime = currentTime.Add(1 * time.Minute)
	require.NoError(t, b.Check(ctx, optionIntent("tradingthetrend_v1", "MSFT251031C00425000", "MSFT", 500)))
	currentTime = currentTime.Add(1 * time.Minute)

	// 3rd fire within 5min window must reject.
	err := b.Check(ctx, optionIntent("tradingthetrend_v1", "NVDA251031C00220000", "NVDA", 500))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fire-rate")

	// After window expires the rolling counter prunes and admits the next fire.
	currentTime = currentTime.Add(6 * time.Minute)
	require.NoError(t, b.Check(ctx, optionIntent("tradingthetrend_v1", "NVDA251031C00220000", "NVDA", 500)))
}

func TestAuthorMirrorBucket_ExitsBypassGate(t *testing.T) {
	ctx := context.Background()
	b := NewAuthorMirrorBucket(tttBucketCfg(), &stubPositions{}, &stubEquity{equity: 100000}, nil, zerolog.Nop())

	exit := optionIntent("copytrade_v1", "AAPL251031C00190000", "AAPL", 500)
	exit.Direction = domain.DirectionCloseLong

	// Even after 2 fires (cap reached), exits must still pass.
	require.NoError(t, b.Check(ctx, optionIntent("copytrade_v1", "AAPL251031C00190000", "AAPL", 500)))
	require.NoError(t, b.Check(ctx, optionIntent("copytrade_v1", "AAPL251031C00190000", "AAPL", 500)))
	assert.NoError(t, b.Check(ctx, exit), "exits must bypass the bucket gate")
}

func TestAuthorMirrorBucket_EquityIntentUsesIntentSymbol(t *testing.T) {
	ctx := context.Background()
	b := NewAuthorMirrorBucket(tttBucketCfg(), &stubPositions{}, &stubEquity{equity: 100000}, nil, zerolog.Nop())

	// Equity intent (no Instrument) bucketed by intent.Symbol.
	err := b.Check(ctx, equityIntent("copytrade_v1", "RKLB", 10, 95))
	assert.NoError(t, err)
}

func TestAuthorMirrorBucket_FailsWhenUnderlyingMissingOnOption(t *testing.T) {
	ctx := context.Background()
	b := NewAuthorMirrorBucket(tttBucketCfg(), &stubPositions{}, &stubEquity{equity: 100000}, nil, zerolog.Nop())

	// Forge an option intent with a blank underlying — bucket can't aggregate, must reject.
	intent := optionIntent("copytrade_v1", "AAPL251031C00190000", "AAPL", 500)
	inst, _ := domain.NewInstrument(domain.InstrumentTypeOption, "AAPL251031C00190000", "AAPL")
	inst.UnderlyingSymbol = ""
	intent.Instrument = &inst

	err := b.Check(ctx, intent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot derive underlying")
}
