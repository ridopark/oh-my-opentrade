package execution_test

import (
	"context"
	"errors"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlippageGuard_RejectsExcessiveSlippage(t *testing.T) {
	// Arrange
	quoteProvider := &mockQuoteProvider{
		Bid: 49000.0,
		Ask: 50100.0, // spread is big, ask is high
	}
	guard := execution.NewSlippageGuard(quoteProvider)

	intent := createTestOrderIntent(t)
	intent.Direction = domain.DirectionLong
	intent.LimitPrice = 50000.0
	intent.MaxSlippageBPS = 10 // 10 BPS = 0.1% = 50. Max Ask allowed = 50050

	// Act
	err := guard.Check(context.Background(), intent)

	// Assert
	require.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "slippage")
	}
}

func TestSlippageGuard_AcceptsWithinSlippage(t *testing.T) {
	// Arrange
	quoteProvider := &mockQuoteProvider{
		Bid: 49900.0,
		Ask: 50020.0, // 20 difference from 50000 < 50
	}
	guard := execution.NewSlippageGuard(quoteProvider)

	intent := createTestOrderIntent(t)
	intent.Direction = domain.DirectionLong
	intent.LimitPrice = 50000.0
	intent.MaxSlippageBPS = 10 // Max Ask allowed = 50050

	// Act
	err := guard.Check(context.Background(), intent)

	// Assert
	assert.NoError(t, err)
}

func TestSlippageGuard_AcceptsExactBoundary(t *testing.T) {
	// Arrange
	quoteProvider := &mockQuoteProvider{
		Bid: 49900.0,
		Ask: 50050.0, // exactly 10 BPS from 50000
	}
	guard := execution.NewSlippageGuard(quoteProvider)

	intent := createTestOrderIntent(t)
	intent.Direction = domain.DirectionLong
	intent.LimitPrice = 50000.0
	intent.MaxSlippageBPS = 10

	// Act
	err := guard.Check(context.Background(), intent)

	// Assert
	assert.NoError(t, err)
}

func TestSlippageGuard_RejectsZeroBidAsk(t *testing.T) {
	// Arrange
	quoteProvider := &mockQuoteProvider{
		Bid: 0.0,
		Ask: 0.0,
	}
	guard := execution.NewSlippageGuard(quoteProvider)

	intent := createTestOrderIntent(t)

	// Act
	err := guard.Check(context.Background(), intent)

	// Assert
	require.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "zero")
	}
}

func TestSlippageGuard_HandlesLongAndShortCorrectly(t *testing.T) {
	// Arrange: Limit=50000, maxSlippage=10 BPS (50 points)
	intentLong := createTestOrderIntent(t)
	intentLong.Direction = domain.DirectionLong
	intentLong.LimitPrice = 50000.0
	intentLong.MaxSlippageBPS = 10

	intentShort := createTestOrderIntent(t)
	intentShort.Direction = domain.DirectionShort
	intentShort.LimitPrice = 50000.0
	intentShort.MaxSlippageBPS = 10

	// Quote where Long would fail (ask too high) but Short would pass (bid is fine)
	// For short, we look at Bid. Max slippage for short means Bid shouldn't be too low.
	// If Limit = 50000, 10 BPS = 50. Bid must be >= 49950.
	quoteProvider := &mockQuoteProvider{
		Bid: 49960.0, // >= 49950 (Short passes)
		Ask: 50060.0, // > 50050 (Long fails)
	}
	guard := execution.NewSlippageGuard(quoteProvider)

	// Act & Assert
	errLong := guard.Check(context.Background(), intentLong)
	assert.Error(t, errLong, "Expected Long to fail due to high ask")

	errShort := guard.Check(context.Background(), intentShort)
	assert.NoError(t, errShort, "Expected Short to pass due to acceptable bid")
}

func TestSlippageGuard_QuoteProviderError(t *testing.T) {
	// Arrange
	quoteProvider := &mockQuoteProvider{
		Err: errors.New("provider offline"),
	}
	guard := execution.NewSlippageGuard(quoteProvider)
	intent := createTestOrderIntent(t)

	// Act
	err := guard.Check(context.Background(), intent)

	// Assert
	require.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "provider offline")
	}
}
