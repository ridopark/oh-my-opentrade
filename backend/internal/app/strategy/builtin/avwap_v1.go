package builtin

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
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

	// Shift prev bars ring buffer
	avwapSt.PrevBars[1] = avwapSt.PrevBars[0]
	avwapSt.PrevBars[0] = bar
	if avwapSt.PrevBarCount < 2 {
		avwapSt.PrevBarCount++
	}

	// Rolling 50-bar high/low window for Fibonacci
	avwapSt.BarHighs50 = append(avwapSt.BarHighs50, bar.High)
	if len(avwapSt.BarHighs50) > 50 {
		avwapSt.BarHighs50 = avwapSt.BarHighs50[1:]
	}
	avwapSt.BarLows50 = append(avwapSt.BarLows50, bar.Low)
	if len(avwapSt.BarLows50) > 50 {
		avwapSt.BarLows50 = avwapSt.BarLows50[1:]
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

	HandoffEnabled    bool
	HandoffBars       int // consecutive bars of accelerating distance from AVWAP (default 3)
	HandoffMinMomBPS  int // minimum final distance from AVWAP in bps to confirm handoff (default 40)

	MinConfluenceScore        int  // minimum confluence score for entry (0 = no gate)
	FibConfluenceEnabled      bool // enable Fibonacci confluence factor
	KeyLevelConfluenceEnabled bool // enable key level confluence factor
	CandleConfluenceEnabled   bool // enable candlestick pattern confluence factor
	BandConfluenceEnabled     bool // enable AVWAP band zone confluence factor
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

	LockedOutSide   start.Side // side that was stopped out; prevents same-direction re-entry

	PrevBars     [2]start.Bar       // 2-bar lookback for candlestick patterns
	PrevBarCount int                // how many prev bars have been stored (0, 1, or 2)
	BarHighs50   []float64          // rolling 50-bar high window for Fibonacci
	BarLows50    []float64          // rolling 50-bar low window for Fibonacci
	KeyLevels    map[string]float64 // key price levels (pd_high, pd_low, or_high, or_low)
}

// entryContext bundles all per-bar read-only data needed by entry/exit helpers,
// so we don't have to pass ~15 individual parameters through every method call.
type entryContext struct {
	ctx          start.Context
	cfg          AVWAPConfig
	bar          start.Bar
	symbol       string
	instanceID   start.InstanceID
	now          time.Time
	cooldown     time.Duration
	avwapValues  map[string]float64
	sortedAnchors []string
	avwapBias    string
	avwapSlope   float64
	slopeOK      bool
	regimeTag    string
	lockedLong   bool
	lockedShort  bool
	etLocation   *time.Location
	keyLevels    map[string]float64
}

// logShortGate emits a structured debug log when a short entry is blocked at a gate.
func logShortGate(ctx start.Context, symbol, gate, anchor string, kvs ...string) {
	if ctx == nil || ctx.Logger() == nil {
		return
	}
	args := []any{"symbol", symbol, "gate", gate, "anchor", anchor}
	for i := 0; i+1 < len(kvs); i += 2 {
		args = append(args, kvs[i], kvs[i+1])
	}
	ctx.Logger().Info("AVWAP short gate blocked", args...)
}

// SetIndicators implements the indicatorSetter interface.
func (s *AVWAPState) SetIndicators(ind start.IndicatorData) {
	s.Indicators = ind
}

func (s *AVWAPState) AnchorNames() []string { return s.Config.Anchors }

// SetKeyLevels stores key price levels (pd_high, pd_low, or_high, or_low) for confluence scoring.
func (s *AVWAPState) SetKeyLevels(levels map[string]float64) {
	s.KeyLevels = levels
}

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
		s.LockedOutSide = ""
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
	s.LockedOutSide = ""
}

func (s *AVWAPState) ClearPendingEntry() {
	s.PendingEntry = ""
	s.PendingEntryAt = time.Time{}
}

