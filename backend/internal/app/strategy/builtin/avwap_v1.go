package builtin

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// AVWAPStrategy implements breakout and bounce entries anchored to VWAP levels.
type AVWAPStrategy struct {
	meta start.Meta
}

// NewAVWAPStrategy creates a new AVWAP Breakout/Bounce strategy.
func NewAVWAPStrategy() *AVWAPStrategy {
	id, _ := start.NewStrategyID("avwap")
	ver, _ := start.NewVersion("1.0.0")
	return &AVWAPStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "AVWAP Breakout/Bounce",
			Description: "Anchored VWAP breakout and bounce strategy with regime gating",
			Author:      "system",
		},
	}
}

func (s *AVWAPStrategy) Meta() start.Meta { return s.meta }
func (s *AVWAPStrategy) WarmupBars() int  { return 30 }
func (s *AVWAPStrategy) ReplayOnBar(_ start.Context, _ string, bar start.Bar, st start.State, indicators start.IndicatorData) (start.State, error) {
	avwapSt, ok := st.(*AVWAPState)
	if !ok {
		return st, fmt.Errorf("AVWAPStrategy.ReplayOnBar: expected *AVWAPState, got %T", st)
	}
	avwapSt.Indicators = indicators
	avwapSt.Calc.Update(bar.Time, bar.High, bar.Low, bar.Close, bar.Volume)

	cap := avwapSt.Config.HigherLowsBars
	if cap < 2 {
		cap = 3
	}
	avwapSt.RecentLows = append(avwapSt.RecentLows, bar.Low)
	if len(avwapSt.RecentLows) > cap {
		avwapSt.RecentLows = avwapSt.RecentLows[len(avwapSt.RecentLows)-cap:]
	}
	avwapSt.RecentHighs = append(avwapSt.RecentHighs, bar.High)
	if len(avwapSt.RecentHighs) > cap {
		avwapSt.RecentHighs = avwapSt.RecentHighs[len(avwapSt.RecentHighs)-cap:]
	}

	return avwapSt, nil
}

// AVWAPConfig holds strategy parameters parsed from DNA.
type AVWAPConfig struct {
	BreakoutEnabled   bool
	HoldBars          int
	VolumeMult        float64
	BounceEnabled     bool
	RSIBounceMax      float64
	RSIBounceMin      float64
	ExitHoldBars      int
	CooldownSeconds   int
	MaxTradesPerDay   int
	AllowRegimes      []string
	Direction         string
	RequireHigherLows bool
	HigherLowsBars    int
	MiddayTrapShield  bool
	MiddayVolumeMult  float64
	AssetClass        string
	Anchors           []string

	MinSlopeBPS                  float64 // minimum AVWAP slope for entries (bps/bar)
	SlopeLookback                int     // number of bars for slope calculation
	RequireCapitulationForShorts bool

	EnforceAVWAPBias     bool
	PullbackEnabled      bool
	PullbackTrendBars    int
	PullbackToleranceBPS int
	PullbackRSIMin       float64
	PullbackRSIMax       float64

	AVWAPStopEnabled   bool
	AVWAPStopBars      int // consecutive bars on wrong side of AVWAP to trigger exit
	AVWAPStopBufferBPS int // buffer in bps before triggering (avoid noise)

	PinchEnabled bool
	PinchMinBPS  int // minimum gap between two AVWAPs (bps)
	PinchMaxBPS  int // maximum gap — if too wide, not a squeeze

	GapReclaimEnabled bool
	GapReclaimBars    int // max bars since crossing below AVWAP to still consider a reclaim

	SwingTrailEnabled   bool
	SwingTrailLookback  int // bars to look back for swing low/high (default 5)
	SwingTrailBufferBPS int // buffer below swing low / above swing high in bps (default 10)
	SwingTrailMinBars   int // min bars to hold before trail activates (default 3)

	HandoffEnabled    bool
	HandoffBars       int // consecutive bars of accelerating distance from AVWAP (default 3)
	HandoffMinMomBPS  int // minimum final distance from AVWAP in bps to confirm handoff (default 40)
}

// AVWAPState is the per-symbol state for the AVWAP strategy.
type AVWAPState struct {
	Symbol         string
	Calc           *start.AnchoredVWAPCalc
	Indicators     start.IndicatorData
	AboveCount     map[string]int
	BelowCount     map[string]int
	TradesToday    int
	CooldownUntil  time.Time
	PositionSide   start.Side
	PendingEntry   start.Side // set on signal emission, cleared on fill/rejection
	PendingEntryAt time.Time  // when PendingEntry was set (for timeout recovery)
	Config         AVWAPConfig
	RecentLows     []float64
	RecentHighs    []float64
	CalcBarCount   int // bars fed to Calc since last reset — used for stabilization

	PeakAboveCount map[string]int // tracks the max AboveCount before a pullback started
	PeakBelowCount map[string]int // tracks the max BelowCount before a pullback started

	StopBelowCount  map[string]int // for LONG positions: consecutive bars below AVWAP
	StopAboveCount  map[string]int // for SHORT positions: consecutive bars above AVWAP
	CrossedBelowBar  map[string]int       // tracks how many bars ago price crossed below each AVWAP (gap reclaim)
	AVWAPDistHistory map[string][]float64 // recent close-to-AVWAP distances per anchor (for handoff detection)

	SwingTrailStop  float64 // current trailing stop price (swing low for longs, swing high for shorts)
	SwingTrailBars  int     // bars since position opened (for min hold gate)
	LockedOutToday  bool    // set when stopped out; prevents re-entry for rest of session
}

// SetIndicators implements the indicatorSetter interface.
func (s *AVWAPState) SetIndicators(ind start.IndicatorData) {
	s.Indicators = ind
}

func (s *AVWAPState) AnchorNames() []string { return s.Config.Anchors }

// ResetAnchors performs a partial update: anchors with unchanged times
// preserve their running VWAP state (CumPV/CumV/M2). New or changed
// anchors start fresh. Removed anchors are dropped.
func (s *AVWAPState) ResetAnchors(anchorTimes map[string]time.Time) {
	if s.Calc == nil {
		s.Calc = start.NewAnchoredVWAPCalc()
		for name, t := range anchorTimes {
			if t.IsZero() {
				continue
			}
			s.Calc.AddAnchor(start.AnchorPoint{Name: name, AnchorTime: t})
		}
		s.AboveCount = make(map[string]int)
		s.BelowCount = make(map[string]int)
		s.PeakAboveCount = make(map[string]int)
		s.PeakBelowCount = make(map[string]int)
		s.StopBelowCount = make(map[string]int)
		s.StopAboveCount = make(map[string]int)
		s.CrossedBelowBar = make(map[string]int)
		s.TradesToday = 0
		s.CalcBarCount = 0
		s.LockedOutToday = false
		return
	}

	existingStates := s.Calc.States()
	existingPoints := s.Calc.AnchorPoints()

	newCalc := start.NewAnchoredVWAPCalc()
	newAbove := make(map[string]int)
	newBelow := make(map[string]int)

	for name, t := range anchorTimes {
		if t.IsZero() {
			continue
		}

		ap := start.AnchorPoint{Name: name, AnchorTime: t}

		if oldAP, exists := existingPoints[name]; exists && oldAP.AnchorTime.Equal(t) {
			if oldState, hasState := existingStates[name]; hasState {
				newCalc.AddAnchor(ap)
				newCalc.Restore([]start.AnchorPoint{ap}, map[string]start.AnchoredVWAPState{name: oldState})
				newAbove[name] = s.AboveCount[name]
				newBelow[name] = s.BelowCount[name]
				continue
			}
		}

		newCalc.AddAnchor(ap)
	}

	s.Calc = newCalc
	s.AboveCount = newAbove
	s.BelowCount = newBelow
	s.PeakAboveCount = make(map[string]int)
	s.PeakBelowCount = make(map[string]int)
	s.StopBelowCount = make(map[string]int)
	s.StopAboveCount = make(map[string]int)
	s.CrossedBelowBar = make(map[string]int)
	s.TradesToday = 0
	s.CalcBarCount = 0 // reset stabilization counter
	s.LockedOutToday = false
}

func (s *AVWAPState) ClearPendingEntry() {
	s.PendingEntry = ""
	s.PendingEntryAt = time.Time{}
}

// UpdateCalc feeds a 1m bar into the AVWAP calculator for smooth chart
// rendering. Does not trigger any signal logic.
func (s *AVWAPState) UpdateCalc(bar start.Bar) {
	if s.Calc != nil {
		s.Calc.Update(bar.Time, bar.High, bar.Low, bar.Close, bar.Volume)
		s.CalcBarCount++
	}
}

