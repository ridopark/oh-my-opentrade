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
	id, _ := start.NewStrategyID("avwap_v1")
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
		s.TradesToday = 0
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
	s.TradesToday = 0
}

func (s *AVWAPState) ClearPendingEntry() {
	s.PendingEntry = ""
	s.PendingEntryAt = time.Time{}
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
		Symbol:     symbol,
		Calc:       calc,
		AboveCount: make(map[string]int),
		BelowCount: make(map[string]int),
		Config:     cfg,
	}

	if prior != nil {
		if avwapPrior, ok := prior.(*AVWAPState); ok {
			st.Calc = avwapPrior.Calc
			st.AboveCount = avwapPrior.AboveCount
			st.BelowCount = avwapPrior.BelowCount
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

	// 2. Update AVWAP calculator.
	avwapSt.Calc.Update(bar.Time, bar.High, bar.Low, bar.Close, bar.Volume)
	avwapValues := avwapSt.Calc.Values()

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

	// 4. Update AboveCount/BelowCount for each active anchor.
	for anchorName, avwapValue := range avwapValues {
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

	// 7. Breakout detection — scan ALL anchors for LONG first, then SHORT.
	if cfg.BreakoutEnabled {
		for anchorName, avwapValue := range avwapValues {
			volRatio := 0.0
			if avwapSt.Indicators.VolumeSMA > 0 {
				volRatio = bar.Volume / avwapSt.Indicators.VolumeSMA
			}
			volumeOK := avwapSt.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*avwapSt.Indicators.VolumeSMA

			if avwapSt.AboveCount[anchorName] >= cfg.HoldBars && volumeOK {
				if regimeTag == "REVERSAL" {
					continue
				}
				if cfg.RequireHigherLows && !hasHigherLows(avwapSt.RecentLows) {
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
			for anchorName, avwapValue := range avwapValues {
				volRatio := 0.0
				if avwapSt.Indicators.VolumeSMA > 0 {
					volRatio = bar.Volume / avwapSt.Indicators.VolumeSMA
				}
				volumeOK := avwapSt.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*avwapSt.Indicators.VolumeSMA

				if avwapSt.BelowCount[anchorName] >= cfg.HoldBars && volumeOK {
					if regimeTag == "REVERSAL" {
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

	// 8. Bounce detection.
	if cfg.BounceEnabled {
		for anchorName, avwapValue := range avwapValues {
			touchesAVWAP := bar.Low <= avwapValue && avwapValue <= bar.High

			// Long bounce: touches AVWAP + RSI < max + bullish candle.
			if touchesAVWAP && avwapSt.Indicators.RSI > 0 && avwapSt.Indicators.RSI < cfg.RSIBounceMax {
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
				if regimeTag == "TREND" {
					continue
				}
				if bar.Close >= bar.Open {
					continue
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
	TradesToday    int                                `json:"trades_today"`
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
	anchors := make([]start.AnchorPoint, 0, len(avwapValues))
	for name := range avwapValues {
		anchors = append(anchors, start.AnchorPoint{Name: name})
	}

	j := avwapStateJSON{
		Symbol:         s.Symbol,
		Config:         s.Config,
		CalcStates:     s.Calc.States(),
		Anchors:        anchors,
		AboveCount:     s.AboveCount,
		BelowCount:     s.BelowCount,
		TradesToday:    s.TradesToday,
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
	return nil
}
