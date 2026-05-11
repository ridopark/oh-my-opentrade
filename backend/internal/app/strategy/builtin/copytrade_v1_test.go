package builtin

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// copyCtx is a lightweight start.Context that captures every EmitDomainEvent
// call so tests can assert on the ChandelierTrailArm / CopytradeExitRequest
// payloads emitted by the strategy's STC path.
type copyCtx struct {
	mu      sync.Mutex
	now     time.Time
	emits   []any
	envMode start.EnvMode
}

func newCopyCtx() *copyCtx {
	return &copyCtx{now: time.Date(2026, 4, 23, 14, 0, 0, 0, time.UTC)}
}

func (c *copyCtx) Now() time.Time       { return c.now }
func (c *copyCtx) Logger() *slog.Logger { return slog.Default() }
func (c *copyCtx) EmitDomainEvent(evt any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.emits = append(c.emits, evt)
	return nil
}
func (c *copyCtx) ProgressEventsSuppressed() bool { return false }
func (c *copyCtx) EnvMode() start.EnvMode {
	if c.envMode != "" {
		return c.envMode
	}
	return start.EnvModePaper
}
func (c *copyCtx) IsBacktest() bool { return false }

func (c *copyCtx) exitRequests() []domain.CopytradeExitRequestPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []domain.CopytradeExitRequestPayload
	for _, e := range c.emits {
		if p, ok := e.(domain.CopytradeExitRequestPayload); ok {
			out = append(out, p)
		}
	}
	return out
}

func (c *copyCtx) armRequests() []domain.ChandelierTrailArmPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []domain.ChandelierTrailArmPayload
	for _, e := range c.emits {
		if p, ok := e.(domain.ChandelierTrailArmPayload); ok {
			out = append(out, p)
		}
	}
	return out
}

func copytradeDefaultParams() map[string]any {
	return map[string]any{
		"skip_avg":                 true,
		"trail_on_partial_enabled": true,
		"trail_giveback_pct":       0.15,
		"default_stc_fraction":     0.33,
		"max_positions":            5,
		"partial_fractions": []any{
			map[string]any{"keyword": "all out", "fraction": 1.0},
			map[string]any{"keyword": "stop hit", "fraction": 1.0},
			map[string]any{"keyword": "stopped", "fraction": 1.0},
			map[string]any{"keyword": "half out", "fraction": 0.5},
			map[string]any{"keyword": "taking more", "fraction": 0.33},
			map[string]any{"keyword": "partial", "fraction": 0.33},
			map[string]any{"keyword": "trim", "fraction": 0.25},
		},
	}
}

func copytradeSignal(action, ticker string, strike float64, right, tail string, price float64) start.CopytradeSignal {
	return start.CopytradeSignal{
		SignalID:  action + ":" + ticker,
		MessageID: "msg-1",
		Author:    "alice",
		PostedAt:  time.Date(2026, 4, 23, 14, 0, 0, 0, time.UTC),
		Action:    action,
		Ticker:    ticker,
		Expiry:    time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		Strike:    strike,
		Right:     right,
		Price:     price,
		Tail:      tail,
		RawLine:   action + " " + ticker,
	}
}

func initCopytrade(t *testing.T, params map[string]any) (*CopytradeStrategy, start.State) {
	t.Helper()
	s := NewCopytradeStrategy()
	st, err := s.Init(nil, "__copytrade__", params, nil)
	require.NoError(t, err)
	return s, st
}

// confirmBTOFills flips Pending=false on the single in-flight position created
// by a BTO OnEvent. Tests that chain BTO → STC use this to simulate the
// runner's handleFill → FillConfirmation dispatch.
func confirmBTOFills(t *testing.T, s *CopytradeStrategy, ctx start.Context, st start.State) start.State {
	t.Helper()
	cst := st.(*copytradeState)
	for _, pos := range cst.Positions {
		if !pos.Pending {
			continue
		}
		fc := start.FillConfirmation{Symbol: pos.ContractSymbol, Side: start.SideBuy, Quantity: 1, Price: pos.EntryPremium}
		next, _, err := s.OnEvent(ctx, "__copytrade__", fc, st)
		require.NoError(t, err)
		st = next
	}
	return st
}