// CheckExitsOn1m evaluates exit-only logic on 1m bars for faster reaction.
// Per Brian Shannon: use short-term chart (1m) to fine-tune exits while
// entries remain on the strategy timeframe (5m). Returns nil if no exit.
func (s *AVWAPState) CheckExitsOn1m(symbol string, bar start.Bar) []start.Signal {
	if s.PositionSide == "" || s.PendingEntry != "" {
		return nil
	}
	cfg := s.Config
	avwapValues := s.Calc.Values()
	if len(avwapValues) == 0 {
		return nil
	}
	sortedAnchors := s.Calc.SortedNames()
	instanceID, _ := start.NewInstanceID(fmt.Sprintf("avwap:1.0.0:%s", symbol))
	cooldown := time.Duration(cfg.CooldownSeconds) * time.Second
	now := bar.Time

	// Update recent lows/highs with 1m data for swing trail precision
	s.RecentLows = append(s.RecentLows, bar.Low)
	cap := cfg.HigherLowsBars
	if cap < 2 {
		cap = 3
	}
	if len(s.RecentLows) > cap {
		s.RecentLows = s.RecentLows[len(s.RecentLows)-cap:]
	}
	s.RecentHighs = append(s.RecentHighs, bar.High)
	if len(s.RecentHighs) > cap {
		s.RecentHighs = s.RecentHighs[len(s.RecentHighs)-cap:]
	}

	// --- AVWAP stop on 1m ---
	if cfg.AVWAPStopEnabled {
		if s.StopBelowCount == nil {
			s.StopBelowCount = make(map[string]int)
		}
		if s.StopAboveCount == nil {
			s.StopAboveCount = make(map[string]int)
		}
		for _, anchorName := range sortedAnchors {
			avwapValue := avwapValues[anchorName]
			bufferAbs := avwapValue * float64(cfg.AVWAPStopBufferBPS) / 10000.0
			if s.PositionSide == start.SideBuy {
				if bar.Close < avwapValue-bufferAbs {
					s.StopBelowCount[anchorName]++
				} else {
					s.StopBelowCount[anchorName] = 0
				}
			} else if s.PositionSide == start.SideSell {
				if bar.Close > avwapValue+bufferAbs {
					s.StopAboveCount[anchorName]++
				} else {
					s.StopAboveCount[anchorName] = 0
				}
			}
		}
		if s.PositionSide == start.SideBuy {
			for _, anchorName := range sortedAnchors {
				if s.StopBelowCount[anchorName] >= cfg.AVWAPStopBars {
					sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideSell, 0.9, map[string]string{
						"ref_price": fmt.Sprintf("%.10f", bar.Close),
						"setup":     "avwap_stop",
						"anchor":    anchorName,
						"source":    "1m",
					})
					if err != nil {
						return nil
					}
					s.PositionSide = ""
					s.CooldownUntil = now.Add(cooldown)
					for an := range s.StopBelowCount {
						s.StopBelowCount[an] = 0
					}
					return []start.Signal{sig}
				}
			}
		} else if s.PositionSide == start.SideSell {
			for _, anchorName := range sortedAnchors {
				if s.StopAboveCount[anchorName] >= cfg.AVWAPStopBars {
					sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideBuy, 0.9, map[string]string{
						"ref_price": fmt.Sprintf("%.10f", bar.Close),
						"setup":     "avwap_stop",
						"anchor":    anchorName,
						"source":    "1m",
					})
					if err != nil {
						return nil
					}
					s.PositionSide = ""
					s.CooldownUntil = now.Add(cooldown)
					for an := range s.StopAboveCount {
						s.StopAboveCount[an] = 0
					}
					return []start.Signal{sig}
				}
			}
		}
	}

	// --- Swing trail stop on 1m ---
	if cfg.SwingTrailEnabled {
		s.SwingTrailBars++
		lookback := cfg.SwingTrailLookback
		if lookback < 2 {
			lookback = 5
		}
		bufferMult := 1.0 - float64(cfg.SwingTrailBufferBPS)/10000.0
		bufferMultShort := 1.0 + float64(cfg.SwingTrailBufferBPS)/10000.0

		if s.PositionSide == start.SideBuy {
			lows := s.RecentLows
			if len(lows) > lookback {
				lows = lows[len(lows)-lookback:]
			}
			if len(lows) > 0 {
				swingLow := lows[0]
				for _, l := range lows[1:] {
					if l < swingLow {
						swingLow = l
					}
				}
				newStop := swingLow * bufferMult
				if newStop > s.SwingTrailStop {
					s.SwingTrailStop = newStop
				}
			}
		} else if s.PositionSide == start.SideSell {
			highs := s.RecentHighs
			if len(highs) > lookback {
				highs = highs[len(highs)-lookback:]
			}
			if len(highs) > 0 {
				swingHigh := highs[0]
				for _, h := range highs[1:] {
					if h > swingHigh {
						swingHigh = h
					}
				}
				newStop := swingHigh * bufferMultShort
				if s.SwingTrailStop == 0 || newStop < s.SwingTrailStop {
					s.SwingTrailStop = newStop
				}
			}
		}

		if s.SwingTrailBars >= cfg.SwingTrailMinBars && s.SwingTrailStop > 0 {
			triggered := false
			if s.PositionSide == start.SideBuy && bar.Close <= s.SwingTrailStop {
				triggered = true
			} else if s.PositionSide == start.SideSell && bar.Close >= s.SwingTrailStop {
				triggered = true
			}
			if triggered {
				exitSide := start.SideSell
				if s.PositionSide == start.SideSell {
					exitSide = start.SideBuy
				}
				sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, exitSide, 0.85, map[string]string{
					"ref_price":  fmt.Sprintf("%.10f", bar.Close),
					"setup":      "swing_trail_stop",
					"stop_level": fmt.Sprintf("%.4f", s.SwingTrailStop),
					"source":     "1m",
				})
				if err != nil {
					return nil
				}
				s.PositionSide = ""
				s.LockedOutToday = true
				s.SwingTrailStop = 0
				s.SwingTrailBars = 0
				s.CooldownUntil = now.Add(cooldown)
				return []start.Signal{sig}
			}
		}
	}

	return nil
}

// AVWAPValues returns the current anchored VWAP values for chart rendering.
// Suppresses output for the first 10 bars after a reset to avoid noisy
// values from an under-accumulated calculator.
func (s *AVWAPState) AVWAPValues() map[string]float64 {
	if s.Calc == nil || s.CalcBarCount < 10 {
		return nil
	}
	return s.Calc.Values()
}

// --- param helpers (shared by strategies in this package) ---

func getFloat64(m map[string]any, key string, def float64) float64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		}
	}
	return def
}

func getInt(m map[string]any, key string, def int) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return def
}

func getBool(m map[string]any, key string, def bool) bool {
	if v, ok := m[key]; ok {
		if b, ok2 := v.(bool); ok2 {
			return b
		}
	}
	return def
}

