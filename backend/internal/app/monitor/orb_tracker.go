package monitor

import (
	"log/slog"
	"math"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

type ORBState string

const (
	ORBStatePreOpen         ORBState = "PRE_OPEN"
	ORBStateFormingRange    ORBState = "FORMING_RANGE"
	ORBStateRangeSet        ORBState = "RANGE_SET"
	ORBStateBreakoutSeen    ORBState = "BREAKOUT_SEEN"
	ORBStateAwaitingRetest  ORBState = "AWAITING_RETEST"
	ORBStateRetestConfirmed ORBState = "RETEST_CONFIRMED"
	ORBStateSignalFired     ORBState = "SIGNAL_FIRED"
	ORBStateDoneForSession  ORBState = "DONE_FOR_SESSION"
	ORBStateInvalid         ORBState = "INVALID"
)

type ORBConfig struct {
	WindowMinutes        int
	MinRVOL              float64
	MinConfidence        float64
	BreakoutConfirmBps   int
	TouchToleranceBps    int
	HoldConfirmBps       int
	MaxRetestBars        int
	AllowMissingBars     int
	MaxSignalsPerSession int
	HTFBiasEnabled       bool
	ATRMultiplier        float64
	SweepCooldownBars    int
	RetestConfirmBars    int // 1 = touch+hold same bar (default), 2 = touch then hold next bar
	VWAPFilterEnabled    bool    // require VWAP alignment at breakout and retest
	MaxRangeATRMult      float64 // skip if OR range > this × ATR (0 = disabled)
	MinRangePctBps       int     // skip if OR range < this bps of midpoint (0 = disabled)
	VIXSkipAbove         float64  // skip ORB entirely when VIX > this (0 = disabled)
	VIXWidenAbove        float64  // widen stops when VIX > this (0 = disabled)
	AllowedRegimes       []string // only allow entries in these regimes (empty = allow all)
	FVGEnabled           bool     // require FVG overlap + engulfing confirmation for retest
	FVGMinRVOL           float64  // min RVOL for FVG displacement bar (default 1.5)
}

func DefaultORBConfig() ORBConfig {
	return ORBConfig{
		WindowMinutes:        30,
		MinRVOL:              1.5,
		MinConfidence:        0.65,
		BreakoutConfirmBps:   2,
		TouchToleranceBps:    2,
		HoldConfirmBps:       0,
		MaxRetestBars:        15,
		AllowMissingBars:     1,
		MaxSignalsPerSession: 1,
	}
}

func orbExtractInt(params map[string]any, key string, fallback int) int {
	if params == nil {
		return fallback
	}
	v, ok := params[key]
	if !ok || v == nil {
		return fallback
	}
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	case float32:
		return int(x)
	default:
		return fallback
	}
}

func orbExtractFloat(params map[string]any, key string, fallback float64) float64 {
	if params == nil {
		return fallback
	}
	v, ok := params[key]
	if !ok || v == nil {
		return fallback
	}
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	default:
		return fallback
	}
}

