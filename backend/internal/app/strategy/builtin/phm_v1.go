// Package builtin — Power Hour Momentum (PHM) strategy.
// Gao et al. (2018) intraday momentum: first 30-min return predicts last 30-min return.
// One directional decision per symbol per day, entered at 15:15-15:30 ET, held to 15:55 EOD flatten.
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

// PHMConfig holds tunable parameters for the Power Hour Momentum strategy.
type PHMConfig struct {
	LookbackBars      int     // default 12 (60min of 5m bars)
	VolumeMult        float64 // default 1.5
	CloseRangePct     float64 // default 0.30
	StopBPS           int     // default 150 (wider — hold to close, safety net only)
	MaxSignalsPerSess int     // default 1 (ONE entry per session)
	LimitOffsetBPS    int     // default 5
	AllowedHoursStart string  // default "15:15"
	AllowedHoursEnd   string  // default "15:45"
	AllowedHoursTZ    string  // default "America/New_York"
	EODFlattenTime    string  // default "15:55"
	HTFBiasEnabled    bool    // default false

	// Gao et al. intraday momentum params
	AMReturnMinPct   float64 // default 0.10 (min absolute AM return for signal, 0.1%)
	EvalTime         string  // default "15:15" (when to evaluate direction, ET)
	MaxEntryBars     int     // default 3 (bars after eval time to attempt entry)
	VolAccelMinRatio float64 // default 1.3 (power hour vol / afternoon vol threshold)
	RequireHTFAlign  bool    // default false

	// Day-of-week filter (e.g., block Monday where weekend gaps corrupt AM signal)
	BlockedDaysOfWeek []string // default [] (empty = no blocking)

	// MOC imbalance second entry window (live only)
	SecondWindowEnabled bool    // default false
	SecondWindowStart   string  // default "15:45"
	SecondWindowEnd     string  // default "15:55"
	ImbalanceMinShares  float64 // default 500000
}

