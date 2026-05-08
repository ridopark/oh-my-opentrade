package builtin

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// TradingTheTrendStrategy is a Discord-author-following options strategy that
// uses a 3-phase break-and-retest state machine on the underlying to validate
// a posted watchlist signal before buying the locked OCC contract. Unlike
// copytrade_v1 (purely event-driven mirror), this strategy is bar-driven on
// the underlying and only consumes the sidecar event to seed its watchlist.
//
// Phase 3a scope: entry signals only. Mechanical exits (chandelier trail,
// premium hard-stop, EOD flatten, time-stop) are deferred to Phase 3b.
type TradingTheTrendStrategy struct {
	meta start.Meta
}

// NewTradingTheTrendStrategy constructs the registry-registered builtin.
func NewTradingTheTrendStrategy() *TradingTheTrendStrategy {
	id, _ := start.NewStrategyID("tradingthetrend_v1")
	ver, _ := start.NewVersion("1.0.0")
	return &TradingTheTrendStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "TradingTheTrend v1",
			Description: "Discord watchlist break-and-retest options strategy",
			Author:      "system",
		},
	}
}

func (s *TradingTheTrendStrategy) Meta() start.Meta { return s.meta }
func (s *TradingTheTrendStrategy) WarmupBars() int  { return 50 }

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// TradingTheTrendConfig holds entry-related parameters parsed from the spec
// params block. Exits, sizing, and risk gates land in follow-up phases.
type TradingTheTrendConfig struct {
	ExpiryDTE          int
	MinDTE             int
	ATRBreakoutMult    float64
	BreakoutBufferATR  float64
	BodyRangeRatio     float64
	VolSurgeMult       float64
	MaxWickRatio       float64
	RetestBandATR      float64
	RetestExpiryBars   int
	InvalidationATR    float64
	RetestQualityGate  bool
	EntryCutoffET      string // "HH:MM" hard cutoff, no entries after this ET wall-clock
	TriggerDriftPct    float64
	FreshnessMaxAgeSec int
}

