package risk

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildAAPLCallSpread(t *testing.T) []domain.ComboLeg {
	t.Helper()
	expiry := time.Date(2026, 5, 15, 16, 0, 0, 0, time.UTC)
	return []domain.ComboLeg{
		{Symbol: "AAPL", Right: domain.OptionRightCall, Strike: 150, Expiry: expiry, Ratio: 1, AssetType: domain.InstrumentTypeOption},
		{Symbol: "AAPL", Right: domain.OptionRightCall, Strike: 155, Expiry: expiry, Ratio: -1, AssetType: domain.InstrumentTypeOption},
	}
}

func TestPortfolioHeat_ComboDebitRiskEqualsNetDebit(t *testing.T) {
	ctx := context.Background()
	legs := buildAAPLCallSpread(t)

	// 1.25 net debit, qty=2, mult=100 -> risk = $250.
	intent, err := domain.NewComboOrderIntent(
		uuid.New(), "tenant", domain.EnvModePaper, "AAPL",
		domain.ComboTypeVerticalCallDebit, legs,
		1.25, 2, "strat", "combo entry", 0.8, "key-debit", 250,
	)
	require.NoError(t, err)

	// With $10k equity and 5% heat cap, $250 = 2.5% projected, passes.
	p := NewPortfolioHeat(0.05, &stubPositions{}, &stubEquity{equity: 10_000}, zerolog.Nop())
	assert.NoError(t, p.Check(ctx, intent))

	// Tighten cap to 2% and the combo should be rejected because projected 2.5%.
	pTight := NewPortfolioHeat(0.02, &stubPositions{}, &stubEquity{equity: 10_000}, zerolog.Nop())
	err = pTight.Check(ctx, intent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "portfolio_heat")
}

func TestPortfolioHeat_ComboCreditRiskEqualsWidthMinusCredit(t *testing.T) {
	ctx := context.Background()
	legs := buildAAPLCallSpread(t)

	// Credit spread: width $5, credit $1 -> risk = ($5 - $1) * 1 qty * 100 mult = $400.
	intent, err := domain.NewComboOrderIntent(
		uuid.New(), "tenant", domain.EnvModePaper, "AAPL",
		domain.ComboTypeVerticalCallCredit, legs,
		1.0, 1, "strat", "combo entry", 0.8, "key-credit", 500,
	)
	require.NoError(t, err)

	// $20k equity, 5% cap -> $400 = 2%, passes.
	p := NewPortfolioHeat(0.05, &stubPositions{}, &stubEquity{equity: 20_000}, zerolog.Nop())
	assert.NoError(t, p.Check(ctx, intent))

	// 1% cap -> $200 limit; $400 projected rejects.
	pTight := NewPortfolioHeat(0.01, &stubPositions{}, &stubEquity{equity: 20_000}, zerolog.Nop())
	err = pTight.Check(ctx, intent)
	require.Error(t, err)
}
