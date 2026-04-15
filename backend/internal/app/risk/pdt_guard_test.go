package risk

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fixedEquity float64

func (f fixedEquity) AccountEquity() float64 { return float64(f) }

type pdtStubAccount struct{ bp ports.BuyingPower }

func (s *pdtStubAccount) GetAccountBuyingPower(_ context.Context) (ports.BuyingPower, error) {
	return s.bp, nil
}

func primeThreeDayTrades(tr *PDTTracker, day time.Time) {
	for i, sym := range []string{"AAA", "BBB", "CCC"} {
		open := day.Add(time.Duration(i) * time.Minute)
		tr.RecordOpen("A1", sym, 10, open)
		tr.RecordClose(context.Background(), "A1", sym, 10, open.Add(time.Minute))
	}
}

func TestPDTGuard_NonPDTAccountBypassed(t *testing.T) {
	tr := NewPDTTracker(nil, nil, zerolog.Nop())
	acct := &pdtStubAccount{bp: ports.BuyingPower{PatternDayTrader: false}}
	g := NewPDTGuard(PDTEnforcementStrict, tr, acct, fixedEquity(10_000), "A1")

	day := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)
	primeThreeDayTrades(tr, day)
	tr.RecordOpen("A1", "ZZZ", 10, day)

	intent := domain.OrderIntent{Symbol: "ZZZ", Direction: domain.DirectionCloseLong}
	assert.NoError(t, g.CheckIntent(context.Background(), intent, day.Add(time.Hour)))
}

func TestPDTGuard_EquityAboveThresholdBypassed(t *testing.T) {
	tr := NewPDTTracker(nil, nil, zerolog.Nop())
	acct := &pdtStubAccount{bp: ports.BuyingPower{PatternDayTrader: true}}
	g := NewPDTGuard(PDTEnforcementStrict, tr, acct, fixedEquity(30_000), "A1")

	day := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)
	primeThreeDayTrades(tr, day)
	tr.RecordOpen("A1", "ZZZ", 10, day)

	intent := domain.OrderIntent{Symbol: "ZZZ", Direction: domain.DirectionCloseLong}
	assert.NoError(t, g.CheckIntent(context.Background(), intent, day.Add(time.Hour)))
}

func TestPDTGuard_FourthSameDayBlocked(t *testing.T) {
	tr := NewPDTTracker(nil, nil, zerolog.Nop())
	acct := &pdtStubAccount{bp: ports.BuyingPower{PatternDayTrader: true}}
	g := NewPDTGuard(PDTEnforcementStrict, tr, acct, fixedEquity(10_000), "A1")

	day := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)
	primeThreeDayTrades(tr, day)
	tr.RecordOpen("A1", "ZZZ", 10, day)

	intent := domain.OrderIntent{Symbol: "ZZZ", Direction: domain.DirectionCloseLong}
	err := g.CheckIntent(context.Background(), intent, day.Add(time.Hour))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pdt")
}

func TestPDTGuard_PriorDayOpenNotCounted(t *testing.T) {
	tr := NewPDTTracker(nil, nil, zerolog.Nop())
	acct := &pdtStubAccount{bp: ports.BuyingPower{PatternDayTrader: true}}
	g := NewPDTGuard(PDTEnforcementStrict, tr, acct, fixedEquity(10_000), "A1")

	d1 := time.Date(2026, 4, 14, 14, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	primeThreeDayTrades(tr, d2) // 3 day trades on d2
	tr.RecordOpen("A1", "ZZZ", 10, d1) // open from prior day

	intent := domain.OrderIntent{Symbol: "ZZZ", Direction: domain.DirectionCloseLong}
	// Exiting a position opened YESTERDAY is NOT a day trade, even
	// with 3 same-day trades already on the books.
	assert.NoError(t, g.CheckIntent(context.Background(), intent, d2.Add(time.Hour)))
}

func TestPDTGuard_EntryIntentBypassed(t *testing.T) {
	tr := NewPDTTracker(nil, nil, zerolog.Nop())
	acct := &pdtStubAccount{bp: ports.BuyingPower{PatternDayTrader: true}}
	g := NewPDTGuard(PDTEnforcementStrict, tr, acct, fixedEquity(10_000), "A1")

	day := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)
	primeThreeDayTrades(tr, day)

	intent := domain.OrderIntent{Symbol: "ZZZ", Direction: domain.DirectionLong}
	assert.NoError(t, g.CheckIntent(context.Background(), intent, day))
}

func TestPDTGuard_OffModeDisabled(t *testing.T) {
	tr := NewPDTTracker(nil, nil, zerolog.Nop())
	acct := &pdtStubAccount{bp: ports.BuyingPower{PatternDayTrader: true}}
	g := NewPDTGuard(PDTEnforcementOff, tr, acct, fixedEquity(10_000), "A1")

	day := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)
	primeThreeDayTrades(tr, day)
	tr.RecordOpen("A1", "ZZZ", 10, day)

	intent := domain.OrderIntent{Symbol: "ZZZ", Direction: domain.DirectionCloseLong}
	assert.NoError(t, g.CheckIntent(context.Background(), intent, day.Add(time.Hour)))
}