func TestCopytrade_BTO_EmitsSignalWithForceTags(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()
	sig := copytradeSignal("BTO", "AAPL", 190, "C", "starter", 1.20)

	next, signals, err := s.OnEvent(ctx, "__copytrade__", sig, st)
	require.NoError(t, err)
	require.Len(t, signals, 1)

	out := signals[0]
	assert.Equal(t, start.SignalEntry, out.Type)
	assert.Equal(t, start.SideBuy, out.Side)
	assert.Equal(t, "AAPL", out.Symbol)
	assert.Equal(t, "2026-04-25", out.Tags["force_expiry"])
	assert.Equal(t, "190", out.Tags["force_strike"])
	assert.Equal(t, "CALL", out.Tags["force_right"])
	assert.Equal(t, "1.2", out.Tags["force_ref_premium"])
	assert.Equal(t, "1", out.Tags["generation"])
	assert.Equal(t, "alice", out.Tags["author"])
	assert.NotEmpty(t, out.Tags["contract_symbol"])

	cst := next.(*copytradeState)
	require.Len(t, cst.Positions, 1)
	for _, pos := range cst.Positions {
		assert.Equal(t, 1.0, pos.RemainingFrac)
		assert.False(t, pos.TrailArmed)
		assert.Equal(t, 1.20, pos.EntryPremium)
	}
	assert.Empty(t, ctx.exitRequests())
	assert.Empty(t, ctx.armRequests())
}

func TestCopytrade_STC_Partial_ArmsChandelierOnce(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "starter", 1.20), st)
	require.NoError(t, err)
	st = confirmBTOFills(t, s, ctx, st)

	st, sigs, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "AAPL", 190, "C", "half out at 1.80", 1.80), st)
	require.NoError(t, err)
	assert.Empty(t, sigs, "STC should not emit start.Signals (exit goes via domain event)")

	exits := ctx.exitRequests()
	require.Len(t, exits, 1)
	assert.Equal(t, 0.5, exits[0].Fraction)
	assert.Equal(t, "half out", exits[0].Reason)
	assert.Equal(t, "AAPL", exits[0].Symbol)
	assert.NotEmpty(t, exits[0].ContractSymbol)
	assert.Equal(t, "copytrade_v1", exits[0].Strategy)

	arms := ctx.armRequests()
	require.Len(t, arms, 1)
	assert.Equal(t, 1.80, arms[0].PeakPremium)
	assert.Equal(t, exits[0].ContractSymbol, arms[0].ContractSymbol)

	// Second partial: no new arm, just another exit request.
	_, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "AAPL", 190, "C", "taking more", 1.95), st)
	require.NoError(t, err)
	assert.Len(t, ctx.exitRequests(), 2, "second partial should still emit exit")
	assert.Len(t, ctx.armRequests(), 1, "chandelier arm must only fire once")
}

func TestCopytrade_STC_AllOut_ClosesAndBumpsGeneration(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "starter", 1.20), st)
	require.NoError(t, err)
	st = confirmBTOFills(t, s, ctx, st)
	st, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "AAPL", 190, "C", "all out at 2.50", 2.50), st)
	require.NoError(t, err)

	cst := st.(*copytradeState)
	assert.Empty(t, cst.Positions, "full close should delete position")
	assert.Len(t, ctx.exitRequests(), 1)
	assert.Equal(t, 1.0, ctx.exitRequests()[0].Fraction)
	assert.Empty(t, ctx.armRequests(), "full close on first STC must not arm chandelier")

	// Re-entry on same contract bumps generation.
	st, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "reload", 1.10), st)
	require.NoError(t, err)
	cst = st.(*copytradeState)
	require.Len(t, cst.Positions, 1)
	for _, pos := range cst.Positions {
		assert.Equal(t, 2, pos.Generation)
	}
}

func TestCopytrade_STC_Default_WhenNoKeywordMatches(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "TSLA", 250, "P", "starter", 3.50), st)
	require.NoError(t, err)
	st = confirmBTOFills(t, s, ctx, st)
	_, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "TSLA", 250, "P", "at 4.00", 4.00), st)
	require.NoError(t, err)

	exits := ctx.exitRequests()
	require.Len(t, exits, 1)
	assert.InDelta(t, 0.33, exits[0].Fraction, 1e-9)
	assert.Equal(t, "default", exits[0].Reason)
}

func TestCopytrade_STC_WithoutBTO_Dropped(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	_, sigs, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "AAPL", 190, "C", "half out", 1.50), st)
	require.NoError(t, err)
	assert.Empty(t, sigs)
	assert.Empty(t, ctx.exitRequests())
	assert.Empty(t, ctx.armRequests())
}

