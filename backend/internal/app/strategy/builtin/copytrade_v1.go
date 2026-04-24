package builtin

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// CopytradeStrategy mirrors option trades from a vetted Discord channel.
// Unlike bar-driven strategies, it never produces signals from OnBar — its
// entire decision path is event-driven (start.CopytradeSignal delivered
// through the runner's handleCopytradeSignal hook).
//
// BTO emits a SignalEntry with force_* tags so risk_sizer pins the exact
// contract. STC publishes CopytradeExitRequestPayload directly via the
// strategy Context; the position monitor converts that into a partial- or
// full-close order using the existing triggerExit path. First partial per
// position also arms the CHANDELIER_TRAIL rule externally.
type CopytradeStrategy struct {
	meta start.Meta
}

// NewCopytradeStrategy constructs the registry-registered builtin.
func NewCopytradeStrategy() *CopytradeStrategy {
	id, _ := start.NewStrategyID("copytrade_v1")
	ver, _ := start.NewVersion("1.0.0")
	return &CopytradeStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "Copytrade v1 — Discord mirror",
			Description: "Mirrors options trades posted by a vetted Discord author.",
			Author:      "system",
		},
	}
}

func (s *CopytradeStrategy) Meta() start.Meta { return s.meta }
func (s *CopytradeStrategy) WarmupBars() int  { return 0 }

// copytradeFullCloseTolerance is the residual-fraction threshold below which
// an STC is treated as a full close and the position is deleted. Handles
// float drift from successive multiplicative partials (e.g. 0.5 * 0.33 * 0.5
// chains) so a tiny leftover doesn't keep the position open with a broker
// order that rounds to zero contracts.
const copytradeFullCloseTolerance = 0.005

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

type copytradePartial struct {
	Keyword  string  `json:"keyword"`
	Fraction float64 `json:"fraction"`
}

type copytradeConfig struct {
	AuthorWhitelist       []string
	SkipAVG               bool
	TrailOnPartial        bool
	TrailGivebackPct      float64
	PartialFractions      []copytradePartial // pre-sorted longest-keyword first
	DefaultSTCFraction    float64
	MaxPositions          int
	PendingTTLPaperSecs   int
	PendingTTLLiveSecs    int
}

func parseCopytradeConfig(params map[string]any) copytradeConfig {
	cfg := copytradeConfig{
		AuthorWhitelist:     getStringSlice(params, "author_whitelist", nil),
		SkipAVG:             getBool(params, "skip_avg", true),
		TrailOnPartial:      getBool(params, "trail_on_partial_enabled", true),
		TrailGivebackPct:    getFloat64(params, "trail_giveback_pct", 0.15),
		DefaultSTCFraction:  getFloat64(params, "default_stc_fraction", 0.33),
		MaxPositions:        getInt(params, "max_positions", 5),
		PendingTTLPaperSecs: getInt(params, "pending_ttl_paper_seconds", 0),
		PendingTTLLiveSecs:  getInt(params, "pending_ttl_live_seconds", 0),
	}
	cfg.PartialFractions = parsePartialFractions(params["partial_fractions"])
	// Longest-keyword first so "all out" matches before "out" (if ever added).
	sort.SliceStable(cfg.PartialFractions, func(i, j int) bool {
		return len(cfg.PartialFractions[i].Keyword) > len(cfg.PartialFractions[j].Keyword)
	})
	return cfg
}

func parsePartialFractions(v any) []copytradePartial {
	// The TOML loader hands us [[params.partial_fractions]] as
	// []map[string]any in production and as []any in some test paths that
	// build params by hand. Accept both.
	var entries []map[string]any
	switch typed := v.(type) {
	case []map[string]any:
		entries = typed
	case []any:
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				entries = append(entries, m)
			}
		}
	default:
		return nil
	}
	out := make([]copytradePartial, 0, len(entries))
	for _, m := range entries {
		kw, _ := m["keyword"].(string)
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw == "" {
			continue
		}
		var frac float64
		switch f := m["fraction"].(type) {
		case float64:
			frac = f
		case int64:
			frac = float64(f)
		case int:
			frac = float64(f)
		}
		if frac <= 0 || frac > 1.0 {
			continue
		}
		out = append(out, copytradePartial{Keyword: kw, Fraction: frac})
	}
	return out
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

