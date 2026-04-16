package domain_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildComboPosition() domain.MonitoredPosition {
	expiry := time.Date(2026, 5, 15, 16, 0, 0, 0, time.UTC)
	legs := []domain.ComboLeg{
		{Symbol: "AAPL", Right: domain.OptionRightCall, Strike: 150, Expiry: expiry, Ratio: 1, AssetType: domain.InstrumentTypeOption},
		{Symbol: "AAPL", Right: domain.OptionRightCall, Strike: 155, Expiry: expiry, Ratio: -1, AssetType: domain.InstrumentTypeOption},
	}
	// Long 150C at $3.00, short 155C at $1.25 -> net debit $1.75.
	return domain.MonitoredPosition{
		Symbol:        "AAPL",
		EntryPrice:    1.75,
		EntryTime:     time.Now(),
		Quantity:      2,
		Side:          "BUY",
		Legs:          legs,
		ComboType:     domain.ComboTypeVerticalCallDebit,
		LegFillPrices: []float64{3.00, 1.25},
	}
}

func TestMonitoredPosition_IsCombo(t *testing.T) {
	pos := buildComboPosition()
	assert.True(t, pos.IsCombo())

	equityPos := domain.MonitoredPosition{Symbol: "AAPL", EntryPrice: 150, Quantity: 10}
	assert.False(t, equityPos.IsCombo())
}

func TestMonitoredPosition_ComboPnL(t *testing.T) {
	pos := buildComboPosition()

	t.Run("zero-move returns zero", func(t *testing.T) {
		got := pos.ComboPnL([]float64{3.00, 1.25})
		assert.InDelta(t, 0.0, got, 1e-9)
	})

	t.Run("both legs widen by $0.50 each -> combo gains $0.50 * 2 qty * 100", func(t *testing.T) {
		// Long leg +0.50 -> +100*2*0.50 = +100.
		// Short leg -0.50 (price dropped) -> short PnL = +100*2*0.50 = +100.
		// Wait: short leg price went DOWN 0.50 means we gain since we sold higher.
		// Our formula: sign=-1 for ratio<0, delta = newPrice - fill.
		// If short leg price rose 0.50 we lose; here we test both cases.
		// Long +0.50, short -0.50 -> total delta price-wise = +1.00 spread widening.
		got := pos.ComboPnL([]float64{3.50, 0.75})
		// Long: (3.50-3.00)*1*2*100 = 100
		// Short: -(0.75-1.25)*1*2*100 = -(-0.50)*200 = +100
		assert.InDelta(t, 200.0, got, 1e-9)
	})

	t.Run("mismatched leg count returns zero", func(t *testing.T) {
		assert.Zero(t, pos.ComboPnL([]float64{3.00}))
	})
}

func TestMonitoredPosition_ClosingLegs(t *testing.T) {
	pos := buildComboPosition()
	closing := pos.ClosingLegs()
	require.Len(t, closing, 2)

	// Ratios inverted.
	assert.Equal(t, -1, closing[0].Ratio)
	assert.Equal(t, 1, closing[1].Ratio)

	// Contract identity preserved.
	assert.Equal(t, pos.Legs[0].Strike, closing[0].Strike)
	assert.Equal(t, pos.Legs[0].Expiry, closing[0].Expiry)
	assert.Equal(t, pos.Legs[0].Right, closing[0].Right)
}

func TestMonitoredPosition_ClosingLegs_NonCombo(t *testing.T) {
	pos := domain.MonitoredPosition{Symbol: "AAPL", EntryPrice: 100, Quantity: 10}
	assert.Nil(t, pos.ClosingLegs())
}