func TestCopytrade_AVG_Skipped(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	_, sigs, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("AVG", "AAPL", 190, "C", "averaged down", 1.10), st)
	require.NoError(t, err)
	assert.Empty(t, sigs)
	assert.Empty(t, ctx.exitRequests())
}

func TestCopytrade_AuthorWhitelist_Blocks(t *testing.T) {
	params := copytradeDefaultParams()
	params["author_whitelist"] = []string{"bob"}
	s, st := initCopytrade(t, params)
	ctx := newCopyCtx()

	_, sigs, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "starter", 1.20), st)
	require.NoError(t, err)
	assert.Empty(t, sigs, "alice is not whitelisted")
	assert.Empty(t, ctx.exitRequests())
}

func TestCopytrade_AuthorWhitelist_Allows(t *testing.T) {
	params := copytradeDefaultParams()
	params["author_whitelist"] = []string{"Alice"} // case-insensitive
	s, st := initCopytrade(t, params)
	ctx := newCopyCtx()

	_, sigs, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "starter", 1.20), st)
	require.NoError(t, err)
	assert.Len(t, sigs, 1)
}

func TestCopytrade_TrailDisabled_NoArmOnPartial(t *testing.T) {
	params := copytradeDefaultParams()
	params["trail_on_partial_enabled"] = false
	s, st := initCopytrade(t, params)
	ctx := newCopyCtx()

	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "starter", 1.20), st)
	require.NoError(t, err)
	st = confirmBTOFills(t, s, ctx, st)
	_, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "AAPL", 190, "C", "half out", 1.80), st)
	require.NoError(t, err)

	assert.Len(t, ctx.exitRequests(), 1)
	assert.Empty(t, ctx.armRequests())
}

func TestCopytrade_StateRoundTrip(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()
	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "starter", 1.20), st)
	require.NoError(t, err)

	data, err := st.Marshal()
	require.NoError(t, err)

	revived := newCopytradeState(parseCopytradeConfig(copytradeDefaultParams()))
	require.NoError(t, revived.Unmarshal(data))
	require.Len(t, revived.Positions, 1)
}

func TestCopytrade_STC_RefusedWhilePending(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "starter", 1.20), st)
	require.NoError(t, err)

	cst := st.(*copytradeState)
	require.Len(t, cst.Positions, 1)
	for _, pos := range cst.Positions {
		assert.True(t, pos.Pending, "BTO must leave position Pending until FillConfirmation")
	}

	st, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "AAPL", 190, "C", "half out", 1.80), st)
	require.NoError(t, err)
	assert.Empty(t, ctx.exitRequests(), "STC must not dispatch while pending")
	for _, pos := range st.(*copytradeState).Positions {
		assert.Equal(t, 1.0, pos.RemainingFrac, "RemainingFrac must not decay on refused STC")
	}
}

func TestCopytrade_FillConfirmation_ClearsPending(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "starter", 1.20), st)
	require.NoError(t, err)

	var contractSym string
	for _, pos := range st.(*copytradeState).Positions {
		contractSym = pos.ContractSymbol
	}
	fc := start.FillConfirmation{Symbol: contractSym, Side: start.SideBuy, Quantity: 5, Price: 1.25}
	st, _, err = s.OnEvent(ctx, "__copytrade__", fc, st)
	require.NoError(t, err)

	for _, pos := range st.(*copytradeState).Positions {
		assert.False(t, pos.Pending, "FillConfirmation BUY must clear Pending")
	}

	// STC now works.
	st, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "AAPL", 190, "C", "half out", 1.80), st)
	require.NoError(t, err)
	assert.Len(t, ctx.exitRequests(), 1)
}

func TestCopytrade_EntryRejection_UnwindsGhostPosition(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "starter", 1.20), st)
	require.NoError(t, err)

	var contractSym string
	for _, pos := range st.(*copytradeState).Positions {
		contractSym = pos.ContractSymbol
	}

	rej := start.EntryRejection{Symbol: contractSym, Side: start.SideBuy, Reason: "options_chain_empty"}
	st, _, err = s.OnEvent(ctx, "__copytrade__", rej, st)
	require.NoError(t, err)

	cst := st.(*copytradeState)
	assert.Empty(t, cst.Positions, "rejection must delete pending position")
	// Generation counter rolls back so the next BTO for the same base starts at gen 1.
	st, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "retry", 1.25), st)
	require.NoError(t, err)
	cst = st.(*copytradeState)
	require.Len(t, cst.Positions, 1)
	for _, pos := range cst.Positions {
		assert.Equal(t, 1, pos.Generation, "after rejection rollback, retry must start at gen 1")
	}
}

