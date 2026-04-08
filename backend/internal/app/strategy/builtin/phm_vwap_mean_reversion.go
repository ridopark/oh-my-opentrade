// Package builtin — Power Hour Mean Reversion (PHM) strategy.
// VWAP snap-back: during Power Hour (15:00-15:45 ET) when price overextends
// beyond +/-2sigma of the daily VWAP, institutional "close-at-VWAP" algorithms
// pull price back to the mean. This is a statistical mean reversion play.
package builtin

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// PHMConfig holds tunable parameters for the VWAP Mean Reversion strategy.
type PHMConfig struct {
	// Time window
	AllowedHoursStart string  // "15:00" — entry window start (ET)
	AllowedHoursEnd   string  // "15:45" — entry window end
	AllowedHoursTZ    string  // "America/New_York"
	EODFlattenTime    string  // "15:50" — flatten before MOC chaos

	// VWAP deviation entry
	SDEntryThreshold float64 // 2.0 — enter when price beyond +/-2.0sigma from VWAP
	SDStopThreshold  float64 // 3.0 — stop if price closes beyond +/-3.0sigma

	// Confluence filters
	MaxADX           float64 // 25.0 — mean reversion works on range days (ADX < 25)
	RSIOverbought    float64 // 70.0 — RSI threshold for short entry
	RSIOversold      float64 // 30.0 — RSI threshold for long entry (named RsiOversold in spec)
	VolumeSpikeMult  float64 // 3.0 — volume spike = 3x avg (exhaustion candle)
	RequireReversal  bool    // true — require reversal candle (close back inside 2sigma band)

	// Day typing — trend day filter
	DayTypeTrendPct float64 // 3.0 — if price up >3% by 14:00, disable mean reversion (trend day)

	// Risk
	StopBPS           int // 50 — stop loss in basis points (tight for mean reversion)
	MaxSignalsPerSess int // 1 — one entry per session
	LimitOffsetBPS    int // 5
	RiskPerTradeBPS   int // 500
	MaxPositionBPS    int // 2000

	// Exit
	TargetVWAP       bool    // true — primary target is VWAP line (the Mean)
	TargetOppositeSD float64 // 1.0 — secondary target: opposite +/-1sigma band (0 = disabled)

	// Filters
	BlockedDaysOfWeek  []string // ["Monday", "Friday"]
	MinConfluenceScore int      // 0 = disabled

	// Anti-pattern guards
	MinSessionVolume int // 500000 — skip low-liquidity symbols (0 = disabled)
	VolAdjustBands   bool // false — scale SD bands by ATR on high-vol days
	MinWindowBars    int // 3 — observe N bars after window opens before entering (0 = disabled)
}

// NewPHMConfigFromDNA reads PHMConfig from DNA params with sensible defaults.
func NewPHMConfigFromDNA(params map[string]any) PHMConfig {
	return PHMConfig{
		AllowedHoursStart:    phmStringParam(params, "allowed_hours_start", "15:00"),
		AllowedHoursEnd:      phmStringParam(params, "allowed_hours_end", "15:45"),
		AllowedHoursTZ:       phmStringParam(params, "allowed_hours_tz", "America/New_York"),
		EODFlattenTime:       phmStringParam(params, "eod_flatten_time", "15:50"),
		SDEntryThreshold:     phmFloatParam(params, "sd_entry_threshold", 2.0),
		SDStopThreshold:      phmFloatParam(params, "sd_stop_threshold", 3.0),
		MaxADX:               phmFloatParam(params, "max_adx", 25.0),
		RSIOverbought:        phmFloatParam(params, "rsi_overbought", 70.0),
		RSIOversold:          phmFloatParam(params, "rsi_oversold", 30.0),
		VolumeSpikeMult:      phmFloatParam(params, "volume_spike_mult", 3.0),
		RequireReversal:      phmBoolParam(params, "require_reversal", true),
		DayTypeTrendPct:      phmFloatParam(params, "day_type_trend_pct", 3.0),
		StopBPS:              phmIntParam(params, "stop_bps", 50),
		MaxSignalsPerSess:    phmIntParam(params, "max_signals_per_session", 1),
		LimitOffsetBPS:       phmIntParam(params, "limit_offset_bps", 5),
		RiskPerTradeBPS:      phmIntParam(params, "risk_per_trade_bps", 500),
		MaxPositionBPS:       phmIntParam(params, "max_position_bps", 2000),
		TargetVWAP:           phmBoolParam(params, "target_vwap", true),
		TargetOppositeSD:     phmFloatParam(params, "target_opposite_sd", 1.0),
		BlockedDaysOfWeek:    phmStringSliceParam(params, "blocked_days_of_week", nil),
		MinConfluenceScore:   phmIntParam(params, "min_confluence_score", 0),
		MinSessionVolume:     phmIntParam(params, "min_session_volume", 0),
		VolAdjustBands:       phmBoolParam(params, "vol_adjust_bands", false),
		MinWindowBars:        phmIntParam(params, "min_window_bars", 0),
	}
}

// ---------------------------------------------------------------------------
// Param helpers (prefixed to avoid collision with other files in package)
// ---------------------------------------------------------------------------

func phmFloatParam(params map[string]any, key string, def float64) float64 {
	if params == nil {
		return def
	}
	v, ok := params[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return def
	}
}