// UpdateCalcAnchor feeds a bar into a single named anchor in the AVWAP calculator.
// Used to replay previous-day bars into individual anchors (pd_high, pd_low)
// without affecting other anchors or the lastBarTime dedup guard.
func (s *AVWAPState) UpdateCalcAnchor(name string, bar start.Bar) {
	if s.Calc != nil {
		s.Calc.UpdateSingleAnchor(name, bar.Time, bar.High, bar.Low, bar.Close, bar.Volume)
	}
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
			switch s.PositionSide {
			case start.SideBuy:
				if bar.Close < avwapValue-bufferAbs {
					s.StopBelowCount[anchorName]++
				} else {
					s.StopBelowCount[anchorName] = 0
				}
			case start.SideSell:
				if bar.Close > avwapValue+bufferAbs {
					s.StopAboveCount[anchorName]++
				} else {
					s.StopAboveCount[anchorName] = 0
				}
			}
		}
		switch s.PositionSide {
		case start.SideBuy:
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
		case start.SideSell:
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
	// Allow ties (<=) but not lower lows (<). Equal lows still represent
	// a valid "higher lows" pattern in real markets.
	for i := 1; i < len(lows); i++ {
		if lows[i] < lows[i-1] {
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

		HandoffEnabled:   getBool(params, "handoff_enabled", false),
		HandoffBars:      getInt(params, "handoff_bars", 3),
		HandoffMinMomBPS: getInt(params, "handoff_min_mom_bps", 40),

		MinConfluenceScore:        getInt(params, "min_confluence_score", 0),
		FibConfluenceEnabled:      getBool(params, "fib_confluence_enabled", true),
		KeyLevelConfluenceEnabled: getBool(params, "key_level_confluence_enabled", true),
		CandleConfluenceEnabled:   getBool(params, "candle_confluence_enabled", true),
		BandConfluenceEnabled:     getBool(params, "band_confluence_enabled", true),
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

// --- Exit methods ---

// evaluateBasicExit checks whether the current position should exit based on
// the ExitHoldBars rule (price held against position for N consecutive bars).
func (s *AVWAPState) evaluateBasicExit(ec entryContext) (*start.Signal, error) {
	if s.PendingEntry != "" {
		return nil, nil
	}
	cfg := ec.cfg
	instanceID := ec.instanceID
	now := ec.now
	cooldown := ec.cooldown
	regimeTag := ec.regimeTag

	if s.PositionSide == start.SideBuy {
		for _, belowCnt := range s.BelowCount {
			if belowCnt >= cfg.ExitHoldBars {
				sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalExit, start.SideSell, 0.8, map[string]string{
					"ref_price": fmt.Sprintf("%.10f", ec.bar.Close),
					"setup":     "avwap_exit",
					"regime_5m": regimeTag,
				})
				if err != nil {
					return nil, err
				}
				s.PositionSide = ""
				s.CooldownUntil = now.Add(cooldown)
				for anchorName := range s.AboveCount {
					s.AboveCount[anchorName] = 0
					s.BelowCount[anchorName] = 0
				}
				return &sig, nil
			}
		}
	}
	if s.PositionSide == start.SideSell {
		for _, aboveCnt := range s.AboveCount {
			if aboveCnt >= cfg.ExitHoldBars {
				sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalExit, start.SideBuy, 0.8, map[string]string{
					"ref_price": fmt.Sprintf("%.10f", ec.bar.Close),
					"setup":     "avwap_exit",
					"regime_5m": regimeTag,
				})
				if err != nil {
					return nil, err
				}
				s.PositionSide = ""
				s.CooldownUntil = now.Add(cooldown)
				for anchorName := range s.AboveCount {
					s.AboveCount[anchorName] = 0
					s.BelowCount[anchorName] = 0
				}
				return &sig, nil
			}
		}
	}
	return nil, nil
}

// evaluateAVWAPStop checks the AVWAP-based stop: exit when price breaks significantly
// past AVWAP against position for AVWAPStopBars consecutive bars.
func (s *AVWAPState) evaluateAVWAPStop(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	if !cfg.AVWAPStopEnabled || s.PositionSide == "" || s.PendingEntry != "" {
		return nil, nil
	}
	if s.StopBelowCount == nil {
		s.StopBelowCount = make(map[string]int)
	}
	if s.StopAboveCount == nil {
		s.StopAboveCount = make(map[string]int)
	}

	instanceID := ec.instanceID
	now := ec.now
	cooldown := ec.cooldown
	regimeTag := ec.regimeTag
	sortedAnchors := ec.sortedAnchors
	avwapValues := ec.avwapValues
	bar := ec.bar

	// Update stop counters for each anchor.
	for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
		bufferAbs := avwapValue * float64(cfg.AVWAPStopBufferBPS) / 10000.0
		switch s.PositionSide {
		case start.SideBuy:
			if bar.Close < avwapValue-bufferAbs {
				s.StopBelowCount[anchorName]++
			} else {
				s.StopBelowCount[anchorName] = 0
			}
		case start.SideSell:
			if bar.Close > avwapValue+bufferAbs {
				s.StopAboveCount[anchorName]++
			} else {
				s.StopAboveCount[anchorName] = 0
			}
		}
	}

	// Check if any anchor triggered the stop.
	switch s.PositionSide {
	case start.SideBuy:
		for _, anchorName := range sortedAnchors {
			if s.StopBelowCount[anchorName] >= cfg.AVWAPStopBars {
				sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalExit, start.SideSell, 0.9, map[string]string{
					"ref_price": fmt.Sprintf("%.10f", bar.Close),
					"setup":     "avwap_stop",
					"anchor":    anchorName,
					"regime_5m": regimeTag,
				})
				if err != nil {
					return nil, err
				}
				s.PositionSide = ""
				s.LockedOutSide = start.SideBuy
				s.CooldownUntil = now.Add(cooldown)
				for an := range s.StopBelowCount {
					s.StopBelowCount[an] = 0
				}
				return &sig, nil
			}
		}
	case start.SideSell:
		for _, anchorName := range sortedAnchors {
			if s.StopAboveCount[anchorName] >= cfg.AVWAPStopBars {
				sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalExit, start.SideBuy, 0.9, map[string]string{
					"ref_price": fmt.Sprintf("%.10f", bar.Close),
					"setup":     "avwap_stop",
					"anchor":    anchorName,
					"regime_5m": regimeTag,
				})
				if err != nil {
					return nil, err
				}
				s.PositionSide = ""
				s.LockedOutSide = start.SideSell
				s.CooldownUntil = now.Add(cooldown)
				for an := range s.StopAboveCount {
					s.StopAboveCount[an] = 0
				}
				return &sig, nil
			}
		}
	}
	return nil, nil
}

// evaluateExits runs all exit checks in order and returns the first triggered signal.
func (s *AVWAPState) evaluateExits(ec entryContext) (*start.Signal, error) {
	if sig, err := s.evaluateBasicExit(ec); err != nil || sig != nil {
		return sig, err
	}
	if sig, err := s.evaluateAVWAPStop(ec); err != nil || sig != nil {
		return sig, err
	}
	return nil, nil
}

// --- Entry methods ---

// evaluateCapitulationReclaim checks section 6e: capitulation AVWAP reclaim long entry.
func (s *AVWAPState) evaluateCapitulationReclaim(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	bar := ec.bar
	now := ec.now
	cooldown := ec.cooldown
	instanceID := ec.instanceID
	regimeTag := ec.regimeTag
	avwapValues := ec.avwapValues
	sortedAnchors := ec.sortedAnchors

	for _, anchorName := range sortedAnchors {
		if !strings.HasPrefix(anchorName, "capitulation") {
			continue
		}
		avwapValue, ok := avwapValues[anchorName]
		if !ok || avwapValue == 0 {
			continue
		}
		volumeOK := s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA
		if ec.lockedLong {
			continue
		}
		if s.AboveCount[anchorName] >= 1 && s.AboveCount[anchorName] <= cfg.HoldBars &&
			bar.Close > avwapValue && bar.Close > bar.Open && volumeOK {
			conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
				s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50)
			if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
				continue
			}
			adjustedStrength := math.Min(1.0, 0.9+float64(conf.Score)*0.03)
			volRatio := 0.0
			if s.Indicators.VolumeSMA > 0 {
				volRatio = bar.Volume / s.Indicators.VolumeSMA
			}
			sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalEntry, start.SideBuy, adjustedStrength, map[string]string{
				"ref_price":         fmt.Sprintf("%.10f", bar.Close),
				"setup":             "avwap_capitulation_reclaim",
				"anchor":            anchorName,
				"avwap":             fmt.Sprintf("%.4f", avwapValue),
				"vol_ratio":         fmt.Sprintf("%.2f", volRatio),
				"regime_5m":         regimeTag,
				"mode":              "capitulation_reclaim",
				"confluence":        fmt.Sprintf("%d", conf.Score),
				"confluence_detail": strings.Join(conf.Factors, "+"),
			})
			if err != nil {
				return nil, err
			}
			s.PendingEntry = start.SideBuy
			s.PendingEntryAt = now
			s.TradesToday++
			s.CooldownUntil = now.Add(cooldown)
			return &sig, nil
		}
	}
	return nil, nil
}

