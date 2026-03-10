package execution_test

import (
	"context"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpreadGuard_RejectsWideSpread(t *testing.T) {
	qp := &mockQuoteProvider{Bid: 100.0, Ask: 101.0}
	guard := execution.NewSpreadGuard(qp, zerolog.Nop())

	intent := createTestOrderIntent(t)
	intent.Meta = map[string]string{"max_spread_bps": "25"}

	err := guard.Check(context.Background(), intent)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "spread_guard")
	assert.Contains(t, err.Error(), "exceeds max")
}

func TestSpreadGuard_AcceptsTightSpread(t *testing.T) {
	qp := &mockQuoteProvider{Bid: 100.00, Ask: 100.10}
	guard := execution.NewSpreadGuard(qp, zerolog.Nop())

	intent := createTestOrderIntent(t)
	intent.Meta = map[string]string{"max_spread_bps": "25"}

	err := guard.Check(context.Background(), intent)
	assert.NoError(t, err)
}

func TestSpreadGuard_NoOpWhenNotConfigured(t *testing.T) {
	qp := &mockQuoteProvider{Bid: 100.0, Ask: 200.0}
	guard := execution.NewSpreadGuard(qp, zerolog.Nop())

	intent := createTestOrderIntent(t)
	intent.Meta = map[string]string{}

	err := guard.Check(context.Background(), intent)
	assert.NoError(t, err)
}

func TestSpreadGuard_NoOpWhenMetaNil(t *testing.T) {
	qp := &mockQuoteProvider{Bid: 100.0, Ask: 200.0}
	guard := execution.NewSpreadGuard(qp, zerolog.Nop())

	intent := createTestOrderIntent(t)
	intent.Meta = nil

	err := guard.Check(context.Background(), intent)
	assert.NoError(t, err)
}

func TestSpreadGuard_RejectsZeroBidAsk(t *testing.T) {
	qp := &mockQuoteProvider{Bid: 0.0, Ask: 0.0}
	guard := execution.NewSpreadGuard(qp, zerolog.Nop())

	intent := createTestOrderIntent(t)
	intent.Meta = map[string]string{"max_spread_bps": "25"}

	err := guard.Check(context.Background(), intent)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "zero bid/ask")
}

func TestSpreadGuard_AllowsOnQuoteError(t *testing.T) {
	qp := &mockQuoteProvider{Err: assert.AnError}
	guard := execution.NewSpreadGuard(qp, zerolog.Nop())

	intent := createTestOrderIntent(t)
	intent.Meta = map[string]string{"max_spread_bps": "25"}

	err := guard.Check(context.Background(), intent)
	assert.NoError(t, err)
}

func TestSpreadGuard_BoundaryExactlyAtMax(t *testing.T) {
	// mid=100.05, spread=0.10, spreadBPS = (0.10/100.05)*10000 ≈ 9.995 bps
	qp := &mockQuoteProvider{Bid: 100.00, Ask: 100.10}
	guard := execution.NewSpreadGuard(qp, zerolog.Nop())

	intent := createTestOrderIntent(t)
	intent.Meta = map[string]string{"max_spread_bps": "10"}

	err := guard.Check(context.Background(), intent)
	assert.NoError(t, err)
}