func TestCopytrade_ExitRejection_RollsRemainingFracBack(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "starter", 1.20), st)
	require.NoError(t, err)
	st = confirmBTOFills(t, s, ctx, st)

	st, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "AAPL", 190, "C", "half out", 1.80), st)
	require.NoError(t, err)

	var contractSym string
	for _, pos := range st.(*copytradeState).Positions {
		contractSym = pos.ContractSymbol
		assert.InDelta(t, 0.5, pos.RemainingFrac, 1e-9, "after first partial, residual should be 0.5")
	}

	// Position monitor rejects the in-flight follow-up. Strategy must undo the 0.5.
	rej := start.CopytradeExitRejection{ContractSymbol: contractSym, Fraction: 0.5, Reason: "exit_in_flight"}
	st, _, err = s.OnEvent(ctx, "__copytrade__", rej, st)
	require.NoError(t, err)
	for _, pos := range st.(*copytradeState).Positions {
		assert.InDelta(t, 1.0, pos.RemainingFrac, 1e-9, "rollback must restore pre-STC residual")
	}
}

// Guard: the strategy must satisfy the start.Strategy contract end-to-end,
// including plugging into an Instance the way the runner does. This is a
// compile-time + tiny runtime check — if the interface drifts, this breaks.
func TestCopytrade_InstanceLifecycle(t *testing.T) {
	t.Parallel()
	s := NewCopytradeStrategy()
	instID, _ := start.NewInstanceID("copytrade_v1:__copytrade__")
	inst := strategy.NewInstance(
		instID,
		s,
		copytradeDefaultParams(),
		strategy.InstanceAssignment{Symbols: []string{"__copytrade__"}, Timeframes: []string{"1m"}, Priority: 90},
		start.LifecyclePaperActive,
		nil,
	)
	ctx := newCopyCtx()
	require.NoError(t, inst.InitSymbol(ctx, "__copytrade__", nil))

	sigs, err := inst.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "NVDA", 900, "C", "starter", 4.25))
	require.NoError(t, err)
	require.Len(t, sigs, 1)
	// Instance.OnEvent does not stamp StrategyInstanceID (only OnBar does);
	// the runner's handleCopytradeSignal stamps it post-hoc before emitSignal.
	// We just verify the signal carries the expected symbol/type.
	assert.Equal(t, "NVDA", sigs[0].Symbol)
	assert.Equal(t, start.SignalEntry, sigs[0].Type)
	_ = context.Background() // keep import
}

func copytradeTTLParams(paperSecs, liveSecs int) map[string]any {
	params := copytradeDefaultParams()
	params["pending_ttl_paper_seconds"] = paperSecs
	params["pending_ttl_live_seconds"] = liveSecs
	return params
}

func (c *copyCtx) entryExpired() []domain.CopytradeEntryExpiredPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []domain.CopytradeEntryExpiredPayload
	for _, e := range c.emits {
		if p, ok := e.(domain.CopytradeEntryExpiredPayload); ok {
			out = append(out, p)
		}
	}
	return out
}

func (c *copyCtx) orphanFills() []domain.CopytradeOrphanFillPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []domain.CopytradeOrphanFillPayload
	for _, e := range c.emits {
		if p, ok := e.(domain.CopytradeOrphanFillPayload); ok {
			out = append(out, p)
		}
	}
	return out
}