// NewPHMConfigFromDNA reads PHMConfig from DNA params with sensible defaults.
func NewPHMConfigFromDNA(params map[string]any) PHMConfig {
	return PHMConfig{
		LookbackBars:      phmIntParam(params, "lookback_bars", 12),
		VolumeMult:        phmFloatParam(params, "volume_mult", 1.5),
		CloseRangePct:     phmFloatParam(params, "close_range_pct", 0.30),
		StopBPS:           phmIntParam(params, "stop_bps", 150),
		MaxSignalsPerSess: phmIntParam(params, "max_signals_per_session", 1),
		LimitOffsetBPS:    phmIntParam(params, "limit_offset_bps", 5),
		AllowedHoursStart: phmStringParam(params, "allowed_hours_start", "15:15"),
		AllowedHoursEnd:   phmStringParam(params, "allowed_hours_end", "15:45"),
		AllowedHoursTZ:    phmStringParam(params, "allowed_hours_tz", "America/New_York"),
		EODFlattenTime:    phmStringParam(params, "eod_flatten_time", "15:55"),
		HTFBiasEnabled:    phmBoolParam(params, "htf_bias_enabled", false),
		AMReturnMinPct:    phmFloatParam(params, "am_return_min_pct", 0.10),
		EvalTime:          phmStringParam(params, "eval_time", "15:15"),
		MaxEntryBars:      phmIntParam(params, "max_entry_bars", 3),
		VolAccelMinRatio:  phmFloatParam(params, "vol_accel_min_ratio", 1.3),
		RequireHTFAlign:   phmBoolParam(params, "require_htf_align", false),

		BlockedDaysOfWeek:   phmStringSliceParam(params, "blocked_days_of_week", nil),
		SecondWindowEnabled: phmBoolParam(params, "second_window_enabled", false),
		SecondWindowStart:   phmStringParam(params, "second_window_start", "15:45"),
		SecondWindowEnd:     phmStringParam(params, "second_window_end", "15:55"),
		ImbalanceMinShares:  phmFloatParam(params, "imbalance_min_shares", 500000),
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

// PHMState holds all per-symbol runtime state for the PHM strategy.
type PHMState struct {
	Symbol     string
	Config     PHMConfig
	Indicators start.IndicatorData
	Timeframe  string

	// Rolling window (size = LookbackBars)
	RecentHighs   []float64
	RecentLows    []float64
	RecentCloses  []float64
	RecentVolumes []float64
	BarCount      int

	// Session tracking
	SessionDate     string  // "2006-01-02"
	SignalsToday    int
	SessionVolSum   float64
	SessionBarCount int

	// Session tracking for Gao et al. model
	SessionOpenPrice float64 // First bar's close at/after 9:30 ET
	AMReturnPct      float64 // Return from session open to 10:00 ET (set once per day)
	AMReturnSet      bool    // Whether AM return has been computed today
	AMDirection      string  // "long" or "short" based on AM return sign

	// Volume acceleration for synthetic MOC proxy
	AfternoonVolSum  float64 // Volume sum 14:00-15:00 ET (baseline)
	AfternoonVolBars int     // Bar count for afternoon baseline
	PowerHourVolSum  float64 // Volume sum 15:00-15:30 ET (acceleration window)
	PowerHourVolBars int     // Bar count for power hour window

	// Entry management
	EvalDone       bool   // Whether we've evaluated today (one shot)
	EntryDirection string // Decided direction for today ("long", "short", "")
	EvalBarIndex   int    // BarCount at time of eval, for MaxEntryBars tracking

	// MOC imbalance second window state
	LastImbalance      float64
	LastImbalancePrice float64
	ImbalanceReceived  bool
	SecondWindowUsed   bool

	// Position/exit management
	PositionSide start.Side
	PendingEntry start.Side
	EntryPrice   float64
	LastClose    float64

	// Cached timezone location (not serialized)
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
			// Fallback: use fixed offset UTC-5 (EST) if load fails.
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
	RecentHighs      []float64           `json:"recent_highs"`
	RecentLows       []float64           `json:"recent_lows"`
	RecentCloses     []float64           `json:"recent_closes"`
	RecentVolumes    []float64           `json:"recent_volumes"`
	BarCount         int                 `json:"bar_count"`
	SessionDate      string              `json:"session_date"`
	SignalsToday     int                 `json:"signals_today"`
	SessionVolSum    float64             `json:"session_vol_sum"`
	SessionBarCount  int                 `json:"session_bar_count"`
	SessionOpenPrice float64             `json:"session_open_price"`
	AMReturnPct      float64             `json:"am_return_pct"`
	AMReturnSet      bool                `json:"am_return_set"`
	AMDirection      string              `json:"am_direction"`
	AfternoonVolSum  float64             `json:"afternoon_vol_sum"`
	AfternoonVolBars int                 `json:"afternoon_vol_bars"`
	PowerHourVolSum  float64             `json:"power_hour_vol_sum"`
	PowerHourVolBars int                 `json:"power_hour_vol_bars"`
	EvalDone           bool                `json:"eval_done"`
	EntryDirection     string              `json:"entry_direction"`
	EvalBarIndex       int                 `json:"eval_bar_index"`
	LastImbalance      float64             `json:"last_imbalance"`
	LastImbalancePrice float64             `json:"last_imbalance_price"`
	ImbalanceReceived  bool                `json:"imbalance_received"`
	SecondWindowUsed   bool                `json:"second_window_used"`
	PositionSide       start.Side          `json:"position_side"`
	PendingEntry       start.Side          `json:"pending_entry"`
	EntryPrice         float64             `json:"entry_price"`
	LastClose          float64             `json:"last_close"`
}

// Marshal serializes PHMState for persistence/recovery.
func (s *PHMState) Marshal() ([]byte, error) {
	j := phmStateJSON{
		Symbol:           s.Symbol,
		Config:           s.Config,
		Indicators:       s.Indicators,
		Timeframe:        s.Timeframe,
		RecentHighs:      s.RecentHighs,
		RecentLows:       s.RecentLows,
		RecentCloses:     s.RecentCloses,
		RecentVolumes:    s.RecentVolumes,
		BarCount:         s.BarCount,
		SessionDate:      s.SessionDate,
		SignalsToday:     s.SignalsToday,
		SessionVolSum:    s.SessionVolSum,
		SessionBarCount:  s.SessionBarCount,
		SessionOpenPrice: s.SessionOpenPrice,
		AMReturnPct:      s.AMReturnPct,
		AMReturnSet:      s.AMReturnSet,
		AMDirection:      s.AMDirection,
		AfternoonVolSum:  s.AfternoonVolSum,
		AfternoonVolBars: s.AfternoonVolBars,
		PowerHourVolSum:  s.PowerHourVolSum,
		PowerHourVolBars: s.PowerHourVolBars,
		EvalDone:           s.EvalDone,
		EntryDirection:     s.EntryDirection,
		EvalBarIndex:       s.EvalBarIndex,
		LastImbalance:      s.LastImbalance,
		LastImbalancePrice: s.LastImbalancePrice,
		ImbalanceReceived:  s.ImbalanceReceived,
		SecondWindowUsed:   s.SecondWindowUsed,
		PositionSide:       s.PositionSide,
		PendingEntry:       s.PendingEntry,
		EntryPrice:         s.EntryPrice,
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
	s.RecentHighs = j.RecentHighs
	s.RecentLows = j.RecentLows
	s.RecentCloses = j.RecentCloses
	s.RecentVolumes = j.RecentVolumes
	s.BarCount = j.BarCount
	s.SessionDate = j.SessionDate
	s.SignalsToday = j.SignalsToday
	s.SessionVolSum = j.SessionVolSum
	s.SessionBarCount = j.SessionBarCount
	s.SessionOpenPrice = j.SessionOpenPrice
	s.AMReturnPct = j.AMReturnPct
	s.AMReturnSet = j.AMReturnSet
	s.AMDirection = j.AMDirection
	s.AfternoonVolSum = j.AfternoonVolSum
	s.AfternoonVolBars = j.AfternoonVolBars
	s.PowerHourVolSum = j.PowerHourVolSum
	s.PowerHourVolBars = j.PowerHourVolBars
	s.EvalDone = j.EvalDone
	s.EntryDirection = j.EntryDirection
	s.EvalBarIndex = j.EvalBarIndex
	s.LastImbalance = j.LastImbalance
	s.LastImbalancePrice = j.LastImbalancePrice
	s.ImbalanceReceived = j.ImbalanceReceived
	s.SecondWindowUsed = j.SecondWindowUsed
	s.PositionSide = j.PositionSide
	s.PendingEntry = j.PendingEntry
	s.EntryPrice = j.EntryPrice
	s.LastClose = j.LastClose
	return nil
}

// ---------------------------------------------------------------------------
// Strategy
// ---------------------------------------------------------------------------

// PHMStrategy implements the Power Hour Momentum strategy.
type PHMStrategy struct {
	meta start.Meta
}

// NewPHMStrategy creates a new Power Hour Momentum strategy.
func NewPHMStrategy() *PHMStrategy {
	id, _ := start.NewStrategyID("phm_power_hour")
	ver, _ := start.NewVersion("1.0.0")
	return &PHMStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "Power Hour Momentum",
			Description: "Gao et al. intraday momentum — AM return predicts power hour direction",
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
			st.RecentHighs = phmPrior.RecentHighs
			st.RecentLows = phmPrior.RecentLows
			st.RecentCloses = phmPrior.RecentCloses
			st.RecentVolumes = phmPrior.RecentVolumes
			st.BarCount = phmPrior.BarCount
			st.SessionDate = phmPrior.SessionDate
			st.SignalsToday = phmPrior.SignalsToday
			st.SessionVolSum = phmPrior.SessionVolSum
			st.SessionBarCount = phmPrior.SessionBarCount
			st.SessionOpenPrice = phmPrior.SessionOpenPrice
			st.AMReturnPct = phmPrior.AMReturnPct
			st.AMReturnSet = phmPrior.AMReturnSet
			st.AMDirection = phmPrior.AMDirection
			st.AfternoonVolSum = phmPrior.AfternoonVolSum
			st.AfternoonVolBars = phmPrior.AfternoonVolBars
			st.PowerHourVolSum = phmPrior.PowerHourVolSum
			st.PowerHourVolBars = phmPrior.PowerHourVolBars
			st.EvalDone = phmPrior.EvalDone
			st.EntryDirection = phmPrior.EntryDirection
			st.EvalBarIndex = phmPrior.EvalBarIndex
			st.PositionSide = phmPrior.PositionSide
			st.PendingEntry = phmPrior.PendingEntry
			st.EntryPrice = phmPrior.EntryPrice
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

	// (a) Session reset
	if barDate != phmSt.SessionDate {
		phmSessionReset(phmSt, barDate)
	}

	// (b) Update session stats
	phmSt.SessionBarCount++
	phmSt.SessionVolSum += bar.Volume

	// (c) Update rolling windows
	phmUpdateRollingWindows(phmSt, bar)

	// (d) Update Gao et al. state: session open, AM return, volume zones
	phmUpdateV2State(phmSt, bar, barET)

	// (e) EOD flatten
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
		return phmSt, []start.Signal{sig}, nil
	}

	// (f) Exit logic — max loss only
	if phmSt.PositionSide != "" && phmSt.PendingEntry == "" {
		exitSig, exited := s.checkExits(phmSt, symbol, bar)
		if exited {
			phmSt.PositionSide = ""
			phmSt.LastClose = 0
			phmSt.EntryPrice = 0
			return phmSt, []start.Signal{exitSig}, nil
		}
		phmSt.LastClose = bar.Close
	}

	// (g) Entry logic
	if phmSt.PositionSide == "" && phmSt.PendingEntry == "" && phmSt.SignalsToday < phmSt.Config.MaxSignalsPerSess {
		entrySig, entered := s.checkEntry(phmSt, symbol, bar, barET, loc)
		if entered {
			return phmSt, []start.Signal{entrySig}, nil
		}
	}

	// (h) Second entry window: MOC imbalance-driven (15:45-15:55 ET, live only)
	if phmSt.Config.SecondWindowEnabled && phmSt.ImbalanceReceived && !phmSt.SecondWindowUsed &&
		phmSt.PositionSide == "" && phmSt.PendingEntry == "" {

		sw2StartH, sw2StartM := phmParseHHMM(phmSt.Config.SecondWindowStart)
		sw2EndH, sw2EndM := phmParseHHMM(phmSt.Config.SecondWindowEnd)
		sw2Start := time.Date(barET.Year(), barET.Month(), barET.Day(), sw2StartH, sw2StartM, 0, 0, loc)
		sw2End := time.Date(barET.Year(), barET.Month(), barET.Day(), sw2EndH, sw2EndM, 0, 0, loc)

		if !barET.Before(sw2Start) && barET.Before(sw2End) {
			absImbalance := math.Abs(phmSt.LastImbalance)
			if absImbalance >= phmSt.Config.ImbalanceMinShares {
				// Direction from imbalance sign
				var imbDirection string
				var side start.Side
				if phmSt.LastImbalance > 0 {
					imbDirection = "long"
					side = start.SideBuy
				} else {
					imbDirection = "short"
					side = start.SideSell
				}

				// AM direction alignment: if AM return was computed, require same direction
				if phmSt.AMReturnSet && phmSt.AMDirection != imbDirection {
					// Conflicting — skip
				} else {
					phmSt.SecondWindowUsed = true

					// Strength: blend imbalance magnitude and AM return
					imbComponent := math.Min(absImbalance/2_000_000, 1.0) * 0.6
					var amComponent float64
					if phmSt.AMReturnSet {
						amComponent = math.Min(math.Abs(phmSt.AMReturnPct)/1.0, 1.0) * 0.4
					}
					strength := clampStrength(imbComponent + amComponent)

					instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
					tags := map[string]string{
						"ref_price":         fmt.Sprintf("%.10f", bar.Close),
						"trigger":           "moc_imbalance",
						"imbalance":         fmt.Sprintf("%.0f", phmSt.LastImbalance),
						"imbalance_price":   fmt.Sprintf("%.4f", phmSt.LastImbalancePrice),
						"imbalance_dir":     imbDirection,
						"am_direction":      phmSt.AMDirection,
					}

					sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, side, strength, tags)
					if err != nil {
						return phmSt, nil, fmt.Errorf("PHMStrategy.OnBar: moc imbalance signal: %w", err)
					}
					phmSt.PendingEntry = side
					phmSt.SignalsToday++
					return phmSt, []start.Signal{sig}, nil
				}
			}
		}
	}

	return phmSt, nil, nil
}

// checkExits evaluates exit conditions. Only max loss (stop_bps) as safety net.
func (s *PHMStrategy) checkExits(phmSt *PHMState, symbol string, bar start.Bar) (start.Signal, bool) {
	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))

	// Max loss stop
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

	return start.Signal{}, false
}

