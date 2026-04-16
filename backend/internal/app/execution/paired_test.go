package execution_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// venueBroker is a minimal BrokerPort mock for paired executor tests.
type venueBroker struct {
	submitFunc func(ctx context.Context, intent domain.OrderIntent) (string, error)
	cancelFunc func(ctx context.Context, orderID string) error
	canceled   []string
}

func (v *venueBroker) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	if v.submitFunc != nil {
		return v.submitFunc(ctx, intent)
	}
	return "oid-" + string(intent.Symbol), nil
}

func (v *venueBroker) CancelOrder(ctx context.Context, orderID string) error {
	v.canceled = append(v.canceled, orderID)
	if v.cancelFunc != nil {
		return v.cancelFunc(ctx, orderID)
	}
	return nil
}

func (v *venueBroker) CancelOpenOrders(context.Context, domain.Symbol, string) (int, error) {
	return 0, nil
}
func (v *venueBroker) GetOrderStatus(context.Context, string) (string, error) { return "filled", nil }
func (v *venueBroker) GetPositions(context.Context, string, domain.EnvMode) ([]domain.Trade, error) {
	return nil, nil
}
func (v *venueBroker) GetPosition(context.Context, domain.Symbol) (float64, error) { return 0, nil }
func (v *venueBroker) ClosePosition(context.Context, domain.Symbol) (string, error) {
	return "", nil
}
func (v *venueBroker) GetOrderDetails(context.Context, string) (ports.OrderDetails, error) {
	return ports.OrderDetails{}, nil
}
func (v *venueBroker) CancelAllOpenOrders(context.Context) (int, error) { return 0, nil }
func (v *venueBroker) GetOpenOrders(context.Context) ([]ports.OpenOrder, error) { return nil, nil }

func makePairedIntent(sym string, venue domain.Venue, groupID string) domain.OrderIntent {
	return domain.OrderIntent{
		ID:             uuid.New(),
		TenantID:       "t1",
		EnvMode:        domain.EnvModePaper,
		Symbol:         domain.Symbol(sym),
		Direction:      domain.DirectionLong,
		LimitPrice:     100,
		StopLoss:       95,
		Quantity:       1,
		Strategy:       "basis",
		Confidence:     0.8,
		IdempotencyKey: uuid.NewString(),
		AssetClass:     domain.AssetClassCrypto,
		Venue:          venue,
		LegGroupID:     groupID,
	}
}

func TestPairedExecutor_SubmitGroup(t *testing.T) {
	log := zerolog.Nop()

	t.Run("successful two-leg submission", func(t *testing.T) {
		coinbase := &venueBroker{}
		hyper := &venueBroker{}
		brokers := map[domain.Venue]ports.BrokerPort{
			domain.VenueCoinbase:     coinbase,
			domain.VenueHyperliquid: hyper,
		}
		pe := execution.NewPairedExecutor(brokers, log, nil)

		intents := []domain.OrderIntent{
			makePairedIntent("BTCUSD", domain.VenueCoinbase, "grp-1"),
			makePairedIntent("BTCUSD-PERP", domain.VenueHyperliquid, "grp-1"),
		}

		results, err := pe.SubmitGroup(context.Background(), intents)
		require.NoError(t, err)
		require.Len(t, results, 2)
		for _, r := range results {
			assert.True(t, r.Submitted)
			assert.NotEmpty(t, r.OrderID)
			assert.NoError(t, r.Error)
			assert.False(t, r.RolledBack)
		}
	})

	t.Run("rollback on second leg failure", func(t *testing.T) {
		coinbase := &venueBroker{}
		hyper := &venueBroker{
			submitFunc: func(_ context.Context, _ domain.OrderIntent) (string, error) {
				return "", errors.New("perp margin insufficient")
			},
		}
		brokers := map[domain.Venue]ports.BrokerPort{
			domain.VenueCoinbase:     coinbase,
			domain.VenueHyperliquid: hyper,
		}
		pe := execution.NewPairedExecutor(brokers, log, nil)

		intents := []domain.OrderIntent{
			makePairedIntent("BTCUSD", domain.VenueCoinbase, "grp-2"),
			makePairedIntent("BTCUSD-PERP", domain.VenueHyperliquid, "grp-2"),
		}

		results, err := pe.SubmitGroup(context.Background(), intents)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "leg[1]")

		// First leg was submitted, then rolled back.
		assert.True(t, results[0].Submitted)
		assert.True(t, results[0].RolledBack)
		assert.Len(t, coinbase.canceled, 1)

		// Second leg was not submitted.
		assert.False(t, results[1].Submitted)
		assert.Error(t, results[1].Error)
	})

	t.Run("no broker for venue", func(t *testing.T) {
		brokers := map[domain.Venue]ports.BrokerPort{
			domain.VenueCoinbase: &venueBroker{},
		}
		pe := execution.NewPairedExecutor(brokers, log, nil)

		intents := []domain.OrderIntent{
			makePairedIntent("BTCUSD", domain.VenueCoinbase, "grp-3"),
			makePairedIntent("BTCUSD-PERP", domain.VenueHyperliquid, "grp-3"),
		}

		results, err := pe.SubmitGroup(context.Background(), intents)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no broker registered")
		assert.True(t, results[0].Submitted)
		assert.True(t, results[0].RolledBack)
	})

	t.Run("empty group returns error", func(t *testing.T) {
		pe := execution.NewPairedExecutor(nil, log, nil)
		_, err := pe.SubmitGroup(context.Background(), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty")
	})

	t.Run("mismatched LegGroupID returns error", func(t *testing.T) {
		pe := execution.NewPairedExecutor(nil, log, nil)
		intents := []domain.OrderIntent{
			makePairedIntent("BTCUSD", domain.VenueCoinbase, "grp-a"),
			makePairedIntent("BTCUSD-PERP", domain.VenueHyperliquid, "grp-b"),
		}
		_, err := pe.SubmitGroup(context.Background(), intents)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not match")
	})

	t.Run("empty LegGroupID returns error", func(t *testing.T) {
		pe := execution.NewPairedExecutor(nil, log, nil)
		intents := []domain.OrderIntent{
			makePairedIntent("BTCUSD", domain.VenueCoinbase, ""),
		}
		_, err := pe.SubmitGroup(context.Background(), intents)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty LegGroupID")
	})

	t.Run("venue resolved from AssetClass when Venue is unset", func(t *testing.T) {
		coinbase := &venueBroker{}
		brokers := map[domain.Venue]ports.BrokerPort{
			domain.VenueCoinbase: coinbase,
		}
		pe := execution.NewPairedExecutor(brokers, log, nil)

		intent := makePairedIntent("BTCUSD", domain.VenueUnspecified, "grp-4")
		intent.AssetClass = domain.AssetClassCrypto // DefaultVenue -> coinbase

		results, err := pe.SubmitGroup(context.Background(), []domain.OrderIntent{intent})
		require.NoError(t, err)
		assert.Equal(t, domain.VenueCoinbase, results[0].Venue)
	})
}
