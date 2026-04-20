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

func TestExposureGuard_RejectsOnBrokerError(t *testing.T) {
	broker := &mockBroker{
		GetPositionsFunc: func(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
			return nil, assert.AnError
		},
	}
	guard := execution.NewExposureGuard(broker, 100_000, zerolog.Nop())
	intent := makeExposureIntent("AAPL", 100, 200)

	err := guard.Check(context.Background(), intent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "positions fetch failed")
}

// Gross semantics: an existing short counts against the cluster budget
// just like an existing long. +$20k long AAPL plus -$10k short NVDA is
// $30k of tech-cluster exposure, not $10k net.
func TestExposureGuard_ShortPositionCountsAsGross(t *testing.T) {
	broker := &mockBroker{
		GetPositionsFunc: func(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
			return []domain.Trade{
				{Symbol: "AAPL", Quantity: 100, Price: 200, Side: "long"},
				{Symbol: "NVDA", Quantity: 100, Price: 100, Side: "short"},
			}, nil
		},
	}
	guard := execution.NewExposureGuard(broker, 100_000, zerolog.Nop())
	// $20k long + $10k short = $30k gross. Tech cap is $35k. A $10k entry
	// would push gross to $40k and must reject.
	intent := makeExposureIntent("GOOGL", 100, 100)

	err := guard.Check(context.Background(), intent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tech_equity")
}

// Short entry is a new position that deploys gross capital too.
func TestExposureGuard_ShortEntryConsumesBudget(t *testing.T) {
	broker := &mockBroker{
		GetPositionsFunc: func(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
			return []domain.Trade{
				{Symbol: "AAPL", Quantity: 100, Price: 300, Side: "long"},
			}, nil
		},
	}
	guard := execution.NewExposureGuard(broker, 100_000, zerolog.Nop())
	// $30k long. Tech cap is $35k. A $10k short entry would be $40k gross
	// and must reject — shorts do not "offset" the long under gross.
	intent := makeExposureIntent("NVDA", 100, 100)
	intent.Direction = domain.DirectionShort

	err := guard.Check(context.Background(), intent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tech_equity")
}

// Short entry into empty cluster still respects cap.
func TestExposureGuard_ShortEntryIntoEmptyClusterCapped(t *testing.T) {
	broker := &mockBroker{
		GetPositionsFunc: func(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
			return []domain.Trade{}, nil
		},
	}
	guard := execution.NewExposureGuard(broker, 100_000, zerolog.Nop())
	// $40k short into an empty tech cluster exceeds the $35k cap.
	intent := makeExposureIntent("TSLA", 200, 200)
	intent.Direction = domain.DirectionShort

	err := guard.Check(context.Background(), intent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tech_equity")
}