func getStringSlice(m map[string]any, key string, def []string) []string {
	v, ok := m[key]
	if !ok {
		return def
	}
	switch sl := v.(type) {
	case []string:
		return sl
	case []any:
		out := make([]string, 0, len(sl))
		for _, item := range sl {
			if s, ok2 := item.(string); ok2 {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return def
}
func getString(m map[string]any, key string, def string) string {
	if v, ok := m[key]; ok {
		if s, ok2 := v.(string); ok2 {
			return s
		}
	}
	return def
}

var etLocation *time.Location

func init() {
	var err error
	etLocation, err = time.LoadLocation("America/New_York")
	if err != nil {
		log.Fatalf("failed to load America/New_York timezone: %v", err)
	}
}

func hasHigherLows(lows []float64) bool {
	if len(lows) < 2 {
		return false
	}
	for i := 1; i < len(lows); i++ {
		if lows[i] <= lows[i-1] {
			return false
		}
	}
	return true
}

func hasLowerHighs(highs []float64) bool {
	if len(highs) < 2 {
		return false
	}
	for i := 1; i < len(highs); i++ {
		if highs[i] >= highs[i-1] {
			return false
		}
	}
	return true
}

func parseAVWAPConfig(params map[string]any) AVWAPConfig {
	cfg := AVWAPConfig{
		BreakoutEnabled:   getBool(params, "breakout_enabled", true),
		HoldBars:          getInt(params, "hold_bars", 2),
		VolumeMult:        getFloat64(params, "volume_mult", 1.5),
		BounceEnabled:     getBool(params, "bounce_enabled", true),
		RSIBounceMax:      getFloat64(params, "rsi_bounce_max", 30),
		ExitHoldBars:      getInt(params, "exit_hold_bars", 2),
		CooldownSeconds:   getInt(params, "cooldown_seconds", 120),
		MaxTradesPerDay:   getInt(params, "max_trades_per_day", 3),
		AllowRegimes:      getStringSlice(params, "allow_regimes", []string{"BALANCE", "REVERSAL"}),
		Direction:         getString(params, "direction", ""),
		RequireHigherLows: getBool(params, "require_higher_lows", false),
		HigherLowsBars:    getInt(params, "higher_lows_bars", 3),
		MiddayTrapShield:  getBool(params, "midday_trap_shield", false),
		MiddayVolumeMult:  getFloat64(params, "midday_volume_mult", 2.0),
		AssetClass:        getString(params, "asset_class", ""),
		Anchors:           getStringSlice(params, "anchors", []string{"session_open"}),

		MinSlopeBPS:                  getFloat64(params, "min_slope_bps", 0.0),
		SlopeLookback:                getInt(params, "slope_lookback", 10),
		RequireCapitulationForShorts: getBool(params, "require_capitulation_for_shorts", false),

		EnforceAVWAPBias:     getBool(params, "enforce_avwap_bias", true),
		PullbackEnabled:      getBool(params, "pullback_enabled", true),
		PullbackTrendBars:    getInt(params, "pullback_trend_bars", 10),
		PullbackToleranceBPS: getInt(params, "pullback_tolerance_bps", 20),
		PullbackRSIMin:       getFloat64(params, "pullback_rsi_min", 40.0),
		PullbackRSIMax:       getFloat64(params, "pullback_rsi_max", 60.0),

		AVWAPStopEnabled:   getBool(params, "avwap_stop_enabled", true),
		AVWAPStopBars:      getInt(params, "avwap_stop_bars", 3),
		AVWAPStopBufferBPS: getInt(params, "avwap_stop_buffer_bps", 10),

		PinchEnabled: getBool(params, "pinch_enabled", false),
		PinchMinBPS:  getInt(params, "pinch_min_bps", 5),
		PinchMaxBPS:  getInt(params, "pinch_max_bps", 50),

		GapReclaimEnabled: getBool(params, "gap_reclaim_enabled", false),
		GapReclaimBars:    getInt(params, "gap_reclaim_bars", 5),

		SwingTrailEnabled:   getBool(params, "swing_trail_enabled", false),
		SwingTrailLookback:  getInt(params, "swing_trail_lookback", 5),
		SwingTrailBufferBPS: getInt(params, "swing_trail_buffer_bps", 10),
		SwingTrailMinBars:   getInt(params, "swing_trail_min_bars", 3),

		HandoffEnabled:   getBool(params, "handoff_enabled", false),
		HandoffBars:      getInt(params, "handoff_bars", 3),
		HandoffMinMomBPS: getInt(params, "handoff_min_mom_bps", 40),
	}
	cfg.RSIBounceMin = 100 - cfg.RSIBounceMax
	return cfg
}

// Init creates initial state for a symbol.
func (s *AVWAPStrategy) Init(ctx start.Context, symbol string, params map[string]any, prior start.State) (start.State, error) {
	cfg := parseAVWAPConfig(params)
	calc := start.NewAnchoredVWAPCalc()

	anchorNames := getStringSlice(params, "anchors", []string{"session_open"})
	added := 0
	for _, name := range anchorNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var anchorTime time.Time
		if name == "session_open" {
			if ctx != nil {
				anchorTime = ctx.Now()
			}
		}
		calc.AddAnchor(start.AnchorPoint{Name: name, AnchorTime: anchorTime})
		added++
	}
	if added == 0 {
		var anchorTime time.Time
		if ctx != nil {
			anchorTime = ctx.Now()
		}
		calc.AddAnchor(start.AnchorPoint{Name: "session_open", AnchorTime: anchorTime})
	}

	st := &AVWAPState{
		Symbol:          symbol,
		Calc:            calc,
		AboveCount:      make(map[string]int),
		BelowCount:      make(map[string]int),
		PeakAboveCount:  make(map[string]int),
		PeakBelowCount:  make(map[string]int),
		StopBelowCount:  make(map[string]int),
		StopAboveCount:  make(map[string]int),
		CrossedBelowBar: make(map[string]int),
		Config:          cfg,
	}

	if prior != nil {
		if avwapPrior, ok := prior.(*AVWAPState); ok {
			st.Calc = avwapPrior.Calc
			st.AboveCount = avwapPrior.AboveCount
			st.BelowCount = avwapPrior.BelowCount
			st.PeakAboveCount = avwapPrior.PeakAboveCount
			st.PeakBelowCount = avwapPrior.PeakBelowCount
			st.StopBelowCount = avwapPrior.StopBelowCount
			st.StopAboveCount = avwapPrior.StopAboveCount
			st.CrossedBelowBar = avwapPrior.CrossedBelowBar
			st.TradesToday = avwapPrior.TradesToday
			st.CooldownUntil = avwapPrior.CooldownUntil
			st.PositionSide = avwapPrior.PositionSide
			st.PendingEntry = avwapPrior.PendingEntry
			st.PendingEntryAt = avwapPrior.PendingEntryAt
			st.Config = cfg
		} else if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Warn("AVWAPStrategy: incompatible prior state, starting fresh", "symbol", symbol)
		}
	}

	return st, nil
}

// OnBar processes a bar and emits breakout/bounce/exit signals.
func (s *AVWAPStrategy) OnBar(ctx start.Context, symbol string, bar start.Bar, st start.State) (start.State, []start.Signal, error) {
	avwapSt, ok := st.(*AVWAPState)
	if !ok {
		return st, nil, fmt.Errorf("AVWAPStrategy.OnBar: expected *AVWAPState, got %T", st)
	}
	cfg := avwapSt.Config

	now := bar.Time
	if ctx != nil {
		now = ctx.Now()
	}

	// Pending-entry timeout: if entry signal was emitted but no fill/rejection after 5 min, reset.
	if avwapSt.PendingEntry != "" && now.Sub(avwapSt.PendingEntryAt) > 5*time.Minute {
		if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Warn("AVWAPStrategy: pending entry timed out, resetting", "symbol", symbol, "side", avwapSt.PendingEntry)
		}
		avwapSt.PendingEntry = ""
		avwapSt.PendingEntryAt = time.Time{}
	}

	// 1. Cooldown / max trades gate.
	if now.Before(avwapSt.CooldownUntil) {
		if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Info("AVWAP gate: cooldown active", "symbol", symbol, "until", avwapSt.CooldownUntil, "now", now)
		}
		return avwapSt, nil, nil
	}
	if avwapSt.TradesToday >= cfg.MaxTradesPerDay {
		if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Info("AVWAP gate: max trades reached", "symbol", symbol, "trades", avwapSt.TradesToday, "max", cfg.MaxTradesPerDay)
		}
		return avwapSt, nil, nil
	}

	// 2. Update AVWAP calculator with this bar.
	// In backtests, 1m bars already feed the calculator via UpdateCalc() in the
	// runner. This 5m bar update is needed for tests and live trading where
	// UpdateCalc may not be called separately.
	avwapSt.Calc.Update(bar.Time, bar.High, bar.Low, bar.Close, bar.Volume)
	avwapValues := avwapSt.Calc.Values()

	// Get sorted anchor names for deterministic iteration order.
	// Go map iteration is random — without this, backtest results
	// vary between runs as different anchors trigger first.
	sortedAnchors := avwapSt.Calc.SortedNames()

	// 2b. Update recent lows/highs sliding window for higher-lows filter.
	avwapSt.RecentLows = append(avwapSt.RecentLows, bar.Low)
	if len(avwapSt.RecentLows) > cfg.HigherLowsBars {
		avwapSt.RecentLows = avwapSt.RecentLows[len(avwapSt.RecentLows)-cfg.HigherLowsBars:]
	}
	avwapSt.RecentHighs = append(avwapSt.RecentHighs, bar.High)
	if len(avwapSt.RecentHighs) > cfg.HigherLowsBars {
		avwapSt.RecentHighs = avwapSt.RecentHighs[len(avwapSt.RecentHighs)-cfg.HigherLowsBars:]
	}

	// 3. Regime gating.
	regimeAllowed := false
	regimeTag := "none"
	if ar, ok2 := avwapSt.Indicators.AnchorRegimes["5m"]; ok2 {
		regimeTag = ar.Type
		for _, allowed := range cfg.AllowRegimes {
			if ar.Type == allowed {
				regimeAllowed = true
				break
			}
		}
	} else {
		regimeAllowed = true
	}

	// 4. Update AboveCount/BelowCount and distance history for each active anchor.
	if avwapSt.AVWAPDistHistory == nil {
		avwapSt.AVWAPDistHistory = make(map[string][]float64)
	}
	for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
		switch {
		case bar.Close > avwapValue:
			avwapSt.AboveCount[anchorName]++
			avwapSt.BelowCount[anchorName] = 0
		case bar.Close < avwapValue:
			avwapSt.BelowCount[anchorName]++
			avwapSt.AboveCount[anchorName] = 0
		default:
			avwapSt.AboveCount[anchorName] = 0
			avwapSt.BelowCount[anchorName] = 0
		}
		// Track distance from AVWAP for handoff detection (signed: positive = above)
		dist := (bar.Close - avwapValue) / avwapValue * 10000.0 // in bps
		hist := avwapSt.AVWAPDistHistory[anchorName]
		hist = append(hist, dist)
		maxHist := 10
		if len(hist) > maxHist {
			hist = hist[len(hist)-maxHist:]
		}
		avwapSt.AVWAPDistHistory[anchorName] = hist
	}

	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
	cooldown := time.Duration(cfg.CooldownSeconds) * time.Second

	// 5. Exit signals (check even if cooldown would block new entries).
	if avwapSt.PositionSide == start.SideBuy && avwapSt.PendingEntry == "" {
		for _, belowCnt := range avwapSt.BelowCount {
			if belowCnt >= cfg.ExitHoldBars {
				sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideSell, 0.8, map[string]string{
					"ref_price": fmt.Sprintf("%.10f", bar.Close),
					"setup":     "avwap_exit",
					"regime_5m": regimeTag,
				})
				if err != nil {
					return avwapSt, nil, err
				}
			avwapSt.PositionSide = ""
			avwapSt.CooldownUntil = now.Add(cooldown)
			// Reset AboveCount/BelowCount to prevent immediate re-exit on next position
			for anchorName := range avwapSt.AboveCount {
				avwapSt.AboveCount[anchorName] = 0
				avwapSt.BelowCount[anchorName] = 0
			}
			return avwapSt, []start.Signal{sig}, nil
			}
		}
	}
	if avwapSt.PositionSide == start.SideSell && avwapSt.PendingEntry == "" {
		for _, aboveCnt := range avwapSt.AboveCount {
			if aboveCnt >= cfg.ExitHoldBars {
				sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideBuy, 0.8, map[string]string{
					"ref_price": fmt.Sprintf("%.10f", bar.Close),
					"setup":     "avwap_exit",
					"regime_5m": regimeTag,
				})
				if err != nil {
					return avwapSt, nil, err
				}
			avwapSt.PositionSide = ""
			avwapSt.CooldownUntil = now.Add(cooldown)
			// Reset AboveCount/BelowCount to prevent immediate re-exit on next position
			for anchorName := range avwapSt.AboveCount {
				avwapSt.AboveCount[anchorName] = 0
				avwapSt.BelowCount[anchorName] = 0
			}
			return avwapSt, []start.Signal{sig}, nil
			}
		}
	}

	// 5b. AVWAP-based stop: exit when price breaks significantly past AVWAP against position.
	if cfg.AVWAPStopEnabled && avwapSt.PositionSide != "" && avwapSt.PendingEntry == "" {
		if avwapSt.StopBelowCount == nil {
			avwapSt.StopBelowCount = make(map[string]int)
		}
		if avwapSt.StopAboveCount == nil {
			avwapSt.StopAboveCount = make(map[string]int)
		}
		// Update stop counters for each anchor.
		for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
			bufferAbs := avwapValue * float64(cfg.AVWAPStopBufferBPS) / 10000.0
			if avwapSt.PositionSide == start.SideBuy {
				if bar.Close < avwapValue-bufferAbs {
					avwapSt.StopBelowCount[anchorName]++
				} else {
					avwapSt.StopBelowCount[anchorName] = 0
				}
			} else if avwapSt.PositionSide == start.SideSell {
				if bar.Close > avwapValue+bufferAbs {
					avwapSt.StopAboveCount[anchorName]++
				} else {
					avwapSt.StopAboveCount[anchorName] = 0
				}
			}
		}
		// Check if any anchor triggered the stop.
		if avwapSt.PositionSide == start.SideBuy {
			for _, anchorName := range sortedAnchors {
				if avwapSt.StopBelowCount[anchorName] >= cfg.AVWAPStopBars {
					sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideSell, 0.9, map[string]string{
						"ref_price": fmt.Sprintf("%.10f", bar.Close),
						"setup":     "avwap_stop",
						"anchor":    anchorName,
						"regime_5m": regimeTag,
					})
					if err != nil {
						return avwapSt, nil, err
					}
					avwapSt.PositionSide = ""
					avwapSt.LockedOutToday = true
					avwapSt.CooldownUntil = now.Add(cooldown)
					for an := range avwapSt.StopBelowCount {
						avwapSt.StopBelowCount[an] = 0
					}
					return avwapSt, []start.Signal{sig}, nil
				}
			}
		} else if avwapSt.PositionSide == start.SideSell {
			for _, anchorName := range sortedAnchors {
				if avwapSt.StopAboveCount[anchorName] >= cfg.AVWAPStopBars {
					sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideBuy, 0.9, map[string]string{
						"ref_price": fmt.Sprintf("%.10f", bar.Close),
						"setup":     "avwap_stop",
						"anchor":    anchorName,
						"regime_5m": regimeTag,
					})
					if err != nil {
						return avwapSt, nil, err
					}
					avwapSt.PositionSide = ""
					avwapSt.LockedOutToday = true
					avwapSt.CooldownUntil = now.Add(cooldown)
					for an := range avwapSt.StopAboveCount {
						avwapSt.StopAboveCount[an] = 0
					}
					return avwapSt, []start.Signal{sig}, nil
				}
			}
		}
	}

	// 5c. Swing trail stop: trail the stop at the recent swing low (longs) or swing high (shorts).
	// Per Brian Shannon: place stop at the low of the dip, trail using higher lows.
	// This respects price structure rather than statistical bands.
	if cfg.SwingTrailEnabled && avwapSt.PositionSide != "" && avwapSt.PendingEntry == "" {
		avwapSt.SwingTrailBars++

		lookback := cfg.SwingTrailLookback
		if lookback < 2 {
			lookback = 5
		}
		bufferMult := 1.0 - float64(cfg.SwingTrailBufferBPS)/10000.0 // for longs: stop slightly below swing low
		bufferMultShort := 1.0 + float64(cfg.SwingTrailBufferBPS)/10000.0 // for shorts: stop slightly above swing high

		if avwapSt.PositionSide == start.SideBuy {
			// Find lowest low in recent lookback window
			lows := avwapSt.RecentLows
			if len(lows) > lookback {
				lows = lows[len(lows)-lookback:]
			}
			if len(lows) > 0 {
				swingLow := lows[0]
				for _, l := range lows[1:] {
					if l < swingLow {
						swingLow = l
					}
				}
				newStop := swingLow * bufferMult
				// Trail UP only — never lower the stop
				if newStop > avwapSt.SwingTrailStop {
					avwapSt.SwingTrailStop = newStop
				}
			}
		} else if avwapSt.PositionSide == start.SideSell {
			// Find highest high in recent lookback window
			highs := avwapSt.RecentHighs
			if len(highs) > lookback {
				highs = highs[len(highs)-lookback:]
			}
			if len(highs) > 0 {
				swingHigh := highs[0]
				for _, h := range highs[1:] {
					if h > swingHigh {
						swingHigh = h
					}
				}
				newStop := swingHigh * bufferMultShort
				// Trail DOWN only — never raise the stop for shorts
				if avwapSt.SwingTrailStop == 0 || newStop < avwapSt.SwingTrailStop {
					avwapSt.SwingTrailStop = newStop
				}
			}
		}

		// Check trigger after min hold period
		if avwapSt.SwingTrailBars >= cfg.SwingTrailMinBars && avwapSt.SwingTrailStop > 0 {
			triggered := false
			if avwapSt.PositionSide == start.SideBuy && bar.Close <= avwapSt.SwingTrailStop {
				triggered = true
			} else if avwapSt.PositionSide == start.SideSell && bar.Close >= avwapSt.SwingTrailStop {
				triggered = true
			}
			if triggered {
				exitSide := start.SideSell
				if avwapSt.PositionSide == start.SideSell {
					exitSide = start.SideBuy
				}
				sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, exitSide, 0.85, map[string]string{
					"ref_price":  fmt.Sprintf("%.10f", bar.Close),
					"setup":      "swing_trail_stop",
					"stop_level": fmt.Sprintf("%.4f", avwapSt.SwingTrailStop),
					"regime_5m":  regimeTag,
				})
				if err != nil {
					return avwapSt, nil, err
				}
				avwapSt.PositionSide = ""
				avwapSt.LockedOutToday = true
				avwapSt.SwingTrailStop = 0
				avwapSt.SwingTrailBars = 0
				avwapSt.CooldownUntil = now.Add(cooldown)
				return avwapSt, []start.Signal{sig}, nil
			}
		}
	}

	// 6. Only entries if flat and regime allowed.
	if avwapSt.PositionSide != "" || avwapSt.PendingEntry != "" {
		if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Info("AVWAP gate: position/pending active", "symbol", symbol, "position", avwapSt.PositionSide, "pending", avwapSt.PendingEntry)
		}
		return avwapSt, nil, nil
	}
	if !regimeAllowed {
		if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Info("AVWAP gate: regime blocked", "symbol", symbol, "regime", regimeTag)
		}
		return avwapSt, nil, nil
	}
	// 6a2. Session lockout — once stopped out, no re-entry until next session.
	// Prevents re-entering broken setups that cooldown alone cannot fix.
	if avwapSt.LockedOutToday {
		return avwapSt, nil, nil
	}

	// 6b. Directional bias gate — determine bias from first anchor's AVWAP value.
	// Require at least minBarsForBias bars before trusting the AVWAP for directional
	// decisions. At session open, the AVWAP has too few data points to be reliable
	// (e.g. 09:44 = only ~9 one-minute bars). This matches the chart stabilization
	// gate (CalcBarCount >= 10) so bias and chart stay aligned.
	const minBarsForBias = 10
	avwapBias := "" // "LONG", "SHORT", or "" (no bias / no data)
	if cfg.EnforceAVWAPBias && len(cfg.Anchors) > 0 && avwapSt.CalcBarCount >= minBarsForBias {
		firstAnchor := cfg.Anchors[0]
		if firstAVWAP, ok2 := avwapValues[firstAnchor]; ok2 {
			if bar.Close > firstAVWAP {
				avwapBias = "LONG"
			} else if bar.Close < firstAVWAP {
				avwapBias = "SHORT"
			}
		}
	}

	// 6b2. AVWAP slope gate — check slope of first anchor for trend confirmation.
	var avwapSlope float64
	slopeOK := false
	if cfg.MinSlopeBPS > 0 && len(cfg.Anchors) > 0 {
		avwapSlope, slopeOK = avwapSt.Calc.Slope(cfg.Anchors[0], cfg.SlopeLookback)
	}

	// 6c. Update PeakAboveCount/PeakBelowCount for pullback detection.
	if avwapSt.PeakAboveCount == nil {
		avwapSt.PeakAboveCount = make(map[string]int)
	}
	if avwapSt.PeakBelowCount == nil {
		avwapSt.PeakBelowCount = make(map[string]int)
	}
	for _, anchorName := range sortedAnchors {
		if avwapSt.AboveCount[anchorName] > avwapSt.PeakAboveCount[anchorName] {
			avwapSt.PeakAboveCount[anchorName] = avwapSt.AboveCount[anchorName]
		}
		if avwapSt.AboveCount[anchorName] == 0 && avwapSt.PeakAboveCount[anchorName] > 0 {
			// price crossed below — peak is frozen for pullback check
		}
		if avwapSt.BelowCount[anchorName] > avwapSt.PeakBelowCount[anchorName] {
			avwapSt.PeakBelowCount[anchorName] = avwapSt.BelowCount[anchorName]
		}
		if avwapSt.BelowCount[anchorName] == 0 && avwapSt.PeakBelowCount[anchorName] > 0 {
			// price crossed above — peak is frozen for pullback check
		}
		// Reset peaks when price re-establishes a trend (AboveCount or BelowCount exceeds prior peak)
		if avwapSt.AboveCount[anchorName] >= cfg.PullbackTrendBars {
			avwapSt.PeakAboveCount[anchorName] = avwapSt.AboveCount[anchorName]
		}
		if avwapSt.BelowCount[anchorName] >= cfg.PullbackTrendBars {
			avwapSt.PeakBelowCount[anchorName] = avwapSt.BelowCount[anchorName]
		}
	}

	// 6e. Capitulation long — price reclaims a capitulation-anchored AVWAP.
	// Per Lance Brightstein: after a panic sell-off, anchor AVWAP to the low.
	// Enter long once price retraces and holds above it — "strong hands" defending.
	// Higher conviction than a normal breakout, works in all regimes including REVERSAL.
	for _, anchorName := range sortedAnchors {
		if !strings.HasPrefix(anchorName, "capitulation") {
			continue
		}
		avwapValue, ok2 := avwapValues[anchorName]
		if !ok2 || avwapValue == 0 {
			continue
		}
		volumeOK := avwapSt.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*avwapSt.Indicators.VolumeSMA
		// Reclaim: price was below capitulation AVWAP, now closes above with volume + bullish candle.
		if avwapSt.AboveCount[anchorName] >= 1 && avwapSt.AboveCount[anchorName] <= cfg.HoldBars &&
			bar.Close > avwapValue && bar.Close > bar.Open && volumeOK {
			volRatio := 0.0
			if avwapSt.Indicators.VolumeSMA > 0 {
				volRatio = bar.Volume / avwapSt.Indicators.VolumeSMA
			}
			sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideBuy, 0.9, map[string]string{
				"ref_price": fmt.Sprintf("%.10f", bar.Close),
				"setup":     "avwap_capitulation_reclaim",
				"anchor":    anchorName,
				"avwap":     fmt.Sprintf("%.4f", avwapValue),
				"vol_ratio": fmt.Sprintf("%.2f", volRatio),
				"regime_5m": regimeTag,
				"mode":      "capitulation_reclaim",
			})
			if err != nil {
				return avwapSt, nil, err
			}
			avwapSt.PendingEntry = start.SideBuy
			avwapSt.PendingEntryAt = now
			avwapSt.TradesToday++
			avwapSt.CooldownUntil = now.Add(cooldown)
			return avwapSt, []start.Signal{sig}, nil
		}
	}

	// 7. Breakout detection — scan ALL anchors for LONG first, then SHORT.
	if cfg.BreakoutEnabled {
		for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
			volRatio := 0.0
			if avwapSt.Indicators.VolumeSMA > 0 {
				volRatio = bar.Volume / avwapSt.Indicators.VolumeSMA
			}
			volumeOK := avwapSt.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*avwapSt.Indicators.VolumeSMA

			if avwapSt.AboveCount[anchorName] >= cfg.HoldBars && volumeOK {
				// Long breakouts are allowed in REVERSAL — reclaiming AVWAP after
				// a downtrend is the primary reversal signal (Brian Shannon).
				if cfg.RequireHigherLows && !hasHigherLows(avwapSt.RecentLows) {
					continue
				}
				if cfg.MinSlopeBPS > 0 && slopeOK && avwapSlope < cfg.MinSlopeBPS {
					if ctx != nil && ctx.Logger() != nil {
						ctx.Logger().Info("AVWAP slope: blocking long breakout", "symbol", symbol, "slope_bps", avwapSlope, "min", cfg.MinSlopeBPS)
					}
					continue
				}
				sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideBuy, 0.7, map[string]string{
					"ref_price": fmt.Sprintf("%.10f", bar.Close),
					"setup":     "avwap_breakout",
					"anchor":    anchorName,
					"avwap":     fmt.Sprintf("%.4f", avwapValue),
					"vol_ratio": fmt.Sprintf("%.2f", volRatio),
					"hold_bars": fmt.Sprintf("%d", avwapSt.AboveCount[anchorName]),
					"mode":      "breakout",
					"regime_5m": regimeTag,
				})
				if err != nil {
					return avwapSt, nil, err
				}
				avwapSt.PendingEntry = start.SideBuy
				avwapSt.PendingEntryAt = now
				avwapSt.TradesToday++
				avwapSt.CooldownUntil = now.Add(cooldown)
				return avwapSt, []start.Signal{sig}, nil
			}
		}

		if !strings.EqualFold(cfg.Direction, "LONG") {
			for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
				volRatio := 0.0
				if avwapSt.Indicators.VolumeSMA > 0 {
					volRatio = bar.Volume / avwapSt.Indicators.VolumeSMA
				}
				volumeOK := avwapSt.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*avwapSt.Indicators.VolumeSMA

				if avwapSt.BelowCount[anchorName] >= cfg.HoldBars && volumeOK {
					if regimeTag == "REVERSAL" {
						continue
					}
					if cfg.EnforceAVWAPBias && avwapBias != "" && avwapBias != "SHORT" {
						if ctx != nil && ctx.Logger() != nil {
							ctx.Logger().Info("AVWAP bias: blocking short breakout", "symbol", symbol, "bias", avwapBias, "anchor", anchorName)
						}
						continue
					}
					if cfg.RequireHigherLows && !hasLowerHighs(avwapSt.RecentHighs) {
						continue
					}
					if cfg.MiddayTrapShield && strings.EqualFold(cfg.AssetClass, "EQUITY") {
						barET := bar.Time.In(etLocation)
						hour := barET.Hour()
						if hour >= 11 && hour < 13 {
							middayVolOK := avwapSt.Indicators.VolumeSMA > 0 && bar.Volume > cfg.MiddayVolumeMult*avwapSt.Indicators.VolumeSMA
							if !middayVolOK {
								continue
							}
						}
					}
					if cfg.MinSlopeBPS > 0 && slopeOK && avwapSlope > -cfg.MinSlopeBPS {
						if ctx != nil && ctx.Logger() != nil {
							ctx.Logger().Info("AVWAP slope: blocking short breakout", "symbol", symbol, "slope_bps", avwapSlope, "min", -cfg.MinSlopeBPS)
						}
						continue
					}
					if cfg.RequireCapitulationForShorts {
						continue // block short breakouts; only pullback shorts allowed
					}
					sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideSell, 0.7, map[string]string{
						"ref_price": fmt.Sprintf("%.10f", bar.Close),
						"setup":     "avwap_breakout",
						"anchor":    anchorName,
						"avwap":     fmt.Sprintf("%.4f", avwapValue),
						"vol_ratio": fmt.Sprintf("%.2f", volRatio),
						"hold_bars": fmt.Sprintf("%d", avwapSt.BelowCount[anchorName]),
						"mode":      "breakout",
						"regime_5m": regimeTag,
					})
					if err != nil {
						return avwapSt, nil, err
					}
					avwapSt.PendingEntry = start.SideSell
					avwapSt.PendingEntryAt = now
					avwapSt.TradesToday++
					avwapSt.CooldownUntil = now.Add(cooldown)
					return avwapSt, []start.Signal{sig}, nil
				}
			}
		}
	}

	// 7b. Pullback-to-AVWAP detection — between breakout and bounce.
	if cfg.PullbackEnabled {
		for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
			if avwapValue == 0 {
				continue
			}
			toleranceFrac := float64(cfg.PullbackToleranceBPS) / 10000.0
			toleranceAbs := avwapValue * toleranceFrac

			volRatio := 0.0
			if avwapSt.Indicators.VolumeSMA > 0 {
				volRatio = bar.Volume / avwapSt.Indicators.VolumeSMA
			}
			volumeOK := avwapSt.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*avwapSt.Indicators.VolumeSMA
			rsiOK := avwapSt.Indicators.RSI >= cfg.PullbackRSIMin && avwapSt.Indicators.RSI <= cfg.PullbackRSIMax

			// Long pullback: was above AVWAP for trend bars, low touches AVWAP, closes above, RSI mid-range.
			// In BALANCE regime, require extra trend bars — 5 bars of "trend" is often just noise.
			requiredTrendBars := cfg.PullbackTrendBars
			if regimeTag == "BALANCE" {
				requiredTrendBars += 3
			}
			if avwapSt.PeakAboveCount[anchorName] >= requiredTrendBars &&
				bar.Low <= avwapValue+toleranceAbs &&
				bar.Close > avwapValue &&
				rsiOK && volumeOK {
				if cfg.EnforceAVWAPBias && avwapBias != "" && avwapBias != "LONG" {
					if ctx != nil && ctx.Logger() != nil {
						ctx.Logger().Info("AVWAP bias: blocking long pullback", "symbol", symbol, "bias", avwapBias, "anchor", anchorName)
					}
					continue
				}
				if cfg.MinSlopeBPS > 0 && slopeOK && avwapSlope < cfg.MinSlopeBPS {
					if ctx != nil && ctx.Logger() != nil {
						ctx.Logger().Info("AVWAP slope: blocking long pullback", "symbol", symbol, "slope_bps", avwapSlope, "min", cfg.MinSlopeBPS)
					}
					continue
				}
				sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideBuy, 0.85, map[string]string{
					"ref_price":   fmt.Sprintf("%.10f", bar.Close),
					"setup":       "avwap_pullback",
					"regime_5m":   regimeTag,
					"anchor":      anchorName,
					"avwap":       fmt.Sprintf("%.4f", avwapValue),
					"vol_ratio":   fmt.Sprintf("%.2f", volRatio),
					"peak_above":  fmt.Sprintf("%d", avwapSt.PeakAboveCount[anchorName]),
					"mode":        "pullback",
				})
				if err != nil {
					return avwapSt, nil, err
				}
				avwapSt.PendingEntry = start.SideBuy
				avwapSt.PendingEntryAt = now
				avwapSt.TradesToday++
				avwapSt.CooldownUntil = now.Add(cooldown)
				avwapSt.PeakAboveCount[anchorName] = 0
				return avwapSt, []start.Signal{sig}, nil
			}

			// Short pullback: was below AVWAP for trend bars, high reaches AVWAP, closes below, RSI mid-range.
			requiredTrendBarsShort := cfg.PullbackTrendBars
			if regimeTag == "BALANCE" {
				requiredTrendBarsShort += 3
			}
			if !strings.EqualFold(cfg.Direction, "LONG") &&
				avwapSt.PeakBelowCount[anchorName] >= requiredTrendBarsShort &&
				bar.High >= avwapValue-toleranceAbs &&
				bar.Close < avwapValue &&
				rsiOK && volumeOK {
				if cfg.EnforceAVWAPBias && avwapBias != "" && avwapBias != "SHORT" {
					if ctx != nil && ctx.Logger() != nil {
						ctx.Logger().Info("AVWAP bias: blocking short pullback", "symbol", symbol, "bias", avwapBias, "anchor", anchorName)
					}
					continue
				}
				if cfg.MinSlopeBPS > 0 && slopeOK && avwapSlope > -cfg.MinSlopeBPS {
					if ctx != nil && ctx.Logger() != nil {
						ctx.Logger().Info("AVWAP slope: blocking short pullback", "symbol", symbol, "slope_bps", avwapSlope, "min", -cfg.MinSlopeBPS)
					}
					continue
				}
				if cfg.RequireCapitulationForShorts {
					if ctx != nil && ctx.Logger() != nil {
						ctx.Logger().Info("AVWAP gate: capitulation required for short pullback", "symbol", symbol, "anchor", anchorName)
					}
					continue
				}
				sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideSell, 0.85, map[string]string{
					"ref_price":   fmt.Sprintf("%.10f", bar.Close),
					"setup":       "avwap_pullback",
					"regime_5m":   regimeTag,
					"anchor":      anchorName,
					"avwap":       fmt.Sprintf("%.4f", avwapValue),
					"vol_ratio":   fmt.Sprintf("%.2f", volRatio),
					"peak_below":  fmt.Sprintf("%d", avwapSt.PeakBelowCount[anchorName]),
					"mode":        "pullback",
				})
				if err != nil {
					return avwapSt, nil, err
				}
				avwapSt.PendingEntry = start.SideSell
				avwapSt.PendingEntryAt = now
				avwapSt.TradesToday++
				avwapSt.CooldownUntil = now.Add(cooldown)
				avwapSt.PeakBelowCount[anchorName] = 0
				return avwapSt, []start.Signal{sig}, nil
			}
		}
	}


	// 7c. Dual-AVWAP "pinch" setup — requires 2+ anchors.
	if cfg.PinchEnabled && len(avwapValues) >= 2 {
		// Find min and max AVWAP values.
		var minAVWAP, maxAVWAP float64
		first := true
		for _, anchorName := range sortedAnchors {
		v := avwapValues[anchorName]
			if first || v < minAVWAP {
				minAVWAP = v
			}
			if first || v > maxAVWAP {
				maxAVWAP = v
			}
			first = false
		}

		volumeOK := avwapSt.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*avwapSt.Indicators.VolumeSMA
		gapBPS := (maxAVWAP - minAVWAP) / minAVWAP * 10000.0
		if gapBPS >= float64(cfg.PinchMinBPS) && gapBPS <= float64(cfg.PinchMaxBPS) {
			// Long pinch breakout: price breaks above maxAVWAP.
			if bar.Close > maxAVWAP && volumeOK {
				if !(cfg.EnforceAVWAPBias && avwapBias != "" && avwapBias != "LONG") {
					sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideBuy, 0.9, map[string]string{
						"ref_price": fmt.Sprintf("%.10f", bar.Close),
						"setup":     "avwap_pinch",
						"regime_5m": regimeTag,
					})
					if err != nil {
						return avwapSt, nil, err
					}
					avwapSt.PendingEntry = start.SideBuy
					avwapSt.PendingEntryAt = now
					avwapSt.TradesToday++
					avwapSt.CooldownUntil = now.Add(cooldown)
					return avwapSt, []start.Signal{sig}, nil
				}
			}

			// Short pinch breakout: price breaks below minAVWAP.
			if bar.Close < minAVWAP && volumeOK && !strings.EqualFold(cfg.Direction, "LONG") {
				if cfg.RequireCapitulationForShorts {
					// Skip — no capitulation anchor for short pinch
				} else if !(cfg.EnforceAVWAPBias && avwapBias != "" && avwapBias != "SHORT") {
					sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideSell, 0.9, map[string]string{
						"ref_price": fmt.Sprintf("%.10f", bar.Close),
						"setup":     "avwap_pinch",
						"regime_5m": regimeTag,
					})
					if err != nil {
						return avwapSt, nil, err
					}
					avwapSt.PendingEntry = start.SideSell
					avwapSt.PendingEntryAt = now
					avwapSt.TradesToday++
					avwapSt.CooldownUntil = now.Add(cooldown)
					return avwapSt, []start.Signal{sig}, nil
				}
			}
		}
	}

	// 7d. Gap reclaim entry — price dips below AVWAP then reclaims it.
	if cfg.GapReclaimEnabled {
		if avwapSt.CrossedBelowBar == nil {
			avwapSt.CrossedBelowBar = make(map[string]int)
		}
		volumeOK := avwapSt.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*avwapSt.Indicators.VolumeSMA
		for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
			prev := avwapSt.CrossedBelowBar[anchorName]
			if bar.Close < avwapValue {
				if prev == 0 {
					avwapSt.CrossedBelowBar[anchorName] = 1 // just crossed below
				} else {
					avwapSt.CrossedBelowBar[anchorName]++
				}
			} else if prev > 0 && prev <= cfg.GapReclaimBars && bar.Close > avwapValue {
				// Reclaim! Price was below for 1-N bars, now closed above.
				avwapSt.CrossedBelowBar[anchorName] = 0
				if volumeOK {
					if cfg.EnforceAVWAPBias && avwapBias != "" && avwapBias != "LONG" {
						continue
					}
					sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideBuy, 0.85, map[string]string{
						"ref_price": fmt.Sprintf("%.10f", bar.Close),
						"setup":     "avwap_gap_reclaim",
						"regime_5m": regimeTag,
						"anchor":    anchorName,
					})
					if err != nil {
						return avwapSt, nil, err
					}
					avwapSt.PendingEntry = start.SideBuy
					avwapSt.PendingEntryAt = now
					avwapSt.TradesToday++
					avwapSt.CooldownUntil = now.Add(cooldown)
					return avwapSt, []start.Signal{sig}, nil
				}
			} else {
				avwapSt.CrossedBelowBar[anchorName] = 0
			}
		}
	}

	// 7e. Handoff entry — momentum acceleration away from AVWAP.
	// Per Brian Shannon: a "handoff point" occurs when price accelerates away from
	// the AVWAP after nearly touching it. Each bar closes further from the line,
	// creating a new momentum layer. This is a high-conviction trend continuation signal.
	if cfg.HandoffEnabled {
		handoffBars := cfg.HandoffBars
		if handoffBars < 2 {
			handoffBars = 3
		}
		for _, anchorName := range sortedAnchors {
			hist := avwapSt.AVWAPDistHistory[anchorName]
			if len(hist) < handoffBars+1 {
				continue
			}
			recent := hist[len(hist)-handoffBars:]
			minMom := float64(cfg.HandoffMinMomBPS)

			// Long handoff: consecutive bars with increasing positive distance from AVWAP.
			// The first bar in the window must be near the AVWAP (within 30 bps) —
			// this ensures we're capturing the "bounce and accelerate" pattern.
			nearAVWAP := hist[len(hist)-handoffBars-1]
			if nearAVWAP >= 0 && nearAVWAP <= 30 { // was near/at AVWAP from above
				allIncreasing := true
				for i := 1; i < len(recent); i++ {
					if recent[i] <= recent[i-1] || recent[i] <= 0 {
						allIncreasing = false
						break
					}
				}
				if allIncreasing && recent[len(recent)-1] >= minMom {
					volumeOK := avwapSt.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*avwapSt.Indicators.VolumeSMA
					if volumeOK {
						if cfg.EnforceAVWAPBias && avwapBias != "" && avwapBias != "LONG" {
							continue
						}
						if cfg.RequireHigherLows && !hasHigherLows(avwapSt.RecentLows) {
							continue
						}
						if cfg.MinSlopeBPS > 0 && slopeOK && avwapSlope < cfg.MinSlopeBPS {
							continue
						}
						sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideBuy, 0.85, map[string]string{
							"ref_price":    fmt.Sprintf("%.10f", bar.Close),
							"setup":        "avwap_handoff",
							"anchor":       anchorName,
							"avwap":        fmt.Sprintf("%.4f", avwapValues[anchorName]),
							"momentum_bps": fmt.Sprintf("%.1f", recent[len(recent)-1]),
							"regime_5m":    regimeTag,
							"mode":         "handoff",
						})
						if err != nil {
							return avwapSt, nil, err
						}
						avwapSt.PendingEntry = start.SideBuy
						avwapSt.PendingEntryAt = now
						avwapSt.TradesToday++
						avwapSt.CooldownUntil = now.Add(cooldown)
						return avwapSt, []start.Signal{sig}, nil
					}
				}
			}

			// Short handoff: consecutive bars with increasing negative distance from AVWAP.
			if !strings.EqualFold(cfg.Direction, "LONG") && nearAVWAP <= 0 && nearAVWAP >= -30 {
				allDecreasing := true
				for i := 1; i < len(recent); i++ {
					if recent[i] >= recent[i-1] || recent[i] >= 0 {
						allDecreasing = false
						break
					}
				}
				if allDecreasing && recent[len(recent)-1] <= -minMom {
					volumeOK := avwapSt.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*avwapSt.Indicators.VolumeSMA
					if volumeOK {
						if cfg.EnforceAVWAPBias && avwapBias != "" && avwapBias != "SHORT" {
							continue
						}
						if cfg.RequireCapitulationForShorts {
							continue
						}
						if cfg.RequireHigherLows && !hasLowerHighs(avwapSt.RecentHighs) {
							continue
						}
						if cfg.MinSlopeBPS > 0 && slopeOK && avwapSlope > -cfg.MinSlopeBPS {
							continue
						}
						sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideSell, 0.85, map[string]string{
							"ref_price":    fmt.Sprintf("%.10f", bar.Close),
							"setup":        "avwap_handoff",
							"anchor":       anchorName,
							"avwap":        fmt.Sprintf("%.4f", avwapValues[anchorName]),
							"momentum_bps": fmt.Sprintf("%.1f", recent[len(recent)-1]),
							"regime_5m":    regimeTag,
							"mode":         "handoff",
						})
						if err != nil {
							return avwapSt, nil, err
						}
						avwapSt.PendingEntry = start.SideSell
						avwapSt.PendingEntryAt = now
						avwapSt.TradesToday++
						avwapSt.CooldownUntil = now.Add(cooldown)
						return avwapSt, []start.Signal{sig}, nil
					}
				}
			}
		}
	}

	// 8. Bounce detection.
	if cfg.BounceEnabled {
		for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
			touchesAVWAP := bar.Low <= avwapValue && avwapValue <= bar.High

			// Long bounce: touches AVWAP + RSI < max + bullish candle.
			if touchesAVWAP && avwapSt.Indicators.RSI > 0 && avwapSt.Indicators.RSI < cfg.RSIBounceMax {
				if cfg.EnforceAVWAPBias && avwapBias != "" && avwapBias != "LONG" {
					if ctx != nil && ctx.Logger() != nil {
						ctx.Logger().Info("AVWAP bias: blocking long bounce", "symbol", symbol, "bias", avwapBias, "anchor", anchorName)
					}
					continue
				}
				if regimeTag == "TREND" {
					continue
				}
				if bar.Close <= bar.Open {
					continue
				}
				sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideBuy, 0.6, map[string]string{
					"ref_price": fmt.Sprintf("%.10f", bar.Close),
					"setup":     "avwap_bounce",
					"anchor":    anchorName,
					"avwap":     fmt.Sprintf("%.4f", avwapValue),
					"rsi":       fmt.Sprintf("%.2f", avwapSt.Indicators.RSI),
					"mode":      "bounce",
					"regime_5m": regimeTag,
				})
				if err != nil {
					return avwapSt, nil, err
				}
				avwapSt.PendingEntry = start.SideBuy
				avwapSt.PendingEntryAt = now
				avwapSt.TradesToday++
				avwapSt.CooldownUntil = now.Add(cooldown)
				return avwapSt, []start.Signal{sig}, nil
			}

			// Direction guard: skip short entries in long-only mode (e.g. crypto).
			if strings.EqualFold(cfg.Direction, "LONG") {
				continue
			}

			// Short bounce: touches AVWAP + RSI > min + bearish candle.
			if touchesAVWAP && avwapSt.Indicators.RSI > cfg.RSIBounceMin {
				if cfg.EnforceAVWAPBias && avwapBias != "" && avwapBias != "SHORT" {
					if ctx != nil && ctx.Logger() != nil {
						ctx.Logger().Info("AVWAP bias: blocking short bounce", "symbol", symbol, "bias", avwapBias, "anchor", anchorName)
					}
					continue
				}
				if regimeTag == "TREND" {
					continue
				}
				if bar.Close >= bar.Open {
					continue
				}
				if cfg.RequireCapitulationForShorts {
					continue // block short bounces; only pullback shorts allowed
				}
				sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideSell, 0.6, map[string]string{
					"ref_price": fmt.Sprintf("%.10f", bar.Close),
					"setup":     "avwap_bounce",
					"anchor":    anchorName,
					"avwap":     fmt.Sprintf("%.4f", avwapValue),
					"rsi":       fmt.Sprintf("%.2f", avwapSt.Indicators.RSI),
					"mode":      "bounce",
					"regime_5m": regimeTag,
				})
				if err != nil {
					return avwapSt, nil, err
				}
				avwapSt.PendingEntry = start.SideSell
				avwapSt.PendingEntryAt = now
				avwapSt.TradesToday++
				avwapSt.CooldownUntil = now.Add(cooldown)
				return avwapSt, []start.Signal{sig}, nil
			}
		}
	}

	return avwapSt, nil, nil
}