func TestExpireStalePending_RemovesStalePendingKeepsOpenAndFresh(t *testing.T) {
	params := copytradeTTLParams(60, 60)
	_, st := initCopytrade(t, params)
	ctx := newCopyCtx()
	cst := st.(*copytradeState)

	base1 := positionBase("alice", "AAPL", time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC), 190, "CALL")
	base2 := positionBase("alice", "TSLA", time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC), 250, "PUT")
	base3 := positionBase("alice", "NVDA", time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC), 900, "CALL")
	base4 := positionBase("alice", "MSFT", time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC), 400, "CALL")

	twoMinAgo := ctx.now.Add(-2 * time.Minute)
	ninetySecAgo := ctx.now.Add(-90 * time.Second)
	tenSecAgo := ctx.now.Add(-10 * time.Second)
	anHourAgo := ctx.now.Add(-1 * time.Hour)

	cst.Positions[positionKey(base1, 1)] = &copytradePosition{ContractSymbol: "AAPL260425C00190000", Base: base1, OpenedAt: twoMinAgo, RemainingFrac: 1.0, Generation: 1, Pending: true}
	cst.Positions[positionKey(base2, 1)] = &copytradePosition{ContractSymbol: "TSLA260425P00250000", Base: base2, OpenedAt: ninetySecAgo, RemainingFrac: 1.0, Generation: 1, Pending: true}
	cst.Positions[positionKey(base3, 1)] = &copytradePosition{ContractSymbol: "NVDA260425C00900000", Base: base3, OpenedAt: tenSecAgo, RemainingFrac: 1.0, Generation: 1, Pending: true}
	cst.Positions[positionKey(base4, 1)] = &copytradePosition{ContractSymbol: "MSFT260425C00400000", Base: base4, OpenedAt: anHourAgo, RemainingFrac: 1.0, Generation: 1, Pending: false}
	cst.Generations[base1] = 1
	cst.Generations[base2] = 1
	cst.Generations[base3] = 1
	cst.Generations[base4] = 1

	out := expireStalePending(ctx, cst)

	assert.NotContains(t, out.Positions, positionKey(base1, 1))
	assert.NotContains(t, out.Positions, positionKey(base2, 1))
	assert.Contains(t, out.Positions, positionKey(base3, 1), "fresh pending must be kept")
	assert.Contains(t, out.Positions, positionKey(base4, 1), "open position must be kept regardless of age")

	_, exists1 := out.Generations[base1]
	_, exists2 := out.Generations[base2]
	assert.False(t, exists1, "generation for base1 must roll back to empty (gen==1)")
	assert.False(t, exists2, "generation for base2 must roll back to empty (gen==1)")
	assert.Equal(t, 1, out.Generations[base3], "generation for still-pending base3 unchanged")
	assert.Equal(t, 1, out.Generations[base4], "generation for open base4 unchanged")

	expired := ctx.entryExpired()
	require.Len(t, expired, 2)
	contracts := []string{expired[0].ContractSymbol, expired[1].ContractSymbol}
	assert.Contains(t, contracts, "AAPL260425C00190000")
	assert.Contains(t, contracts, "TSLA260425P00250000")
	for _, e := range expired {
		assert.Equal(t, "copytrade_v1", e.StrategyID)
		assert.Equal(t, ctx.now, e.ExpiredAt)
		assert.Greater(t, e.AgeSeconds, 60.0, "AgeSeconds must reflect actual elapsed time past TTL")
	}
}

func TestExpireStalePending_RespectsTTLMode(t *testing.T) {
	t.Run("paper_mode_30s_ttl_expires_60s_pending", func(t *testing.T) {
		params := copytradeTTLParams(30, 0)
		_, st := initCopytrade(t, params)
		ctx := newCopyCtx()
		ctx.envMode = start.EnvModePaper
		cst := st.(*copytradeState)

		base := positionBase("alice", "AAPL", time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC), 190, "CALL")
		cst.Positions[positionKey(base, 1)] = &copytradePosition{
			ContractSymbol: "AAPL260425C00190000", Base: base,
			OpenedAt: ctx.now.Add(-60 * time.Second), RemainingFrac: 1.0, Generation: 1, Pending: true,
		}
		cst.Generations[base] = 1

		out := expireStalePending(ctx, cst)
		assert.Empty(t, out.Positions, "60s-old pending must expire under 30s paper TTL")
		assert.Len(t, ctx.entryExpired(), 1)
	})

	t.Run("live_mode_ttl_zero_disables_sweep", func(t *testing.T) {
		params := copytradeTTLParams(30, 0)
		_, st := initCopytrade(t, params)
		ctx := newCopyCtx()
		ctx.envMode = start.EnvModeLive
		cst := st.(*copytradeState)

		base := positionBase("alice", "AAPL", time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC), 190, "CALL")
		cst.Positions[positionKey(base, 1)] = &copytradePosition{
			ContractSymbol: "AAPL260425C00190000", Base: base,
			OpenedAt: ctx.now.Add(-60 * time.Second), RemainingFrac: 1.0, Generation: 1, Pending: true,
		}
		cst.Generations[base] = 1

		out := expireStalePending(ctx, cst)
		assert.Len(t, out.Positions, 1, "live TTL=0 must leave pending alone (feature disabled)")
		assert.Empty(t, ctx.entryExpired())
	})
}

