// Package builtin — Power Hour Momentum (PHM) strategy.
// Captures institutional MOC flows in the 14:30-15:50 ET window.
package builtin

import (
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// PHMConfig holds tunable parameters for the Power Hour Momentum strategy.
type PHMConfig struct {
	LookbackBars       int     // default 12 (60min of 5m bars)
	VolumeMult         float64 // default 1.5
	CloseRangePct      float64 // default 0.30
	ATRTrailMult       float64 // default 1.5
	StopBPS            int     // default 60
	MaxSignalsPerSess  int     // default 2
	StagnationBars     int     // default 3
	StagnationSDThresh float64 // default 0.3
	LimitOffsetBPS     int     // default 5
	AllowedHoursStart  string  // default "14:30"
	AllowedHoursEnd    string  // default "15:50"
	AllowedHoursTZ     string  // default "America/New_York"
	EODFlattenTime     string  // default "15:55"
	HTFBiasEnabled     bool    // default false
}

// NewPHMConfigFromDNA reads PHMConfig from DNA params with sensible defaults.
func NewPHMConfigFromDNA(params map[string]any) PHMConfig {
	return PHMConfig{
		LookbackBars:       phmIntParam(params, "lookback_bars", 12),
		VolumeMult:         phmFloatParam(params, "volume_mult", 1.5),
		CloseRangePct:      phmFloatParam(params, "close_range_pct", 0.30),
		ATRTrailMult:       phmFloatParam(params, "atr_trail_mult", 1.5),
		StopBPS:            phmIntParam(params, "stop_bps", 60),
		MaxSignalsPerSess:  phmIntParam(params, "max_signals_per_session", 2),
		StagnationBars:     phmIntParam(params, "stagnation_bars", 3),
		StagnationSDThresh: phmFloatParam(params, "stagnation_sd_thresh", 0.3),
		LimitOffsetBPS:     phmIntParam(params, "limit_offset_bps", 5),
		AllowedHoursStart:  phmStringParam(params, "allowed_hours_start", "14:30"),
		AllowedHoursEnd:    phmStringParam(params, "allowed_hours_end", "15:50"),
		AllowedHoursTZ:     phmStringParam(params, "allowed_hours_tz", "America/New_York"),
		EODFlattenTime:     phmStringParam(params, "eod_flatten_time", "15:55"),
		HTFBiasEnabled:     phmBoolParam(params, "htf_bias_enabled", false),
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

	// Position/exit management
	PositionSide    start.Side
	PendingEntry    start.Side
	EntryPrice      float64
	TrailStop       float64
	StagnationCount int
	LastClose       float64

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
	Symbol          string              `json:"symbol"`
	Config          PHMConfig           `json:"config"`
	Indicators      start.IndicatorData `json:"indicators"`
	Timeframe       string              `json:"timeframe"`
	RecentHighs     []float64           `json:"recent_highs"`
	RecentLows      []float64           `json:"recent_lows"`
	RecentCloses    []float64           `json:"recent_closes"`
	RecentVolumes   []float64           `json:"recent_volumes"`
	BarCount        int                 `json:"bar_count"`
	SessionDate     string              `json:"session_date"`
	SignalsToday    int                 `json:"signals_today"`
	SessionVolSum   float64             `json:"session_vol_sum"`
	SessionBarCount int                 `json:"session_bar_count"`
	PositionSide    start.Side          `json:"position_side"`
	PendingEntry    start.Side          `json:"pending_entry"`
	EntryPrice      float64             `json:"entry_price"`
	TrailStop       float64             `json:"trail_stop"`
	StagnationCount int                 `json:"stagnation_count"`
	LastClose       float64             `json:"last_close"`
}

// Marshal serializes PHMState for persistence/recovery.
func (s *PHMState) Marshal() ([]byte, error) {
	j := phmStateJSON{
		Symbol:          s.Symbol,
		Config:          s.Config,
		Indicators:      s.Indicators,
		Timeframe:       s.Timeframe,
		RecentHighs:     s.RecentHighs,
		RecentLows:      s.RecentLows,
		RecentCloses:    s.RecentCloses,
		RecentVolumes:   s.RecentVolumes,
		BarCount:        s.BarCount,
		SessionDate:     s.SessionDate,
		SignalsToday:    s.SignalsToday,
		SessionVolSum:   s.SessionVolSum,
		SessionBarCount: s.SessionBarCount,
		PositionSide:    s.PositionSide,
		PendingEntry:    s.PendingEntry,
		EntryPrice:      s.EntryPrice,
		TrailStop:       s.TrailStop,
		StagnationCount: s.StagnationCount,
		LastClose:       s.LastClose,
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
	s.PositionSide = j.PositionSide
	s.PendingEntry = j.PendingEntry
	s.EntryPrice = j.EntryPrice
	s.TrailStop = j.TrailStop
	s.StagnationCount = j.StagnationCount
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
			Description: "Captures institutional MOC flows in the 14:30-15:50 ET window",
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
			st.PositionSide = phmPrior.PositionSide
			st.PendingEntry = phmPrior.PendingEntry
			st.EntryPrice = phmPrior.EntryPrice
			st.TrailStop = phmPrior.TrailStop
			st.StagnationCount = phmPrior.StagnationCount
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

	// (b) Session reset
	if barDate != phmSt.SessionDate {
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
		phmSt.TrailStop = 0
		phmSt.StagnationCount = 0
		phmSt.LastClose = 0
		phmSt.EntryPrice = 0
	}

	// (c) Update session stats
	phmSt.SessionBarCount++
	phmSt.SessionVolSum += bar.Volume

	// (d) Update rolling windows
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

	// (f) EOD flatten
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
		phmSt.TrailStop = 0
		phmSt.StagnationCount = 0
		phmSt.LastClose = 0
		phmSt.EntryPrice = 0
		return phmSt, []start.Signal{sig}, nil
	}

	// (g) Exit logic
	if phmSt.PositionSide != "" && phmSt.PendingEntry == "" {
		exitSig, exited := s.checkExits(phmSt, symbol, bar)
		if exited {
			phmSt.PositionSide = ""
			phmSt.TrailStop = 0
			phmSt.StagnationCount = 0
			phmSt.LastClose = 0
			phmSt.EntryPrice = 0
			return phmSt, []start.Signal{exitSig}, nil
		}
		phmSt.LastClose = bar.Close
	}

	// (h) Entry logic
	if phmSt.PositionSide == "" && phmSt.PendingEntry == "" && phmSt.SignalsToday < phmSt.Config.MaxSignalsPerSess {
		entrySig, entered := s.checkEntry(phmSt, symbol, bar, barET, loc)
		if entered {
			return phmSt, []start.Signal{entrySig}, nil
		}
	}

	return phmSt, nil, nil
}

// checkExits evaluates all exit conditions. Returns (signal, true) if exit triggered.
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

	// Trailing stop
	if phmSt.PositionSide == start.SideBuy {
		if bar.Close < phmSt.TrailStop {
			sig, _ := start.NewSignal(instanceID, symbol, start.SignalExit, phmSt.PositionSide, 1.0, map[string]string{
				"reason": "trail_stop",
			})
			return sig, true
		}
		// Ratchet up
		newTrail := bar.Close - phmSt.Config.ATRTrailMult*phmSt.Indicators.ATR
		if newTrail > phmSt.TrailStop {
			phmSt.TrailStop = newTrail
		}
	} else {
		if bar.Close > phmSt.TrailStop {
			sig, _ := start.NewSignal(instanceID, symbol, start.SignalExit, phmSt.PositionSide, 1.0, map[string]string{
				"reason": "trail_stop",
			})
			return sig, true
		}
		// Ratchet down
		newTrail := bar.Close + phmSt.Config.ATRTrailMult*phmSt.Indicators.ATR
		if newTrail < phmSt.TrailStop {
			phmSt.TrailStop = newTrail
		}
	}

	// Stagnation
	if phmSt.LastClose != 0 && phmSt.Indicators.VWAPSD > 0 {
		if math.Abs(bar.Close-phmSt.LastClose) < phmSt.Config.StagnationSDThresh*phmSt.Indicators.VWAPSD {
			phmSt.StagnationCount++
			if phmSt.StagnationCount >= phmSt.Config.StagnationBars {
				sig, _ := start.NewSignal(instanceID, symbol, start.SignalExit, phmSt.PositionSide, 1.0, map[string]string{
					"reason": "stagnation",
				})
				return sig, true
			}
		} else {
			phmSt.StagnationCount = 0
		}
	}

	return start.Signal{}, false
}

// checkEntry evaluates entry conditions. Returns (signal, true) if entry triggered.
func (s *PHMStrategy) checkEntry(phmSt *PHMState, symbol string, bar start.Bar, barET time.Time, loc *time.Location) (start.Signal, bool) {
	// Time window check
	startH, startM := phmParseHHMM(phmSt.Config.AllowedHoursStart)
	endH, endM := phmParseHHMM(phmSt.Config.AllowedHoursEnd)
	windowStart := time.Date(barET.Year(), barET.Month(), barET.Day(), startH, startM, 0, 0, loc)
	windowEnd := time.Date(barET.Year(), barET.Month(), barET.Day(), endH, endM, 0, 0, loc)

	if barET.Before(windowStart) || !barET.Before(windowEnd) {
		return start.Signal{}, false
	}

	lb := phmSt.Config.LookbackBars
	if lb < 1 {
		lb = 12
	}

	// Need enough bars
	if len(phmSt.RecentHighs) < lb {
		return start.Signal{}, false
	}

	// Need session volume data
	if phmSt.SessionBarCount < 1 {
		return start.Signal{}, false
	}

	// VWAP bias
	longOnly := false
	shortOnly := false
	switch {
	case bar.Close > phmSt.Indicators.VWAP:
		longOnly = true
	case bar.Close < phmSt.Indicators.VWAP:
		shortOnly = true
	default:
		return start.Signal{}, false // At VWAP — no signal.
	}

	// Session average volume
	sessionAvgVol := phmSt.SessionVolSum / float64(phmSt.SessionBarCount)

	// Volume filter
	if bar.Volume <= phmSt.Config.VolumeMult*sessionAvgVol {
		return start.Signal{}, false
	}

	// 60-min high/low breakout check (excluding current bar)
	n := len(phmSt.RecentHighs)
	priorHighs := phmSt.RecentHighs[:n-1]
	priorLows := phmSt.RecentLows[:n-1]

	rollingHigh := phmSliceMax(priorHighs)
	rollingLow := phmSliceMin(priorLows)

	isNewHigh := bar.High > rollingHigh
	isNewLow := bar.Low < rollingLow

	// Close range filter
	barRange := bar.High - bar.Low
	if barRange == 0 {
		return start.Signal{}, false
	}

	var side start.Side
	trigger := ""

	switch {
	case longOnly && isNewHigh:
		// Close in top portion
		closeRatio := (bar.Close - bar.Low) / barRange
		if closeRatio < (1.0 - phmSt.Config.CloseRangePct) {
			return start.Signal{}, false
		}
		side = start.SideBuy
		trigger = "60m_high"
	case shortOnly && isNewLow:
		// Close in bottom portion
		closeRatio := (bar.High - bar.Close) / barRange
		if closeRatio < (1.0 - phmSt.Config.CloseRangePct) {
			return start.Signal{}, false
		}
		side = start.SideSell
		trigger = "60m_low"
	default:
		return start.Signal{}, false
	}

	// HTF bias filter (optional)
	if phmSt.Config.HTFBiasEnabled {
		daily, ok := phmSt.Indicators.HTF["1d"]
		if !ok || daily.Bias == "" {
			return start.Signal{}, false
		}
		if side == start.SideBuy && daily.Bias == "BEARISH" {
			return start.Signal{}, false
		}
		if side == start.SideSell && daily.Bias == "BULLISH" {
			return start.Signal{}, false
		}
	}

	// Compute strength
	volumeRatio := bar.Volume / sessionAvgVol
	strength := clampStrength(math.Min(volumeRatio/3.0, 1.0))

	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
	tags := map[string]string{
		"ref_price":       fmt.Sprintf("%.10f", bar.Close),
		"trigger":         trigger,
		"session_avg_vol": fmt.Sprintf("%.0f", sessionAvgVol),
		"vwap":            fmt.Sprintf("%.4f", phmSt.Indicators.VWAP),
		"volume_ratio":    fmt.Sprintf("%.2f", volumeRatio),
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
		atr := phmSt.Indicators.ATR
		if e.Side == start.SideBuy {
			phmSt.TrailStop = e.Price - phmSt.Config.ATRTrailMult*atr
		} else {
			phmSt.TrailStop = e.Price + phmSt.Config.ATRTrailMult*atr
		}
		phmSt.PendingEntry = ""
		phmSt.StagnationCount = 0
		phmSt.LastClose = 0
		return phmSt, nil, nil

	case start.EntryRejection:
		_ = e
		phmSt.PendingEntry = ""
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
		phmSt.TrailStop = 0
		phmSt.StagnationCount = 0
		phmSt.LastClose = 0
		phmSt.EntryPrice = 0
	}

	// Update session stats
	phmSt.SessionBarCount++
	phmSt.SessionVolSum += bar.Volume

	// Update rolling windows
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

	return phmSt, nil
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

// phmSliceMax returns the maximum value in a float64 slice.
func phmSliceMax(s []float64) float64 {
	if len(s) == 0 {
		return math.Inf(-1)
	}
	m := s[0]
	for _, v := range s[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

// phmSliceMin returns the minimum value in a float64 slice.
func phmSliceMin(s []float64) float64 {
	if len(s) == 0 {
		return math.Inf(1)
	}
	m := s[0]
	for _, v := range s[1:] {
		if v < m {
			m = v
		}
	}
	return m
}