// OnEvent handles fill confirmations and entry rejections for AVWAP strategy.
func (s *AVWAPStrategy) OnEvent(ctx start.Context, symbol string, evt any, st start.State) (start.State, []start.Signal, error) {
	avwapSt, ok := st.(*AVWAPState)
	if !ok {
		return st, nil, fmt.Errorf("AVWAPStrategy.OnEvent: expected *AVWAPState, got %T", st)
	}

	switch e := evt.(type) {
	case start.FillConfirmation:
		if avwapSt.PendingEntry != "" {
			avwapSt.PositionSide = avwapSt.PendingEntry
			avwapSt.PendingEntry = ""
			avwapSt.PendingEntryAt = time.Time{}
			avwapSt.SwingTrailStop = 0
			avwapSt.SwingTrailBars = 0
			if ctx != nil && ctx.Logger() != nil {
				ctx.Logger().Info("AVWAPStrategy: fill confirmed, position active", "symbol", symbol, "side", avwapSt.PositionSide, "price", e.Price)
			}
		}
		return avwapSt, nil, nil

	case start.EntryRejection:
		if avwapSt.PendingEntry != "" {
			if ctx != nil && ctx.Logger() != nil {
				ctx.Logger().Warn("AVWAPStrategy: entry rejected, clearing pending", "symbol", symbol, "side", avwapSt.PendingEntry, "reason", e.Reason)
			}
			avwapSt.PendingEntry = ""
			avwapSt.PendingEntryAt = time.Time{}
			avwapSt.CooldownUntil = time.Time{}
			if avwapSt.TradesToday > 0 {
				avwapSt.TradesToday--
			}
		}
		return avwapSt, nil, nil

	default:
		return avwapSt, nil, nil
	}
}

