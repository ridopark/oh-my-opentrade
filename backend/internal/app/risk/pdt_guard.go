package risk

import (
	"context"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// PDTEnforcementMode mirrors TradingConfig.PDTEnforcement.
type PDTEnforcementMode string

const (
	PDTEnforcementStrict PDTEnforcementMode = "strict"
	PDTEnforcementOff    PDTEnforcementMode = "off"
)

// PDTEquityThreshold is the FINRA-defined floor under which PDT accounts
// are capped at 3 day trades per rolling 5 business days.
const PDTEquityThreshold = 25000.0

// PDTDayTradeCap is the per-window cap when below threshold.
const PDTDayTradeCap = 3

// PDTGuard translates a PDTTracker into a gate.PDTChecker. It inspects
// the account's PatternDayTrader flag and equity at decision time and
// only blocks when:
//
//  1. Mode is strict (explicit off disables the gate).
//  2. PatternDayTrader is true.
//  3. Account equity < $25,000.
//  4. Today's day-trade count is already >= 3.
//  5. The intent is an exit on a symbol where the tracker holds a
//     same-day open lot (i.e. closing it completes a same-day
//     round-trip and would be the 4th day trade).
type PDTGuard struct {
	mode      PDTEnforcementMode
	tracker   *PDTTracker
	account   ports.AccountPort
	equitySrc EquitySource
	accountID string
}

// NewPDTGuard constructs the guard. If mode is empty it defaults to "strict".
func NewPDTGuard(mode PDTEnforcementMode, tracker *PDTTracker, account ports.AccountPort, equity EquitySource, accountID string) *PDTGuard {
	if mode == "" {
		mode = PDTEnforcementStrict
	}
	return &PDTGuard{
		mode:      mode,
		tracker:   tracker,
		account:   account,
		equitySrc: equity,
		accountID: accountID,
	}
}

// CheckIntent is the gate-facing entry point: the gate package's pdtGate
// calls this with the intent and a clock-provided "now". Keeping the
// signature intent-based rather than gate-context-based keeps the risk
// package free of a gate import.
func (g *PDTGuard) CheckIntent(ctx context.Context, intent domain.OrderIntent, now time.Time) error {
	if g.mode == PDTEnforcementOff {
		return nil
	}
	if !intent.Direction.IsExit() {
		return nil
	}
	if g.account == nil {
		return nil
	}
	bp, err := g.account.GetAccountBuyingPower(ctx)
	if err != nil {
		return fmt.Errorf("pdt: fetch buying power: %w", err)
	}
	if !bp.PatternDayTrader {
		return nil
	}
	var equity float64
	if g.equitySrc != nil {
		equity = g.equitySrc.AccountEquity()
	}
	if equity >= PDTEquityThreshold {
		return nil
	}
	if g.tracker == nil {
		return nil
	}
	if !g.tracker.HasSameDayOpen(g.accountID, string(intent.Symbol), now) {
		return nil
	}
	count := g.tracker.DayTradeCount(g.accountID, now)
	if count >= PDTDayTradeCap {
		return fmt.Errorf(
			"pdt: 4th same-day round-trip blocked (count=%d, equity=%.2f < %.0f)",
			count, equity, PDTEquityThreshold,
		)
	}
	return nil
}
