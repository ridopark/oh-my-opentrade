package gate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
)

// EarningsBlackoutChecker looks up the next scheduled earnings entry for
// a symbol. Implementations wrap ports.EarningsCalendarPort; the gate
// asks the checker rather than the port directly so tests can stub a
// tiny in-memory fake without touching the repo contract.
type EarningsBlackoutChecker interface {
	NextEarnings(ctx context.Context, symbol string) (*ports.EarningsEntry, error)
}

// Enforcement modes for the earnings blackout gate, per strategy.
const (
	EarningsBlackoutOff        = "off"
	EarningsBlackoutStrict     = "strict"
	EarningsBlackoutPermissive = "permissive"
)

// earningsBlackoutGate refuses new entries placed close to a symbol's
// scheduled earnings release. Exit intents always pass through.
//
// The enforcement window is expressed in trading days and applied
// symmetrically around the announcement date:
//
//   - "strict"     — reject entries anywhere inside (-1, +1) trading
//     days of the release. Practically, this means "no new entries the
//     day before, the day of, or the day after earnings".
//   - "permissive" — reject only on the announcement day itself. Prior
//     and trailing days are allowed.
//   - "off" (or unset) — gate passes through. Also the default for any
//     strategy not listed in the config map.
type earningsBlackoutGate struct {
	checker EarningsBlackoutChecker
	// modes maps strategy name to enforcement mode. A missing key or
	// an empty value is treated as "off".
	modes map[string]string
	nowFn func() time.Time
}

func newEarningsBlackoutGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
	return &earningsBlackoutGate{
		checker: deps.EarningsBlackoutGuard,
		modes:   deps.EarningsBlackoutModes,
		nowFn:   time.Now,
	}, nil
}

func (g *earningsBlackoutGate) Name() string { return "earnings_blackout_gate" }

func (g *earningsBlackoutGate) Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
	if g.checker == nil {
		return nil
	}
	if gctx.Intent.Direction.IsExit() {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(g.modes[gctx.Intent.Strategy]))
	if mode == "" || mode == EarningsBlackoutOff {
		return nil
	}
	if mode != EarningsBlackoutStrict && mode != EarningsBlackoutPermissive {
		// Unknown value — fail-open rather than surprise the operator.
		return nil
	}

	symbol := string(gctx.Intent.Symbol)
	if symbol == "" {
		return nil
	}
	entry, err := g.checker.NextEarnings(ctx, symbol)
	if err != nil {
		// Do not block on lookup failures — earnings data is a
		// best-effort signal. Upstream logs the error.
		return nil
	}
	if entry == nil {
		return nil
	}

	now := g.nowFn()
	if inBlackout(now, entry.EarningsDate, mode) {
		return &GateResult{
			GateName: "earnings_blackout_gate",
			Reason: fmt.Sprintf("earnings_blackout: %s reports %s %s (mode=%s)",
				symbol,
				entry.EarningsDate.Format("2006-01-02"),
				hourLabel(entry.Hour),
				mode,
			),
		}
	}
	return nil
}

// inBlackout reports whether `now` falls inside the blackout window for
// an earnings announcement on `earningsDate`. We use calendar-day deltas
// (rounded to the earnings date's day) as a pragmatic proxy for trading
// days — the upstream refresh only stores DATE precision anyway. Weekend
// drift is minor compared to the 1-day symmetric window.
func inBlackout(now, earningsDate time.Time, mode string) bool {
	nowDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	eDay := time.Date(earningsDate.Year(), earningsDate.Month(), earningsDate.Day(), 0, 0, 0, 0, now.Location())
	diffDays := int(nowDay.Sub(eDay).Hours() / 24)
	switch mode {
	case EarningsBlackoutStrict:
		return diffDays >= -1 && diffDays <= 1
	case EarningsBlackoutPermissive:
		return diffDays == 0
	}
	return false
}

func hourLabel(h string) string {
	if h == "" {
		return "tbd"
	}
	return strings.ToLower(h)
}

// EarningsCalendarAdapter wraps an EarningsCalendarPort so it satisfies
// EarningsBlackoutChecker. Kept alongside the gate so bootstrap sites do
// not have to hand-roll a shim.
type EarningsCalendarAdapter struct {
	Port ports.EarningsCalendarPort
}

// NextEarnings delegates to the underlying port.
func (a EarningsCalendarAdapter) NextEarnings(ctx context.Context, symbol string) (*ports.EarningsEntry, error) {
	if a.Port == nil {
		return nil, nil
	}
	return a.Port.GetNextEarnings(ctx, symbol)
}
