package simbroker_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/adapters/simbroker"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeGroupIntent(sym string, dir domain.Direction, qty float64, groupID string) domain.OrderIntent {
	return domain.OrderIntent{
		ID:             uuid.New(),
		TenantID:       "t1",
		EnvMode:        domain.EnvModePaper,
		Symbol:         domain.Symbol(sym),
		Direction:      dir,
		LimitPrice:     100,
		StopLoss:       95,
		Quantity:       qty,
		Strategy:       "basis-carry",
		Confidence:     0.8,
		IdempotencyKey: uuid.NewString(),
		AssetClass:     domain.AssetClassCrypto,
		LegGroupID:     groupID,
	}
}

func TestBroker_SubmitGroup(t *testing.T) {
	now := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)

	t.Run("all legs filled", func(t *testing.T) {
		b := simbroker.New(simbroker.Config{
			SlippageBPS:     5,
			InitialEquity:   1_000_000,
			DisableFillChan: true,
		}, zerolog.Nop())

		b.UpdatePrice("BTCUSD", 60_000, now)
		b.UpdatePrice("BTCUSD-PERP", 60_100, now)

		intents := []domain.OrderIntent{
			makeGroupIntent("BTCUSD", domain.DirectionLong, 0.5, "g1"),
			makeGroupIntent("BTCUSD-PERP", domain.DirectionShort, 0.5, "g1"),
		}

		ids, err := b.SubmitGroup(context.Background(), intents)
		require.NoError(t, err)
		require.Len(t, ids, 2)
		assert.NotEmpty(t, ids[0])
		assert.NotEmpty(t, ids[1])
		assert.NotEqual(t, ids[0], ids[1])
	})

	t.Run("rejected when one leg has no price", func(t *testing.T) {
		b := simbroker.New(simbroker.Config{
			SlippageBPS:     5,
			InitialEquity:   1_000_000,
			DisableFillChan: true,
		}, zerolog.Nop())

		b.UpdatePrice("BTCUSD", 60_000, now)
		// BTCUSD-PERP has no price

		intents := []domain.OrderIntent{
			makeGroupIntent("BTCUSD", domain.DirectionLong, 0.5, "g2"),
			makeGroupIntent("BTCUSD-PERP", domain.DirectionShort, 0.5, "g2"),
		}

		ids, err := b.SubmitGroup(context.Background(), intents)
		require.Error(t, err)
		assert.Nil(t, ids)
		assert.Contains(t, err.Error(), "no price")
	})

	t.Run("empty group returns error", func(t *testing.T) {
		b := simbroker.New(simbroker.Config{DisableFillChan: true}, zerolog.Nop())
		ids, err := b.SubmitGroup(context.Background(), nil)
		require.Error(t, err)
		assert.Nil(t, ids)
		assert.Contains(t, err.Error(), "empty")
	})
}