// evaluateBreakout checks section 7: AVWAP breakout entries (long and short).
func (s *AVWAPState) evaluateBreakout(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	if !cfg.BreakoutEnabled {
		return nil, nil
	}
	bar := ec.bar
	now := ec.now
	cooldown := ec.cooldown
	instanceID := ec.instanceID
	regimeTag := ec.regimeTag
	avwapValues := ec.avwapValues
	sortedAnchors := ec.sortedAnchors
	ctx := ec.ctx

	// Long breakouts
	for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
		volRatio := 0.0
		if s.Indicators.VolumeSMA > 0 {
			volRatio = bar.Volume / s.Indicators.VolumeSMA
		}
		volumeOK := s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA

		if s.AboveCount[anchorName] >= cfg.HoldBars && volumeOK && !ec.lockedLong {
			if cfg.RequireHigherLows && !hasHigherLows(s.RecentLows) {
				continue
			}
			if cfg.MinSlopeBPS > 0 && ec.slopeOK && ec.avwapSlope < cfg.MinSlopeBPS {
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP slope: blocking long breakout", "symbol", ec.symbol, "slope_bps", ec.avwapSlope, "min", cfg.MinSlopeBPS)
				}
				continue
			}
			conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
				s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50)
			if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
				continue
			}
			adjustedStrength := math.Min(1.0, 0.7+float64(conf.Score)*0.03)
			sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalEntry, start.SideBuy, adjustedStrength, map[string]string{
				"ref_price":         fmt.Sprintf("%.10f", bar.Close),
				"setup":             "avwap_breakout",
				"anchor":            anchorName,
				"avwap":             fmt.Sprintf("%.4f", avwapValue),
				"vol_ratio":         fmt.Sprintf("%.2f", volRatio),
				"hold_bars":         fmt.Sprintf("%d", s.AboveCount[anchorName]),
				"mode":              "breakout",
				"regime_5m":         regimeTag,
				"confluence":        fmt.Sprintf("%d", conf.Score),
				"confluence_detail": strings.Join(conf.Factors, "+"),
			})
			if err != nil {
				return nil, err
			}
			s.PendingEntry = start.SideBuy
			s.PendingEntryAt = now
			s.TradesToday++
			s.CooldownUntil = now.Add(cooldown)
			return &sig, nil
		}
	}

	// Short breakouts
	if !strings.EqualFold(cfg.Direction, "LONG") {
		for _, anchorName := range sortedAnchors {
			avwapValue := avwapValues[anchorName]
			volRatio := 0.0
			if s.Indicators.VolumeSMA > 0 {
				volRatio = bar.Volume / s.Indicators.VolumeSMA
			}
			volumeOK := s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA

			if s.BelowCount[anchorName] >= cfg.HoldBars && volumeOK && !ec.lockedShort {
				// Regime gating handled by evaluateEntries — breakout only called in REVERSAL
				if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "SHORT" {
					logShortGate(ctx, ec.symbol, "enforce_avwap_bias", anchorName, "bias", ec.avwapBias)
					if ctx != nil && ctx.Logger() != nil {
						ctx.Logger().Info("AVWAP bias: blocking short breakout", "symbol", ec.symbol, "bias", ec.avwapBias, "anchor", anchorName)
					}
					continue
				}
				// Note: require_higher_lows is NOT applied to shorts — the bias gate
				// + slope gate already confirm downtrend structure. Requiring strict
				// lower highs blocked 9000+ short attempts in backtests.
				if cfg.MiddayTrapShield && strings.EqualFold(cfg.AssetClass, "EQUITY") {
					barET := bar.Time.In(ec.etLocation)
					hour := barET.Hour()
					if hour >= 11 && hour < 13 {
						middayVolOK := s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.MiddayVolumeMult*s.Indicators.VolumeSMA
						if !middayVolOK {
							logShortGate(ctx, ec.symbol, "midday_trap_shield", anchorName)
							continue
						}
					}
				}
				if cfg.MinSlopeBPS > 0 && ec.slopeOK && ec.avwapSlope > -cfg.MinSlopeBPS {
					logShortGate(ctx, ec.symbol, "min_slope_bps", anchorName, "slope_bps", fmt.Sprintf("%.2f", ec.avwapSlope))
					if ctx != nil && ctx.Logger() != nil {
						ctx.Logger().Info("AVWAP slope: blocking short breakout", "symbol", ec.symbol, "slope_bps", ec.avwapSlope, "min", -cfg.MinSlopeBPS)
					}
					continue
				}
				if cfg.RequireCapitulationForShorts && ec.avwapBias == "LONG" {
					logShortGate(ctx, ec.symbol, "require_capitulation", anchorName, "bias", ec.avwapBias)
					continue
				}
				conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
					s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50)
				if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
					continue
				}
				adjustedStrength := math.Min(1.0, 0.7+float64(conf.Score)*0.03)
				sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalEntry, start.SideSell, adjustedStrength, map[string]string{
					"ref_price":         fmt.Sprintf("%.10f", bar.Close),
					"setup":             "avwap_breakout",
					"anchor":            anchorName,
					"avwap":             fmt.Sprintf("%.4f", avwapValue),
					"vol_ratio":         fmt.Sprintf("%.2f", volRatio),
					"hold_bars":         fmt.Sprintf("%d", s.BelowCount[anchorName]),
					"mode":              "breakout",
					"regime_5m":         regimeTag,
					"confluence":        fmt.Sprintf("%d", conf.Score),
					"confluence_detail": strings.Join(conf.Factors, "+"),
				})
				if err != nil {
					return nil, err
				}
				s.PendingEntry = start.SideSell
				s.PendingEntryAt = now
				s.TradesToday++
				s.CooldownUntil = now.Add(cooldown)
				return &sig, nil
			}
		}
	}
	return nil, nil
}