func orbExtractStringSlice(params map[string]any, key string) []string {
	if params == nil {
		return nil
	}
	v, ok := params[key]
	if !ok || v == nil {
		return nil
	}
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func orbExtractBool(params map[string]any, key string, fallback bool) bool {
	if params == nil {
		return fallback
	}
	v, ok := params[key]
	if !ok || v == nil {
		return fallback
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return fallback
}

func NewORBConfigFromDNA(params map[string]any) ORBConfig {
	def := DefaultORBConfig()
	return ORBConfig{
		WindowMinutes:        orbExtractInt(params, "orb_window_minutes", def.WindowMinutes),
		MinRVOL:              orbExtractFloat(params, "min_rvol", def.MinRVOL),
		MinConfidence:        orbExtractFloat(params, "min_confidence", def.MinConfidence),
		BreakoutConfirmBps:   orbExtractInt(params, "breakout_confirm_bps", def.BreakoutConfirmBps),
		TouchToleranceBps:    orbExtractInt(params, "touch_tolerance_bps", def.TouchToleranceBps),
		HoldConfirmBps:       orbExtractInt(params, "hold_confirm_bps", def.HoldConfirmBps),
		MaxRetestBars:        orbExtractInt(params, "max_retest_bars", def.MaxRetestBars),
		AllowMissingBars:     orbExtractInt(params, "allow_missing_range_bars", def.AllowMissingBars),
		MaxSignalsPerSession: orbExtractInt(params, "max_signals_per_session", def.MaxSignalsPerSession),
		HTFBiasEnabled:       orbExtractBool(params, "htf_bias_enabled", false),
		ATRMultiplier:        orbExtractFloat(params, "atr_multiplier", 0),
		SweepCooldownBars:    orbExtractInt(params, "sweep_cooldown_bars", 0),
		RetestConfirmBars:    orbExtractInt(params, "retest_confirm_bars", 1),
		VWAPFilterEnabled:    orbExtractBool(params, "vwap_filter_enabled", false),
		MaxRangeATRMult:      orbExtractFloat(params, "max_range_atr_mult", 0),
		MinRangePctBps:       orbExtractInt(params, "min_range_pct_bps", 0),
		VIXSkipAbove:         orbExtractFloat(params, "vix_skip_above", 0),
		VIXWidenAbove:        orbExtractFloat(params, "vix_widen_above", 0),
		AllowedRegimes:       orbExtractStringSlice(params, "regime_filter.allowed_regimes"),
		FVGEnabled:           orbExtractBool(params, "fvg_enabled", false),
		FVGMinRVOL:           orbExtractFloat(params, "fvg_min_rvol", 1.5),
	}
}

type BreakoutInfo struct {
	Direction  domain.Direction
	BreakBar   time.Time
	BreakClose float64
	RVOL       float64
	Confirmed  bool
}

// FairValueGap represents an imbalance zone created by an impulsive 3-candle move.
// Bullish FVG: bar[0].High < bar[2].Low (gap up — zone between them)
// Bearish FVG: bar[0].Low > bar[2].High (gap down — zone between them)
type FairValueGap struct {
	Direction domain.Direction // LONG (bullish FVG) or SHORT (bearish FVG)
	High      float64         // upper bound of FVG zone
	Low       float64         // lower bound of FVG zone
	RVOL      float64         // relative volume of the displacement (middle) bar
	Time      time.Time       // time of the displacement bar
}

// OverlapsLevel returns true if the FVG zone contains the given price level.
func (fvg FairValueGap) OverlapsLevel(level float64) bool {
	return level >= fvg.Low && level <= fvg.High
}

type RetestInfo struct {
	TouchBar       time.Time
	TouchPrice     float64
	Touched        bool // true once the retest touch has occurred (waiting for next bar hold)
	HoldBar        time.Time
	HoldClose      float64
	BarsSinceBreak int
	Confirmed      bool
}

type ORBSession struct {
	Symbol            string
	SessionKey        string
	RTHOpenUTC        time.Time
	RTHEndUTC         time.Time
	State             ORBState
	RangeBarCount     int
	OrbHigh           float64
	OrbLow            float64
	Breakout          BreakoutInfo
	Retest            RetestInfo
	SignalsFired      int
	BarsSinceBreakout int
	SweepCooldown     int
	PrevVWAP          float64 // previous bar's VWAP for slope calculation
	RangeInvalid      bool    // true if OR range failed ATR/size check

	// FVG tracking
	RecentBars [3]domain.MarketBar // rolling window of last 3 bars for FVG detection
	BarCount   int                 // total bars seen (for rolling window)
	ActiveFVG  *FairValueGap       // FVG overlapping ORH/ORL found after breakout
	PrevBar    *domain.MarketBar   // previous bar for engulfing detection
	FVGStop    float64             // stop-loss level from FVG far edge
}

type ORBTracker struct {
	sessions map[string]*ORBSession
	logger   *slog.Logger
}

func NewORBTracker() *ORBTracker {
	return &ORBTracker{sessions: make(map[string]*ORBSession), logger: slog.Default()}
}

// NewORBTrackerWithSource creates an ORBTracker whose log lines include a
// "source" field (e.g. "monitor" or "strategy") so callers can distinguish
// which tracker produced a given log entry.
func NewORBTrackerWithSource(source string) *ORBTracker {
	return &ORBTracker{sessions: make(map[string]*ORBSession), logger: slog.Default().With("source", source)}
}

func (t *ORBTracker) SetLogger(l *slog.Logger) {
	if l != nil {
		t.logger = l
	}
}

func (t *ORBTracker) GetSession(symbol string) *ORBSession {
	if t == nil {
		return nil
	}
	return t.sessions[symbol]
}

func (t *ORBTracker) ResetSession(symbol string) {
	if t == nil {
		return
	}
	delete(t.sessions, symbol)
}

// cycleToRangeSet resets the breakout/retest tracking within a session so the
// tracker watches for the next breakout from the same opening range.
// The range itself (OrbHigh/OrbLow) is preserved.
func (t *ORBTracker) cycleToRangeSet(sess *ORBSession) {
	sess.State = ORBStateRangeSet
	sess.Breakout = BreakoutInfo{}
	sess.Retest = RetestInfo{}
	sess.BarsSinceBreakout = 0
	sess.ActiveFVG = nil
	sess.PrevBar = nil
	sess.FVGStop = 0
}

func (t *ORBTracker) OnBar(bar domain.MarketBar, snap domain.IndicatorSnapshot, cfg ORBConfig, replay bool) (*SetupCondition, bool) {
	if t.sessions == nil {
		t.sessions = make(map[string]*ORBSession)
	}

	sym := bar.Symbol.String()

	// ORB is an equity-only strategy — crypto has no opening range.
	if bar.Symbol.IsCryptoSymbol() {
		return nil, false
	}

	key := SessionKeyET(bar.Time)
	sess, ok := t.sessions[sym]
	if !ok || sess == nil || sess.SessionKey != key {
		sess = &ORBSession{
			Symbol:     sym,
			SessionKey: key,
			RTHOpenUTC: RTHOpenUTC(bar.Time),
			RTHEndUTC:  RTHEndUTC(bar.Time),
			State:      ORBStatePreOpen,
		}
		t.sessions[sym] = sess
		t.logger.Info("orb: new session", "symbol", sym, "key", key, "state", sess.State)
	}

	// Track VWAP for slope calculation (update every bar, regardless of state)
	defer func() {
		sess.PrevVWAP = snap.VWAP
		// Shift rolling bar window for FVG detection
		sess.RecentBars[0] = sess.RecentBars[1]
		sess.RecentBars[1] = sess.RecentBars[2]
		sess.RecentBars[2] = bar
		sess.BarCount++
	}()

	if sess.State == ORBStateSignalFired || sess.State == ORBStateDoneForSession || sess.State == ORBStateInvalid {
		t.logger.Debug("orb: terminal state", "symbol", sym, "state", sess.State)
		return nil, false
	}

	within := IsWithinORBWindow(bar.Time, cfg.WindowMinutes)

	switch sess.State {
	case ORBStatePreOpen:
		if within {
			sess.State = ORBStateFormingRange
			sess.OrbHigh = bar.High
			sess.OrbLow = bar.Low
			sess.RangeBarCount = 1
			t.logger.Info("orb: forming range", "symbol", sym, "high", sess.OrbHigh, "low", sess.OrbLow)
		}
		return nil, false

	case ORBStateFormingRange:
		if within {
			sess.OrbHigh = math.Max(sess.OrbHigh, bar.High)
			sess.OrbLow = math.Min(sess.OrbLow, bar.Low)
			sess.RangeBarCount++
			return nil, false
		}

		// Compute expected bar count from window minutes and bar duration.
		// e.g. 15-min window with 5m bars → 3 expected bars.
		barMinutes := barDurationMinutes(bar.Timeframe)
		expectedBars := cfg.WindowMinutes / barMinutes
		if expectedBars < 1 {
			expectedBars = 1
		}
		required := expectedBars - cfg.AllowMissingBars
		if required < 1 {
			required = 1
		}
		if sess.RangeBarCount >= required {
			sess.State = ORBStateRangeSet
			t.logger.Info("orb: range set", "symbol", sym, "high", sess.OrbHigh, "low", sess.OrbLow, "bars", sess.RangeBarCount)

			// OR range vs daily ATR check: skip if range is too wide or too narrow.
			// Uses daily ATR from HTF data (not the 1m ATR in snap.ATR which is wrong scale).
			dailyATR := 0.0
			if htf, ok := snap.HTF[domain.Timeframe("1d")]; ok {
				dailyATR = htf.DailyATR
			}
			if !orbRangeValid(sess.OrbHigh, sess.OrbLow, dailyATR, cfg.MaxRangeATRMult, cfg.MinRangePctBps) {
				orRange := sess.OrbHigh - sess.OrbLow
				sess.State = ORBStateInvalid
				sess.RangeInvalid = true
				t.logger.Warn("orb: range invalid (size check)", "symbol", sym,
					"or_range", orRange, "daily_atr", dailyATR,
					"max_atr_mult", cfg.MaxRangeATRMult, "min_bps", cfg.MinRangePctBps)
				return nil, false
			}
			return t.onRangeSetBar(sess, bar, snap, cfg)
		}
		sess.State = ORBStateInvalid
		t.logger.Warn("orb: invalid range", "symbol", sym, "bars", sess.RangeBarCount, "required", required)
		return nil, false

	case ORBStateRangeSet:
		return t.onRangeSetBar(sess, bar, snap, cfg)

	case ORBStateAwaitingRetest:
		if sess.SignalsFired >= cfg.MaxSignalsPerSession {
			sess.State = ORBStateDoneForSession
			return nil, false
		}
		sess.BarsSinceBreakout++
		if sess.BarsSinceBreakout > cfg.MaxRetestBars {
			t.logger.Info("orb: retest timeout, cycling to RANGE_SET", "symbol", sym, "bars_since_breakout", sess.BarsSinceBreakout, "max", cfg.MaxRetestBars)
			t.cycleToRangeSet(sess)
			return nil, false
		}

		confirmBps := float64(cfg.BreakoutConfirmBps) / 10000.0
		switch sess.Breakout.Direction {
		case domain.DirectionLong:
			if bar.Close < sess.OrbLow*(1.0-confirmBps) {
				t.logger.Info("orb: breakout invalidated, cycling to RANGE_SET", "symbol", sym, "direction", "LONG")
				t.cycleToRangeSet(sess)
				return nil, false
			}
		case domain.DirectionShort:
			if bar.Close > sess.OrbHigh*(1.0+confirmBps) {
				t.logger.Info("orb: breakout invalidated, cycling to RANGE_SET", "symbol", sym, "direction", "SHORT")
				t.cycleToRangeSet(sess)
				return nil, false
			}
		}

		// --- FVG-enhanced retest logic ---
		if cfg.FVGEnabled {
			return t.onAwaitingRetestFVG(sess, bar, snap, cfg, sym, replay)
		}

		// --- Legacy retest logic (non-FVG) ---
		touchTol := float64(cfg.TouchToleranceBps) / 10000.0
		holdBps := float64(cfg.HoldConfirmBps) / 10000.0

		var touchThisBar bool
		var touchPrice float64
		var holdThisBar bool
		if sess.Breakout.Direction == domain.DirectionLong {
			level := sess.OrbHigh * (1.0 + touchTol)
			touchThisBar = bar.Low <= level
			touchPrice = bar.Low
			holdThisBar = bar.Close > sess.OrbHigh*(1.0+holdBps) && bar.Close > bar.Open
		} else {
			level := sess.OrbLow * (1.0 - touchTol)
			touchThisBar = bar.High >= level
			touchPrice = bar.High
			holdThisBar = bar.Close < sess.OrbLow*(1.0-holdBps) && bar.Close < bar.Open
		}

		if touchThisBar && !sess.Retest.Touched {
			sess.Retest.TouchBar = bar.Time
			sess.Retest.TouchPrice = touchPrice
			sess.Retest.Touched = true
			sess.Retest.BarsSinceBreak = sess.BarsSinceBreakout
			if cfg.RetestConfirmBars >= 2 {
				t.logger.Info("orb: retest touch (waiting for confirm bar)", "symbol", sym, "direction", sess.Breakout.Direction, "touch_price", touchPrice)
				return nil, false
			}
		}

		if sess.Retest.Touched && holdThisBar && cfg.VWAPFilterEnabled {
			dir := sess.Breakout.Direction
			if !vwapPositionOK(dir, bar.Close, snap.VWAP) || !vwapSlopeOK(dir, snap.VWAP, sess.PrevVWAP) {
				t.logger.Info("orb: retest hold rejected (VWAP misaligned)", "symbol", sym,
					"direction", dir, "close", bar.Close, "vwap", snap.VWAP, "prev_vwap", sess.PrevVWAP)
				return nil, false
			}
		}
		if sess.Retest.Touched && holdThisBar {
			sess.Retest.HoldBar = bar.Time
			sess.Retest.HoldClose = bar.Close
			sess.Retest.Confirmed = true
			sess.State = ORBStateRetestConfirmed
			t.logger.Info("orb: retest confirmed", "symbol", sym, "direction", sess.Breakout.Direction, "hold_close", bar.Close, "confidence", orbConfidence(sess, bar, snap, cfg))
			setup := &SetupCondition{
				Symbol:     bar.Symbol,
				Timeframe:  bar.Timeframe,
				Direction:  sess.Breakout.Direction,
				Trigger:    "orb_break_retest",
				Snapshot:   snap,
				Regime:     domain.MarketRegime{},
				BarClose:   bar.Close,
				ORBHigh:    sess.OrbHigh,
				ORBLow:     sess.OrbLow,
				RVOL:       sess.Breakout.RVOL,
				Confidence: orbConfidence(sess, bar, snap, cfg),
			}
			if setup.Confidence < cfg.MinConfidence {
				t.logger.Info("orb: low confidence, cycling to RANGE_SET", "symbol", sym, "confidence", setup.Confidence, "min", cfg.MinConfidence)
				t.cycleToRangeSet(sess)
				return nil, false
			}
			if replay {
				t.logger.Info("orb: replay signal suppressed, cycling to RANGE_SET", "symbol", sym, "direction", sess.Breakout.Direction)
				t.cycleToRangeSet(sess)
				return nil, false
			}
			sess.SignalsFired++
			if sess.SignalsFired >= cfg.MaxSignalsPerSession {
				sess.State = ORBStateDoneForSession
				t.logger.Info("orb: max signals reached", "symbol", sym, "fired", sess.SignalsFired, "max", cfg.MaxSignalsPerSession)
			} else {
				t.cycleToRangeSet(sess)
				t.logger.Info("orb: signal fired, cycling to RANGE_SET", "symbol", sym, "fired", sess.SignalsFired, "max", cfg.MaxSignalsPerSession)
			}
			return setup, true
		}

		return nil, false
	default:
		return nil, false
	}
}

func (t *ORBTracker) onRangeSetBar(sess *ORBSession, bar domain.MarketBar, snap domain.IndicatorSnapshot, cfg ORBConfig) (*SetupCondition, bool) {
	if cfg.SweepCooldownBars > 0 {
		wickAbove := bar.High > sess.OrbHigh && bar.Close <= sess.OrbHigh
		wickBelow := bar.Low < sess.OrbLow && bar.Close >= sess.OrbLow
		if wickAbove || wickBelow {
			sess.SweepCooldown = cfg.SweepCooldownBars
			t.logger.Info("orb: liquidity sweep detected", "symbol", sess.Symbol, "high", bar.High, "low", bar.Low, "close", bar.Close, "cooldown", cfg.SweepCooldownBars)
			return nil, false
		}

		if sess.SweepCooldown > 0 {
			sess.SweepCooldown--
			return nil, false
		}
	}

	var rvol float64
	if snap.VolumeSMA > 0 {
		rvol = bar.Volume / snap.VolumeSMA
	}
	confirmBps := float64(cfg.BreakoutConfirmBps) / 10000.0
	longBreak := bar.Close > sess.OrbHigh*(1.0+confirmBps) && rvol >= cfg.MinRVOL
	shortBreak := bar.Close < sess.OrbLow*(1.0-confirmBps) && rvol >= cfg.MinRVOL

	// VWAP position filter at breakout: price must be on the correct side of VWAP.
	// No slope requirement here — VWAP slope is too noisy early in the session.
	if cfg.VWAPFilterEnabled && (longBreak || shortBreak) {
		dir := domain.DirectionLong
		if shortBreak {
			dir = domain.DirectionShort
		}
		if !vwapPositionOK(dir, bar.Close, snap.VWAP) {
			t.logger.Info("orb: breakout rejected (price wrong side of VWAP)", "symbol", sess.Symbol,
				"direction", dir, "close", bar.Close, "vwap", snap.VWAP)
			return nil, false
		}
	}

	if longBreak {
		sess.Breakout = BreakoutInfo{Direction: domain.DirectionLong, BreakBar: bar.Time, BreakClose: bar.Close, RVOL: rvol, Confirmed: true}
		sess.State = ORBStateBreakoutSeen
		sess.State = ORBStateAwaitingRetest
		sess.BarsSinceBreakout = 0
		t.logger.Info("orb: breakout", "symbol", sess.Symbol, "direction", sess.Breakout.Direction, "close", bar.Close, "rvol", rvol)
		return nil, false
	}
	if shortBreak {
		sess.Breakout = BreakoutInfo{Direction: domain.DirectionShort, BreakBar: bar.Time, BreakClose: bar.Close, RVOL: rvol, Confirmed: true}
		sess.State = ORBStateBreakoutSeen
		sess.State = ORBStateAwaitingRetest
		sess.BarsSinceBreakout = 0
		t.logger.Info("orb: breakout", "symbol", sess.Symbol, "direction", sess.Breakout.Direction, "close", bar.Close, "rvol", rvol)
		return nil, false
	}
	return nil, false
}

// onAwaitingRetestFVG handles the FVG-enhanced retest logic.
// Flow:
//  1. Scan for FVGs after each bar — look for an FVG overlapping ORH (long) or ORL (short)
//  2. Once found, wait for price to enter the FVG zone (retest)
//  3. Confirm with engulfing candle (current bar engulfs previous bar in trade direction)
//  4. Set stop-loss at the far edge of the FVG
func (t *ORBTracker) onAwaitingRetestFVG(sess *ORBSession, bar domain.MarketBar, snap domain.IndicatorSnapshot, cfg ORBConfig, sym string, replay bool) (*SetupCondition, bool) {
	dir := sess.Breakout.Direction

	// Step 1: If no active FVG yet, scan for one
	if sess.ActiveFVG == nil {
		fvg := detectFVG(sess.RecentBars, sess.BarCount, snap.VolumeSMA, cfg.FVGMinRVOL)
		if fvg != nil && fvg.Direction == dir {
			// Check FVG overlaps the ORB level
			level := sess.OrbHigh
			if dir == domain.DirectionShort {
				level = sess.OrbLow
			}
			if fvg.OverlapsLevel(level) {
				sess.ActiveFVG = fvg
				t.logger.Info("orb: FVG detected overlapping ORB level", "symbol", sym,
					"direction", dir, "fvg_high", fvg.High, "fvg_low", fvg.Low,
					"level", level, "fvg_rvol", fvg.RVOL)
			}
		}
		sess.PrevBar = &bar
		return nil, false
	}

	// Step 2: FVG is active — check if price retests into the FVG zone
	fvg := sess.ActiveFVG
	var priceInFVG bool
	if dir == domain.DirectionLong {
		// For longs, retest = price dips DOWN into the FVG zone
		priceInFVG = bar.Low <= fvg.High
	} else {
		// For shorts, retest = price pokes UP into the FVG zone
		priceInFVG = bar.High >= fvg.Low
	}

	if !priceInFVG {
		sess.PrevBar = &bar
		return nil, false
	}

	// Price is in FVG zone — record touch if not already touched
	if !sess.Retest.Touched {
		sess.Retest.Touched = true
		sess.Retest.TouchBar = bar.Time
		if dir == domain.DirectionLong {
			sess.Retest.TouchPrice = bar.Low
		} else {
			sess.Retest.TouchPrice = bar.High
		}
		sess.Retest.BarsSinceBreak = sess.BarsSinceBreakout
		t.logger.Info("orb: FVG retest touch", "symbol", sym, "direction", dir,
			"touch_price", sess.Retest.TouchPrice, "fvg_high", fvg.High, "fvg_low", fvg.Low)
		sess.PrevBar = &bar
		return nil, false
	}

	// Step 3: Already touched — check for engulfing confirmation
	if sess.PrevBar == nil {
		sess.PrevBar = &bar
		return nil, false
	}

	if !isEngulfing(dir, bar, *sess.PrevBar) {
		sess.PrevBar = &bar
		return nil, false
	}

	// Step 4: Engulfing confirmed! Set stop at FVG far edge.
	if dir == domain.DirectionLong {
		// Long stop = below the FVG low (the manipulation wick)
		sess.FVGStop = fvg.Low
	} else {
		// Short stop = above the FVG high (the manipulation wick)
		sess.FVGStop = fvg.High
	}

	// VWAP check if enabled
	if cfg.VWAPFilterEnabled {
		if !vwapPositionOK(dir, bar.Close, snap.VWAP) || !vwapSlopeOK(dir, snap.VWAP, sess.PrevVWAP) {
			t.logger.Info("orb: FVG engulfing rejected (VWAP misaligned)", "symbol", sym,
				"direction", dir, "close", bar.Close, "vwap", snap.VWAP)
			sess.PrevBar = &bar
			return nil, false
		}
	}

	// Signal confirmed
	sess.Retest.HoldBar = bar.Time
	sess.Retest.HoldClose = bar.Close
	sess.Retest.Confirmed = true
	sess.State = ORBStateRetestConfirmed

	t.logger.Info("orb: FVG retest confirmed (engulfing)", "symbol", sym,
		"direction", dir, "close", bar.Close,
		"fvg_high", fvg.High, "fvg_low", fvg.Low, "fvg_stop", sess.FVGStop,
		"confidence", orbConfidence(sess, bar, snap, cfg))

	setup := &SetupCondition{
		Symbol:     bar.Symbol,
		Timeframe:  bar.Timeframe,
		Direction:  dir,
		Trigger:    "orb_break_retest",
		Snapshot:   snap,
		Regime:     domain.MarketRegime{},
		BarClose:   bar.Close,
		ORBHigh:    sess.OrbHigh,
		ORBLow:     sess.OrbLow,
		RVOL:       sess.Breakout.RVOL,
		Confidence: orbConfidence(sess, bar, snap, cfg),
		FVGStop:    sess.FVGStop,
	}

	if setup.Confidence < cfg.MinConfidence {
		t.logger.Info("orb: FVG low confidence, cycling to RANGE_SET", "symbol", sym, "confidence", setup.Confidence, "min", cfg.MinConfidence)
		t.cycleToRangeSet(sess)
		return nil, false
	}
	if replay {
		t.cycleToRangeSet(sess)
		return nil, false
	}

	sess.SignalsFired++
	if sess.SignalsFired >= cfg.MaxSignalsPerSession {
		sess.State = ORBStateDoneForSession
	} else {
		t.cycleToRangeSet(sess)
	}
	return setup, true
}

// detectFVG checks the last 3 bars for a Fair Value Gap pattern.
// Bullish FVG: bar[0].High < bar[2].Low — impulsive gap up
// Bearish FVG: bar[0].Low > bar[2].High — impulsive gap down
// Returns nil if no FVG detected or if displacement bar volume is insufficient.
func detectFVG(bars [3]domain.MarketBar, barCount int, volSMA float64, minRVOL float64) *FairValueGap {
	if barCount < 3 {
		return nil
	}
	bar0, bar1, bar2 := bars[0], bars[1], bars[2] // bar0=oldest, bar2=newest

	var rvol float64
	if volSMA > 0 {
		rvol = bar1.Volume / volSMA // displacement is the middle bar
	}

	// Require displacement volume
	if minRVOL > 0 && rvol < minRVOL {
		return nil
	}

	// Bullish FVG: gap between bar0's high and bar2's low
	if bar0.High < bar2.Low {
		return &FairValueGap{
			Direction: domain.DirectionLong,
			High:      bar2.Low,
			Low:       bar0.High,
			RVOL:      rvol,
			Time:      bar1.Time,
		}
	}

	// Bearish FVG: gap between bar2's high and bar0's low
	if bar0.Low > bar2.High {
		return &FairValueGap{
			Direction: domain.DirectionShort,
			High:      bar0.Low,
			Low:       bar2.High,
			RVOL:      rvol,
			Time:      bar1.Time,
		}
	}

	return nil
}

// isEngulfing checks if the current bar engulfs the previous bar.
// For longs: current close > previous high (bullish engulfing).
// For shorts: current close < previous low (bearish engulfing).
func isEngulfing(dir domain.Direction, current, previous domain.MarketBar) bool {
	if dir == domain.DirectionLong {
		return current.Close > previous.High
	}
	return current.Close < previous.Low
}

// vwapPositionOK checks that price is on the correct side of VWAP for the direction.
// Long: price > VWAP. Short: price < VWAP. No slope requirement.
func vwapPositionOK(dir domain.Direction, price, vwap float64) bool {
	if vwap <= 0 {
		return true // no VWAP data — don't block
	}
	if dir == domain.DirectionLong {
		return price > vwap
	}
	return price < vwap
}

// vwapSlopeOK checks that VWAP slope is not against the trade direction.
// Long: slope >= 0 (flat or rising). Short: slope <= 0 (flat or falling).
// Returns true if no previous VWAP is available (insufficient data).
func vwapSlopeOK(dir domain.Direction, vwap, prevVWAP float64) bool {
	if vwap <= 0 || prevVWAP <= 0 {
		return true
	}
	slope := vwap - prevVWAP
	if dir == domain.DirectionLong {
		return slope >= 0
	}
	return slope <= 0
}

// orbRangeValid checks that the opening range is neither too wide nor too narrow.
func orbRangeValid(orbHigh, orbLow, atr float64, maxATRMult float64, minBps int) bool {
	orRange := orbHigh - orbLow
	midpoint := (orbHigh + orbLow) / 2.0
	if maxATRMult > 0 && atr > 0 && orRange > maxATRMult*atr {
		return false // range too wide
	}
	if minBps > 0 && midpoint > 0 {
		minRange := midpoint * float64(minBps) / 10000.0
		if orRange < minRange {
			return false // range too narrow (noise)
		}
	}
	return true
}

func barDurationMinutes(tf domain.Timeframe) int {
	switch string(tf) {
	case "1m":
		return 1
	case "5m":
		return 5
	case "15m":
		return 15
	case "1h":
		return 60
	default:
		return 1
	}
}

func orbConfidence(sess *ORBSession, bar domain.MarketBar, snap domain.IndicatorSnapshot, cfg ORBConfig) float64 {
	conf := 0.50
	if sess.Breakout.RVOL >= cfg.MinRVOL {
		conf += 0.25
	}
	if sess.Breakout.Direction == domain.DirectionLong {
		if bar.Close > bar.Open {
			conf += 0.10
		}
	} else {
		if bar.Close < bar.Open {
			conf += 0.10
		}
	}

	if sess.BarsSinceBreakout <= 5 {
		conf += 0.10
	} else if sess.BarsSinceBreakout <= cfg.MaxRetestBars {
		conf += 0.05
	}

	// NR7 bonus: prior day compression → higher breakout probability
	if htf, ok := snap.HTF[domain.Timeframe("1d")]; ok && htf.NR7 {
		conf += 0.05
	}

	if conf > 0.95 {
		conf = 0.95
	}
	return conf
}