func phmIntParam(params map[string]any, key string, def int) int {
	if params == nil {
		return def
	}
	v, ok := params[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	default:
		return def
	}
}

func phmStringParam(params map[string]any, key string, def string) string {
	if params == nil {
		return def
	}
	v, ok := params[key]
	if !ok {
		return def
	}
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func phmBoolParam(params map[string]any, key string, def bool) bool {
	if params == nil {
		return def
	}
	v, ok := params[key]
	if !ok {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func phmStringSliceParam(params map[string]any, key string, def []string) []string {
	if params == nil {
		return def
	}
	v, ok := params[key]
	if !ok {
		return def
	}
	switch sl := v.(type) {
	case []string:
		return sl
	case []any:
		result := make([]string, 0, len(sl))
		for _, item := range sl {
			if s, ok2 := item.(string); ok2 {
				result = append(result, s)
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	return def
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

// PHMState holds all per-symbol runtime state for the VWAP Mean Reversion strategy.
type PHMState struct {
	Symbol     string
	Config     PHMConfig
	Indicators start.IndicatorData
	Timeframe  string

	// Session tracking
	SessionDate      string  // "2006-01-02"
	SignalsToday     int
	SessionBarCount  int
	SessionVolSum    float64
	SessionOpenPrice float64 // first bar at 9:30 ET

	// Rolling volume window (last 10 bars for volume spike detection)
	RecentVolumes []float64
	BarCount      int

	// RSI divergence tracking
	PrevRSI  float64   // previous bar's RSI
	PrevHigh float64   // previous bar's high (for bearish divergence)
	PrevLow  float64   // previous bar's low (for bullish divergence)
	PrevBar  start.Bar // for reversal candle detection
	HasPrevBar bool

	// Price at 14:00 ET for day typing
	Price1400    float64 // price at 14:00 ET for trend day detection
	Price1400Set bool

	// Peak RSI tracking for divergence
	AfternoonPeakRSI   float64 // highest RSI since 14:00
	AfternoonTroughRSI float64 // lowest RSI since 14:00 (start at 100)
	AfternoonPeakHigh  float64 // highest price when RSI peaked
	AfternoonTroughLow float64 // lowest price when RSI troughed

	// Day type flag
	IsTrendDay bool

	// Window bar tracking (for min_window_bars cooldown)
	WindowStartBarCount int  // BarCount when entry window first opened today
	WindowStartSet      bool // whether we've recorded the window start

	// Position management
	PositionSide start.Side
	PendingEntry start.Side
	EntryPrice   float64
	EntryVWAP    float64 // VWAP at entry time (target for exit)
	EntrySD      float64 // SD at entry time
	LastClose    float64

	// Cached timezone
	etLoc     *time.Location
	etLocOnce sync.Once
}

// SetIndicators updates the cached indicator data.
func (s *PHMState) SetIndicators(ind start.IndicatorData) {
	s.Indicators = ind
}

// loadET lazily loads the ET timezone location.
func (s *PHMState) loadET() *time.Location {
	s.etLocOnce.Do(func() {
		loc, err := time.LoadLocation(s.Config.AllowedHoursTZ)
		if err != nil {
			loc = time.FixedZone("EST", -5*3600)
		}
		s.etLoc = loc
	})
	return s.etLoc
}

// phmStateJSON is the JSON wire format for PHMState persistence.
type phmStateJSON struct {
	Symbol           string              `json:"symbol"`
	Config           PHMConfig           `json:"config"`
	Indicators       start.IndicatorData `json:"indicators"`
	Timeframe        string              `json:"timeframe"`
	SessionDate      string              `json:"session_date"`
	SignalsToday     int                 `json:"signals_today"`
	SessionBarCount  int                 `json:"session_bar_count"`
	SessionVolSum    float64             `json:"session_vol_sum"`
	SessionOpenPrice float64             `json:"session_open_price"`
	RecentVolumes    []float64           `json:"recent_volumes"`
	BarCount         int                 `json:"bar_count"`
	PrevRSI          float64             `json:"prev_rsi"`
	PrevHigh         float64             `json:"prev_high"`
	PrevLow          float64             `json:"prev_low"`
	PrevBar          start.Bar           `json:"prev_bar"`
	HasPrevBar       bool                `json:"has_prev_bar"`
	Price1400        float64             `json:"price_1400"`
	Price1400Set     bool                `json:"price_1400_set"`
	AfternoonPeakRSI   float64 `json:"afternoon_peak_rsi"`
	AfternoonTroughRSI float64 `json:"afternoon_trough_rsi"`
	AfternoonPeakHigh  float64 `json:"afternoon_peak_high"`
	AfternoonTroughLow float64 `json:"afternoon_trough_low"`
	IsTrendDay          bool                `json:"is_trend_day"`
	WindowStartBarCount int                 `json:"window_start_bar_count"`
	WindowStartSet      bool                `json:"window_start_set"`
	PositionSide        start.Side          `json:"position_side"`
	PendingEntry     start.Side          `json:"pending_entry"`
	EntryPrice       float64             `json:"entry_price"`
	EntryVWAP        float64             `json:"entry_vwap"`
	EntrySD          float64             `json:"entry_sd"`
	LastClose        float64             `json:"last_close"`
}

// Marshal serializes PHMState for persistence/recovery.
func (s *PHMState) Marshal() ([]byte, error) {
	j := phmStateJSON{
		Symbol:             s.Symbol,
		Config:             s.Config,
		Indicators:         s.Indicators,
		Timeframe:          s.Timeframe,
		SessionDate:        s.SessionDate,
		SignalsToday:       s.SignalsToday,
		SessionBarCount:    s.SessionBarCount,
		SessionVolSum:      s.SessionVolSum,
		SessionOpenPrice:   s.SessionOpenPrice,
		RecentVolumes:      s.RecentVolumes,
		BarCount:           s.BarCount,
		PrevRSI:            s.PrevRSI,
		PrevHigh:           s.PrevHigh,
		PrevLow:            s.PrevLow,
		PrevBar:            s.PrevBar,
		HasPrevBar:         s.HasPrevBar,
		Price1400:          s.Price1400,
		Price1400Set:       s.Price1400Set,
		AfternoonPeakRSI:   s.AfternoonPeakRSI,
		AfternoonTroughRSI: s.AfternoonTroughRSI,
		AfternoonPeakHigh:  s.AfternoonPeakHigh,
		AfternoonTroughLow: s.AfternoonTroughLow,
		IsTrendDay:          s.IsTrendDay,
		WindowStartBarCount: s.WindowStartBarCount,
		WindowStartSet:      s.WindowStartSet,
		PositionSide:        s.PositionSide,
		PendingEntry:       s.PendingEntry,
		EntryPrice:         s.EntryPrice,
		EntryVWAP:          s.EntryVWAP,
		EntrySD:            s.EntrySD,
		LastClose:          s.LastClose,
	}
	return json.Marshal(j)
}

// Unmarshal restores PHMState from persisted bytes.
func (s *PHMState) Unmarshal(data []byte) error {
	var j phmStateJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return fmt.Errorf("PHMState.Unmarshal: %w", err)
	}
	s.Symbol = j.Symbol
	s.Config = j.Config
	s.Indicators = j.Indicators
	s.Timeframe = j.Timeframe
	s.SessionDate = j.SessionDate
	s.SignalsToday = j.SignalsToday
	s.SessionBarCount = j.SessionBarCount
	s.SessionVolSum = j.SessionVolSum
	s.SessionOpenPrice = j.SessionOpenPrice
	s.RecentVolumes = j.RecentVolumes
	s.BarCount = j.BarCount
	s.PrevRSI = j.PrevRSI
	s.PrevHigh = j.PrevHigh
	s.PrevLow = j.PrevLow
	s.PrevBar = j.PrevBar
	s.HasPrevBar = j.HasPrevBar
	s.Price1400 = j.Price1400
	s.Price1400Set = j.Price1400Set
	s.AfternoonPeakRSI = j.AfternoonPeakRSI
	s.AfternoonTroughRSI = j.AfternoonTroughRSI
	s.AfternoonPeakHigh = j.AfternoonPeakHigh
	s.AfternoonTroughLow = j.AfternoonTroughLow
	s.IsTrendDay = j.IsTrendDay
	s.WindowStartBarCount = j.WindowStartBarCount
	s.WindowStartSet = j.WindowStartSet
	s.PositionSide = j.PositionSide
	s.PendingEntry = j.PendingEntry
	s.EntryPrice = j.EntryPrice
	s.EntryVWAP = j.EntryVWAP
	s.EntrySD = j.EntrySD
	s.LastClose = j.LastClose
	return nil
}

// ---------------------------------------------------------------------------
// Strategy
// ---------------------------------------------------------------------------

// PHMStrategy implements the Power Hour VWAP Mean Reversion strategy.
type PHMStrategy struct {
	meta start.Meta
}

// NewPHMStrategy creates a new Power Hour VWAP Mean Reversion strategy.
func NewPHMStrategy() *PHMStrategy {
	id, _ := start.NewStrategyID("phm_power_hour")
	ver, _ := start.NewVersion("1.0.0")
	return &PHMStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "Power Hour VWAP Mean Reversion",
			Description: "VWAP snap-back during Power Hour — enter on +/-2sigma deviation, target VWAP",
			Author:      "system",
		},
	}
}

// Meta returns strategy metadata.
func (s *PHMStrategy) Meta() start.Meta { return s.meta }

// WarmupBars returns the number of bars needed before live signals.
func (s *PHMStrategy) WarmupBars() int { return 12 }

// Init creates initial state for a symbol.
func (s *PHMStrategy) Init(_ start.Context, symbol string, params map[string]any, prior start.State) (start.State, error) {
	cfg := NewPHMConfigFromDNA(params)

	barTF := "5m"
	if v, ok := params["bar_timeframe"]; ok {
		if str, ok := v.(string); ok && str != "" {
			barTF = str
		}
	}

	st := &PHMState{
		Symbol:    symbol,
		Config:    cfg,
		Timeframe: barTF,
	}

	// Attempt to restore from prior state if available.
	if prior != nil {
		if phmPrior, ok := prior.(*PHMState); ok {
			st.SessionDate = phmPrior.SessionDate
			st.SignalsToday = phmPrior.SignalsToday
			st.SessionBarCount = phmPrior.SessionBarCount
			st.SessionVolSum = phmPrior.SessionVolSum
			st.SessionOpenPrice = phmPrior.SessionOpenPrice
			st.RecentVolumes = phmPrior.RecentVolumes
			st.BarCount = phmPrior.BarCount
			st.PrevRSI = phmPrior.PrevRSI
			st.PrevHigh = phmPrior.PrevHigh
			st.PrevLow = phmPrior.PrevLow
			st.PrevBar = phmPrior.PrevBar
			st.HasPrevBar = phmPrior.HasPrevBar
			st.Price1400 = phmPrior.Price1400
			st.Price1400Set = phmPrior.Price1400Set
			st.AfternoonPeakRSI = phmPrior.AfternoonPeakRSI
			st.AfternoonTroughRSI = phmPrior.AfternoonTroughRSI
			st.AfternoonPeakHigh = phmPrior.AfternoonPeakHigh
			st.AfternoonTroughLow = phmPrior.AfternoonTroughLow
			st.IsTrendDay = phmPrior.IsTrendDay
			st.WindowStartBarCount = phmPrior.WindowStartBarCount
			st.WindowStartSet = phmPrior.WindowStartSet
			st.PositionSide = phmPrior.PositionSide
			st.PendingEntry = phmPrior.PendingEntry
			st.EntryPrice = phmPrior.EntryPrice
			st.EntryVWAP = phmPrior.EntryVWAP
			st.EntrySD = phmPrior.EntrySD
			st.LastClose = phmPrior.LastClose
			// Refresh config from params (may have been updated).
			st.Config = cfg
		}
	}

	return st, nil
}

// ---------------------------------------------------------------------------
// OnBar — main bar processing logic
// ---------------------------------------------------------------------------

// OnBar processes a market bar and produces entry/exit signals.
func (s *PHMStrategy) OnBar(_ start.Context, symbol string, bar start.Bar, st start.State) (start.State, []start.Signal, error) {
	phmSt, ok := st.(*PHMState)
	if !ok {
		return st, nil, fmt.Errorf("PHMStrategy.OnBar: expected *PHMState, got %T", st)
	}

	loc := phmSt.loadET()
	barET := bar.Time.In(loc)
	barDate := barET.Format("2006-01-02")

	// (1) Session reset on new date
	if barDate != phmSt.SessionDate {
		phmSessionReset(phmSt, barDate)
	}

	// (2) Update session stats
	phmSt.SessionBarCount++
	phmSt.SessionVolSum += bar.Volume

	// (3) Update rolling volume window (last 10 bars)
	phmUpdateRollingVolumes(phmSt, bar)

	// (4) Update state: open price, day type, RSI tracking, prev bar
	phmUpdateState(phmSt, bar, barET)

	// (5) EOD flatten check
	eodH, eodM := phmParseHHMM(phmSt.Config.EODFlattenTime)
	eodTime := time.Date(barET.Year(), barET.Month(), barET.Day(), eodH, eodM, 0, 0, loc)
	if !barET.Before(eodTime) && phmSt.PositionSide != "" {
		instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
		sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, phmSt.PositionSide, 1.0, map[string]string{
			"reason": "eod_flatten",
		})
		if err != nil {
			return phmSt, nil, fmt.Errorf("PHMStrategy.OnBar: eod flatten signal: %w", err)
		}
		phmSt.PositionSide = ""
		phmSt.LastClose = 0
		phmSt.EntryPrice = 0
		phmSt.EntryVWAP = 0
		phmSt.EntrySD = 0
		return phmSt, []start.Signal{sig}, nil
	}

	// (6) Exit checks (VWAP target, SD target, stop)
	if phmSt.PositionSide != "" && phmSt.PendingEntry == "" {
		exitSig, exited := s.checkExits(phmSt, symbol, bar)
		if exited {
			phmSt.PositionSide = ""
			phmSt.LastClose = 0
			phmSt.EntryPrice = 0
			phmSt.EntryVWAP = 0
			phmSt.EntrySD = 0
			return phmSt, []start.Signal{exitSig}, nil
		}
		phmSt.LastClose = bar.Close
	}

	// (7) Entry check (4 confluence layers)
	if phmSt.PositionSide == "" && phmSt.PendingEntry == "" && phmSt.SignalsToday < phmSt.Config.MaxSignalsPerSess {
		entrySig, entered := s.checkEntry(phmSt, symbol, bar, barET, loc)
		if entered {
			return phmSt, []start.Signal{entrySig}, nil
		}
	}

	return phmSt, nil, nil
}

// ---------------------------------------------------------------------------
// Exit Logic
// ---------------------------------------------------------------------------

// checkExits evaluates exit conditions for mean reversion.
func (s *PHMStrategy) checkExits(phmSt *PHMState, symbol string, bar start.Bar) (start.Signal, bool) {
	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))

	vwap := phmSt.Indicators.VWAP
	sd := phmSt.Indicators.VWAPSD

	// (1) Primary target: price returns to VWAP
	if phmSt.Config.TargetVWAP && vwap > 0 {
		if phmSt.PositionSide == start.SideBuy && bar.Close >= vwap {
			sig, _ := start.NewSignal(instanceID, symbol, start.SignalExit, phmSt.PositionSide, 1.0, map[string]string{
				"reason": "target_vwap",
				"vwap":   fmt.Sprintf("%.4f", vwap),
			})
			return sig, true
		}
		if phmSt.PositionSide == start.SideSell && bar.Close <= vwap {
			sig, _ := start.NewSignal(instanceID, symbol, start.SignalExit, phmSt.PositionSide, 1.0, map[string]string{
				"reason": "target_vwap",
				"vwap":   fmt.Sprintf("%.4f", vwap),
			})
			return sig, true
		}
	}

	// (2) Secondary target: opposite +/-1sigma band
	if phmSt.Config.TargetOppositeSD > 0 && vwap > 0 && sd > 0 {
		if phmSt.PositionSide == start.SideBuy {
			// Long position: target upper band (opposite side from where we entered below VWAP)
			upperTarget := vwap + phmSt.Config.TargetOppositeSD*sd
			if bar.Close >= upperTarget {
				sig, _ := start.NewSignal(instanceID, symbol, start.SignalExit, phmSt.PositionSide, 1.0, map[string]string{
					"reason":        "target_opposite_sd",
					"target_price":  fmt.Sprintf("%.4f", upperTarget),
				})
				return sig, true
			}
		}
		if phmSt.PositionSide == start.SideSell {
			// Short position: target lower band
			lowerTarget := vwap - phmSt.Config.TargetOppositeSD*sd
			if bar.Close <= lowerTarget {
				sig, _ := start.NewSignal(instanceID, symbol, start.SignalExit, phmSt.PositionSide, 1.0, map[string]string{
					"reason":        "target_opposite_sd",
					"target_price":  fmt.Sprintf("%.4f", lowerTarget),
				})
				return sig, true
			}
		}
	}

	// (3) Stop loss: price closes beyond +/-3sigma band (parabolic move, bail)
	if sd > 0 && vwap > 0 {
		deviation := (bar.Close - vwap) / sd
		if phmSt.PositionSide == start.SideBuy && deviation < -phmSt.Config.SDStopThreshold {
			sig, _ := start.NewSignal(instanceID, symbol, start.SignalExit, phmSt.PositionSide, 1.0, map[string]string{
				"reason":       "sd_stop",
				"deviation_sd": fmt.Sprintf("%.2f", deviation),
			})
			return sig, true
		}
		if phmSt.PositionSide == start.SideSell && deviation > phmSt.Config.SDStopThreshold {
			sig, _ := start.NewSignal(instanceID, symbol, start.SignalExit, phmSt.PositionSide, 1.0, map[string]string{
				"reason":       "sd_stop",
				"deviation_sd": fmt.Sprintf("%.2f", deviation),
			})
			return sig, true
		}
	}

	// (4) Max loss: StopBPS basis points from entry (safety net)
	if phmSt.EntryPrice > 0 {
		var adverseBPS float64
		if phmSt.PositionSide == start.SideBuy {
			adverseBPS = (phmSt.EntryPrice - bar.Close) / phmSt.EntryPrice * 10000
		} else {
			adverseBPS = (bar.Close - phmSt.EntryPrice) / phmSt.EntryPrice * 10000
		}
		if adverseBPS > float64(phmSt.Config.StopBPS) {
			sig, _ := start.NewSignal(instanceID, symbol, start.SignalExit, phmSt.PositionSide, 1.0, map[string]string{
				"reason": "max_loss",
			})
			return sig, true
		}
	}

	return start.Signal{}, false
}

// ---------------------------------------------------------------------------
// Entry Logic — 4 Confluence Layers
// ---------------------------------------------------------------------------

// checkEntry evaluates VWAP mean reversion entry conditions.
func (s *PHMStrategy) checkEntry(phmSt *PHMState, symbol string, bar start.Bar, barET time.Time, loc *time.Location) (start.Signal, bool) {
	cfg := phmSt.Config

	// Time window check
	startH, startM := phmParseHHMM(cfg.AllowedHoursStart)
	endH, endM := phmParseHHMM(cfg.AllowedHoursEnd)
	windowStart := time.Date(barET.Year(), barET.Month(), barET.Day(), startH, startM, 0, 0, loc)
	windowEnd := time.Date(barET.Year(), barET.Month(), barET.Day(), endH, endM, 0, 0, loc)

	if barET.Before(windowStart) || !barET.Before(windowEnd) {
		return start.Signal{}, false
	}

	// Track when the entry window first opened today
	if !phmSt.WindowStartSet {
		phmSt.WindowStartBarCount = phmSt.BarCount
		phmSt.WindowStartSet = true
	}

	// Min window bars cooldown — observe N bars before entering
	if cfg.MinWindowBars > 0 {
		barsInWindow := phmSt.BarCount - phmSt.WindowStartBarCount
		if barsInWindow < cfg.MinWindowBars {
			return start.Signal{}, false
		}
	}

	// Day-of-week filter
	if len(cfg.BlockedDaysOfWeek) > 0 {
		dayName := barET.Weekday().String()
		for _, blocked := range cfg.BlockedDaysOfWeek {
			if strings.EqualFold(blocked, dayName) {
				return start.Signal{}, false
			}
		}
	}

	// Day type filter: trend day disables mean reversion
	if phmSt.IsTrendDay {
		return start.Signal{}, false
	}

	// ADX filter: mean reversion works on range days
	if phmSt.Indicators.ADX > 0 && phmSt.Indicators.ADX > cfg.MaxADX {
		return start.Signal{}, false
	}

	// Minimum session volume gate — skip illiquid tickers
	if cfg.MinSessionVolume > 0 && phmSt.SessionVolSum < float64(cfg.MinSessionVolume) {
		return start.Signal{}, false
	}

	// --- Layer 1: Statistical Deviation (REQUIRED) ---
	vwap := phmSt.Indicators.VWAP
	sd := phmSt.Indicators.VWAPSD
	if vwap <= 0 || sd <= 0 {
		return start.Signal{}, false
	}

	// Vol-adjusted bands: on high-ATR days, widen the SD threshold so we
	// don't enter prematurely on noisy price action.
	effectiveSDThreshold := cfg.SDEntryThreshold
	if cfg.VolAdjustBands && phmSt.Indicators.ATR > 0 && phmSt.Indicators.VolumeSMA > 0 {
		// RVOL proxy for intraday volatility expansion
		rvol := bar.Volume / phmSt.Indicators.VolumeSMA
		if rvol > 2.0 {
			// Scale up: e.g., RVOL 3.0 → threshold * 1.25
			effectiveSDThreshold *= 1.0 + (rvol-2.0)*0.25
		}
	}

	deviation := (bar.Close - vwap) / sd

	var isShort, isLong bool
	var side start.Side

	switch {
	case deviation >= effectiveSDThreshold:
		isShort = true
		side = start.SideSell
	case deviation <= -effectiveSDThreshold:
		isLong = true
		side = start.SideBuy
	default:
		return start.Signal{}, false // not extended enough
	}

	// Confluence scoring
	const maxPossibleScore = 55 // 10+8+7+6+5+4+5 + 10(dark pool)
	var confluenceScore int
	var confluenceFactors []string

	// +/-2sigma breach: 10 points (required — always awarded here)
	confluenceScore += 10
	confluenceFactors = append(confluenceFactors, "vwap_2sd_breach")

	// --- Layer 2: Reversal Candle Confirmation ---
	upperBand := vwap + cfg.SDEntryThreshold*sd
	lowerBand := vwap - cfg.SDEntryThreshold*sd

	hasReversalCandle := false
	if cfg.RequireReversal {
		if isShort && bar.Close < upperBand && bar.High > upperBand {
			hasReversalCandle = true
		}
		if isLong && bar.Close > lowerBand && bar.Low < lowerBand {
			hasReversalCandle = true
		}
		if !hasReversalCandle {
			return start.Signal{}, false
		}
	} else {
		// Even without RequireReversal, check for reversal candle for scoring
		if isShort && bar.Close < upperBand && bar.High > upperBand {
			hasReversalCandle = true
		}
		if isLong && bar.Close > lowerBand && bar.Low < lowerBand {
			hasReversalCandle = true
		}
	}

	if hasReversalCandle {
		confluenceScore += 8
		confluenceFactors = append(confluenceFactors, "reversal_candle")
	}

	// Candle patterns: shooting star / hammer
	barRange := bar.High - bar.Low
	body := math.Abs(bar.Close - bar.Open)
	hasCandlePattern := false
	if barRange > 0 && body > 0 {
		if isShort {
			// Shooting star: long upper wick, small body at bottom
			upperWick := bar.High - math.Max(bar.Open, bar.Close)
			if upperWick > 2*body {
				hasCandlePattern = true
			}
		}
		if isLong {
			// Hammer: long lower wick, small body at top
			lowerWick := math.Min(bar.Open, bar.Close) - bar.Low
			if lowerWick > 2*body {
				hasCandlePattern = true
			}
		}
	}
	if hasCandlePattern {
		confluenceScore += 5
		confluenceFactors = append(confluenceFactors, "candle_pattern")
	}

	// --- Layer 3: RSI Divergence (bonus) ---
	rsi := phmSt.Indicators.RSI
	hasRSIDivergence := false

	if rsi > 0 && phmSt.HasPrevBar {
		if isShort && phmSt.AfternoonPeakRSI > 0 {
			// Bearish divergence: price makes new high but RSI lower than previous peak
			if bar.High > phmSt.AfternoonPeakHigh && rsi < phmSt.AfternoonPeakRSI {
				hasRSIDivergence = true
			}
		}
		if isLong && phmSt.AfternoonTroughRSI < 100 {
			// Bullish divergence: price makes new low but RSI higher than previous trough
			if bar.Low < phmSt.AfternoonTroughLow && rsi > phmSt.AfternoonTroughRSI {
				hasRSIDivergence = true
			}
		}
	}
	if hasRSIDivergence {
		confluenceScore += 7
		confluenceFactors = append(confluenceFactors, "rsi_divergence")
	}

	// RSI overbought/oversold threshold
	if isShort && rsi > cfg.RSIOverbought {
		confluenceScore += 5
		confluenceFactors = append(confluenceFactors, "rsi_overbought")
	}
	if isLong && rsi > 0 && rsi < cfg.RSIOversold {
		confluenceScore += 5
		confluenceFactors = append(confluenceFactors, "rsi_oversold")
	}

	// --- Layer 4: Volume Exhaustion (bonus) ---
	avgRecentVol := phmAvgVolume(phmSt.RecentVolumes)
	volumeRatio := 0.0
	if avgRecentVol > 0 {
		volumeRatio = bar.Volume / avgRecentVol
	}

	if avgRecentVol > 0 && bar.Volume > avgRecentVol*cfg.VolumeSpikeMult {
		// Volume spike detected — check for blow-off confirmation
		blowOff := false
		if phmSt.HasPrevBar {
			if isShort && bar.Close < phmSt.PrevBar.High {
				blowOff = true // blow-off top: high volume but failed to hold new high
			}
			if isLong && bar.Close > phmSt.PrevBar.Low {
				blowOff = true // capitulation: high volume but failed to hold new low
			}
		}
		if blowOff || !phmSt.HasPrevBar {
			confluenceScore += 6
			confluenceFactors = append(confluenceFactors, "volume_exhaustion")
		}
	}

	// ADX < 20 — strong range day bonus
	if phmSt.Indicators.ADX > 0 && phmSt.Indicators.ADX < 20 {
		confluenceScore += 4
		confluenceFactors = append(confluenceFactors, "adx_range_day")
	}

	// --- Layer 5: Dark Pool Large Print Confluence ---
	// Institutions parking orders in dark pools at the ±2σ level confirms
	// a floor/ceiling is being established.
	dpConf := start.ScoreDarkPool(phmSt.Indicators, isLong)
	if dpConf.Score > 0 {
		confluenceScore += dpConf.Score
		confluenceFactors = append(confluenceFactors, dpConf.Factors...)
	}

	// Gate by MinConfluenceScore if enabled
	if cfg.MinConfluenceScore > 0 && confluenceScore < cfg.MinConfluenceScore {
		return start.Signal{}, false
	}

	// Also compute base confluence for universal factors
	baseConf := start.ComputeBaseConfluence(bar, phmSt.Indicators, isLong)
	phmSpecific := start.ConfluenceResult{
		Score:   confluenceScore,
		Factors: confluenceFactors,
	}
	merged := start.MergeConfluence(baseConf, phmSpecific)

	// Compute strength: confluence_score / max_possible_score clamped to [0, 1]
	strength := float64(confluenceScore) / float64(maxPossibleScore)
	strength = clampStrength(strength)
	// Use higher of strategy-specific strength and merged confluence-based strength
	mergedStrength := float64(merged.Score) / 120.0 // ~120 max combined
	if mergedStrength > strength {
		strength = clampStrength(mergedStrength)
	}
	if strength < 0.1 {
		strength = 0.1
	}

	// Determine day type label
	dayType := "range"
	if phmSt.IsTrendDay {
		dayType = "trend"
	}

	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
	tags := map[string]string{
		"trigger":           "vwap_mean_reversion",
		"ref_price":         fmt.Sprintf("%.10f", bar.Close),
		"vwap":              fmt.Sprintf("%.4f", vwap),
		"vwap_sd":           fmt.Sprintf("%.4f", sd),
		"deviation_sd":      fmt.Sprintf("%.2f", deviation),
		"rsi":               fmt.Sprintf("%.2f", rsi),
		"adx":               fmt.Sprintf("%.2f", phmSt.Indicators.ADX),
		"volume_ratio":      fmt.Sprintf("%.2f", volumeRatio),
		"confluence":        fmt.Sprintf("%d", merged.Score),
		"confluence_detail": merged.FormatDetail(),
		"day_type":          dayType,
		"reversal_candle":   fmt.Sprintf("%t", hasReversalCandle),
	}

	sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, side, strength, tags)
	if err != nil {
		return start.Signal{}, false
	}

	phmSt.PendingEntry = side
	phmSt.EntryVWAP = vwap
	phmSt.EntrySD = sd
	phmSt.SignalsToday++
	return sig, true
}