// evaluatePullback checks section 7b: pullback-to-AVWAP entries.
func (s *AVWAPState) evaluatePullback(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	if !cfg.PullbackEnabled {
		return nil, nil
	}
	bar := ec.bar
	now := ec.now
	cooldown := ec.cooldown
	instanceID := ec.instanceID
	regimeTag := ec.regimeTag
	avwapValues := ec.avwapValues
	sortedAnchors := ec.sortedAnchors
	ctx := ec.ctx

	for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
		if avwapValue == 0 {
			continue
		}
		toleranceFrac := float64(cfg.PullbackToleranceBPS) / 10000.0
		toleranceAbs := avwapValue * toleranceFrac

		volRatio := 0.0
		if s.Indicators.VolumeSMA > 0 {
			volRatio = bar.Volume / s.Indicators.VolumeSMA
		}
		volumeOK := s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA
		rsiOK := s.Indicators.RSI >= cfg.PullbackRSIMin && s.Indicators.RSI <= cfg.PullbackRSIMax

		// Long pullback: was above AVWAP for trend bars, low touches AVWAP, closes above, RSI mid-range.
		requiredTrendBars := cfg.PullbackTrendBars
		if regimeTag == "BALANCE" {
			requiredTrendBars += 3
		}
		if !ec.lockedLong && s.PeakAboveCount[anchorName] >= requiredTrendBars &&
			bar.Low <= avwapValue+toleranceAbs &&
			bar.Close > avwapValue &&
			rsiOK && volumeOK {
			if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "LONG" {
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP bias: blocking long pullback", "symbol", ec.symbol, "bias", ec.avwapBias, "anchor", anchorName)
				}
				continue
			}
			if cfg.MinSlopeBPS > 0 && ec.slopeOK && ec.avwapSlope < cfg.MinSlopeBPS {
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP slope: blocking long pullback", "symbol", ec.symbol, "slope_bps", ec.avwapSlope, "min", cfg.MinSlopeBPS)
				}
				continue
			}
			conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
				s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50)
			if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
				continue
			}
			adjustedStrength := math.Min(1.0, 0.85+float64(conf.Score)*0.03)
			sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalEntry, start.SideBuy, adjustedStrength, map[string]string{
				"ref_price":         fmt.Sprintf("%.10f", bar.Close),
				"setup":             "avwap_pullback",
				"regime_5m":         regimeTag,
				"anchor":            anchorName,
				"avwap":             fmt.Sprintf("%.4f", avwapValue),
				"vol_ratio":         fmt.Sprintf("%.2f", volRatio),
				"peak_above":        fmt.Sprintf("%d", s.PeakAboveCount[anchorName]),
				"mode":              "pullback",
				"confluence":        fmt.Sprintf("%d", conf.Score),
				"confluence_detail": strings.Join(conf.Factors, "+"),
			})
			if err != nil {
				return nil, err
			}
			s.PendingEntry = start.SideBuy
			s.PendingEntryAt = now
			s.TradesToday++
			s.CooldownUntil = now.Add(cooldown)
			s.PeakAboveCount[anchorName] = 0
			return &sig, nil
		}

		// Short pullback: was below AVWAP for trend bars, high reaches AVWAP, closes below, RSI mid-range.
		requiredTrendBarsShort := cfg.PullbackTrendBars
		if regimeTag == "BALANCE" {
			requiredTrendBarsShort += 3
		}
		if !ec.lockedShort && !strings.EqualFold(cfg.Direction, "LONG") &&
			s.PeakBelowCount[anchorName] >= requiredTrendBarsShort &&
			bar.High >= avwapValue-toleranceAbs &&
			bar.Close < avwapValue &&
			rsiOK && volumeOK {
			if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "SHORT" {
				logShortGate(ctx, ec.symbol, "enforce_avwap_bias", anchorName, "bias", ec.avwapBias)
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP bias: blocking short pullback", "symbol", ec.symbol, "bias", ec.avwapBias, "anchor", anchorName)
				}
				continue
			}
			if cfg.MinSlopeBPS > 0 && ec.slopeOK && ec.avwapSlope > -cfg.MinSlopeBPS {
				logShortGate(ctx, ec.symbol, "min_slope_bps", anchorName, "slope_bps", fmt.Sprintf("%.2f", ec.avwapSlope))
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP slope: blocking short pullback", "symbol", ec.symbol, "slope_bps", ec.avwapSlope, "min", -cfg.MinSlopeBPS)
				}
				continue
			}
			if cfg.RequireCapitulationForShorts && ec.avwapBias == "LONG" {
				logShortGate(ctx, ec.symbol, "require_capitulation", anchorName, "bias", ec.avwapBias)
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP gate: capitulation required for short pullback (above AVWAP)", "symbol", ec.symbol, "anchor", anchorName)
				}
				continue
			}
			conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
				s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50)
			if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
				continue
			}
			adjustedStrength := math.Min(1.0, 0.85+float64(conf.Score)*0.03)
			sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalEntry, start.SideSell, adjustedStrength, map[string]string{
				"ref_price":         fmt.Sprintf("%.10f", bar.Close),
				"setup":             "avwap_pullback",
				"regime_5m":         regimeTag,
				"anchor":            anchorName,
				"avwap":             fmt.Sprintf("%.4f", avwapValue),
				"vol_ratio":         fmt.Sprintf("%.2f", volRatio),
				"peak_below":        fmt.Sprintf("%d", s.PeakBelowCount[anchorName]),
				"mode":              "pullback",
				"confluence":        fmt.Sprintf("%d", conf.Score),
				"confluence_detail": strings.Join(conf.Factors, "+"),
			})
			if err != nil {
				return nil, err
			}
			s.PendingEntry = start.SideSell
			s.PendingEntryAt = now
			s.TradesToday++
			s.CooldownUntil = now.Add(cooldown)
			s.PeakBelowCount[anchorName] = 0
			return &sig, nil
		}
	}
	return nil, nil
}