func parseTradingTheTrendConfig(params map[string]any) TradingTheTrendConfig {
	return TradingTheTrendConfig{
		ExpiryDTE:          getInt(params, "expiry_dte", 0),
		MinDTE:             getInt(params, "min_dte", 2),
		ATRBreakoutMult:    getFloat64(params, "atr_breakout_mult", 1.5),
		BreakoutBufferATR:  getFloat64(params, "breakout_buffer_atr", 0.2),
		BodyRangeRatio:     getFloat64(params, "body_range_ratio", 0.5),
		VolSurgeMult:       getFloat64(params, "vol_surge_mult", 1.5),
		MaxWickRatio:       getFloat64(params, "max_wick_ratio", 0.4),
		RetestBandATR:      getFloat64(params, "retest_band_atr", 0.15),
		RetestExpiryBars:   getInt(params, "retest_expiry_bars", 20),
		InvalidationATR:    getFloat64(params, "invalidation_atr", 0.5),
		RetestQualityGate:  getBool(params, "retest_quality_gate", true),
		EntryCutoffET:      getString(params, "entry_cutoff_et", "13:30"),
		TriggerDriftPct:    getFloat64(params, "trigger_drift_pct", 0.5),
		FreshnessMaxAgeSec: getInt(params, "freshness_max_age_secs", 60),
	}
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

// TTTPhase is the per-symbol entry-state-machine phase.
type TTTPhase int

const (
	TTTPhaseIdle           TTTPhase = iota
	TTTPhaseWatching                // armed by signal, awaiting momentum breakout
	TTTPhaseWaitingRetest           // breakout confirmed, waiting for retest of trigger
	TTTPhaseConfirming              // retest entered, waiting for hold-confirm bar
)

// TradingTheTrendState is the per-symbol strategy state. One arm per ticker
// per session: a fresh signal on the same ticker overwrites the existing arm.
type TradingTheTrendState struct {
	Symbol     string                `json:"-"`
	Config     TradingTheTrendConfig `json:"-"`
	Indicators start.IndicatorData   `json:"-"`

	Phase TTTPhase

	Trigger             float64
	Strike              float64
	Right               domain.OptionRight
	BreakoutSide        start.Side
	SignalPostedAt      time.Time
	BarsSincePhaseEntry int

	PrevBar    start.Bar
	HasPrevBar bool

	// EnteredToday is set after a Signal emission to suppress repeat entries
	// on the same ticker within the same RTH session.
	EnteredToday bool
	LastSessionDay string // "YYYY-MM-DD" in ET, used to reset EnteredToday at session boundary

	PositionSide   start.Side
	PendingEntry   start.Side
	PendingEntryAt time.Time
}

// SetIndicators implements the indicatorSetter interface used by the runner.
func (s *TradingTheTrendState) SetIndicators(ind start.IndicatorData) {
	s.Indicators = ind
}

// ClearPendingEntry implements the pendingClearer interface used by the runner.
func (s *TradingTheTrendState) ClearPendingEntry() {
	s.PendingEntry = ""
	s.PendingEntryAt = time.Time{}
}

func (s *TradingTheTrendState) armPendingEntry(side start.Side, now time.Time, ctx start.Context) {
	armPendingEntry(&s.PositionSide, &s.PendingEntry, &s.PendingEntryAt, side, now, ctx)
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func (s *TradingTheTrendStrategy) Init(ctx start.Context, symbol string, params map[string]any, prior start.State) (start.State, error) {
	cfg := parseTradingTheTrendConfig(params)
	st := &TradingTheTrendState{
		Symbol: symbol,
		Config: cfg,
	}
	if prior != nil {
		if pst, ok := prior.(*TradingTheTrendState); ok {
			*st = *pst
			st.Config = cfg
		} else if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Warn("TradingTheTrendStrategy: incompatible prior state, starting fresh", "symbol", symbol)
		}
	}
	return st, nil
}

// ---------------------------------------------------------------------------
// ReplayOnBar (warmup, no signals)
// ---------------------------------------------------------------------------

func (s *TradingTheTrendStrategy) ReplayOnBar(_ start.Context, _ string, bar start.Bar, st start.State, indicators start.IndicatorData) (start.State, error) {
	tst, ok := st.(*TradingTheTrendState)
	if !ok {
		return st, fmt.Errorf("TradingTheTrendStrategy.ReplayOnBar: expected *TradingTheTrendState, got %T", st)
	}
	tst.Indicators = indicators
	tst.PrevBar = bar
	tst.HasPrevBar = true
	return tst, nil
}

// ---------------------------------------------------------------------------
// OnBar — state machine + entry decision
// ---------------------------------------------------------------------------

func (s *TradingTheTrendStrategy) OnBar(ctx start.Context, symbol string, bar start.Bar, st start.State) (start.State, []start.Signal, error) {
	tst, ok := st.(*TradingTheTrendState)
	if !ok {
		return st, nil, fmt.Errorf("TradingTheTrendStrategy.OnBar: expected *TradingTheTrendState, got %T", st)
	}
	cfg := tst.Config

	now := bar.Time
	if ctx != nil {
		now = ctx.Now()
	}

	resetIfNewSession(tst, now)

	if tst.Phase == TTTPhaseIdle {
		tst.PrevBar = bar
		tst.HasPrevBar = true
		return tst, nil, nil
	}

	if tst.PositionSide != "" || tst.PendingEntry != "" {
		tst.PrevBar = bar
		tst.HasPrevBar = true
		return tst, nil, nil
	}

	atr := tst.Indicators.ATR

	// Hard cutoff: no NEW phase advancement that ends in entry after the
	// configured ET cutoff. Phase machine is reset; armed signals from the
	// morning are dropped at this boundary on the same trading day.
	if afterEntryCutoff(now, cfg.EntryCutoffET) {
		tst.Phase = TTTPhaseIdle
		tst.PrevBar = bar
		tst.HasPrevBar = true
		return tst, nil, nil
	}

	advanceTTTStateMachine(tst, bar, atr, cfg)

	if tst.Phase == TTTPhaseConfirming && atr > 0 && !tst.EnteredToday {
		// Confirming bar: the bar that *enters* Confirming is the retest hit
		// itself. The hold-confirm rule applies to the NEXT bar, so we wait
		// at least one bar in Confirming before evaluating the hold-confirm.
		// BarsSincePhaseEntry==0 indicates we just transitioned in this call.
		if tst.BarsSincePhaseEntry > 0 {
			if checkTTTHoldConfirm(tst, bar, atr, cfg) {
				// Trigger-drift gate: reject if underlying is more than
				// trigger_drift_pct past trigger relative to confirm bar close.
				driftLimit := tst.Trigger * (1.0 + cfg.TriggerDriftPct/100.0)
				if cfg.TriggerDriftPct > 0 && bar.Close > driftLimit {
					if ctx != nil && ctx.Logger() != nil {
						ctx.Logger().Info("TradingTheTrendStrategy: drift gate rejected entry",
							"symbol", symbol, "trigger", tst.Trigger,
							"close", bar.Close, "drift_pct", cfg.TriggerDriftPct)
					}
					tst.Phase = TTTPhaseIdle
					tst.PrevBar = bar
					tst.HasPrevBar = true
					return tst, nil, nil
				}

				sig, err := s.buildEntrySignal(ctx, tst, symbol, bar, now)
				if err != nil {
					return tst, nil, err
				}
				tst.armPendingEntry(tst.BreakoutSide, now, ctx)
				tst.EnteredToday = true
				tst.Phase = TTTPhaseIdle
				tst.PrevBar = bar
				tst.HasPrevBar = true
				return tst, []start.Signal{sig}, nil
			}
			// Hold-confirm failed on the dedicated confirm bar. Per the
			// prereg's "one entry per signal per day" lock, drop back to
			// Idle rather than re-arming for another retest cycle.
			tst.Phase = TTTPhaseIdle
		}
	}

	tst.PrevBar = bar
	tst.HasPrevBar = true
	return tst, nil, nil
}

// ---------------------------------------------------------------------------
// OnEvent — receives TradingTheTrendSignal + broker feedback
// ---------------------------------------------------------------------------

func (s *TradingTheTrendStrategy) OnEvent(ctx start.Context, symbol string, evt any, st start.State) (start.State, []start.Signal, error) {
	tst, ok := st.(*TradingTheTrendState)
	if !ok {
		return st, nil, fmt.Errorf("TradingTheTrendStrategy.OnEvent: expected *TradingTheTrendState, got %T", st)
	}

	switch e := evt.(type) {
	case start.FillConfirmation:
		if tst.PendingEntry != "" {
			tst.PositionSide = tst.PendingEntry
			tst.PendingEntry = ""
			tst.PendingEntryAt = time.Time{}
			if ctx != nil && ctx.Logger() != nil {
				ctx.Logger().Info("TradingTheTrendStrategy: fill confirmed",
					"symbol", symbol, "side", tst.PositionSide, "price", e.Price)
			}
		}
		return tst, nil, nil

	case start.EntryRejection:
		if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Warn("TradingTheTrendStrategy: entry rejected",
				"symbol", symbol, "reason", e.Reason)
		}
		tst.PositionSide = ""
		tst.PendingEntry = ""
		tst.PendingEntryAt = time.Time{}
		return tst, nil, nil

	case start.TradingTheTrendSignal:
		return s.handleSignal(ctx, tst, symbol, e)

	default:
		return tst, nil, nil
	}
}