// ---------------------------------------------------------------------------
// OnEvent
// ---------------------------------------------------------------------------

// OnEvent processes fill confirmations and entry rejections.
func (s *PHMStrategy) OnEvent(_ start.Context, symbol string, evt any, st start.State) (start.State, []start.Signal, error) {
	phmSt, ok := st.(*PHMState)
	if !ok {
		return st, nil, fmt.Errorf("PHMStrategy.OnEvent: expected *PHMState, got %T", st)
	}

	switch e := evt.(type) {
	case start.FillConfirmation:
		phmSt.PositionSide = e.Side
		phmSt.EntryPrice = e.Price
		phmSt.PendingEntry = ""
		phmSt.LastClose = 0
		return phmSt, nil, nil

	case start.EntryRejection:
		_ = e
		phmSt.PendingEntry = ""
		return phmSt, nil, nil

	case start.AuctionImbalanceUpdate:
		// Not used in mean reversion strategy, but handle gracefully.
		return phmSt, nil, nil

	default:
		return phmSt, nil, nil
	}
}

// ---------------------------------------------------------------------------
// ReplayOnBar
// ---------------------------------------------------------------------------

// ReplayOnBar processes a historical bar for state recovery without emitting signals.
func (s *PHMStrategy) ReplayOnBar(_ start.Context, _ string, bar start.Bar, st start.State, indicators start.IndicatorData) (start.State, error) {
	phmSt, ok := st.(*PHMState)
	if !ok {
		return st, fmt.Errorf("PHMStrategy.ReplayOnBar: expected *PHMState, got %T", st)
	}

	phmSt.SetIndicators(indicators)

	loc := phmSt.loadET()
	barET := bar.Time.In(loc)
	barDate := barET.Format("2006-01-02")

	// Session reset
	if barDate != phmSt.SessionDate {
		phmSessionReset(phmSt, barDate)
	}

	// Update session stats
	phmSt.SessionBarCount++
	phmSt.SessionVolSum += bar.Volume

	// Update rolling volumes
	phmUpdateRollingVolumes(phmSt, bar)

	// Update state (open price, day type, RSI tracking, prev bar)
	phmUpdateState(phmSt, bar, barET)

	return phmSt, nil
}

