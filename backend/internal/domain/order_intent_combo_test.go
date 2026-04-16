package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newVerticalCallSpreadLegs(t *testing.T) []domain.ComboLeg {
	t.Helper()
	expiry := time.Date(2026, 5, 15, 16, 0, 0, 0, time.UTC)
	return []domain.ComboLeg{
		{Symbol: "AAPL", Right: domain.OptionRightCall, Strike: 150, Expiry: expiry, Ratio: 1, AssetType: domain.InstrumentTypeOption},
		{Symbol: "AAPL", Right: domain.OptionRightCall, Strike: 155, Expiry: expiry, Ratio: -1, AssetType: domain.InstrumentTypeOption},
	}
}

func TestNewComboOrderIntent(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(legs *[]domain.ComboLeg, netPx *float64, maxLoss *float64)
		wantErr string
	}{
		{
			name:   "valid vertical call debit",
			mutate: func(_ *[]domain.ComboLeg, _ *float64, _ *float64) {},
		},
		{
			name: "single leg rejected",
			mutate: func(legs *[]domain.ComboLeg, _ *float64, _ *float64) {
				*legs = (*legs)[:1]
			},
			wantErr: "exactly 2 legs",
		},
		{
			name: "ratios do not sum to zero",
			mutate: func(legs *[]domain.ComboLeg, _ *float64, _ *float64) {
				(*legs)[1].Ratio = -2
			},
			wantErr: "sum to zero",
		},
		{
			name: "zero ratio leg rejected",
			mutate: func(legs *[]domain.ComboLeg, _ *float64, _ *float64) {
				(*legs)[0].Ratio = 0
			},
			wantErr: "non-zero",
		},
		{
			name: "mismatched underlying",
			mutate: func(legs *[]domain.ComboLeg, _ *float64, _ *float64) {
				(*legs)[1].Symbol = "MSFT"
			},
			wantErr: "does not match combo underlying",
		},
		{
			name: "mismatched expiry",
			mutate: func(legs *[]domain.ComboLeg, _ *float64, _ *float64) {
				(*legs)[1].Expiry = (*legs)[1].Expiry.Add(7 * 24 * time.Hour)
			},
			wantErr: "does not match leg 0 expiry",
		},
		{
			name: "max loss missing",
			mutate: func(_ *[]domain.ComboLeg, _ *float64, maxLoss *float64) {
				*maxLoss = 0
			},
			wantErr: "MaxLossUSD must be > 0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legs := newVerticalCallSpreadLegs(t)
			netPx := 1.25
			maxLoss := 125.0
			tc.mutate(&legs, &netPx, &maxLoss)

			intent, err := domain.NewComboOrderIntent(
				uuid.New(), "tenant-a", domain.EnvModePaper, "AAPL",
				domain.ComboTypeVerticalCallDebit, legs,
				netPx, 1, "strategy-x", "test rationale", 0.8,
				"idempotency-key-1", maxLoss,
			)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.True(t, intent.IsCombo())
			assert.Len(t, intent.Legs, 2)
			assert.Equal(t, domain.ComboTypeVerticalCallDebit, intent.ComboType)
			assert.Equal(t, domain.Symbol("AAPL"), intent.Symbol)
			assert.NotNil(t, intent.Instrument)
			assert.Equal(t, domain.InstrumentTypeOption, intent.Instrument.Type)
		})
	}
}

func TestComboType_IsDebit(t *testing.T) {
	assert.True(t, domain.ComboTypeVerticalCallDebit.IsDebit())
	assert.True(t, domain.ComboTypeVerticalPutDebit.IsDebit())
	assert.False(t, domain.ComboTypeVerticalCallCredit.IsDebit())
	assert.False(t, domain.ComboTypeVerticalPutCredit.IsDebit())
}

func TestComboRisk(t *testing.T) {
	legs := newVerticalCallSpreadLegs(t)
	t.Run("debit spread risk equals net debit times mult", func(t *testing.T) {
		intent, err := domain.NewComboOrderIntent(
			uuid.New(), "t", domain.EnvModePaper, "AAPL",
			domain.ComboTypeVerticalCallDebit, legs,
			1.25, 2, "s", "r", 0.8, "k1", 250,
		)
		require.NoError(t, err)
		// risk = 1.25 * 2 * 100 = 250
		assert.InDelta(t, 250.0, domain.ComboRisk(intent), 1e-9)
	})

	t.Run("credit spread risk equals width minus credit", func(t *testing.T) {
		intent, err := domain.NewComboOrderIntent(
			uuid.New(), "t", domain.EnvModePaper, "AAPL",
			domain.ComboTypeVerticalCallCredit, legs,
			1.0, 1, "s", "r", 0.8, "k2", 400,
		)
		require.NoError(t, err)
		// width = 5, credit = 1, qty = 1, mult = 100 -> 400
		assert.InDelta(t, 400.0, domain.ComboRisk(intent), 1e-9)
	})

	t.Run("non-combo returns zero", func(t *testing.T) {
		assert.Zero(t, domain.ComboRisk(domain.OrderIntent{}))
	})
}

func TestOCCSymbol(t *testing.T) {
	leg := newVerticalCallSpreadLegs(t)[0]
	got := string(leg.OCCSymbol())
	assert.Equal(t, "AAPL260515C00150000", got)
}