// checkEntry evaluates Gao et al. intraday momentum entry conditions.
//
// Entry logic:
//  1. Time window: eval_time to eval_time + max_entry_bars*5m
//  2. One-shot evaluation at eval_time: AM direction, VWAP confirmation, volume acceleration
//  3. After evaluation, enter on first qualifying bar with volume + close range filters
func (s *PHMStrategy) checkEntry(phmSt *PHMState, symbol string, bar start.Bar, barET time.Time, loc *time.Location) (start.Signal, bool) {
	// Time window check
	startH, startM := phmParseHHMM(phmSt.Config.AllowedHoursStart)
	endH, endM := phmParseHHMM(phmSt.Config.AllowedHoursEnd)
	windowStart := time.Date(barET.Year(), barET.Month(), barET.Day(), startH, startM, 0, 0, loc)
	windowEnd := time.Date(barET.Year(), barET.Month(), barET.Day(), endH, endM, 0, 0, loc)

	if barET.Before(windowStart) || !barET.Before(windowEnd) {
		return start.Signal{}, false
	}

	// Need session volume data
	if phmSt.SessionBarCount < 1 {
		return start.Signal{}, false
	}

	// --- Day-of-week filter ---
	if len(phmSt.Config.BlockedDaysOfWeek) > 0 {
		dayName := barET.Weekday().String()
		for _, blocked := range phmSt.Config.BlockedDaysOfWeek {
			if strings.EqualFold(blocked, dayName) {
				return start.Signal{}, false
			}
		}
	}

	// --- One-shot evaluation at eval_time ---
	evalH, evalM := phmParseHHMM(phmSt.Config.EvalTime)
	evalTime := time.Date(barET.Year(), barET.Month(), barET.Day(), evalH, evalM, 0, 0, loc)

	if !phmSt.EvalDone && !barET.Before(evalTime) {
		phmSt.EvalDone = true
		phmSt.EvalBarIndex = phmSt.BarCount

		// (a) AM Direction (Gao et al.)
		if !phmSt.AMReturnSet || math.Abs(phmSt.AMReturnPct) < phmSt.Config.AMReturnMinPct {
			return start.Signal{}, false // No signal today
		}

		var direction string
		if phmSt.AMReturnPct > 0 {
			direction = "long"
		} else {
			direction = "short"
		}

		// (b) VWAP confirmation
		vwap := phmSt.Indicators.VWAP
		if vwap > 0 {
			if direction == "long" && bar.Close < vwap {
				return start.Signal{}, false // Conflicting
			}
			if direction == "short" && bar.Close > vwap {
				return start.Signal{}, false // Conflicting
			}
		}

		// (c) Volume acceleration (synthetic MOC proxy)
		if phmSt.AfternoonVolBars > 0 && phmSt.PowerHourVolBars > 0 {
			afternoonAvg := phmSt.AfternoonVolSum / float64(phmSt.AfternoonVolBars)
			powerHourAvg := phmSt.PowerHourVolSum / float64(phmSt.PowerHourVolBars)
			if afternoonAvg > 0 && powerHourAvg/afternoonAvg < phmSt.Config.VolAccelMinRatio {
				return start.Signal{}, false // No institutional acceleration
			}
		}

		// (d) HTF alignment (optional)
		if phmSt.Config.RequireHTFAlign {
			daily, ok := phmSt.Indicators.HTF["1d"]
			if !ok || daily.Bias == "" {
				return start.Signal{}, false
			}
			if direction == "long" && daily.Bias == "BEARISH" {
				return start.Signal{}, false
			}
			if direction == "short" && daily.Bias == "BULLISH" {
				return start.Signal{}, false
			}
		}

		phmSt.EntryDirection = direction
	}

	// After evaluation, enter on first qualifying bar within MaxEntryBars
	if phmSt.EntryDirection == "" {
		return start.Signal{}, false
	}

	barsSinceEval := phmSt.BarCount - phmSt.EvalBarIndex
	if barsSinceEval > phmSt.Config.MaxEntryBars {
		return start.Signal{}, false // Entry window expired
	}

	// Volume confirmation on entry bar
	sessionAvgVol := phmSt.SessionVolSum / float64(phmSt.SessionBarCount)
	if bar.Volume <= phmSt.Config.VolumeMult*sessionAvgVol {
		return start.Signal{}, false
	}

	// Close range filter
	barRange := bar.High - bar.Low
	if barRange == 0 {
		return start.Signal{}, false
	}

	var side start.Side
	if phmSt.EntryDirection == "long" {
		closeRatio := (bar.Close - bar.Low) / barRange
		if closeRatio < (1.0 - phmSt.Config.CloseRangePct) {
			return start.Signal{}, false
		}
		side = start.SideBuy
	} else {
		closeRatio := (bar.High - bar.Close) / barRange
		if closeRatio < (1.0 - phmSt.Config.CloseRangePct) {
			return start.Signal{}, false
		}
		side = start.SideSell
	}

	// Compute strength — blend |AM return| and volume acceleration
	amMagnitude := math.Abs(phmSt.AMReturnPct)
	amComponent := math.Min(amMagnitude/1.0, 1.0) * 0.5 // 1% AM return = max

	var volComponent float64
	if phmSt.AfternoonVolBars > 0 && phmSt.PowerHourVolBars > 0 {
		afternoonAvg := phmSt.AfternoonVolSum / float64(phmSt.AfternoonVolBars)
		if afternoonAvg > 0 {
			volAccelRatio := (phmSt.PowerHourVolSum / float64(phmSt.PowerHourVolBars)) / afternoonAvg
			volComponent = math.Min(volAccelRatio/2.0, 1.0) * 0.5 // 2x accel = max
		}
	}
	strength := clampStrength(amComponent + volComponent)

	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
	tags := map[string]string{
		"ref_price":        fmt.Sprintf("%.10f", bar.Close),
		"trigger":          "gao_intraday_momentum",
		"session_avg_vol":  fmt.Sprintf("%.0f", sessionAvgVol),
		"vwap":             fmt.Sprintf("%.4f", phmSt.Indicators.VWAP),
		"am_return_pct":    fmt.Sprintf("%.4f", phmSt.AMReturnPct),
		"am_direction":     phmSt.AMDirection,
		"entry_direction":  phmSt.EntryDirection,
		"volume_ratio":     fmt.Sprintf("%.2f", bar.Volume/sessionAvgVol),
	}

	sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, side, strength, tags)
	if err != nil {
		return start.Signal{}, false
	}

	phmSt.PendingEntry = side
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
		phmSt.LastImbalance = e.Imbalance
		phmSt.LastImbalancePrice = e.Price
		phmSt.ImbalanceReceived = true
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

	// Update rolling windows
	phmUpdateRollingWindows(phmSt, bar)

	// Update Gao et al. state (session open, AM return, volume zones)
	phmUpdateV2State(phmSt, bar, barET)

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
	phmSt.RecentHighs = phmSt.RecentHighs[:0]
	phmSt.RecentLows = phmSt.RecentLows[:0]
	phmSt.RecentCloses = phmSt.RecentCloses[:0]
	phmSt.RecentVolumes = phmSt.RecentVolumes[:0]
	phmSt.PositionSide = ""
	phmSt.PendingEntry = ""
	phmSt.LastClose = 0
	phmSt.EntryPrice = 0

	// Gao et al. state
	phmSt.SessionOpenPrice = 0
	phmSt.AMReturnPct = 0
	phmSt.AMReturnSet = false
	phmSt.AMDirection = ""
	phmSt.AfternoonVolSum = 0
	phmSt.AfternoonVolBars = 0
	phmSt.PowerHourVolSum = 0
	phmSt.PowerHourVolBars = 0
	phmSt.EvalDone = false
	phmSt.EntryDirection = ""
	phmSt.EvalBarIndex = 0

	// MOC imbalance second window
	phmSt.LastImbalance = 0
	phmSt.LastImbalancePrice = 0
	phmSt.ImbalanceReceived = false
	phmSt.SecondWindowUsed = false
}