func TestParseHoldingTarget(t *testing.T) {
	cases := []struct {
		tail string
		want float64
		ok   bool
	}{
		{"Holding half", 0.5, true},
		{"holding HALF", 0.5, true},
		{"Still holding 1/3", 1.0 / 3.0, true},
		{"still holding 2/3", 2.0 / 3.0, true},
		{"holding 1/4", 0.25, true},
		{"holding quarter", 0.25, true},
		{"holding third", 1.0 / 3.0, true},
		{"holding 3/4", 0.75, true},
		{"partial. Holding half", 0.5, true},
		{"partial selling a few", 0, false},
		{"all out", 0, false},
		{"trim", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseHoldingTarget(tc.tail)
		assert.Equal(t, tc.ok, ok, "tail=%q ok mismatch", tc.tail)
		if ok {
			assert.InDelta(t, tc.want, got, 1e-9, "tail=%q value mismatch", tc.tail)
		}
	}
}

func TestResolveFraction_TargetBeatsKeyword(t *testing.T) {
	cfg := []copytradePartial{
		{Keyword: "all out", Fraction: 1.0},
		{Keyword: "partial", Fraction: 0.33},
		{Keyword: "trim", Fraction: 0.25},
	}
	// "partial. Holding half" with full position: target 0.5 -> sell 0.5.
	frac, reason := resolveFraction("partial. Holding half", 1.0, cfg, 0.33)
	assert.InDelta(t, 0.5, frac, 1e-9)
	assert.Equal(t, "target_holding", reason)

	// "partial. Still holding 1/3" with full position: sell 2/3.
	frac, reason = resolveFraction("partial. Still holding 1/3", 1.0, cfg, 0.33)
	assert.InDelta(t, 2.0/3.0, frac, 1e-9)
	assert.Equal(t, "target_holding", reason)

	// Already partialed to 0.5, author says "holding half" -> no-op (already at target).
	frac, reason = resolveFraction("Holding half", 0.5, cfg, 0.33)
	assert.Equal(t, 0.0, frac)
	assert.Equal(t, "target_holding", reason)

	// Already partialed to 0.5, author says "Holding 1/3" -> sell 1/3 of remaining.
	frac, reason = resolveFraction("Holding 1/3", 0.5, cfg, 0.33)
	assert.InDelta(t, 1.0/3.0, frac, 1e-9)
	assert.Equal(t, "target_holding", reason)

	// No target, keyword wins.
	frac, reason = resolveFraction("partial selling a few", 1.0, cfg, 0.33)
	assert.InDelta(t, 0.33, frac, 1e-9)
	assert.Equal(t, "partial", reason)

	// No target, no keyword, default.
	frac, reason = resolveFraction("at 4.00", 1.0, cfg, 0.33)
	assert.InDelta(t, 0.33, frac, 1e-9)
	assert.Equal(t, "default", reason)
}

func TestCopytrade_STC_HoldingHalf_SellsHalf(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "TSLA", 420, "C", "starter", 3.00), st)
	require.NoError(t, err)
	st = confirmBTOFills(t, s, ctx, st)

	_, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "TSLA", 420, "C", "partial. Holding half", 5.00), st)
	require.NoError(t, err)

	exits := ctx.exitRequests()
	require.Len(t, exits, 1)
	assert.InDelta(t, 0.5, exits[0].Fraction, 1e-9)
	assert.Equal(t, "target_holding", exits[0].Reason)
}

func TestCopytrade_STC_HoldingOneThird_SellsTwoThirds(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "RIOT", 22, "C", "starter", 2.00), st)
	require.NoError(t, err)
	st = confirmBTOFills(t, s, ctx, st)

	_, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "RIOT", 22, "C", "partial. Still holding 1/3", 3.44), st)
	require.NoError(t, err)

	exits := ctx.exitRequests()
	require.Len(t, exits, 1)
	assert.InDelta(t, 2.0/3.0, exits[0].Fraction, 1e-9)
	assert.Equal(t, "target_holding", exits[0].Reason)
}

func TestOrphanFillEmitsEventAndErrorLogs(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	fc := start.FillConfirmation{Symbol: "AAPL260425C00190000", Side: start.SideBuy, Quantity: 5, Price: 1.35}
	_, _, err := s.OnEvent(ctx, "__copytrade__", fc, st)
	require.NoError(t, err)

	orphans := ctx.orphanFills()
	require.Len(t, orphans, 1)
	assert.Equal(t, "copytrade_v1", orphans[0].StrategyID)
	assert.Equal(t, "AAPL260425C00190000", orphans[0].ContractSymbol)
	assert.Equal(t, 1.35, orphans[0].FillPrice)
	assert.Equal(t, 5.0, orphans[0].Qty)
	assert.Equal(t, ctx.now, orphans[0].ObservedAt)
}

