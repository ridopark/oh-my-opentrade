package indicator_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/indicator/indicatortest"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

// TestService_ResetSessionIndicators_ClearsSessionState pins the contract
// that ResetSessionIndicators clears per-symbol session-VWAP state so the
// next bar after reset accumulates VWAP from a clean baseline rather than
// carrying yesterday's running totals forward.
func TestService_ResetSessionIndicators_ClearsSessionState(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}
	yesterdayOpen := time.Date(2026, 4, 14, 9, 30, 0, 0, loc)
	todayOpen := time.Date(2026, 4, 15, 9, 30, 0, 0, loc)

	sym := domain.Symbol("AAPL")

	// Reference: only today's bars seed VWAP.
	ref := indicator.NewService("ref")
	todayBars := indicatortest.MakeBars(sym, 100.0, todayOpen, 30)
	ref.WarmUp(todayBars)
	refSnap, ok := ref.LastSnapshot(sym, domain.Timeframe("1m"))
	if !ok {
		t.Fatalf("ref: LastSnapshot missing after today bars")
	}

	// Boot path: yesterday seeds calc, then reset, then today's bars feed in.
	boot := indicator.NewService("boot")
	yesterdayBars := indicatortest.MakeBars(sym, 200.0, yesterdayOpen, 390)
	boot.WarmUp(yesterdayBars)
	preReset, ok := boot.LastSnapshot(sym, domain.Timeframe("1m"))
	if !ok {
		t.Fatalf("boot: LastSnapshot missing after yesterday bars")
	}
	if preReset.VWAP <= 0 {
		t.Fatalf("boot: VWAP must be non-zero after seeding yesterday bars, got %v", preReset.VWAP)
	}

	boot.ResetSessionIndicators(sym)
	boot.WarmUp(todayBars)

	bootSnap, ok := boot.LastSnapshot(sym, domain.Timeframe("1m"))
	if !ok {
		t.Fatalf("boot: LastSnapshot missing after reset+today bars")
	}

	// Session VWAP should match a today-only run, proving yesterday's
	// accumulated PV/V did not leak through the reset boundary.
	if got, want := bootSnap.VWAP, refSnap.VWAP; got != want {
		t.Fatalf("session VWAP leak across reset: boot=%v ref=%v", got, want)
	}
}