// copytradePosition tracks a single open copytrade position keyed by
// (author, ticker, expiry, strike, right, generation). RemainingFrac starts at
// 1.0 on BTO and decays multiplicatively on each STC; TrailArmed guards the
// one-shot CHANDELIER_TRAIL arming on first partial.
type copytradePosition struct {
	ContractSymbol string    `json:"contractSymbol"`
	Base           string    `json:"base"` // positionBase(author,ticker,expiry,strike,right) — lets EntryRejection unwind Generations[base]
	OpenedAt       time.Time `json:"openedAt"`
	EntryPremium   float64   `json:"entryPremium"`
	RemainingFrac  float64   `json:"remainingFrac"`
	TrailArmed     bool      `json:"trailArmed"`
	Generation     int       `json:"generation"`
	// Pending is true between BTO emit and FillConfirmation. STC posts are
	// refused while true so a pre-fill STC can't decrement RemainingFrac
	// against a position the broker hasn't opened yet.
	Pending bool `json:"pending"`
}

// copytradeState is the per-instance catch-all state. It lives under the
// sentinel symbol copytradeSentinelSymbol (the strategy has no per-symbol
// routing). Positions is keyed by positionKey(...); Generations tracks the
// next generation number per (author, ticker, expiry, strike, right) tuple so
// re-entries after "all out" produce a distinct key and avoid colliding with
// stale state if an STC for the closed position arrives late.
type copytradeState struct {
	Config      copytradeConfig        `json:"-"`
	Positions   map[string]*copytradePosition `json:"positions"`
	Generations map[string]int                `json:"generations"`
}

func newCopytradeState(cfg copytradeConfig) *copytradeState {
	return &copytradeState{
		Config:      cfg,
		Positions:   make(map[string]*copytradePosition),
		Generations: make(map[string]int),
	}
}

func (st *copytradeState) Marshal() ([]byte, error)    { return json.Marshal(st) }
func (st *copytradeState) Unmarshal(data []byte) error { return json.Unmarshal(data, st) }

func positionBase(author, ticker string, expiry time.Time, strike float64, right string) string {
	return fmt.Sprintf("%s|%s|%s|%g|%s",
		strings.ToLower(author),
		strings.ToUpper(ticker),
		expiry.Format("2006-01-02"),
		strike,
		strings.ToUpper(right),
	)
}

func positionKey(base string, generation int) string {
	return fmt.Sprintf("%s|g%d", base, generation)
}

// ---------------------------------------------------------------------------
// Strategy interface
// ---------------------------------------------------------------------------

func (s *CopytradeStrategy) Init(_ start.Context, _ string, params map[string]any, prior start.State) (start.State, error) {
	cfg := parseCopytradeConfig(params)
	if prior != nil {
		if st, ok := prior.(*copytradeState); ok {
			st.Config = cfg
			if st.Positions == nil {
				st.Positions = make(map[string]*copytradePosition)
			}
			if st.Generations == nil {
				st.Generations = make(map[string]int)
			}
			return st, nil
		}
	}
	return newCopytradeState(cfg), nil
}

// OnBar is intentionally a no-op: copytrade has no timeframe logic, only
// event-driven reactions to the Discord sidecar.
func (s *CopytradeStrategy) OnBar(_ start.Context, _ string, _ start.Bar, st start.State) (start.State, []start.Signal, error) {
	return st, nil, nil
}