// TestCopytrade_STC_QueuedWhilePending_DispatchesOnFillConfirmation locks the
// queue+drain behavior added to recover the dropped-STC backtest cascade
// (RC1 in _workspace/copytrade_backtest_fix_plan.md). Pre-fix, a STC arriving
// in the same minute as the BTO was silently dropped because the BTO was
// still Pending.
func TestCopytrade_STC_QueuedWhilePending_DispatchesOnFillConfirmation(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "starter", 1.20), st)
	require.NoError(t, err)

	// STC arrives while BTO still Pending — should queue, not dispatch.
	st, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "AAPL", 190, "C", "half out at 1.80", 1.80), st)
	require.NoError(t, err)
	assert.Empty(t, ctx.exitRequests(), "STC must not dispatch while pending")

	cst := st.(*copytradeState)
	var contractSym string
	var queued int
	for _, pos := range cst.Positions {
		contractSym = pos.ContractSymbol
		queued = len(pos.QueuedSTCs)
		assert.Equal(t, 1.0, pos.RemainingFrac)
	}
	assert.Equal(t, 1, queued, "STC must be queued on the Pending position")

	// FillConfirmation should drain the queue.
	fc := start.FillConfirmation{Symbol: contractSym, Side: start.SideBuy, Quantity: 5, Price: 1.25}
	st, _, err = s.OnEvent(ctx, "__copytrade__", fc, st)
	require.NoError(t, err)

	exits := ctx.exitRequests()
	require.Len(t, exits, 1, "queued STC must dispatch on FillConfirmation")
	assert.InDelta(t, 0.5, exits[0].Fraction, 1e-9)
	assert.Equal(t, "half out", exits[0].Reason)
	assert.Equal(t, contractSym, exits[0].ContractSymbol)

	for _, pos := range st.(*copytradeState).Positions {
		assert.False(t, pos.Pending)
		assert.InDelta(t, 0.5, pos.RemainingFrac, 1e-9, "queue drain must apply the partial")
		assert.Empty(t, pos.QueuedSTCs, "queue must be cleared after drain")
	}
}

// TestCopytrade_STC_MultipleQueued_DispatchInOrder confirms FIFO drain when
// several STCs queue against the same Pending BTO.
func TestCopytrade_STC_MultipleQueued_DispatchInOrder(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "starter", 1.20), st)
	require.NoError(t, err)

	for _, tail := range []string{"trim at 1.50", "trim at 1.60", "trim at 1.70"} {
		st, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "AAPL", 190, "C", tail, 1.50), st)
		require.NoError(t, err)
	}
	assert.Empty(t, ctx.exitRequests(), "queued STCs must not dispatch while pending")

	var contractSym string
	for _, pos := range st.(*copytradeState).Positions {
		contractSym = pos.ContractSymbol
		assert.Equal(t, 3, len(pos.QueuedSTCs))
	}

	fc := start.FillConfirmation{Symbol: contractSym, Side: start.SideBuy, Quantity: 5, Price: 1.25}
	_, _, err = s.OnEvent(ctx, "__copytrade__", fc, st)
	require.NoError(t, err)

	exits := ctx.exitRequests()
	require.Len(t, exits, 3, "all queued STCs must dispatch in order")
	for _, e := range exits {
		assert.Equal(t, "trim", e.Reason)
		assert.InDelta(t, 0.25, e.Fraction, 1e-9)
	}
}