func (s *TradingTheTrendStrategy) handleSignal(ctx start.Context, tst *TradingTheTrendState, symbol string, sig start.TradingTheTrendSignal) (start.State, []start.Signal, error) {
	cfg := tst.Config

	if !strings.EqualFold(sig.Ticker, symbol) {
		return tst, nil, nil
	}

	now := time.Time{}
	if ctx != nil {
		now = ctx.Now()
	}

	if cfg.FreshnessMaxAgeSec > 0 && !sig.PostedAt.IsZero() && !now.IsZero() {
		age := now.Sub(sig.PostedAt)
		ttl := time.Duration(cfg.FreshnessMaxAgeSec) * time.Second
		if age > ttl {
			if ctx != nil && ctx.Logger() != nil {
				ctx.Logger().Info("TradingTheTrendStrategy: dropping stale signal",
					"symbol", symbol, "age", age, "ttl", ttl)
			}
			return tst, nil, nil
		}
	}

	resetIfNewSession(tst, now)

	if tst.EnteredToday {
		return tst, nil, nil
	}
	if afterEntryCutoff(now, cfg.EntryCutoffET) {
		return tst, nil, nil
	}

	right := normalizeRight(sig.Right)
	side := start.SideBuy
	if right == domain.OptionRightPut {
		side = start.SideSell
	}

	tst.Phase = TTTPhaseWatching
	tst.Trigger = sig.Trigger
	tst.Strike = sig.Strike
	tst.Right = right
	tst.BreakoutSide = side
	tst.SignalPostedAt = sig.PostedAt
	tst.BarsSincePhaseEntry = 0

	if ctx != nil && ctx.Logger() != nil {
		ctx.Logger().Info("TradingTheTrendStrategy: signal armed",
			"symbol", symbol, "trigger", sig.Trigger,
			"strike", sig.Strike, "right", string(right))
	}
	return tst, nil, nil
}