// ---------------------------------------------------------------------------
// Rolling Window Update Helper
// ---------------------------------------------------------------------------

// phmUpdateRollingWindows maintains the lookback rolling windows.
func phmUpdateRollingWindows(phmSt *PHMState, bar start.Bar) {
	lb := phmSt.Config.LookbackBars
	if lb < 1 {
		lb = 12
	}
	phmSt.RecentHighs = append(phmSt.RecentHighs, bar.High)
	if len(phmSt.RecentHighs) > lb {
		phmSt.RecentHighs = phmSt.RecentHighs[len(phmSt.RecentHighs)-lb:]
	}
	phmSt.RecentLows = append(phmSt.RecentLows, bar.Low)
	if len(phmSt.RecentLows) > lb {
		phmSt.RecentLows = phmSt.RecentLows[len(phmSt.RecentLows)-lb:]
	}
	phmSt.RecentCloses = append(phmSt.RecentCloses, bar.Close)
	if len(phmSt.RecentCloses) > lb {
		phmSt.RecentCloses = phmSt.RecentCloses[len(phmSt.RecentCloses)-lb:]
	}
	phmSt.RecentVolumes = append(phmSt.RecentVolumes, bar.Volume)
	if len(phmSt.RecentVolumes) > lb {
		phmSt.RecentVolumes = phmSt.RecentVolumes[len(phmSt.RecentVolumes)-lb:]
	}
	phmSt.BarCount++
}

