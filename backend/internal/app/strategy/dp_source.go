package strategy

import (
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// DPSource is the read-side abstraction the strategy runner uses to consume
// dark-pool 5m bars during evaluation. The contract is per-(symbol, time)
// lookup at the bucket-aligned 5m boundary; callers always pass the
// truncated bucket start.
//
// Two concrete impls live in this package:
//
//   - staticDPSource wraps a pre-loaded map[DPLookupKey]DarkPoolBar built
//     by the backtest runner from omo-data's persisted darkpool_bars rows.
//     Zero-allocation read after construction.
//
//   - noopDPSource is the default — installed by NewRunner so r.dpSource
//     is never nil. Returns (zero, false) and HasData()=false. The runner
//     uses HasData to skip whole DP overlay blocks.
//
// Phase 4 of the parity plan adds a third impl in
// backend/internal/app/livedarkpool that aggregates trades on the live
// event bus. It satisfies this interface so the runner integration is a
// no-op rewire — no per-call branching on backtest vs live.
type DPSource interface {
	Lookup(sym string, t time.Time) (domain.DarkPoolBar, bool)
	HasData() bool
}

// staticDPSource serves DP bars from a pre-loaded map. Used by the backtest
// runner via SetDarkPoolLookup; the field is unexported because the runner
// owns the lifecycle.
type staticDPSource struct {
	lookup map[DPLookupKey]domain.DarkPoolBar
}

// Lookup returns the DP bar for the given symbol/time bucket. Returns
// (zero, false) when the key is unknown — same shape callers expected from
// the raw map indexing it replaced.
func (s staticDPSource) Lookup(sym string, t time.Time) (domain.DarkPoolBar, bool) {
	dp, ok := s.lookup[DPLookupKey{Symbol: sym, Time: t}]
	return dp, ok
}

// HasData reports whether the underlying map has any entries. The runner
// uses this to skip DP overlay blocks entirely when no DP data is available
// (live mode pre-Phase-4, or a backtest run with DP disabled).
func (s staticDPSource) HasData() bool { return len(s.lookup) > 0 }

// noopDPSource is the zero-DP source the runner installs by default. All
// methods return false so the DP overlay blocks short-circuit without
// allocating per-bar work.
type noopDPSource struct{}

func (noopDPSource) Lookup(_ string, _ time.Time) (domain.DarkPoolBar, bool) {
	return domain.DarkPoolBar{}, false
}

func (noopDPSource) HasData() bool { return false }
