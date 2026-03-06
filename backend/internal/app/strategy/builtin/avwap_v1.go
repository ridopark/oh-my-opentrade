package builtin

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// AVWAPStrategy implements breakout and bounce entries anchored to VWAP levels.
type AVWAPStrategy struct {
	meta strat.Meta
}

// NewAVWAPStrategy creates a new AVWAP Breakout/Bounce strategy.
func NewAVWAPStrategy() *AVWAPStrategy {
	id, _ := strat.NewStrategyID("avwap_v1")
	ver, _ := strat.NewVersion("1.0.0")
	return &AVWAPStrategy{
		meta: strat.Meta{
			ID:          id,
			Version:     ver,
			Name:        "AVWAP Breakout/Bounce",
			Description: "Anchored VWAP breakout and bounce strategy with regime gating",
			Author:      "system",
		},
	}
}

func (s *AVWAPStrategy) Meta() strat.Meta { return s.meta }
func (s *AVWAPStrategy) WarmupBars() int  { return 30 }
func (s *AVWAPStrategy) ReplayOnBar(_ strat.Context, _ string, bar strat.Bar, st strat.State, indicators strat.IndicatorData) (strat.State, error) {
	avwapSt, ok := st.(*AVWAPState)
	if !ok {
		return st, fmt.Errorf("AVWAPStrategy.ReplayOnBar: expected *AVWAPState, got %T", st)
	}
	avwapSt.Indicators = indicators
	avwapSt.Calc.Update(bar.Time, bar.High, bar.Low, bar.Close, bar.Volume)
	return avwapSt, nil
}

// AVWAPConfig holds strategy parameters parsed from DNA.
type AVWAPConfig struct {
	BreakoutEnabled bool
	HoldBars        int
	VolumeMult      float64
	BounceEnabled   bool
	RSIBounceMax    float64
	RSIBounceMin    float64
	ExitHoldBars    int
	CooldownSeconds int
	MaxTradesPerDay int
	AllowRegimes    []string
	Direction       string
}

// AVWAPState is the per-symbol state for the AVWAP strategy.
type AVWAPState struct {
	Symbol         string
	Calc           *strat.AnchoredVWAPCalc
	Indicators     strat.IndicatorData
	AboveCount     map[string]int
	BelowCount     map[string]int
	TradesToday    int
	CooldownUntil  time.Time
	PositionSide   strat.Side
	PendingEntry   strat.Side // set on signal emission, cleared on fill/rejection
	PendingEntryAt time.Time  // when PendingEntry was set (for timeout recovery)
	Config         AVWAPConfig
}

