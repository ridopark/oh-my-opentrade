package positionmonitor

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

func newServiceForTest(t *testing.T) *Service {
	t.Helper()
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())
	return NewService(&mockEventBus{}, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop())
}

// TestListOpenContractsByUnderlying_FiltersToMatchingOptionContracts verifies
// the lookup returns option positions whose OCC underlying matches and skips
// equity / crypto / mismatched-underlying positions.
func TestListOpenContractsByUnderlying_FiltersToMatchingOptionContracts(t *testing.T) {
	svc := newServiceForTest(t)

	now := time.Now()
	expiry := time.Date(2026, 5, 8, 16, 0, 0, 0, time.UTC)

	// Open option contract on MRVL.
	svc.processFill(fillMsg{
		Symbol:         domain.Symbol("MRVL260508C00162500"),
		Side:           "BUY",
		Price:          2.10,
		Quantity:       3,
		FilledAt:       now,
		Strategy:       "macd_only_v1",
		AssetClass:     domain.AssetClassEquity,
		InstrumentType: domain.InstrumentTypeOption,
		OptionExpiry:   expiry,
		OptionRight:    "CALL",
	})

	// Open option contract on AAPL (different underlying).
	svc.processFill(fillMsg{
		Symbol:         domain.Symbol("AAPL260515C00200000"),
		Side:           "BUY",
		Price:          5.50,
		Quantity:       1,
		FilledAt:       now,
		Strategy:       "avwap_v4",
		AssetClass:     domain.AssetClassEquity,
		InstrumentType: domain.InstrumentTypeOption,
		OptionExpiry:   time.Date(2026, 5, 15, 16, 0, 0, 0, time.UTC),
		OptionRight:    "CALL",
	})

	// Open equity position on MRVL - must NOT be returned.
	svc.processFill(fillMsg{
		Symbol:         domain.Symbol("MRVL"),
		Side:           "BUY",
		Price:          67.50,
		Quantity:       100,
		FilledAt:       now,
		Strategy:       "avwap_v4",
		AssetClass:     domain.AssetClassEquity,
		InstrumentType: domain.InstrumentTypeEquity,
	})

	got := svc.ListOpenContractsByUnderlying("MRVL")
	assert.Len(t, got, 1, "only the MRVL option contract should match")
	assert.Equal(t, domain.Symbol("MRVL260508C00162500"), got[0].Symbol)
	assert.Equal(t, domain.InstrumentTypeOption, got[0].InstrumentType)

	gotAAPL := svc.ListOpenContractsByUnderlying("AAPL")
	assert.Len(t, gotAAPL, 1)
	assert.Equal(t, domain.Symbol("AAPL260515C00200000"), gotAAPL[0].Symbol)

	gotMissing := svc.ListOpenContractsByUnderlying("NVDA")
	assert.Empty(t, gotMissing, "no positions for NVDA")

	gotEmpty := svc.ListOpenContractsByUnderlying("")
	assert.Empty(t, gotEmpty, "empty underlying must return nil, not panic or scan")
}

// TestListOpenContractsByUnderlying_MultipleContractsSameUnderlying verifies
// the plan-default close-all behavior: when multiple contracts share an
// underlying, every one is returned so the risk_sizer can fan out close
// intents.
func TestListOpenContractsByUnderlying_MultipleContractsSameUnderlying(t *testing.T) {
	svc := newServiceForTest(t)
	now := time.Now()

	for i, occ := range []string{"MRVL260508C00162500", "MRVL260515C00170000", "MRVL260522C00175000"} {
		svc.processFill(fillMsg{
			Symbol:         domain.Symbol(occ),
			Side:           "BUY",
			Price:          float64(2 + i),
			Quantity:       float64(i + 1),
			FilledAt:       now,
			Strategy:       "macd_only_v1",
			AssetClass:     domain.AssetClassEquity,
			InstrumentType: domain.InstrumentTypeOption,
			OptionExpiry:   time.Date(2026, 5, 8+(7*i), 16, 0, 0, 0, time.UTC),
			OptionRight:    "CALL",
		})
	}

	got := svc.ListOpenContractsByUnderlying("MRVL")
	assert.Len(t, got, 3, "every open contract under MRVL must be returned")

	syms := make(map[domain.Symbol]bool, len(got))
	for _, p := range got {
		syms[p.Symbol] = true
	}
	assert.True(t, syms["MRVL260508C00162500"])
	assert.True(t, syms["MRVL260515C00170000"])
	assert.True(t, syms["MRVL260522C00175000"])
}