// --- Serialization ---

type avwapStateJSON struct {
	Symbol         string                             `json:"symbol"`
	Config         AVWAPConfig                        `json:"config"`
	CalcStates     map[string]start.AnchoredVWAPState `json:"calc_states"`
	Anchors        []start.AnchorPoint                `json:"anchors"`
	AboveCount     map[string]int                     `json:"above_count"`
	BelowCount     map[string]int                     `json:"below_count"`
	PeakAboveCount  map[string]int                     `json:"peak_above_count,omitempty"`
	PeakBelowCount  map[string]int                     `json:"peak_below_count,omitempty"`
	StopBelowCount  map[string]int                     `json:"stop_below_count,omitempty"`
	StopAboveCount  map[string]int                     `json:"stop_above_count,omitempty"`
	CrossedBelowBar map[string]int                     `json:"crossed_below_bar,omitempty"`
	TradesToday     int                                `json:"trades_today"`
	CooldownUntil  time.Time                          `json:"cooldown_until"`
	PositionSide   start.Side                         `json:"position_side"`
	PendingEntry   start.Side                         `json:"pending_entry"`
	PendingEntryAt time.Time                          `json:"pending_entry_at"`
	Indicators     start.IndicatorData                `json:"indicators"`
	RecentLows     []float64                          `json:"recent_lows,omitempty"`
	RecentHighs    []float64                          `json:"recent_highs,omitempty"`
}