// OnEvent dispatches on start.CopytradeSignal plus broker feedback events
// (FillConfirmation, EntryRejection) routed by the runner's fallback lookup.
// All other event types are ignored.
func (s *CopytradeStrategy) OnEvent(ctx start.Context, _ string, evt any, st start.State) (start.State, []start.Signal, error) {
	cst, ok := st.(*copytradeState)
	if !ok {
		return st, nil, fmt.Errorf("CopytradeStrategy.OnEvent: expected *copytradeState, got %T", st)
	}

	cst = expireStalePending(ctx, cst)

	switch e := evt.(type) {
	case start.FillConfirmation:
		return s.handleFillConfirmation(ctx, cst, e)
	case start.EntryRejection:
		return s.handleEntryRejection(ctx, cst, e)
	case start.CopytradeExitRejection:
		return s.handleExitRejection(ctx, cst, e)
	}

	sig, ok := evt.(start.CopytradeSignal)
	if !ok {
		return st, nil, nil
	}
	if !authorAllowed(cst.Config.AuthorWhitelist, sig.Author) {
		if ctx != nil {
			ctx.Logger().Info("copytrade: dropping signal — author not in whitelist",
				"author", sig.Author, "ticker", sig.Ticker, "action", sig.Action)
		}
		return cst, nil, nil
	}

	switch strings.ToUpper(sig.Action) {
	case "BTO":
		return s.handleBTO(ctx, cst, sig)
	case "STC":
		return s.handleSTC(ctx, cst, sig)
	case "AVG":
		if cst.Config.SkipAVG {
			if ctx != nil {
				ctx.Logger().Info("copytrade: skipping AVG", "ticker", sig.Ticker, "author", sig.Author)
			}
			return cst, nil, nil
		}
		if ctx != nil {
			ctx.Logger().Info("copytrade: observed AVG (log-only)",
				"ticker", sig.Ticker, "author", sig.Author, "price", sig.Price)
		}
		return cst, nil, nil
	default:
		if ctx != nil {
			ctx.Logger().Warn("copytrade: unknown action", "action", sig.Action)
		}
		return cst, nil, nil
	}
}

func (s *CopytradeStrategy) handleBTO(ctx start.Context, cst *copytradeState, sig start.CopytradeSignal) (start.State, []start.Signal, error) {
	if cst.Config.MaxPositions > 0 && len(cst.Positions) >= cst.Config.MaxPositions {
		if ctx != nil {
			ctx.Logger().Warn("copytrade: dropping BTO — max_positions reached",
				"ticker", sig.Ticker, "open", len(cst.Positions), "cap", cst.Config.MaxPositions)
		}
		return cst, nil, nil
	}

	right := normalizeRight(sig.Right)
	base := positionBase(sig.Author, sig.Ticker, sig.Expiry, sig.Strike, string(right))
	gen := cst.Generations[base] + 1
	key := positionKey(base, gen)

	if _, exists := cst.Positions[key]; exists {
		if ctx != nil {
			ctx.Logger().Warn("copytrade: ignoring duplicate BTO for live position", "key", key)
		}
		return cst, nil, nil
	}

	contractSym := domain.FormatOCCSymbol(strings.ToUpper(sig.Ticker), sig.Expiry, right, sig.Strike)

	openedAt := sig.PostedAt
	if openedAt.IsZero() && ctx != nil {
		openedAt = ctx.Now()
	}
	cst.Positions[key] = &copytradePosition{
		ContractSymbol: contractSym,
		Base:           base,
		OpenedAt:       openedAt,
		EntryPremium:   sig.Price,
		RemainingFrac:  1.0,
		Generation:     gen,
		Pending:        true,
	}
	cst.Generations[base] = gen

	tags := map[string]string{
		"author":                     sig.Author,
		"generation":                 strconv.Itoa(gen),
		"contract_symbol":            contractSym,
		"signal_id":                  sig.SignalID,
		"copytrade_action":           "BTO",
		"ref_price":                  formatFloat(sig.Price),
		"force_expiry":               sig.Expiry.Format("2006-01-02"),
		"force_strike":               formatFloat(sig.Strike),
		"force_right":                string(right),
		"force_ref_premium":          formatFloat(sig.Price),
	}

	entry, err := start.NewSignal(
		start.InstanceID(""),
		strings.ToUpper(sig.Ticker),
		start.SignalEntry,
		start.SideBuy,
		0.9, // fixed — author conviction is binary; sizing happens in risk_sizer
		tags,
	)
	if err != nil {
		return cst, nil, fmt.Errorf("copytrade: NewSignal BTO: %w", err)
	}
	if ctx != nil {
		ctx.Logger().Info("copytrade: BTO signal emitted",
			"ticker", sig.Ticker, "author", sig.Author,
			"expiry", sig.Expiry.Format("2006-01-02"),
			"strike", sig.Strike, "right", string(right),
			"premium", sig.Price, "generation", gen)
	}
	return cst, []start.Signal{entry}, nil
}