// ---------------------------------------------------------------------------
// v2 State Update Helper — Gao et al. Intraday Momentum
// ---------------------------------------------------------------------------

// phmUpdateV2State tracks session open, AM return, and volume zone accumulation.
// Called from both OnBar and ReplayOnBar.
func phmUpdateV2State(phmSt *PHMState, bar start.Bar, barET time.Time) {
	h := barET.Hour()
	m := barET.Minute()

	// Track session open (first bar at/after 9:30 ET)
	if phmSt.SessionOpenPrice == 0 && (h > 9 || (h == 9 && m >= 30)) {
		phmSt.SessionOpenPrice = bar.Close
	}

	// Compute AM return at 10:00 ET (once per day)
	if !phmSt.AMReturnSet && h >= 10 {
		if phmSt.SessionOpenPrice > 0 {
			phmSt.AMReturnPct = (bar.Close - phmSt.SessionOpenPrice) / phmSt.SessionOpenPrice * 100
			phmSt.AMReturnSet = true
			if phmSt.AMReturnPct > 0 {
				phmSt.AMDirection = "long"
			} else {
				phmSt.AMDirection = "short"
			}
		}
	}

	// Accumulate afternoon volume baseline (14:00-15:00 ET)
	if h == 14 {
		phmSt.AfternoonVolSum += bar.Volume
		phmSt.AfternoonVolBars++
	}

	// Accumulate power hour volume (15:00-15:30 ET)
	if h == 15 && m < 30 {
		phmSt.PowerHourVolSum += bar.Volume
		phmSt.PowerHourVolBars++
	}
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
