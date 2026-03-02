package execution_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRiskEngine_RejectsWhenRiskExceeds2Percent(t *testing.T) {
	// Arrange
	engine := execution.NewRiskEngine(0.02)
	intent := createValidOrderIntent(t, domain.DirectionLong, 100.0, 90.0, 10.0) // Risk = (100-90)*10 = 100
	accountEquity := 4000.0                                                      // 2% of 4000 = 80, so 100 risk > 80

	// Act
	err := engine.Validate(intent, accountEquity)

	// Assert
	require.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "exceeds maximum risk")
	}
}

func TestRiskEngine_AcceptsValidRisk(t *testing.T) {
	// Arrange
	engine := execution.NewRiskEngine(0.02)
	intent := createValidOrderIntent(t, domain.DirectionLong, 100.0, 90.0, 10.0) // Risk = 100
	accountEquity := 10000.0                                                     // 2% of 10000 = 200, 100 <= 200

	// Act
	err := engine.Validate(intent, accountEquity)

	// Assert
	assert.NoError(t, err)
}

func TestRiskEngine_RejectsZeroStopLoss(t *testing.T) {
	// Arrange
	engine := execution.NewRiskEngine(0.02)
	intent := createValidOrderIntent(t, domain.DirectionLong, 100.0, 90.0, 10.0)
	intent.StopLoss = 0 // bypass constructor validation to test risk engine specifically
	accountEquity := 10000.0

	// Act
	err := engine.Validate(intent, accountEquity)

	// Assert
	require.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "stop loss")
	}
}

func TestRiskEngine_RejectsNegativeQuantity(t *testing.T) {
	// Arrange
	engine := execution.NewRiskEngine(0.02)
	intent := createValidOrderIntent(t, domain.DirectionLong, 100.0, 90.0, -10.0)
	accountEquity := 10000.0

	// Act
	err := engine.Validate(intent, accountEquity)

	// Assert
	require.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "quantity")
	}
}

func TestRiskEngine_AcceptsExactly2PercentRisk(t *testing.T) {
	// Arrange
	engine := execution.NewRiskEngine(0.02)
	intent := createValidOrderIntent(t, domain.DirectionLong, 100.0, 90.0, 10.0) // Risk = 100
	accountEquity := 5000.0                                                      // 2% of 5000 = 100

	// Act
	err := engine.Validate(intent, accountEquity)

	// Assert
	assert.NoError(t, err)
}

func TestRiskEngine_RejectsWithoutLimitOrder(t *testing.T) {
	// Arrange
	engine := execution.NewRiskEngine(0.02)
	intent := createValidOrderIntent(t, domain.DirectionLong, 100.0, 90.0, 10.0)
	intent.LimitPrice = 0 // test that non-limit orders are rejected
	accountEquity := 10000.0

	// Act
	err := engine.Validate(intent, accountEquity)

	// Assert
	require.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "limit price")
	}
}

func TestRiskEngine_CalculatesRiskForLongCorrectly(t *testing.T) {
	// Arrange
	engine := execution.NewRiskEngine(0.02)
	// Long: (100 - 80) * 10 = 200 risk
	intent := createValidOrderIntent(t, domain.DirectionLong, 100.0, 80.0, 10.0)

	// Act & Assert
	// Equity = 9999, 2% = 199.98 -> Should reject
	err1 := engine.Validate(intent, 9999.0)
	assert.Error(t, err1, "Expected error because 200 > 199.98")

	// Equity = 10000, 2% = 200 -> Should accept
	err2 := engine.Validate(intent, 10000.0)
	assert.NoError(t, err2, "Expected no error because 200 <= 200")
}

func TestRiskEngine_CalculatesRiskForShortCorrectly(t *testing.T) {
	// Arrange
	engine := execution.NewRiskEngine(0.02)
	// Short: (120 - 100) * 10 = 200 risk
	intent := createValidOrderIntent(t, domain.DirectionShort, 100.0, 120.0, 10.0)

	// Act & Assert
	// Equity = 9999, 2% = 199.98 -> Should reject
	err1 := engine.Validate(intent, 9999.0)
	assert.Error(t, err1, "Expected error because 200 > 199.98")

	// Equity = 10000, 2% = 200 -> Should accept
	err2 := engine.Validate(intent, 10000.0)
	assert.NoError(t, err2, "Expected no error because 200 <= 200")
}

// createValidOrderIntent is a helper for risk tests
func createValidOrderIntent(t *testing.T, dir domain.Direction, limitPrice, stopLoss, qty float64) domain.OrderIntent {
	t.Helper()
	intent, err := domain.NewOrderIntent(
		uuid.New(),
		"tenant-1",
		domain.EnvModePaper,
		"BTCUSD",
		dir,
		limitPrice,
		stopLoss,
		10, // 10 BPS slippage
		qty,
		"strategy-1",
		"rationale",
		0.8,
		"idem-key",
	)
	if err != nil && qty > 0 && limitPrice > 0 && stopLoss > 0 {
		require.NoError(t, err)
	}

	// Force the struct values if constructor rejected them (we need this for tests testing bad states)
	intent.Direction = dir
	intent.LimitPrice = limitPrice
	intent.StopLoss = stopLoss
	intent.Quantity = qty

	return intent
}
