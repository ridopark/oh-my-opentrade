package risk

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// PDTTracker tracks intraday round-trip ("day trade") counts per account so
// the pdt_guard execution gate can enforce the FINRA PDT rule: accounts
// flagged PatternDayTrader with equity < $25k may not execute a 4th day
// trade in any rolling 5 business-day window.
//
// A "day trade" is the open+close of the same symbol in the same trading
// session. The tracker maintains two in-memory structures:
//
//   - openLots — FIFO queue of lots still open per (account, symbol).
//     Each lot records its opened_at timestamp so we can tell at close
//     time whether the round-trip was same-session (→ day trade) or
//     spans sessions (→ not a day trade).
//   - dayCounts — (account, trading_date) → count of completed same-day
//     round-trips.
//
// Fill observation is push-based via RecordFill. Callers (execution
// service / position monitor) invoke RecordFill at every fill event.
// Because a clean fill-event hook is not yet wired into the execution
// service (see Sprint 4.5 gating note in EQUITY-OPTIONS-GAP-PLAN), the
// tracker exposes an interface-compatible stub path: callers can
// instantiate PDTTracker, wire it into gate deps, and start pushing
// fills when the hook lands. Until then DayTradeCount returns 0 and
// the gate passes through.
type PDTTracker struct {
	mu         sync.Mutex
	clock      func() time.Time
	sink       DayTradeSink
	openLots   map[lotKey][]openLot
	dayCounts  map[countKey]int
	log        zerolog.Logger
}

// DayTradeSink persists completed round-trips. Typically backed by
// timescaledb.DayTradeRepo. May be nil (in-memory-only operation).
type DayTradeSink interface {
	RecordDayTrade(ctx context.Context, dt DayTrade) error
}

// DayTrade is a completed same-session round-trip.
type DayTrade struct {
	AccountID    string
	TradingDate  time.Time // date portion only; hour/min ignored
	Symbol       string
	QtyTraded    int
	OpenedAt     time.Time
	ClosedAt     time.Time
}

type lotKey struct {
	AccountID string
	Symbol    string
}

type countKey struct {
	AccountID   string
	TradingDate string // YYYY-MM-DD in account-local session tz
}

type openLot struct {
	Qty      int
	OpenedAt time.Time
}

// NewPDTTracker constructs an in-memory PDT tracker. If sink is non-nil,
// each completed same-day round-trip is persisted. clock defaults to
// time.Now when nil.
func NewPDTTracker(sink DayTradeSink, clock func() time.Time, log zerolog.Logger) *PDTTracker {
	if clock == nil {
		clock = time.Now
	}
	return &PDTTracker{
		clock:     clock,
		sink:      sink,
		openLots:  make(map[lotKey][]openLot),
		dayCounts: make(map[countKey]int),
		log:       log.With().Str("component", "pdt_tracker").Logger(),
	}
}

// RecordOpen registers a new opening fill for (account, symbol).
func (p *PDTTracker) RecordOpen(accountID, symbol string, qty int, at time.Time) {
	if qty <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	k := lotKey{AccountID: accountID, Symbol: symbol}
	p.openLots[k] = append(p.openLots[k], openLot{Qty: qty, OpenedAt: at})
}

// RecordClose registers a closing fill. Each lot closed whose OpenedAt
// is on the same trading date as closedAt counts as a day trade. Returns
// the number of day-trade matches produced by this close.
func (p *PDTTracker) RecordClose(ctx context.Context, accountID, symbol string, qty int, closedAt time.Time) int {
	if qty <= 0 {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	k := lotKey{AccountID: accountID, Symbol: symbol}
	lots := p.openLots[k]
	remaining := qty
	dayTrades := 0
	date := closedAt.Format("2006-01-02")

	for remaining > 0 && len(lots) > 0 {
		head := &lots[0]
		take := head.Qty
		if take > remaining {
			take = remaining
		}
		sameDay := head.OpenedAt.Format("2006-01-02") == date
		if sameDay {
			dayTrades++
			ck := countKey{AccountID: accountID, TradingDate: date}
			p.dayCounts[ck]++
			if p.sink != nil {
				dt := DayTrade{
					AccountID:   accountID,
					TradingDate: closedAt.Truncate(24 * time.Hour),
					Symbol:      symbol,
					QtyTraded:   take,
					OpenedAt:    head.OpenedAt,
					ClosedAt:    closedAt,
				}
				if err := p.sink.RecordDayTrade(ctx, dt); err != nil {
					p.log.Warn().Err(err).Str("symbol", symbol).Msg("pdt_tracker: persist day trade failed")
				}
			}
		}
		head.Qty -= take
		remaining -= take
		if head.Qty <= 0 {
			lots = lots[1:]
		}
	}
	if len(lots) == 0 {
		delete(p.openLots, k)
	} else {
		p.openLots[k] = lots
	}
	return dayTrades
}

// DayTradeCount returns the count of same-day round-trips observed for
// (account, date). Date uses YYYY-MM-DD against the tracker's clock tz.
func (p *PDTTracker) DayTradeCount(accountID string, date time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dayCounts[countKey{AccountID: accountID, TradingDate: date.Format("2006-01-02")}]
}

// HasSameDayOpen reports whether the account currently holds an open lot
// in the given symbol that was opened on `date`. The pdt_guard uses this
// to decide whether an exit intent would create a same-day round-trip.
func (p *PDTTracker) HasSameDayOpen(accountID, symbol string, date time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	k := lotKey{AccountID: accountID, Symbol: symbol}
	d := date.Format("2006-01-02")
	for _, lot := range p.openLots[k] {
		if lot.OpenedAt.Format("2006-01-02") == d {
			return true
		}
	}
	return false
}

// Reset clears in-memory state. Intended for test use only.
func (p *PDTTracker) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.openLots = make(map[lotKey][]openLot)
	p.dayCounts = make(map[countKey]int)
}