// evaluatePinch checks section 7c: dual-AVWAP pinch breakout entries.
func (s *AVWAPState) evaluatePinch(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	avwapValues := ec.avwapValues
	sortedAnchors := ec.sortedAnchors
	if !cfg.PinchEnabled || len(avwapValues) < 2 {
		return nil, nil
	}
	bar := ec.bar
	now := ec.now
	cooldown := ec.cooldown
	instanceID := ec.instanceID
	regimeTag := ec.regimeTag
	ctx := ec.ctx

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

	volumeOK := s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA
	// Use first anchor's AVWAP value for confluence scoring in pinch
	pinchAVWAPValue := avwapValues[sortedAnchors[0]]
	gapBPS := (maxAVWAP - minAVWAP) / minAVWAP * 10000.0
	if gapBPS >= float64(cfg.PinchMinBPS) && gapBPS <= float64(cfg.PinchMaxBPS) {
		// Long pinch breakout: price breaks above maxAVWAP.
		if bar.Close > maxAVWAP && volumeOK && !ec.lockedLong {
			if !cfg.EnforceAVWAPBias || ec.avwapBias == "" || ec.avwapBias == "LONG" {
				conf := computeConfluence(cfg, bar, pinchAVWAPValue, ec.avwapValues, s.Indicators,
					s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50)
				if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
					// skip — confluence too low
				} else {
					adjustedStrength := math.Min(1.0, 0.9+float64(conf.Score)*0.03)
					sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalEntry, start.SideBuy, adjustedStrength, map[string]string{
						"ref_price":         fmt.Sprintf("%.10f", bar.Close),
						"setup":             "avwap_pinch",
						"regime_5m":         regimeTag,
						"confluence":        fmt.Sprintf("%d", conf.Score),
						"confluence_detail": strings.Join(conf.Factors, "+"),
					})
					if err != nil {
						return nil, err
					}
					s.PendingEntry = start.SideBuy
					s.PendingEntryAt = now
					s.TradesToday++
					s.CooldownUntil = now.Add(cooldown)
					return &sig, nil
				}
			}
		}

		// Short pinch breakout: price breaks below minAVWAP.
		if bar.Close < minAVWAP && volumeOK && !ec.lockedShort && !strings.EqualFold(cfg.Direction, "LONG") {
			switch {
			case cfg.RequireCapitulationForShorts && ec.avwapBias == "LONG":
				logShortGate(ctx, ec.symbol, "require_capitulation", "pinch", "bias", ec.avwapBias)
				// Skip — above AVWAP, capitulation required for short pinch
			case !cfg.EnforceAVWAPBias || ec.avwapBias == "" || ec.avwapBias == "SHORT":
				conf := computeConfluence(cfg, bar, pinchAVWAPValue, ec.avwapValues, s.Indicators,
					s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50)
				if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
					// skip — confluence too low
				} else {
					adjustedStrength := math.Min(1.0, 0.9+float64(conf.Score)*0.03)
					sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalEntry, start.SideSell, adjustedStrength, map[string]string{
						"ref_price":         fmt.Sprintf("%.10f", bar.Close),
						"setup":             "avwap_pinch",
						"regime_5m":         regimeTag,
						"confluence":        fmt.Sprintf("%d", conf.Score),
						"confluence_detail": strings.Join(conf.Factors, "+"),
					})
					if err != nil {
						return nil, err
					}
					s.PendingEntry = start.SideSell
					s.PendingEntryAt = now
					s.TradesToday++
					s.CooldownUntil = now.Add(cooldown)
					return &sig, nil
				}
			default:
				logShortGate(ctx, ec.symbol, "enforce_avwap_bias", "pinch", "bias", ec.avwapBias)
			}
		}
	}
	return nil, nil
}

// evaluateGapReclaim checks section 7d: gap reclaim long entry.
func (s *AVWAPState) evaluateGapReclaim(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	if !cfg.GapReclaimEnabled {
		return nil, nil
	}
	if s.CrossedBelowBar == nil {
		s.CrossedBelowBar = make(map[string]int)
	}
	bar := ec.bar
	now := ec.now
	cooldown := ec.cooldown
	instanceID := ec.instanceID
	regimeTag := ec.regimeTag
	avwapValues := ec.avwapValues
	sortedAnchors := ec.sortedAnchors
	ctx := ec.ctx

	volumeOK := s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA
	for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
		prev := s.CrossedBelowBar[anchorName]
		switch {
		case bar.Close < avwapValue:
			if prev == 0 {
				s.CrossedBelowBar[anchorName] = 1 // just crossed below
			} else {
				s.CrossedBelowBar[anchorName]++
			}
		case prev > 0 && prev <= cfg.GapReclaimBars && bar.Close > avwapValue:
			// Reclaim! Price was below for 1-N bars, now closed above.
			s.CrossedBelowBar[anchorName] = 0
			if volumeOK && !ec.lockedLong {
				if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "LONG" {
					if ctx != nil && ctx.Logger() != nil {
						ctx.Logger().Info("AVWAP bias: blocking long gap reclaim", "symbol", ec.symbol, "bias", ec.avwapBias, "anchor", anchorName)
					}
					continue
				}
				conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
					s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50)
				if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
					continue
				}
				adjustedStrength := math.Min(1.0, 0.85+float64(conf.Score)*0.03)
				sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalEntry, start.SideBuy, adjustedStrength, map[string]string{
					"ref_price":         fmt.Sprintf("%.10f", bar.Close),
					"setup":             "avwap_gap_reclaim",
					"regime_5m":         regimeTag,
					"anchor":            anchorName,
					"confluence":        fmt.Sprintf("%d", conf.Score),
					"confluence_detail": strings.Join(conf.Factors, "+"),
				})
				if err != nil {
					return nil, err
				}
				s.PendingEntry = start.SideBuy
				s.PendingEntryAt = now
				s.TradesToday++
				s.CooldownUntil = now.Add(cooldown)
				return &sig, nil
			}
		default:
			s.CrossedBelowBar[anchorName] = 0
		}
	}
	return nil, nil
}