func (s *AVWAPState) Marshal() ([]byte, error) {
	// Extract anchor points for serialization.
	avwapValues := s.Calc.Values()
	marshalSorted := s.Calc.SortedNames()
	anchors := make([]start.AnchorPoint, 0, len(avwapValues))
	for _, name := range marshalSorted {
		anchors = append(anchors, start.AnchorPoint{Name: name})
	}

	j := avwapStateJSON{
		Symbol:         s.Symbol,
		Config:         s.Config,
		CalcStates:     s.Calc.States(),
		Anchors:        anchors,
		AboveCount:     s.AboveCount,
		BelowCount:     s.BelowCount,
		PeakAboveCount:  s.PeakAboveCount,
		PeakBelowCount:  s.PeakBelowCount,
		StopBelowCount:  s.StopBelowCount,
		StopAboveCount:  s.StopAboveCount,
		CrossedBelowBar: s.CrossedBelowBar,
		TradesToday:     s.TradesToday,
		CooldownUntil:  s.CooldownUntil,
		PositionSide:   s.PositionSide,
		PendingEntry:   s.PendingEntry,
		PendingEntryAt: s.PendingEntryAt,
		Indicators:     s.Indicators,
		RecentLows:     s.RecentLows,
		RecentHighs:    s.RecentHighs,
	}
	return json.Marshal(j)
}

