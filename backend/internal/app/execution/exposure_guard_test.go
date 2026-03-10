package execution_test

import (
	"context"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeExposureIntent(sym string, qty, price float64) domain.OrderIntent {
	return domain.OrderIntent{
		Symbol:     domain.Symbol(sym),
		Direction:  domain.DirectionLong,
		Quantity:   qty,
		LimitPrice: price,
		TenantID:   "t1",
		EnvMode:    domain.EnvModePaper,
		AssetClass: domain.AssetClassEquity,
	}
}

func TestExposureGuard_AllowsWithinCap(t *testing.T) {
	broker := &mockBroker{
		GetPositionsFunc: func(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
			return []domain.Trade{
				{Symbol: "AAPL", Quantity: 50, Price: 200, Side: "long"},
			}, nil
		},
	}
	guard := execution.NewExposureGuard(broker, 100_000, zerolog.Nop())
	intent := makeExposureIntent("MSFT", 50, 400)

	err := guard.Check(context.Background(), intent)
	assert.NoError(t, err)
}

func TestExposureGuard_RejectsTechOverCap(t *testing.T) {
	broker := &mockBroker{
		GetPositionsFunc: func(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
			return []domain.Trade{
				{Symbol: "AAPL", Quantity: 100, Price: 200, Side: "long"},
				{Symbol: "MSFT", Quantity: 30, Price: 400, Side: "long"},
			}, nil
		},
	}
	guard := execution.NewExposureGuard(broker, 100_000, zerolog.Nop())
	intent := makeExposureIntent("GOOGL", 20, 170)

	err := guard.Check(context.Background(), intent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exposure_guard")
	assert.Contains(t, err.Error(), "tech_equity")
}

func TestExposureGuard_CryptoCluster(t *testing.T) {
	broker := &mockBroker{
		GetPositionsFunc: func(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
			return []domain.Trade{
				{Symbol: "BTC/USD", Quantity: 0.5, Price: 60000, Side: "long"},
			}, nil
		},
	}
	guard := execution.NewExposureGuard(broker, 100_000, zerolog.Nop())
	intent := makeExposureIntent("ETH/USD", 3, 3000)
	intent.AssetClass = domain.AssetClassCrypto
	intent.Symbol = "ETH/USD"

	err := guard.Check(context.Background(), intent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "crypto")
}

func TestExposureGuard_DefensiveCluster(t *testing.T) {
	broker := &mockBroker{
		GetPositionsFunc: func(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
			return []domain.Trade{
				{Symbol: "SPY", Quantity: 50, Price: 500, Side: "long"},
			}, nil
		},
	}
	guard := execution.NewExposureGuard(broker, 100_000, zerolog.Nop())
	intent := makeExposureIntent("SPY", 20, 500)

	err := guard.Check(context.Background(), intent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "defensive")
}

func TestExposureGuard_SkipsExitOrders(t *testing.T) {
	broker := &mockBroker{}
	guard := execution.NewExposureGuard(broker, 100_000, zerolog.Nop())
	intent := makeExposureIntent("AAPL", 100, 200)
	intent.Direction = domain.DirectionCloseLong

	err := guard.Check(context.Background(), intent)
	assert.NoError(t, err)
}

func TestExposureGuard_AllowsOnBrokerError(t *testing.T) {
	broker := &mockBroker{
		GetPositionsFunc: func(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
			return nil, assert.AnError
		},
	}
	guard := execution.NewExposureGuard(broker, 100_000, zerolog.Nop())
	intent := makeExposureIntent("AAPL", 100, 200)

	err := guard.Check(context.Background(), intent)
	assert.NoError(t, err)
}