// ---------------------------------------------------------------------------
// Session Reset Helper
// ---------------------------------------------------------------------------

// phmSessionReset clears all per-session state fields on date change.
func phmSessionReset(phmSt *PHMState, barDate string) {
	phmSt.SessionDate = barDate
	phmSt.SignalsToday = 0
	phmSt.SessionVolSum = 0
	phmSt.SessionBarCount = 0
	phmSt.RecentVolumes = phmSt.RecentVolumes[:0]
	phmSt.BarCount = 0
	phmSt.PositionSide = ""
	phmSt.PendingEntry = ""
	phmSt.LastClose = 0
	phmSt.EntryPrice = 0
	phmSt.EntryVWAP = 0
	phmSt.EntrySD = 0

	// Session open / day typing
	phmSt.SessionOpenPrice = 0
	phmSt.Price1400 = 0
	phmSt.Price1400Set = false
	phmSt.IsTrendDay = false
	phmSt.WindowStartBarCount = 0
	phmSt.WindowStartSet = false

	// RSI divergence tracking
	phmSt.PrevRSI = 0
	phmSt.PrevHigh = 0
	phmSt.PrevLow = 0
	phmSt.PrevBar = start.Bar{}
	phmSt.HasPrevBar = false
	phmSt.AfternoonPeakRSI = 0
	phmSt.AfternoonTroughRSI = 100
	phmSt.AfternoonPeakHigh = 0
	phmSt.AfternoonTroughLow = 0
}

