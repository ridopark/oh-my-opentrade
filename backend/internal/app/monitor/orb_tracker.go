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
	}
}

type BreakoutInfo struct {
	Direction  domain.Direction
	BreakBar   time.Time
	BreakClose float64
	RVOL       float64
	Confirmed  bool
}

type RetestInfo struct {
	TouchBar       time.Time
	TouchPrice     float64
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
}

type ORBTracker struct {
	sessions map[string]*ORBSession
	logger   *slog.Logger
}

func NewORBTracker() *ORBTracker {
	return &ORBTracker{sessions: make(map[string]*ORBSession), logger: slog.Default()}
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

func (t *ORBTracker) OnBar(bar domain.MarketBar, snap domain.IndicatorSnapshot, cfg ORBConfig, replay bool) (*SetupCondition, bool) {
	if t.sessions == nil {
		t.sessions = make(map[string]*ORBSession)
	}

	sym := bar.Symbol.String()
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

		required := cfg.WindowMinutes - cfg.AllowMissingBars
		if required < 1 {
			required = 1
		}
		if sess.RangeBarCount >= required {
			sess.State = ORBStateRangeSet
			t.logger.Info("orb: range set", "symbol", sym, "high", sess.OrbHigh, "low", sess.OrbLow, "bars", sess.RangeBarCount)
			return t.onRangeSetBar(sess, bar, snap, cfg)
		} else {
			sess.State = ORBStateInvalid
			t.logger.Warn("orb: invalid range", "symbol", sym, "bars", sess.RangeBarCount, "required", required)
			return nil, false
		}

	case ORBStateRangeSet:
		return t.onRangeSetBar(sess, bar, snap, cfg)

	case ORBStateAwaitingRetest:
		if sess.SignalsFired >= cfg.MaxSignalsPerSession {
			sess.State = ORBStateDoneForSession
			return nil, false
		}
		sess.BarsSinceBreakout++
		if sess.BarsSinceBreakout > cfg.MaxRetestBars {
			sess.State = ORBStateDoneForSession
			t.logger.Info("orb: retest timeout", "symbol", sym, "bars_since_breakout", sess.BarsSinceBreakout, "max", cfg.MaxRetestBars)
			return nil, false
		}

		confirmBps := float64(cfg.BreakoutConfirmBps) / 10000.0
		if sess.Breakout.Direction == domain.DirectionLong {
			if bar.Close < sess.OrbLow*(1.0-confirmBps) {
				sess.State = ORBStateDoneForSession
				return nil, false
			}
		} else if sess.Breakout.Direction == domain.DirectionShort {
			if bar.Close > sess.OrbHigh*(1.0+confirmBps) {
				sess.State = ORBStateDoneForSession
				return nil, false
			}
		}

		touchTol := float64(cfg.TouchToleranceBps) / 10000.0
		holdBps := float64(cfg.HoldConfirmBps) / 10000.0

		var touched bool
		var hold bool
		var touchPrice float64
		if sess.Breakout.Direction == domain.DirectionLong {
			level := sess.OrbHigh * (1.0 + touchTol)
			touched = bar.Low <= level
			touchPrice = bar.Low
			hold = bar.Close > sess.OrbHigh*(1.0+holdBps)
		} else {
			level := sess.OrbLow * (1.0 - touchTol)
			touched = bar.High >= level
			touchPrice = bar.High
			hold = bar.Close < sess.OrbLow*(1.0-holdBps)
		}

		if touched {
			sess.Retest.TouchBar = bar.Time
			sess.Retest.TouchPrice = touchPrice
			sess.Retest.BarsSinceBreak = sess.BarsSinceBreakout
			if hold {
				sess.Retest.HoldBar = bar.Time
				sess.Retest.HoldClose = bar.Close
				sess.Retest.Confirmed = true
				sess.State = ORBStateRetestConfirmed
				t.logger.Info("orb: retest confirmed", "symbol", sym, "direction", sess.Breakout.Direction, "hold_close", bar.Close, "confidence", orbConfidence(sess, bar, cfg))
				setup := &SetupCondition{
					Symbol:     bar.Symbol,
					Timeframe:  bar.Timeframe,
					Direction:  sess.Breakout.Direction,
					Trigger:    "ORB Break & Retest",
					Snapshot:   snap,
					Regime:     domain.MarketRegime{},
					BarClose:   bar.Close,
					ORBHigh:    sess.OrbHigh,
					ORBLow:     sess.OrbLow,
					RVOL:       sess.Breakout.RVOL,
					Confidence: orbConfidence(sess, bar, cfg),
				}
				sess.State = ORBStateSignalFired
				sess.SignalsFired++
				sess.State = ORBStateDoneForSession
				// Filter: reject if computed confidence is below the DNA min_confidence threshold.
				if setup.Confidence < cfg.MinConfidence {
					t.logger.Info("orb: low confidence", "symbol", sym, "confidence", setup.Confidence, "min", cfg.MinConfidence)
					return nil, false
				}
				if replay {
					t.logger.Info("orb: replay signal suppressed", "symbol", sym, "direction", sess.Breakout.Direction)
					return nil, false
				}
				return setup, true
			}
		}

		return nil, false
	default:
		return nil, false
	}
}

func (t *ORBTracker) onRangeSetBar(sess *ORBSession, bar domain.MarketBar, snap domain.IndicatorSnapshot, cfg ORBConfig) (*SetupCondition, bool) {
	var rvol float64
	if snap.VolumeSMA > 0 {
		rvol = bar.Volume / snap.VolumeSMA
	}
	confirmBps := float64(cfg.BreakoutConfirmBps) / 10000.0
	longBreak := bar.Close > sess.OrbHigh*(1.0+confirmBps) && rvol >= cfg.MinRVOL
	shortBreak := bar.Close < sess.OrbLow*(1.0-confirmBps) && rvol >= cfg.MinRVOL
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

func orbConfidence(sess *ORBSession, bar domain.MarketBar, cfg ORBConfig) float64 {
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
	if conf > 0.95 {
		conf = 0.95
	}
	return conf
}