// SetIndicators implements the indicatorSetter interface.
func (s *AVWAPState) SetIndicators(ind strat.IndicatorData) {
	s.Indicators = ind
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

func parseAVWAPConfig(params map[string]any) AVWAPConfig {
	cfg := AVWAPConfig{
		BreakoutEnabled: getBool(params, "breakout_enabled", true),
		HoldBars:        getInt(params, "hold_bars", 2),
		VolumeMult:      getFloat64(params, "volume_mult", 1.5),
		BounceEnabled:   getBool(params, "bounce_enabled", true),
		RSIBounceMax:    getFloat64(params, "rsi_bounce_max", 30),
		ExitHoldBars:    getInt(params, "exit_hold_bars", 2),
		CooldownSeconds: getInt(params, "cooldown_seconds", 120),
		MaxTradesPerDay: getInt(params, "max_trades_per_day", 3),
		AllowRegimes:    getStringSlice(params, "allow_regimes", []string{"BALANCE", "REVERSAL"}),
		Direction:       getString(params, "direction", ""),
	}
	cfg.RSIBounceMin = 100 - cfg.RSIBounceMax
	return cfg
}

// Init creates initial state for a symbol.
func (s *AVWAPStrategy) Init(ctx strat.Context, symbol string, params map[string]any, prior strat.State) (strat.State, error) {
	cfg := parseAVWAPConfig(params)
	calc := strat.NewAnchoredVWAPCalc()

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
		calc.AddAnchor(strat.AnchorPoint{Name: name, AnchorTime: anchorTime})
		added++
	}
	if added == 0 {
		var anchorTime time.Time
		if ctx != nil {
			anchorTime = ctx.Now()
		}
		calc.AddAnchor(strat.AnchorPoint{Name: "session_open", AnchorTime: anchorTime})
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
func (s *AVWAPStrategy) OnBar(ctx strat.Context, symbol string, bar strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
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
		return avwapSt, nil, nil
	}
	if avwapSt.TradesToday >= cfg.MaxTradesPerDay {
		return avwapSt, nil, nil
	}

	// 2. Update AVWAP calculator.
	avwapSt.Calc.Update(bar.Time, bar.High, bar.Low, bar.Close, bar.Volume)
	avwapValues := avwapSt.Calc.Values()

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
		if bar.Close > avwapValue {
			avwapSt.AboveCount[anchorName]++
			avwapSt.BelowCount[anchorName] = 0
		} else if bar.Close < avwapValue {
			avwapSt.BelowCount[anchorName]++
			avwapSt.AboveCount[anchorName] = 0
		} else {
			avwapSt.AboveCount[anchorName] = 0
			avwapSt.BelowCount[anchorName] = 0
		}
	}

	instanceID, _ := strat.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
	cooldown := time.Duration(cfg.CooldownSeconds) * time.Second

	// 5. Exit signals (check even if cooldown would block new entries).
	if avwapSt.PositionSide == strat.SideBuy && avwapSt.PendingEntry == "" {
		for _, belowCnt := range avwapSt.BelowCount {
			if belowCnt >= cfg.ExitHoldBars {
				sig, err := strat.NewSignal(instanceID, symbol, strat.SignalExit, strat.SideSell, 0.8, map[string]string{
					"ref_price": fmt.Sprintf("%.4f", bar.Close),
					"setup":     "avwap_exit",
					"regime_5m": regimeTag,
				})
				if err != nil {
					return avwapSt, nil, err
				}
				avwapSt.PositionSide = ""
				avwapSt.CooldownUntil = now.Add(cooldown)
				return avwapSt, []strat.Signal{sig}, nil
			}
		}
	}
	if avwapSt.PositionSide == strat.SideSell && avwapSt.PendingEntry == "" {
		for _, aboveCnt := range avwapSt.AboveCount {
			if aboveCnt >= cfg.ExitHoldBars {
				sig, err := strat.NewSignal(instanceID, symbol, strat.SignalExit, strat.SideBuy, 0.8, map[string]string{
					"ref_price": fmt.Sprintf("%.4f", bar.Close),
					"setup":     "avwap_exit",
					"regime_5m": regimeTag,
				})
				if err != nil {
					return avwapSt, nil, err
				}
				avwapSt.PositionSide = ""
				avwapSt.CooldownUntil = now.Add(cooldown)
				return avwapSt, []strat.Signal{sig}, nil
			}
		}
	}

	// 6. Only entries if flat and regime allowed.
	if avwapSt.PositionSide != "" || avwapSt.PendingEntry != "" {
		return avwapSt, nil, nil
	}
	if !regimeAllowed {
		return avwapSt, nil, nil
	}

	// 7. Breakout detection.
	if cfg.BreakoutEnabled {
		for anchorName, avwapValue := range avwapValues {
			volRatio := 0.0
			if avwapSt.Indicators.VolumeSMA > 0 {
				volRatio = bar.Volume / avwapSt.Indicators.VolumeSMA
			}
			volumeOK := avwapSt.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*avwapSt.Indicators.VolumeSMA

			// Long breakout.
			if avwapSt.AboveCount[anchorName] >= cfg.HoldBars && volumeOK {
				if regimeTag == "REVERSAL" {
					continue
				}
				sig, err := strat.NewSignal(instanceID, symbol, strat.SignalEntry, strat.SideBuy, 0.7, map[string]string{
					"ref_price": fmt.Sprintf("%.4f", bar.Close),
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
				avwapSt.PendingEntry = strat.SideBuy
				avwapSt.PendingEntryAt = now
				avwapSt.TradesToday++
				avwapSt.CooldownUntil = now.Add(cooldown)
				return avwapSt, []strat.Signal{sig}, nil
			}

			// Direction guard: skip short entries in long-only mode (e.g. crypto).
			if strings.EqualFold(cfg.Direction, "LONG") {
				continue
			}

			// Short breakout.
			if avwapSt.BelowCount[anchorName] >= cfg.HoldBars && volumeOK {
				if regimeTag == "REVERSAL" {
					continue
				}
				sig, err := strat.NewSignal(instanceID, symbol, strat.SignalEntry, strat.SideSell, 0.7, map[string]string{
					"ref_price": fmt.Sprintf("%.4f", bar.Close),
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
				avwapSt.PendingEntry = strat.SideSell
				avwapSt.PendingEntryAt = now
				avwapSt.TradesToday++
				avwapSt.CooldownUntil = now.Add(cooldown)
				return avwapSt, []strat.Signal{sig}, nil
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
				sig, err := strat.NewSignal(instanceID, symbol, strat.SignalEntry, strat.SideBuy, 0.6, map[string]string{
					"ref_price": fmt.Sprintf("%.4f", bar.Close),
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
				avwapSt.PendingEntry = strat.SideBuy
				avwapSt.PendingEntryAt = now
				avwapSt.TradesToday++
				avwapSt.CooldownUntil = now.Add(cooldown)
				return avwapSt, []strat.Signal{sig}, nil
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
				sig, err := strat.NewSignal(instanceID, symbol, strat.SignalEntry, strat.SideSell, 0.6, map[string]string{
					"ref_price": fmt.Sprintf("%.4f", bar.Close),
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
				avwapSt.PendingEntry = strat.SideSell
				avwapSt.PendingEntryAt = now
				avwapSt.TradesToday++
				avwapSt.CooldownUntil = now.Add(cooldown)
				return avwapSt, []strat.Signal{sig}, nil
			}
		}
	}

	return avwapSt, nil, nil
}

// OnEvent handles fill confirmations and entry rejections for AVWAP strategy.
func (s *AVWAPStrategy) OnEvent(ctx strat.Context, symbol string, evt any, st strat.State) (strat.State, []strat.Signal, error) {
	avwapSt, ok := st.(*AVWAPState)
	if !ok {
		return st, nil, fmt.Errorf("AVWAPStrategy.OnEvent: expected *AVWAPState, got %T", st)
	}

	switch e := evt.(type) {
	case strat.FillConfirmation:
		if avwapSt.PendingEntry != "" {
			avwapSt.PositionSide = avwapSt.PendingEntry
			avwapSt.PendingEntry = ""
			avwapSt.PendingEntryAt = time.Time{}
			if ctx != nil && ctx.Logger() != nil {
				ctx.Logger().Info("AVWAPStrategy: fill confirmed, position active", "symbol", symbol, "side", avwapSt.PositionSide, "price", e.Price)
			}
		}
		return avwapSt, nil, nil

	case strat.EntryRejection:
		if avwapSt.PendingEntry != "" {
			if ctx != nil && ctx.Logger() != nil {
				ctx.Logger().Warn("AVWAPStrategy: entry rejected, clearing pending", "symbol", symbol, "side", avwapSt.PendingEntry, "reason", e.Reason)
			}
			avwapSt.PendingEntry = ""
			avwapSt.PendingEntryAt = time.Time{}
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
	CalcStates     map[string]strat.AnchoredVWAPState `json:"calc_states"`
	Anchors        []strat.AnchorPoint                `json:"anchors"`
	AboveCount     map[string]int                     `json:"above_count"`
	BelowCount     map[string]int                     `json:"below_count"`
	TradesToday    int                                `json:"trades_today"`
	CooldownUntil  time.Time                          `json:"cooldown_until"`
	PositionSide   strat.Side                         `json:"position_side"`
	PendingEntry   strat.Side                         `json:"pending_entry"`
	PendingEntryAt time.Time                          `json:"pending_entry_at"`
	Indicators     strat.IndicatorData                `json:"indicators"`
}

func (s *AVWAPState) Marshal() ([]byte, error) {
	// Extract anchor points for serialization.
	anchors := make([]strat.AnchorPoint, 0)
	avwapValues := s.Calc.Values()
	for name := range avwapValues {
		anchors = append(anchors, strat.AnchorPoint{Name: name})
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

	s.Calc = strat.NewAnchoredVWAPCalc()
	s.Calc.Restore(j.Anchors, j.CalcStates)

	if s.AboveCount == nil {
		s.AboveCount = make(map[string]int)
	}
	if s.BelowCount == nil {
		s.BelowCount = make(map[string]int)
	}
	return nil
}