// TestCopytrade_STC_QueuedFullCloseShortCircuits verifies that once a queued
// drain produces a full close, subsequent queued STCs are NOT dispatched
// (no point exiting an already-closed position).
func TestCopytrade_STC_QueuedFullCloseShortCircuits(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "starter", 1.20), st)
	require.NoError(t, err)

	// 1: partial. 2: all out (full close). 3: another STC that must NOT fire.
	st, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "AAPL", 190, "C", "half out at 1.80", 1.80), st)
	require.NoError(t, err)
	st, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "AAPL", 190, "C", "all out at 2.00", 2.00), st)
	require.NoError(t, err)
	st, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "AAPL", 190, "C", "trim at 2.10", 2.10), st)
	require.NoError(t, err)

	var contractSym string
	for _, pos := range st.(*copytradeState).Positions {
		contractSym = pos.ContractSymbol
		assert.Equal(t, 3, len(pos.QueuedSTCs))
	}

	fc := start.FillConfirmation{Symbol: contractSym, Side: start.SideBuy, Quantity: 5, Price: 1.25}
	st, _, err = s.OnEvent(ctx, "__copytrade__", fc, st)
	require.NoError(t, err)

	exits := ctx.exitRequests()
	require.Len(t, exits, 2, "drain stops after full-close STC; trailing STC must not fire")
	assert.InDelta(t, 0.5, exits[0].Fraction, 1e-9, "first dispatch is the partial")
	assert.InDelta(t, 1.0, exits[1].Fraction, 1e-9, "second dispatch is the full close")

	cst := st.(*copytradeState)
	assert.Empty(t, cst.Positions, "full close from drain must delete the position")
}

// TestCopytrade_QueuedSTCs_DiscardedOnEntryRejection ensures a rejected BTO
// drops any queued STCs without emitting orphan exits against a position the
// broker never opened.
func TestCopytrade_QueuedSTCs_DiscardedOnEntryRejection(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("BTO", "AAPL", 190, "C", "starter", 1.20), st)
	require.NoError(t, err)

	st, _, err = s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "AAPL", 190, "C", "half out", 1.80), st)
	require.NoError(t, err)

	var contractSym string
	for _, pos := range st.(*copytradeState).Positions {
		contractSym = pos.ContractSymbol
		assert.Equal(t, 1, len(pos.QueuedSTCs))
	}

	rej := start.EntryRejection{Symbol: contractSym, Side: start.SideBuy, Reason: "options_chain_empty"}
	st, _, err = s.OnEvent(ctx, "__copytrade__", rej, st)
	require.NoError(t, err)

	assert.Empty(t, ctx.exitRequests(), "rejection must not dispatch queued STCs")
	cst := st.(*copytradeState)
	assert.Empty(t, cst.Positions, "rejection deletes the pending position")
}

// TestCopytrade_QueuedSTCs_DiscardedOnTTLExpiry mirrors the rejection case for
// the TTL sweep path: queued STCs are dropped without emitting orphan exits.
func TestCopytrade_QueuedSTCs_DiscardedOnTTLExpiry(t *testing.T) {
	params := copytradeTTLParams(30, 30)
	s, st := initCopytrade(t, params)
	ctx := newCopyCtx()
	ctx.envMode = start.EnvModePaper

	// Plant a Pending position older than the TTL with a queued STC.
	cst := st.(*copytradeState)
	base := positionBase("alice", "AAPL", time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC), 190, "CALL")
	contractSym := "AAPL260425C00190000"
	cst.Positions[positionKey(base, 1)] = &copytradePosition{
		ContractSymbol: contractSym,
		Base:           base,
		OpenedAt:       ctx.now.Add(-2 * time.Minute),
		RemainingFrac:  1.0,
		Generation:     1,
		Pending:        true,
		QueuedSTCs: []start.CopytradeSignal{
			copytradeSignal("STC", "AAPL", 190, "C", "half out", 1.80),
		},
	}
	cst.Generations[base] = 1

	// expireStalePending runs at the head of OnEvent — any event triggers it.
	// Use an STC for a different unknown symbol to keep state clean.
	_, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "NONEXIST", 1, "C", "noop", 0), st)
	require.NoError(t, err)

	assert.Empty(t, ctx.exitRequests(), "TTL expiry must not dispatch queued STCs")
	expired := ctx.entryExpired()
	require.Len(t, expired, 1, "TTL sweep must still emit EntryExpired for the position")
	assert.Equal(t, contractSym, expired[0].ContractSymbol)
}

// TestCopytrade_STC_WithoutBTO_NotQueued preserves the prior drop behavior:
// an STC with no Generations[base] entry has no position to queue against.
func TestCopytrade_STC_WithoutBTO_NotQueued(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	ctx := newCopyCtx()

	st, _, err := s.OnEvent(ctx, "__copytrade__", copytradeSignal("STC", "AAPL", 190, "C", "half out", 1.50), st)
	require.NoError(t, err)
	assert.Empty(t, ctx.exitRequests())

	cst := st.(*copytradeState)
	assert.Empty(t, cst.Positions, "STC with no prior BTO must not create a position")
}
