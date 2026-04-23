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

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

type copytradePartial struct {
	Keyword  string  `json:"keyword"`
	Fraction float64 `json:"fraction"`
}

type copytradeConfig struct {
	AuthorWhitelist    []string
	SkipAVG            bool
	TrailOnPartial     bool
	TrailGivebackPct   float64
	PartialFractions   []copytradePartial // pre-sorted longest-keyword first
	DefaultSTCFraction float64
	MaxPositions       int
}

func parseCopytradeConfig(params map[string]any) copytradeConfig {
	cfg := copytradeConfig{
		AuthorWhitelist:    getStringSlice(params, "author_whitelist", nil),
		SkipAVG:            getBool(params, "skip_avg", true),
		TrailOnPartial:     getBool(params, "trail_on_partial_enabled", true),
		TrailGivebackPct:   getFloat64(params, "trail_giveback_pct", 0.15),
		DefaultSTCFraction: getFloat64(params, "default_stc_fraction", 0.33),
		MaxPositions:       getInt(params, "max_positions", 5),
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
	OpenedAt       time.Time `json:"openedAt"`
	EntryPremium   float64   `json:"entryPremium"`
	RemainingFrac  float64   `json:"remainingFrac"`
	TrailArmed     bool      `json:"trailArmed"`
	Generation     int       `json:"generation"`
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

// OnEvent dispatches on start.CopytradeSignal. All other event types are
// ignored (no fills or rejections route to this instance because it has no
// per-symbol routing).
func (s *CopytradeStrategy) OnEvent(ctx start.Context, _ string, evt any, st start.State) (start.State, []start.Signal, error) {
	sig, ok := evt.(start.CopytradeSignal)
	if !ok {
		return st, nil, nil
	}
	cst, ok := st.(*copytradeState)
	if !ok {
		return st, nil, fmt.Errorf("CopytradeStrategy.OnEvent: expected *copytradeState, got %T", st)
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
		OpenedAt:       openedAt,
		EntryPremium:   sig.Price,
		RemainingFrac:  1.0,
		Generation:     gen,
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
	fullClose := fraction >= 1.0 || newRemaining < 0.005

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
	// once per position. Matches the handoff spec line 249-251.
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