func (s *CopytradeStrategy) handleSTC(ctx start.Context, cst *copytradeState, sig start.CopytradeSignal) (start.State, []start.Signal, error) {
	right := normalizeRight(sig.Right)
	base := positionBase(sig.Author, sig.Ticker, sig.Expiry, sig.Strike, string(right))
	gen := cst.Generations[base]
	if gen == 0 {
		if ctx != nil {
			ctx.Logger().Warn("copytrade: STC with no prior BTO — dropping",
				"ticker", sig.Ticker, "author", sig.Author, "base", base)
		}
		return cst, nil, nil
	}
	key := positionKey(base, gen)
	pos, ok := cst.Positions[key]
	if !ok {
		if ctx != nil {
			ctx.Logger().Warn("copytrade: STC but position not tracked — dropping", "key", key)
		}
		return cst, nil, nil
	}
	if pos.Pending {
		if ctx != nil {
			ctx.Logger().Warn("copytrade: STC refused — BTO not yet confirmed by broker",
				"key", key, "contract_symbol", pos.ContractSymbol, "tail", sig.Tail)
		}
		return cst, nil, nil
	}

	fraction, keyword := resolveFraction(sig.Tail, cst.Config.PartialFractions, cst.Config.DefaultSTCFraction)
	// Clamp by remaining fraction so repeated partials never request more
	// than what's actually open (e.g. two "half out" posts).
	if fraction > pos.RemainingFrac {
		fraction = pos.RemainingFrac
	}
	if fraction <= 0 {
		if ctx != nil {
			ctx.Logger().Warn("copytrade: STC fraction resolved to 0 — dropping", "key", key)
		}
		return cst, nil, nil
	}

	// Fraction is expressed as "close this fraction of REMAINING contracts"
	// at the position-monitor level (it multiplies into pos.Quantity there).
	// The strategy's internal RemainingFrac is the multiplicative residual
	// so we can model partial-over-partial without knowing absolute qty.
	newRemaining := pos.RemainingFrac * (1.0 - fraction)
	fullClose := fraction >= 1.0 || newRemaining < copytradeFullCloseTolerance

	exitPayload := domain.CopytradeExitRequestPayload{
		Strategy:       "copytrade_v1",
		Symbol:         strings.ToUpper(sig.Ticker),
		ContractSymbol: pos.ContractSymbol,
		Fraction:       fraction,
		Reason:         keyword,
	}
	if ctx != nil {
		if err := ctx.EmitDomainEvent(exitPayload); err != nil {
			ctx.Logger().Error("copytrade: emit CopytradeExitRequest failed", "error", err)
		}
	}

	// First-partial trail arming: only on a strictly partial close and only
	// once per position. Full-close STCs do not arm (the position is exiting).
	if !fullClose && !pos.TrailArmed && cst.Config.TrailOnPartial {
		armPayload := domain.ChandelierTrailArmPayload{
			Strategy:       "copytrade_v1",
			Symbol:         strings.ToUpper(sig.Ticker),
			ContractSymbol: pos.ContractSymbol,
			PeakPremium:    sig.Price,
		}
		if ctx != nil {
			if err := ctx.EmitDomainEvent(armPayload); err != nil {
				ctx.Logger().Error("copytrade: emit ChandelierTrailArm failed", "error", err)
			}
		}
		pos.TrailArmed = true
	}

	if fullClose {
		delete(cst.Positions, key)
		if ctx != nil {
			ctx.Logger().Info("copytrade: position closed",
				"key", key, "keyword", keyword, "fraction", fraction)
		}
	} else {
		pos.RemainingFrac = newRemaining
		if ctx != nil {
			ctx.Logger().Info("copytrade: partial close",
				"key", key, "keyword", keyword, "fraction", fraction,
				"remaining_frac", newRemaining, "trail_armed", pos.TrailArmed)
		}
	}

	return cst, nil, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func authorAllowed(whitelist []string, author string) bool {
	if len(whitelist) == 0 {
		return true
	}
	a := strings.ToLower(strings.TrimSpace(author))
	for _, w := range whitelist {
		if strings.ToLower(strings.TrimSpace(w)) == a {
			return true
		}
	}
	return false
}

func normalizeRight(r string) domain.OptionRight {
	switch strings.ToUpper(strings.TrimSpace(r)) {
	case "P", "PUT":
		return domain.OptionRightPut
	default:
		return domain.OptionRightCall
	}
}

// resolveFraction scans the partial-fraction table (assumed longest-keyword
// first) for the first keyword that appears in tail (case-insensitive). If
// none match, returns the default. The matched keyword is returned for audit.
func resolveFraction(tail string, table []copytradePartial, def float64) (float64, string) {
	lowered := strings.ToLower(tail)
	for _, p := range table {
		if strings.Contains(lowered, p.Keyword) {
			return p.Fraction, p.Keyword
		}
	}
	return def, "default"
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// handleFillConfirmation flips Pending=false on the matching pending position.
// Matches by (ContractSymbol, Pending=true) on BUY-side fills; SELL-side fills
// (partial/full closes) do not affect Pending. When no Pending position matches
// a BUY fill, emits CopytradeOrphanFillPayload so operators get paged — this
// race happens when TTL sweep deleted the position a beat before the fill.
func (s *CopytradeStrategy) handleFillConfirmation(ctx start.Context, cst *copytradeState, fc start.FillConfirmation) (start.State, []start.Signal, error) {
	if fc.Side != start.SideBuy {
		return cst, nil, nil
	}
	var match *copytradePosition
	for _, pos := range cst.Positions {
		if pos == nil || !pos.Pending || pos.ContractSymbol != fc.Symbol {
			continue
		}
		if match == nil || pos.OpenedAt.Before(match.OpenedAt) {
			match = pos
		}
	}
	if match == nil {
		if ctx != nil {
			ctx.Logger().Error("copytrade: ORPHAN FILL — buy fill with no matching Pending position (likely late cancel); manual intervention required",
				"contract_symbol", fc.Symbol, "fill_price", fc.Price, "fill_qty", fc.Quantity)
			_ = ctx.EmitDomainEvent(domain.CopytradeOrphanFillPayload{
				StrategyID:     "copytrade_v1",
				ContractSymbol: fc.Symbol,
				FillPrice:      fc.Price,
				Qty:            fc.Quantity,
				ObservedAt:     ctx.Now(),
			})
		}
		return cst, nil, nil
	}
	match.Pending = false
	if ctx != nil {
		ctx.Logger().Info("copytrade: BTO fill confirmed",
			"contract_symbol", fc.Symbol, "generation", match.Generation,
			"fill_price", fc.Price, "fill_qty", fc.Quantity)
	}
	return cst, nil, nil
}

// handleExitRejection rolls RemainingFrac back when position monitor refuses a
// partial exit (e.g. prior exit still in flight). The strategy already
// decremented on the emit path; without rollback, strategy and broker residuals
// diverge. Matches by (ContractSymbol, Pending=false); a Pending match means
// the strategy was never authoritative for this contract and rollback is a
// no-op to avoid corrupting state.
func (s *CopytradeStrategy) handleExitRejection(ctx start.Context, cst *copytradeState, rej start.CopytradeExitRejection) (start.State, []start.Signal, error) {
	if rej.Fraction <= 0 || rej.Fraction >= 1.0 {
		return cst, nil, nil
	}
	var match *copytradePosition
	for _, pos := range cst.Positions {
		if pos == nil || pos.Pending || pos.ContractSymbol != rej.ContractSymbol {
			continue
		}
		if match == nil || pos.Generation > match.Generation {
			match = pos
		}
	}
	if match == nil {
		if ctx != nil {
			ctx.Logger().Warn("copytrade: exit rejection for unknown position — ignoring",
				"contract_symbol", rej.ContractSymbol, "fraction", rej.Fraction, "reason", rej.Reason)
		}
		return cst, nil, nil
	}
	before := match.RemainingFrac
	match.RemainingFrac /= 1.0 - rej.Fraction
	if match.RemainingFrac > 1.0 {
		match.RemainingFrac = 1.0
	}
	if ctx != nil {
		ctx.Logger().Warn("copytrade: exit rejected — rolling RemainingFrac back",
			"contract_symbol", rej.ContractSymbol,
			"fraction", rej.Fraction,
			"reason", rej.Reason,
			"remaining_before", before,
			"remaining_after", match.RemainingFrac)
	}
	return cst, nil, nil
}

// handleEntryRejection deletes the pending position and rolls Generations[base]
// back so a subsequent re-entry for the same (author,ticker,expiry,strike,right)
// starts at the same generation instead of skipping one. Matches by
// (ContractSymbol, Pending=true) on BUY-side rejections.
func (s *CopytradeStrategy) handleEntryRejection(ctx start.Context, cst *copytradeState, rej start.EntryRejection) (start.State, []start.Signal, error) {
	if rej.Side != start.SideBuy {
		return cst, nil, nil
	}
	var (
		matchKey string
		match    *copytradePosition
	)
	for k, pos := range cst.Positions {
		if pos == nil || !pos.Pending || pos.ContractSymbol != rej.Symbol {
			continue
		}
		if match == nil || pos.OpenedAt.Before(match.OpenedAt) {
			matchKey = k
			match = pos
		}
	}
	if match == nil {
		return cst, nil, nil
	}
	delete(cst.Positions, matchKey)
	rollbackGeneration(cst, match.Base, match.Generation)
	if ctx != nil {
		ctx.Logger().Warn("copytrade: BTO rejected — unwinding ghost position",
			"contract_symbol", rej.Symbol, "generation", match.Generation, "reason", rej.Reason)
	}
	return cst, nil, nil
}

// rollbackGeneration ensures a retry for `base` reuses the same generation
// rather than skipping one, by decrementing only when the caller held the
// top gen.
func rollbackGeneration(cst *copytradeState, base string, generation int) {
	if base == "" {
		return
	}
	if cst.Generations[base] != generation {
		return
	}
	if generation <= 1 {
		delete(cst.Generations, base)
		return
	}
	cst.Generations[base] = generation - 1
}

// expireStalePending emits CopytradeEntryExpiredPayload per eviction so
// execution cancels the outstanding broker order. TTL=0 disables.
func expireStalePending(ctx start.Context, cst *copytradeState) *copytradeState {
	if ctx == nil {
		return cst
	}
	ttl := pickPendingTTL(ctx, cst.Config)
	if ttl <= 0 {
		return cst
	}
	now := ctx.Now()
	var toExpire []string
	for posKey, pos := range cst.Positions {
		if pos == nil || !pos.Pending {
			continue
		}
		if now.Sub(pos.OpenedAt) <= ttl {
			continue
		}
		toExpire = append(toExpire, posKey)
	}
	if len(toExpire) == 0 {
		return cst
	}
	for _, posKey := range toExpire {
		pos := cst.Positions[posKey]
		ageSec := now.Sub(pos.OpenedAt).Seconds()
		_ = ctx.EmitDomainEvent(domain.CopytradeEntryExpiredPayload{
			StrategyID:     "copytrade_v1",
			ContractSymbol: pos.ContractSymbol,
			PositionKey:    posKey,
			ExpiredAt:      now,
			AgeSeconds:     ageSec,
		})
		ctx.Logger().Warn("copytrade: ghost position expired — no fill within TTL",
			"contract_symbol", pos.ContractSymbol,
			"base", pos.Base,
			"generation", pos.Generation,
			"age_seconds", ageSec,
			"ttl_seconds", ttl.Seconds())
		delete(cst.Positions, posKey)
		rollbackGeneration(cst, pos.Base, pos.Generation)
	}
	return cst
}

// pickPendingTTL returns 0 when the sweep is disabled for the current env.
// Backtests inherit the paper TTL because they run as EnvModePaper.
func pickPendingTTL(ctx start.Context, cfg copytradeConfig) time.Duration {
	switch ctx.EnvMode() {
	case start.EnvModeLive:
		return time.Duration(cfg.PendingTTLLiveSecs) * time.Second
	default:
		return time.Duration(cfg.PendingTTLPaperSecs) * time.Second
	}
}