// evaluateHandoff checks section 7e: momentum handoff entries.
// Per Brian Shannon: a "handoff point" occurs when price accelerates away from
// the AVWAP after nearly touching it.
func (s *AVWAPState) evaluateHandoff(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	if !cfg.HandoffEnabled {
		return nil, nil
	}
	bar := ec.bar
	now := ec.now
	cooldown := ec.cooldown
	instanceID := ec.instanceID
	regimeTag := ec.regimeTag
	avwapValues := ec.avwapValues
	sortedAnchors := ec.sortedAnchors
	ctx := ec.ctx

	handoffBars := cfg.HandoffBars
	if handoffBars < 2 {
		handoffBars = 3
	}

	for _, anchorName := range sortedAnchors {
		hist := s.AVWAPDistHistory[anchorName]
		if len(hist) < handoffBars+1 {
			continue
		}
		recent := hist[len(hist)-handoffBars:]
		minMom := float64(cfg.HandoffMinMomBPS)
		nearAVWAP := hist[len(hist)-handoffBars-1]

		// Long handoff: consecutive bars with increasing positive distance from AVWAP.
		if !ec.lockedLong && nearAVWAP >= 0 && nearAVWAP <= 30 {
			allIncreasing := true
			for i := 1; i < len(recent); i++ {
				if recent[i] <= recent[i-1] || recent[i] <= 0 {
					allIncreasing = false
					break
				}
			}
			if allIncreasing && recent[len(recent)-1] >= minMom {
				volumeOK := s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA
				if volumeOK {
					if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "LONG" {
						continue
					}
					if cfg.RequireHigherLows && !hasHigherLows(s.RecentLows) {
						continue
					}
					if cfg.MinSlopeBPS > 0 && ec.slopeOK && ec.avwapSlope < cfg.MinSlopeBPS {
						continue
					}
					handoffAVWAP := avwapValues[anchorName]
					conf := computeConfluence(cfg, bar, handoffAVWAP, ec.avwapValues, s.Indicators,
						s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50)
					if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
						continue
					}
					adjustedStrength := math.Min(1.0, 0.85+float64(conf.Score)*0.03)
					sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalEntry, start.SideBuy, adjustedStrength, map[string]string{
						"ref_price":         fmt.Sprintf("%.10f", bar.Close),
						"setup":             "avwap_handoff",
						"anchor":            anchorName,
						"avwap":             fmt.Sprintf("%.4f", handoffAVWAP),
						"momentum_bps":      fmt.Sprintf("%.1f", recent[len(recent)-1]),
						"regime_5m":         regimeTag,
						"mode":              "handoff",
						"confluence":        fmt.Sprintf("%d", conf.Score),
						"confluence_detail": strings.Join(conf.Factors, "+"),
					})
					if err != nil {
						return nil, err
					}
					s.PendingEntry = start.SideBuy
					s.PendingEntryAt = now
					s.TradesToday++
					s.CooldownUntil = now.Add(cooldown)
					return &sig, nil
				}
			}
		}

		// Short handoff: consecutive bars with increasing negative distance from AVWAP.
		if !ec.lockedShort && !strings.EqualFold(cfg.Direction, "LONG") && nearAVWAP <= 0 && nearAVWAP >= -30 {
			allDecreasing := true
			for i := 1; i < len(recent); i++ {
				if recent[i] >= recent[i-1] || recent[i] >= 0 {
					allDecreasing = false
					break
				}
			}
			if allDecreasing && recent[len(recent)-1] <= -minMom {
				volumeOK := s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA
				if volumeOK {
					if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "SHORT" {
						logShortGate(ctx, ec.symbol, "enforce_avwap_bias", anchorName, "bias", ec.avwapBias)
						continue
					}
					if cfg.RequireCapitulationForShorts && ec.avwapBias == "LONG" {
						logShortGate(ctx, ec.symbol, "require_capitulation", anchorName, "bias", ec.avwapBias)
						continue
					}
					// lower-highs gate removed for shorts — bias + slope are sufficient
					if cfg.MinSlopeBPS > 0 && ec.slopeOK && ec.avwapSlope > -cfg.MinSlopeBPS {
						logShortGate(ctx, ec.symbol, "min_slope_bps", anchorName, "slope_bps", fmt.Sprintf("%.2f", ec.avwapSlope))
						continue
					}
					shortHandoffAVWAP := avwapValues[anchorName]
					conf := computeConfluence(cfg, bar, shortHandoffAVWAP, ec.avwapValues, s.Indicators,
						s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50)
					if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
						continue
					}
					adjustedStrength := math.Min(1.0, 0.85+float64(conf.Score)*0.03)
					sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalEntry, start.SideSell, adjustedStrength, map[string]string{
						"ref_price":         fmt.Sprintf("%.10f", bar.Close),
						"setup":             "avwap_handoff",
						"anchor":            anchorName,
						"avwap":             fmt.Sprintf("%.4f", shortHandoffAVWAP),
						"momentum_bps":      fmt.Sprintf("%.1f", recent[len(recent)-1]),
						"regime_5m":         regimeTag,
						"mode":              "handoff",
						"confluence":        fmt.Sprintf("%d", conf.Score),
						"confluence_detail": strings.Join(conf.Factors, "+"),
					})
					if err != nil {
						return nil, err
					}
					s.PendingEntry = start.SideSell
					s.PendingEntryAt = now
					s.TradesToday++
					s.CooldownUntil = now.Add(cooldown)
					return &sig, nil
				}
			}
		}
	}
	return nil, nil
}

// evaluateBounce checks section 8: AVWAP bounce entries (long and short).
func (s *AVWAPState) evaluateBounce(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	if !cfg.BounceEnabled {
		return nil, nil
	}
	bar := ec.bar
	now := ec.now
	cooldown := ec.cooldown
	instanceID := ec.instanceID
	regimeTag := ec.regimeTag
	avwapValues := ec.avwapValues
	sortedAnchors := ec.sortedAnchors
	ctx := ec.ctx

	for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
		touchesAVWAP := bar.Low <= avwapValue && avwapValue <= bar.High

		// Long bounce: touches AVWAP + RSI < max + bullish candle.
		if !ec.lockedLong && touchesAVWAP && s.Indicators.RSI > 0 && s.Indicators.RSI < cfg.RSIBounceMax {
			if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "LONG" {
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP bias: blocking long bounce", "symbol", ec.symbol, "bias", ec.avwapBias, "anchor", anchorName)
				}
				continue
			}
			// Regime gating is handled by evaluateEntries (bounce only called in TREND)
			if bar.Close <= bar.Open {
				continue
			}
			conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
				s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50)
			if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
				continue
			}
			adjustedStrength := math.Min(1.0, 0.6+float64(conf.Score)*0.03)
			sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalEntry, start.SideBuy, adjustedStrength, map[string]string{
				"ref_price":         fmt.Sprintf("%.10f", bar.Close),
				"setup":             "avwap_bounce",
				"anchor":            anchorName,
				"avwap":             fmt.Sprintf("%.4f", avwapValue),
				"rsi":               fmt.Sprintf("%.2f", s.Indicators.RSI),
				"mode":              "bounce",
				"regime_5m":         regimeTag,
				"confluence":        fmt.Sprintf("%d", conf.Score),
				"confluence_detail": strings.Join(conf.Factors, "+"),
			})
			if err != nil {
				return nil, err
			}
			s.PendingEntry = start.SideBuy
			s.PendingEntryAt = now
			s.TradesToday++
			s.CooldownUntil = now.Add(cooldown)
			return &sig, nil
		}

		// Direction guard: skip short entries in long-only mode (e.g. crypto).
		if strings.EqualFold(cfg.Direction, "LONG") {
			continue
		}

		// Short bounce: touches AVWAP + RSI > min + bearish candle.
		if !ec.lockedShort && touchesAVWAP && s.Indicators.RSI > cfg.RSIBounceMin {
			if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "SHORT" {
				logShortGate(ctx, ec.symbol, "enforce_avwap_bias", anchorName, "bias", ec.avwapBias)
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP bias: blocking short bounce", "symbol", ec.symbol, "bias", ec.avwapBias, "anchor", anchorName)
				}
				continue
			}
			// Regime gating is handled by evaluateEntries (bounce only called in TREND)
			if bar.Close >= bar.Open {
				logShortGate(ctx, ec.symbol, "bearish_candle", anchorName)
				continue
			}
			if cfg.RequireCapitulationForShorts && ec.avwapBias == "LONG" {
				logShortGate(ctx, ec.symbol, "require_capitulation", anchorName, "bias", ec.avwapBias)
				continue // block short bounces above AVWAP without capitulation
			}
			conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
				s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50)
			if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
				continue
			}
			adjustedStrength := math.Min(1.0, 0.6+float64(conf.Score)*0.03)
			sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalEntry, start.SideSell, adjustedStrength, map[string]string{
				"ref_price":         fmt.Sprintf("%.10f", bar.Close),
				"setup":             "avwap_bounce",
				"anchor":            anchorName,
				"avwap":             fmt.Sprintf("%.4f", avwapValue),
				"rsi":               fmt.Sprintf("%.2f", s.Indicators.RSI),
				"mode":              "bounce",
				"regime_5m":         regimeTag,
				"confluence":        fmt.Sprintf("%d", conf.Score),
				"confluence_detail": strings.Join(conf.Factors, "+"),
			})
			if err != nil {
				return nil, err
			}
			s.PendingEntry = start.SideSell
			s.PendingEntryAt = now
			s.TradesToday++
			s.CooldownUntil = now.Add(cooldown)
			return &sig, nil
		}
	}
	return nil, nil
}