// ---------------------------------------------------------------------------
// State machine
// ---------------------------------------------------------------------------

func advanceTTTStateMachine(st *TradingTheTrendState, bar start.Bar, atr float64, cfg TradingTheTrendConfig) {
	st.BarsSincePhaseEntry++
	switch st.Phase {
	case TTTPhaseWatching:
		if atr <= 0 {
			return
		}
		if checkTTTMomentumBreakout(st, bar, atr, cfg) {
			st.Phase = TTTPhaseWaitingRetest
			st.BarsSincePhaseEntry = 0
		}

	case TTTPhaseWaitingRetest:
		if atr <= 0 {
			return
		}
		// Invalidation: deep retrace below trigger.
		if st.BreakoutSide == start.SideBuy && bar.Close < st.Trigger-cfg.InvalidationATR*atr {
			st.Phase = TTTPhaseIdle
			return
		}
		if st.BreakoutSide == start.SideSell && bar.Close > st.Trigger+cfg.InvalidationATR*atr {
			st.Phase = TTTPhaseIdle
			return
		}
		// Expiry: too many bars without a retest.
		if st.BarsSincePhaseEntry > cfg.RetestExpiryBars {
			st.Phase = TTTPhaseIdle
			return
		}
		if isTTTInRetestZone(st, bar, atr, cfg) {
			st.Phase = TTTPhaseConfirming
			st.BarsSincePhaseEntry = 0
		}

	case TTTPhaseConfirming:
		// Hold-confirm checked by OnBar after this advances. If the bar
		// fails the hold-confirm, drop back to Idle for the day rather
		// than re-attempting from Watching: the prereg locks one entry
		// per signal per day, so re-entering would violate that.
	}
}

// checkTTTMomentumBreakout is the prereg-locked Phase A momentum filter.
func checkTTTMomentumBreakout(st *TradingTheTrendState, bar start.Bar, atr float64, cfg TradingTheTrendConfig) bool {
	bodySize := math.Abs(bar.Close - bar.Open)
	barRange := bar.High - bar.Low
	if barRange <= 0 {
		return false
	}
	if bodySize/barRange < cfg.BodyRangeRatio {
		return false
	}
	if barRange < cfg.ATRBreakoutMult*atr {
		return false
	}
	if st.Indicators.VolumeSMA > 0 && bar.Volume < cfg.VolSurgeMult*st.Indicators.VolumeSMA {
		return false
	}
	wickSize := barRange - bodySize
	if wickSize/barRange > cfg.MaxWickRatio {
		return false
	}
	if st.BreakoutSide == start.SideBuy {
		if bar.Close <= st.Trigger+cfg.BreakoutBufferATR*atr {
			return false
		}
		if bar.Close <= bar.Open {
			return false
		}
	} else {
		if bar.Close >= st.Trigger-cfg.BreakoutBufferATR*atr {
			return false
		}
		if bar.Close >= bar.Open {
			return false
		}
	}
	return true
}

// isTTTInRetestZone is the prereg-locked Phase B retest check.
func isTTTInRetestZone(st *TradingTheTrendState, bar start.Bar, atr float64, cfg TradingTheTrendConfig) bool {
	band := cfg.RetestBandATR * atr
	if st.BreakoutSide == start.SideBuy {
		return bar.Low <= st.Trigger+band && bar.Low >= st.Trigger-band
	}
	return bar.High >= st.Trigger-band && bar.High <= st.Trigger+band
}