// ---------------------------------------------------------------------------
// Rolling Volume Update Helper
// ---------------------------------------------------------------------------

// phmUpdateRollingVolumes maintains a rolling window of the last 10 bar volumes.
func phmUpdateRollingVolumes(phmSt *PHMState, bar start.Bar) {
	const windowSize = 10
	phmSt.RecentVolumes = append(phmSt.RecentVolumes, bar.Volume)
	if len(phmSt.RecentVolumes) > windowSize {
		phmSt.RecentVolumes = phmSt.RecentVolumes[len(phmSt.RecentVolumes)-windowSize:]
	}
	phmSt.BarCount++
}

// ---------------------------------------------------------------------------
// State Update Helper — VWAP Mean Reversion
// ---------------------------------------------------------------------------

// phmUpdateState tracks session open, day type, RSI tracking, and prev bar.
// Called from both OnBar and ReplayOnBar.
func phmUpdateState(phmSt *PHMState, bar start.Bar, barET time.Time) {
	h := barET.Hour()
	m := barET.Minute()

	// Track session open (first bar at/after 9:30 ET)
	if phmSt.SessionOpenPrice == 0 && (h > 9 || (h == 9 && m >= 30)) {
		phmSt.SessionOpenPrice = bar.Close
	}

	// At 14:00 ET, capture price for day type detection
	if !phmSt.Price1400Set && h >= 14 {
		phmSt.Price1400 = bar.Close
		phmSt.Price1400Set = true

		// Compute day type: if abs(return) > DayTypeTrendPct%, it's a trend day
		if phmSt.SessionOpenPrice > 0 && phmSt.Config.DayTypeTrendPct > 0 {
			dayReturn := math.Abs(bar.Close-phmSt.SessionOpenPrice) / phmSt.SessionOpenPrice * 100
			if dayReturn > phmSt.Config.DayTypeTrendPct {
				phmSt.IsTrendDay = true
			}
		}
	}

	// Track RSI peaks/troughs since 14:00 for divergence detection
	rsi := phmSt.Indicators.RSI
	if h >= 14 && rsi > 0 {
		if rsi > phmSt.AfternoonPeakRSI {
			phmSt.AfternoonPeakRSI = rsi
			phmSt.AfternoonPeakHigh = bar.High
		}
		if rsi < phmSt.AfternoonTroughRSI {
			phmSt.AfternoonTroughRSI = rsi
			phmSt.AfternoonTroughLow = bar.Low
		}
	}

	// Update previous bar tracking
	phmSt.PrevRSI = rsi
	phmSt.PrevHigh = bar.High
	phmSt.PrevLow = bar.Low
	phmSt.PrevBar = bar
	phmSt.HasPrevBar = true
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// phmParseHHMM parses "HH:MM" into hours and minutes. Falls back to 0,0.
func phmParseHHMM(s string) (int, int) {
	var h, m int
	_, err := fmt.Sscanf(s, "%d:%d", &h, &m)
	if err != nil {
		return 0, 0
	}
	return h, m
}

// phmAvgVolume computes the arithmetic mean of a float64 slice.
func phmAvgVolume(volumes []float64) float64 {
	if len(volumes) == 0 {
		return 0
	}
	var sum float64
	for _, v := range volumes {
		sum += v
	}
	return sum / float64(len(volumes))
}