// evaluateEntries runs all entry checks in conviction-priority order.
// Confluence scoring handles quality filtering — no regime gating needed.
// Priority (per Brian Shannon research):
//   1. Pinch — massive pent-up energy from AVWAP convergence
//   2. Capitulation Reclaim — institutional turnover / exhaustion reversal
//   3. Gap Reclaim — immediate response to a fresh catalyst
//   4. Pullback — confluence entry (AVWAP + trend structure)
//   5. Handoff — riding an accelerating "fast trend"
//   6. Breakout — sustained above/below AVWAP with volume
//   7. Bounce — lowest conviction, mean-reversion at AVWAP
func (s *AVWAPState) evaluateEntries(ec entryContext) (*start.Signal, error) {
	if sig, err := s.evaluatePinch(ec); err != nil || sig != nil {
		return sig, err
	}
	if sig, err := s.evaluateCapitulationReclaim(ec); err != nil || sig != nil {
		return sig, err
	}
	if sig, err := s.evaluateGapReclaim(ec); err != nil || sig != nil {
		return sig, err
	}
	if sig, err := s.evaluatePullback(ec); err != nil || sig != nil {
		return sig, err
	}
	if sig, err := s.evaluateHandoff(ec); err != nil || sig != nil {
		return sig, err
	}
	if sig, err := s.evaluateBreakout(ec); err != nil || sig != nil {
		return sig, err
	}
	if sig, err := s.evaluateBounce(ec); err != nil || sig != nil {
		return sig, err
	}
	return nil, nil
}

// --- Confluence scoring ---

type confluenceResult struct {
	Score   int
	Factors []string
}

