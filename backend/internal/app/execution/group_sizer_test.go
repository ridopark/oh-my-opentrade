package execution_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeGroupSizerIntent(sym string, qty float64) domain.OrderIntent {
	return domain.OrderIntent{
		ID:             uuid.New(),
		Symbol:         domain.Symbol(sym),
		Quantity:       qty,
		LegGroupID:     "grp-1",
		IdempotencyKey: uuid.NewString(),
	}
}

func TestGroupSizer_Validate(t *testing.T) {
	t.Run("under limit passes", func(t *testing.T) {
		gs := execution.NewGroupSizer(100_000)
		intents := []domain.OrderIntent{
			makeGroupSizerIntent("BTCUSD", 1),
			makeGroupSizerIntent("ETHUSD", 10),
		}
		prices := map[domain.Symbol]float64{
			"BTCUSD": 30_000,
			"ETHUSD": 2_000,
		}
		err := gs.Validate(intents, prices)
		require.NoError(t, err)
	})

	t.Run("over limit fails", func(t *testing.T) {
		gs := execution.NewGroupSizer(50_000)
		intents := []domain.OrderIntent{
			makeGroupSizerIntent("BTCUSD", 1),
			makeGroupSizerIntent("ETHUSD", 10),
		}
		prices := map[domain.Symbol]float64{
			"BTCUSD": 30_000,
			"ETHUSD": 2_500,
		}
		// 30000 + 25000 = 55000 > 50000
		err := gs.Validate(intents, prices)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds cap")
	})

	t.Run("exactly at limit passes", func(t *testing.T) {
		gs := execution.NewGroupSizer(50_000)
		intents := []domain.OrderIntent{
			makeGroupSizerIntent("BTCUSD", 1),
		}
		prices := map[domain.Symbol]float64{
			"BTCUSD": 50_000,
		}
		err := gs.Validate(intents, prices)
		require.NoError(t, err)
	})

	t.Run("zero cap disables validation", func(t *testing.T) {
		gs := execution.NewGroupSizer(0)
		intents := []domain.OrderIntent{
			makeGroupSizerIntent("BTCUSD", 100),
		}
		prices := map[domain.Symbol]float64{
			"BTCUSD": 100_000,
		}
		err := gs.Validate(intents, prices)
		require.NoError(t, err)
	})

	t.Run("missing price returns error", func(t *testing.T) {
		gs := execution.NewGroupSizer(100_000)
		intents := []domain.OrderIntent{
			makeGroupSizerIntent("BTCUSD", 1),
		}
		prices := map[domain.Symbol]float64{} // no BTCUSD
		err := gs.Validate(intents, prices)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no price")
	})

	t.Run("empty intents passes", func(t *testing.T) {
		gs := execution.NewGroupSizer(100_000)
		err := gs.Validate(nil, nil)
		require.NoError(t, err)
	})
}