// checkTTTHoldConfirm is the prereg-locked Phase C confirmation rule:
// next bar must close above trigger + breakout_buffer_atr * ATR with
// body/range >= body_range_ratio (bullish for calls; inverse for puts).
// Retest-quality gate is locked ON per prereg — when true, an additional
// directional check is enforced (close > prev close for calls).
func checkTTTHoldConfirm(st *TradingTheTrendState, bar start.Bar, atr float64, cfg TradingTheTrendConfig) bool {
	bodySize := math.Abs(bar.Close - bar.Open)
	barRange := bar.High - bar.Low
	if barRange <= 0 {
		return false
	}
	if bodySize/barRange < cfg.BodyRangeRatio {
		return false
	}
	if st.BreakoutSide == start.SideBuy {
		if bar.Close <= st.Trigger+cfg.BreakoutBufferATR*atr {
			return false
		}
		if bar.Close <= bar.Open {
			return false
		}
		if cfg.RetestQualityGate && st.HasPrevBar && bar.Close <= st.PrevBar.Close {
			return false
		}
	} else {
		if bar.Close >= st.Trigger-cfg.BreakoutBufferATR*atr {
			return false
		}
		if bar.Close >= bar.Open {
			return false
		}
		if cfg.RetestQualityGate && st.HasPrevBar && bar.Close >= st.PrevBar.Close {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Entry construction
// ---------------------------------------------------------------------------

func (s *TradingTheTrendStrategy) buildEntrySignal(ctx start.Context, tst *TradingTheTrendState, symbol string, bar start.Bar, now time.Time) (start.Signal, error) {
	expiry := nearestFridayWithMinDTE(now, tst.Config.ExpiryDTE, tst.Config.MinDTE)
	contractSym := domain.FormatOCCSymbol(strings.ToUpper(symbol), expiry, tst.Right, tst.Strike)

	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
	tags := map[string]string{
		"setup":           "tradingthetrend_break_retest",
		"contract_symbol": contractSym,
		"force_expiry":    expiry.Format("2006-01-02"),
		"force_strike":    strconv.FormatFloat(tst.Strike, 'f', -1, 64),
		"force_right":     string(tst.Right),
		"trigger":         strconv.FormatFloat(tst.Trigger, 'f', -1, 64),
		"ref_price":       fmt.Sprintf("%.4f", bar.Close),
	}
	if !tst.SignalPostedAt.IsZero() {
		tags["posted_at"] = tst.SignalPostedAt.UTC().Format(time.RFC3339)
	}
	sig, err := start.NewSignal(instanceID, strings.ToUpper(symbol), start.SignalEntry, tst.BreakoutSide, 0.85, tags)
	if err != nil {
		return start.Signal{}, fmt.Errorf("tradingthetrend: NewSignal: %w", err)
	}
	if ctx != nil && ctx.Logger() != nil {
		ctx.Logger().Info("TradingTheTrendStrategy: entry signal emitted",
			"symbol", symbol, "contract", contractSym,
			"expiry", expiry.Format("2006-01-02"))
	}
	return sig, nil
}

// nearestFridayWithMinDTE returns the next Friday at least minDTE days from
// `now`, plus an additional dteOffset weeks (dteOffset/7 rounded). The plan
// uses ExpiryDTE=0 to mean "nearest weekly Friday with min_dte enforced";
// positive ExpiryDTE adds approximately that many days but still rolls
// forward to a Friday.
func nearestFridayWithMinDTE(now time.Time, dteOffset, minDTE int) time.Time {
	if minDTE < 0 {
		minDTE = 0
	}
	target := now.AddDate(0, 0, dteOffset)
	for target.Weekday() != time.Friday {
		target = target.AddDate(0, 0, 1)
	}
	// Compute DTE in calendar days from now to the target Friday at 00:00.
	startDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for dte := int(target.Sub(startDay) / (24 * time.Hour)); dte < minDTE; {
		target = target.AddDate(0, 0, 7)
		dte = int(target.Sub(startDay) / (24 * time.Hour))
	}
	return target
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func resetIfNewSession(st *TradingTheTrendState, now time.Time) {
	if now.IsZero() {
		return
	}
	day := now.In(etLocation).Format("2006-01-02")
	if st.LastSessionDay == "" {
		st.LastSessionDay = day
		return
	}
	if st.LastSessionDay != day {
		st.LastSessionDay = day
		st.EnteredToday = false
		// Phase machine resets at session boundary per prereg.
		st.Phase = TTTPhaseIdle
		st.BarsSincePhaseEntry = 0
	}
}

// afterEntryCutoff returns true when the ET wall-clock at `now` is at or past
// cutoff "HH:MM". A blank cutoff disables the gate.
func afterEntryCutoff(now time.Time, cutoff string) bool {
	if now.IsZero() || cutoff == "" {
		return false
	}
	hh, mm, ok := parseHHMM(cutoff)
	if !ok {
		return false
	}
	local := now.In(etLocation)
	if local.Hour() > hh {
		return true
	}
	if local.Hour() == hh && local.Minute() >= mm {
		return true
	}
	return false
}

var hhmmRe = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)

func parseHHMM(s string) (int, int, bool) {
	m := hhmmRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, false
	}
	hh, _ := strconv.Atoi(m[1])
	mm, _ := strconv.Atoi(m[2])
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, 0, false
	}
	return hh, mm, true
}

// ---------------------------------------------------------------------------
// Parser (Go port of services/discord-tradingthetrend/parser.py)
// ---------------------------------------------------------------------------

// TradingTheTrendParsed mirrors the Python ParsedSignal output. The strategy
// itself does not call this — the Go-side parser exists for parity tests
// against the Python implementation's golden cases.
type TradingTheTrendParsed struct {
	Ticker  string
	Right   string
	Strike  float64
	Trigger float64
	RawLine string
}

var tttLineRe = regexp.MustCompile(`(?i)^\s*([A-Z]{1,6})\s+(\d+(?:\.\d+)?)([CP])\s*>\s*(\d+(?:\.\d+)?)\s*$`)

// ParseTradingTheTrendMessage parses a multi-line Discord message body and
// returns one TradingTheTrendParsed per matching line.
func ParseTradingTheTrendMessage(text string) []TradingTheTrendParsed {
	var out []TradingTheTrendParsed
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		m := tttLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		strike, _ := strconv.ParseFloat(m[2], 64)
		trigger, _ := strconv.ParseFloat(m[4], 64)
		out = append(out, TradingTheTrendParsed{
			Ticker:  strings.ToUpper(m[1]),
			Right:   strings.ToUpper(m[3]),
			Strike:  strike,
			Trigger: trigger,
			RawLine: line,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Serialization
// ---------------------------------------------------------------------------

type tradingTheTrendStateJSON struct {
	Symbol              string              `json:"symbol"`
	Phase               TTTPhase            `json:"phase"`
	Trigger             float64             `json:"trigger"`
	Strike              float64             `json:"strike"`
	Right               domain.OptionRight  `json:"right"`
	BreakoutSide        start.Side          `json:"breakout_side"`
	SignalPostedAt      time.Time           `json:"signal_posted_at"`
	BarsSincePhaseEntry int                 `json:"bars_since_phase_entry"`
	PrevBar             barJSON             `json:"prev_bar"`
	HasPrevBar          bool                `json:"has_prev_bar"`
	EnteredToday        bool                `json:"entered_today"`
	LastSessionDay      string              `json:"last_session_day"`
	PositionSide        start.Side          `json:"position_side"`
	PendingEntry        start.Side          `json:"pending_entry"`
	PendingEntryAt      time.Time           `json:"pending_entry_at"`
	Indicators          start.IndicatorData `json:"indicators"`
}

func (s *TradingTheTrendState) Marshal() ([]byte, error) {
	j := tradingTheTrendStateJSON{
		Symbol:              s.Symbol,
		Phase:               s.Phase,
		Trigger:             s.Trigger,
		Strike:              s.Strike,
		Right:               s.Right,
		BreakoutSide:        s.BreakoutSide,
		SignalPostedAt:      s.SignalPostedAt,
		BarsSincePhaseEntry: s.BarsSincePhaseEntry,
		PrevBar:             barToJSON(s.PrevBar),
		HasPrevBar:          s.HasPrevBar,
		EnteredToday:        s.EnteredToday,
		LastSessionDay:      s.LastSessionDay,
		PositionSide:        s.PositionSide,
		PendingEntry:        s.PendingEntry,
		PendingEntryAt:      s.PendingEntryAt,
		Indicators:          s.Indicators,
	}
	return json.Marshal(j)
}

func (s *TradingTheTrendState) Unmarshal(data []byte) error {
	var j tradingTheTrendStateJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return fmt.Errorf("TradingTheTrendState.Unmarshal: %w", err)
	}
	s.Symbol = j.Symbol
	s.Phase = j.Phase
	s.Trigger = j.Trigger
	s.Strike = j.Strike
	s.Right = j.Right
	s.BreakoutSide = j.BreakoutSide
	s.SignalPostedAt = j.SignalPostedAt
	s.BarsSincePhaseEntry = j.BarsSincePhaseEntry
	s.PrevBar = jsonToBar(j.PrevBar)
	s.HasPrevBar = j.HasPrevBar
	s.EnteredToday = j.EnteredToday
	s.LastSessionDay = j.LastSessionDay
	s.PositionSide = j.PositionSide
	s.PendingEntry = j.PendingEntry
	s.PendingEntryAt = j.PendingEntryAt
	s.Indicators = j.Indicators
	return nil
}