func computeConfluence(
	cfg AVWAPConfig,
	bar start.Bar,
	avwapValue float64,
	avwapValues map[string]float64,
	indicators start.IndicatorData,
	prevBars [2]start.Bar,
	prevBarCount int,
	keyLevels map[string]float64,
	barHighs50, barLows50 []float64,
) confluenceResult {
	var res confluenceResult

	// Tolerance: ATR/2, fallback to avwapValue*0.002
	tolerance := avwapValue * 0.002
	if indicators.ATR > 0 {
		tolerance = indicators.ATR / 2
	}

	// Factor 1: Fibonacci (+3)
	if cfg.FibConfluenceEnabled && len(barHighs50) >= 20 && len(barLows50) >= 20 {
		maxH := barHighs50[0]
		for _, h := range barHighs50[1:] {
			if h > maxH {
				maxH = h
			}
		}
		minL := barLows50[0]
		for _, l := range barLows50[1:] {
			if l < minL {
				minL = l
			}
		}
		rng := maxH - minL
		if rng > 0 {
			fib382 := maxH - rng*0.382
			fib500 := maxH - rng*0.500
			fib618 := maxH - rng*0.618
			for _, fib := range []struct {
				level float64
				name  string
			}{
				{fib382, "fib_38.2"},
				{fib500, "fib_50"},
				{fib618, "fib_61.8"},
			} {
				if math.Abs(avwapValue-fib.level) <= tolerance {
					res.Score += 3
					res.Factors = append(res.Factors, fib.name)
					break // only count once
				}
			}
		}
	}

	// Factor 2: Key Level (+3)
	if cfg.KeyLevelConfluenceEnabled && len(keyLevels) > 0 {
		for name, level := range keyLevels {
			if math.Abs(avwapValue-level) <= tolerance {
				res.Score += 3
				res.Factors = append(res.Factors, "key_"+name)
				break // only count once
			}
		}
	}

	// Factor 3: Candlestick (+2)
	if cfg.CandleConfluenceEnabled && prevBarCount >= 1 {
		matched := false

		// Inside Bar: current bar range significantly smaller than previous bar
		// (must be contained AND range < 70% of previous range — filters noise)
		prevRange := prevBars[0].High - prevBars[0].Low
		curRange := bar.High - bar.Low
		if bar.High <= prevBars[0].High && bar.Low >= prevBars[0].Low &&
			prevRange > 0 && curRange < 0.7*prevRange {
			res.Score += 2
			res.Factors = append(res.Factors, "inside_bar")
			matched = true
		}

		// Strength candle: body > 60% of range
		if !matched && bar.High != bar.Low {
			body := bar.Close - bar.Open
			if body < 0 {
				body = -body
			}
			rng := bar.High - bar.Low
			if body > 0.6*rng {
				res.Score += 2
				res.Factors = append(res.Factors, "strength_candle")
				matched = true
			}
		}

		// Morning Star: 3-bar pattern (prevBars[1] bearish, prevBars[0] small body, bar bullish)
		if !matched && prevBarCount >= 2 {
			prev2 := prevBars[1]
			prev1 := prevBars[0]
			prev2Bearish := prev2.Close < prev2.Open
			prev1Range := prev1.High - prev1.Low
			prev1Body := prev1.Close - prev1.Open
			if prev1Body < 0 {
				prev1Body = -prev1Body
			}
			prev1Small := prev1Range > 0 && prev1Body < 0.3*prev1Range
			curBullish := bar.Close > bar.Open
			if prev2Bearish && prev1Small && curBullish {
				res.Score += 2
				res.Factors = append(res.Factors, "morning_star")
			}
		}
	}

	// Factor 4: Band Zone (+2)
	if cfg.BandConfluenceEnabled && len(avwapValues) >= 2 {
		var minAVWAP, maxAVWAP float64
		first := true
		for _, v := range avwapValues {
			if first || v < minAVWAP {
				minAVWAP = v
			}
			if first || v > maxAVWAP {
				maxAVWAP = v
			}
			first = false
		}
		if bar.Close >= minAVWAP && bar.Close <= maxAVWAP {
			res.Score += 2
			res.Factors = append(res.Factors, "band_zone")
		}
	}

	return res
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

	// 2c. Shift prev bars ring buffer for candlestick patterns.
	avwapSt.PrevBars[1] = avwapSt.PrevBars[0]
	avwapSt.PrevBars[0] = bar
	if avwapSt.PrevBarCount < 2 {
		avwapSt.PrevBarCount++
	}

	// 2d. Rolling 50-bar high/low window for Fibonacci.
	avwapSt.BarHighs50 = append(avwapSt.BarHighs50, bar.High)
	if len(avwapSt.BarHighs50) > 50 {
		avwapSt.BarHighs50 = avwapSt.BarHighs50[1:]
	}
	avwapSt.BarLows50 = append(avwapSt.BarLows50, bar.Low)
	if len(avwapSt.BarLows50) > 50 {
		avwapSt.BarLows50 = avwapSt.BarLows50[1:]
	}

	// 3. Regime gating.
	regimeAllowed := false
	regimeTag := "none"
	if ar, ok2 := avwapSt.Indicators.AnchorRegimes["5m"]; ok2 {
		regimeTag = ar.Type
		for _, allowed := range cfg.AllowRegimes {
			if ar.Type == allowed || (allowed == "TREND" && (ar.Type == "TREND_UP" || ar.Type == "TREND_DOWN")) {
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

	ec := entryContext{
		ctx:           ctx,
		cfg:           cfg,
		bar:           bar,
		symbol:        symbol,
		instanceID:    instanceID,
		now:           now,
		cooldown:      cooldown,
		avwapValues:   avwapValues,
		sortedAnchors: sortedAnchors,
		regimeTag:     regimeTag,
		etLocation:    etLocation,
		keyLevels:     avwapSt.KeyLevels,
	}

	// 5. Exit signals (check even if cooldown would block new entries).
	if sig, err := avwapSt.evaluateExits(ec); err != nil {
		return avwapSt, nil, err
	} else if sig != nil {
		return avwapSt, []start.Signal{*sig}, nil
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

	// 6a2. Session lockout — once stopped out on a side, no re-entry in the
	// same direction until next session. A long stop-out doesn't block shorts.
	ec.lockedLong = avwapSt.LockedOutSide == start.SideBuy
	ec.lockedShort = avwapSt.LockedOutSide == start.SideSell

	// 6b. Directional bias gate — determine bias from first anchor's AVWAP value.
	// Require at least minBarsForBias bars before trusting the AVWAP for directional
	// decisions. At session open, the AVWAP has too few data points to be reliable
	// (e.g. 09:44 = only ~9 one-minute bars). This matches the chart stabilization
	// gate (CalcBarCount >= 10) so bias and chart stay aligned.
	const minBarsForBias = 10
	if cfg.EnforceAVWAPBias && len(cfg.Anchors) > 0 && avwapSt.CalcBarCount >= minBarsForBias {
		firstAnchor := cfg.Anchors[0]
		if firstAVWAP, ok2 := avwapValues[firstAnchor]; ok2 {
			if bar.Close > firstAVWAP {
				ec.avwapBias = "LONG"
			} else if bar.Close < firstAVWAP {
				ec.avwapBias = "SHORT"
			}
		}
	}

	// 6b2. AVWAP slope gate — check slope of first anchor for trend confirmation.
	if cfg.MinSlopeBPS > 0 && len(cfg.Anchors) > 0 {
		ec.avwapSlope, ec.slopeOK = avwapSt.Calc.Slope(cfg.Anchors[0], cfg.SlopeLookback)
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
		// NOTE: when AboveCount==0 && PeakAboveCount>0, peak is frozen for pullback check (no-op).
		if avwapSt.BelowCount[anchorName] > avwapSt.PeakBelowCount[anchorName] {
			avwapSt.PeakBelowCount[anchorName] = avwapSt.BelowCount[anchorName]
		}
		// NOTE: when BelowCount==0 && PeakBelowCount>0, peak is frozen for pullback check (no-op).
		// Reset peaks when price re-establishes a trend (AboveCount or BelowCount exceeds prior peak)
		if avwapSt.AboveCount[anchorName] >= cfg.PullbackTrendBars {
			avwapSt.PeakAboveCount[anchorName] = avwapSt.AboveCount[anchorName]
		}
		if avwapSt.BelowCount[anchorName] >= cfg.PullbackTrendBars {
			avwapSt.PeakBelowCount[anchorName] = avwapSt.BelowCount[anchorName]
		}
	}

	// 6e–8. Entry evaluation.
	if sig, err := avwapSt.evaluateEntries(ec); err != nil {
		return avwapSt, nil, err
	} else if sig != nil {
		return avwapSt, []start.Signal{*sig}, nil
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
	PrevBarCount   int                                `json:"prev_bar_count,omitempty"`
	BarHighs50     []float64                          `json:"bar_highs_50,omitempty"`
	BarLows50      []float64                          `json:"bar_lows_50,omitempty"`
	KeyLevels      map[string]float64                 `json:"key_levels,omitempty"`
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
		PrevBarCount:   s.PrevBarCount,
		BarHighs50:     s.BarHighs50,
		BarLows50:      s.BarLows50,
		KeyLevels:      s.KeyLevels,
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
	s.PrevBarCount = j.PrevBarCount
	s.BarHighs50 = j.BarHighs50
	s.BarLows50 = j.BarLows50
	s.KeyLevels = j.KeyLevels

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