func (s *AVWAPState) Unmarshal(data []byte) error {
	var j avwapStateJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return fmt.Errorf("AVWAPState.Unmarshal: %w", err)
	}
	s.Symbol = j.Symbol
	s.Config = j.Config
	s.AboveCount = j.AboveCount
	s.BelowCount = j.BelowCount
	s.PeakAboveCount = j.PeakAboveCount
	s.PeakBelowCount = j.PeakBelowCount
	s.StopBelowCount = j.StopBelowCount
	s.StopAboveCount = j.StopAboveCount
	s.CrossedBelowBar = j.CrossedBelowBar
	s.TradesToday = j.TradesToday
	s.CooldownUntil = j.CooldownUntil
	s.PositionSide = j.PositionSide
	s.PendingEntry = j.PendingEntry
	s.PendingEntryAt = j.PendingEntryAt
	s.Indicators = j.Indicators
	s.RecentLows = j.RecentLows
	s.RecentHighs = j.RecentHighs

	s.Calc = start.NewAnchoredVWAPCalc()
	s.Calc.Restore(j.Anchors, j.CalcStates)

	if s.AboveCount == nil {
		s.AboveCount = make(map[string]int)
	}
	if s.BelowCount == nil {
		s.BelowCount = make(map[string]int)
	}
	if s.PeakAboveCount == nil {
		s.PeakAboveCount = make(map[string]int)
	}
	if s.PeakBelowCount == nil {
		s.PeakBelowCount = make(map[string]int)
	}
	if s.StopBelowCount == nil {
		s.StopBelowCount = make(map[string]int)
	}
	if s.StopAboveCount == nil {
		s.StopAboveCount = make(map[string]int)
	}
	if s.CrossedBelowBar == nil {
		s.CrossedBelowBar = make(map[string]int)
	}
	return nil
}
