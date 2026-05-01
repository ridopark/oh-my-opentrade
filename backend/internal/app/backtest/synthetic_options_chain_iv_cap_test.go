package backtest

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenerateChain_ClampsIVWhenMaxIVSet locks the operator-controlled cap
// shipped to keep high-vol leveraged ETFs (SOXL, SOXS, SQQQ, etc.) tradeable
// in synthetic-chain backtests. Without the clamp the BSM delta surface
// flattens so far that the strategy's target_delta band catches no contracts.
func TestGenerateChain_ClampsIVWhenMaxIVSet(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.MaxIV = 0.80

	gen := NewSyntheticChainGenerator(cfg, constantSpot(126.74), constantIV(1.20))
	asOf := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	chain, err := gen.GenerateChain(context.Background(), "SOXL", asOf, domain.OptionRightCall, 5, 14)
	require.NoError(t, err)
	require.NotEmpty(t, chain)

	for _, snap := range chain {
		assert.LessOrEqual(t, snap.IV, 0.80+1e-9, "IV must be clamped to MaxIV; got %v", snap.IV)
	}

	// The whole point of the cap is to rescue the macd_only_v1 SOXL case
	// where 0/122 contracts landed in [0.50, 0.65] delta. With IV clamped
	// to 0.80 the BSM delta surface is steep enough that the band catches
	// a non-trivial slice of the chain.
	bandHits := 0
	for _, snap := range chain {
		if snap.Delta >= 0.50 && snap.Delta <= 0.65 {
			bandHits++
		}
	}
	assert.GreaterOrEqual(t, bandHits, 3,
		"with MaxIV=0.80 the [0.50, 0.65] delta band must catch >=3 contracts (saw %d)", bandHits)
}

// TestGenerateChain_DefaultMaxIVPassesThroughUnchanged proves the default
// path (MaxIV=0) is byte-identical to today's behavior so existing backtest
// baselines stay reproducible.
func TestGenerateChain_DefaultMaxIVPassesThroughUnchanged(t *testing.T) {
	cfg := defaultTestConfig() // MaxIV defaults to zero

	gen := NewSyntheticChainGenerator(cfg, constantSpot(126.74), constantIV(1.20))
	asOf := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	chain, err := gen.GenerateChain(context.Background(), "SOXL", asOf, domain.OptionRightCall, 5, 14)
	require.NoError(t, err)
	require.NotEmpty(t, chain)

	for _, snap := range chain {
		assert.InDelta(t, 1.20, snap.IV, 1e-9, "default path must propagate provider IV unchanged; got %v", snap.IV)
	}
}
