package builtin

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// ConfluenceResult holds the computed confluence score and contributing factors.
type ConfluenceResult struct {
	Score   int
	Factors []string
}

// BollingerMACDStrategy implements the Trading Rush MACD crossover
// strategy with configurable entry confirmation filters.
//
// Entry (long): MACD line crosses above signal line (within zero band),
// close > 9 EMA, close > 200 MA, with optional directional close,
// ADX, VWAP, body ratio, and signal quality scoring filters.
// Stop: below swing low. Target: R:R × stop distance.
type BollingerMACDStrategy struct {
	meta start.Meta
}

func NewBollingerMACDStrategy() *BollingerMACDStrategy {
	id, _ := start.NewStrategyID("bollinger_macd")
	ver, _ := start.NewVersion("2.0.0")
	return &BollingerMACDStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "BB+MACD Confluence (Trading Rush)",
			Description: "30m Bollinger Band breakout + MACD histogram with 1.5x R:R dynamic target",
			Author:      "system",
		},
	}
}

func (s *BollingerMACDStrategy) Meta() start.Meta { return s.meta }
func (s *BollingerMACDStrategy) WarmupBars() int  { return 30 }

// BMConfig holds all DNA parameters parsed from TOML [params].
type BMConfig struct {
	MACDZeroBand        float64 // crossover must occur with MACD < this value (0=below zero, >0=relaxed band)
	RiskRewardRatio     float64 // target = entry + ratio * (entry - stop)
	SwingLookback       int     // bars to look back for swing low/high
	VolumeMult          float64
	AllowedHoursStart   string
	AllowedHoursEnd     string
	AllowedHoursTZ      string
	CooldownSeconds     int
	MaxTradesPerDay     int
	StabilizationBars   int
	MinConfluenceScore int // minimum confluence score (0-100) for entry. Default 0 (disabled).
}

// BMState holds per-symbol state.
type BMState struct {
	Symbol         string              `json:"symbol"`
	Indicators     start.IndicatorData `json:"-"`
	Config         BMConfig            `json:"config"`
	CalcBarCount   int                 `json:"calcBarCount"`
	PositionSide   start.Side          `json:"positionSide,omitempty"`
	PendingEntry   start.Side          `json:"pendingEntry,omitempty"`
	PendingEntryAt time.Time           `json:"pendingEntryAt,omitzero"`
	TradesToday    int                 `json:"tradesToday"`
	CooldownUntil  time.Time           `json:"cooldownUntil,omitzero"`
	PrevMACDHist    float64             `json:"prevMACDHist"`
	PrevMACDLine    float64             `json:"prevMACDLine"`
	PrevMACDSignalL float64             `json:"prevMACDSignalL"` // previous MACD signal line value
	PrevMACDHists   []float64           `json:"prevMACDHists,omitempty"` // rolling histogram window for acceleration
	LastTradeDate   string              `json:"lastTradeDate,omitempty"`

	// Position tracking for 1.5x R:R exit
	EntryPrice  float64 `json:"entryPrice,omitempty"`
	StopPrice   float64 `json:"stopPrice,omitempty"`
	TargetPrice float64 `json:"targetPrice,omitempty"`

	// Rolling bar window for swing detection
	RecentLows  []float64 `json:"recentLows,omitempty"`
	RecentHighs []float64 `json:"recentHighs,omitempty"`

	// Debug gate counters (reset daily)
	GateStabilization int `json:"-"`
	GateCooldown      int `json:"-"`
	GateHours         int `json:"-"`
	GateRegime        int `json:"-"`
	GatePosition      int `json:"-"`
	GateNoSetup       int `json:"-"`
	GatePassedAll     int `json:"-"`
	DebugBarCount     int `json:"-"`
}

// SetIndicators implements the indicatorSetter interface used by the runner.
func (st *BMState) SetIndicators(ind start.IndicatorData) {
	st.Indicators = ind
}

func (st *BMState) Marshal() ([]byte, error)   { return json.Marshal(st) }
func (st *BMState) Unmarshal(data []byte) error { return json.Unmarshal(data, st) }

func parseBMConfig(params map[string]any) BMConfig {
	return BMConfig{
		MACDZeroBand:        getFloat64(params, "macd_zero_band", 0.0),
		RiskRewardRatio:     getFloat64(params, "risk_reward_ratio", 1.5),
		SwingLookback:       getInt(params, "swing_lookback", 10),
		VolumeMult:          getFloat64(params, "volume_mult", 1.0),
		AllowedHoursStart:   getString(params, "allowed_hours_start", "09:35"),
		AllowedHoursEnd:     getString(params, "allowed_hours_end", "15:45"),
		AllowedHoursTZ:      getString(params, "allowed_hours_tz", "America/New_York"),
		CooldownSeconds:     getInt(params, "cooldown_seconds", 1800),
		MaxTradesPerDay:     getInt(params, "max_trades_per_day", 2),
		StabilizationBars:   getInt(params, "stabilization_bars", 30),
		MinConfluenceScore: getInt(params, "min_confluence_score", 0),
	}
}

func (s *BollingerMACDStrategy) Init(_ start.Context, symbol string, params map[string]any, prior start.State) (start.State, error) {
	cfg := parseBMConfig(params)
	st := &BMState{
		Symbol: symbol,
		Config: cfg,
	}
	if prior != nil {
		if bmPrior, ok := prior.(*BMState); ok {
			st = bmPrior
			st.Config = cfg
		}
	}
	return st, nil
}

func (s *BollingerMACDStrategy) OnBar(ctx start.Context, symbol string, bar start.Bar, st start.State) (start.State, []start.Signal, error) {
	bmSt, ok := st.(*BMState)
	if !ok {
		return st, nil, fmt.Errorf("BollingerMACDStrategy.OnBar: expected *BMState, got %T", st)
	}
	cfg := bmSt.Config
	ind := bmSt.Indicators

	now := bar.Time
	if ctx != nil {
		now = ctx.Now()
	}

	bmSt.CalcBarCount++

	// Update rolling bar windows for swing detection.
	bmSt.RecentLows = append(bmSt.RecentLows, bar.Low)
	if len(bmSt.RecentLows) > cfg.SwingLookback {
		bmSt.RecentLows = bmSt.RecentLows[len(bmSt.RecentLows)-cfg.SwingLookback:]
	}
	bmSt.RecentHighs = append(bmSt.RecentHighs, bar.High)
	if len(bmSt.RecentHighs) > cfg.SwingLookback {
		bmSt.RecentHighs = bmSt.RecentHighs[len(bmSt.RecentHighs)-cfg.SwingLookback:]
	}

	// Daily reset.
	todayStr := now.Format("2006-01-02")
	if bmSt.LastTradeDate != todayStr {
		if ctx != nil && bmSt.LastTradeDate != "" && bmSt.DebugBarCount > 0 {
			ctx.Logger().Info("BB+MACD daily gate summary",
				"symbol", symbol, "date", bmSt.LastTradeDate,
				"bars", bmSt.DebugBarCount,
				"stab", bmSt.GateStabilization, "cd", bmSt.GateCooldown,
				"hrs", bmSt.GateHours, "regime", bmSt.GateRegime,
				"pos", bmSt.GatePosition, "nosetup", bmSt.GateNoSetup,
				"passed", bmSt.GatePassedAll)
		}
		bmSt.TradesToday = 0
		bmSt.CooldownUntil = time.Time{}
		bmSt.LastTradeDate = todayStr
		bmSt.GateStabilization = 0
		bmSt.GateCooldown = 0
		bmSt.GateHours = 0
		bmSt.GateRegime = 0
		bmSt.GatePosition = 0
		bmSt.GateNoSetup = 0
		bmSt.GatePassedAll = 0
		bmSt.DebugBarCount = 0
	}
	bmSt.DebugBarCount++

	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))

	// ─── EXIT EVALUATION (while in position) ─────────────────────────
	if bmSt.PositionSide != "" {
		var exitSig *start.Signal

		switch bmSt.PositionSide {
		case start.SideBuy:
			// Long exit: hit target or stop
			if bmSt.TargetPrice > 0 && bar.High >= bmSt.TargetPrice {
				sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideSell, 0.9, map[string]string{
					"setup":     "bb_macd_target_hit",
					"ref_price": fmt.Sprintf("%.10f", bmSt.TargetPrice),
					"reason":    fmt.Sprintf("1.5R target: entry=%.2f stop=%.2f target=%.2f", bmSt.EntryPrice, bmSt.StopPrice, bmSt.TargetPrice),
				})
				if err == nil {
					exitSig = &sig
				}
			} else if bmSt.StopPrice > 0 && bar.Low <= bmSt.StopPrice {
				sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideSell, 0.9, map[string]string{
					"setup":     "bb_macd_stop_hit",
					"ref_price": fmt.Sprintf("%.10f", bmSt.StopPrice),
					"reason":    fmt.Sprintf("swing stop: entry=%.2f stop=%.2f", bmSt.EntryPrice, bmSt.StopPrice),
				})
				if err == nil {
					exitSig = &sig
				}
			}
		case start.SideSell:
			// Short exit: hit target or stop
			if bmSt.TargetPrice > 0 && bar.Low <= bmSt.TargetPrice {
				sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideBuy, 0.9, map[string]string{
					"setup":     "bb_macd_target_hit",
					"ref_price": fmt.Sprintf("%.10f", bmSt.TargetPrice),
					"reason":    fmt.Sprintf("1.5R target: entry=%.2f stop=%.2f target=%.2f", bmSt.EntryPrice, bmSt.StopPrice, bmSt.TargetPrice),
				})
				if err == nil {
					exitSig = &sig
				}
			} else if bmSt.StopPrice > 0 && bar.High >= bmSt.StopPrice {
				sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideBuy, 0.9, map[string]string{
					"setup":     "bb_macd_stop_hit",
					"ref_price": fmt.Sprintf("%.10f", bmSt.StopPrice),
					"reason":    fmt.Sprintf("swing stop: entry=%.2f stop=%.2f", bmSt.EntryPrice, bmSt.StopPrice),
				})
				if err == nil {
					exitSig = &sig
				}
			}
		}

		if exitSig != nil {
			bmSt.PositionSide = ""
			bmSt.EntryPrice = 0
			bmSt.StopPrice = 0
			bmSt.TargetPrice = 0
			cooldown := time.Duration(cfg.CooldownSeconds) * time.Second
			bmSt.CooldownUntil = now.Add(cooldown)
			if ctx != nil {
				ctx.Logger().Info("BB+MACD EXIT", "symbol", symbol, "setup", exitSig.Tags["setup"],
					"reason", exitSig.Tags["reason"])
			}
			bmSt.PrevMACDHist = ind.MACDHistogram
			return bmSt, []start.Signal{*exitSig}, nil
		}

		bmSt.PrevMACDHist = ind.MACDHistogram
		return bmSt, nil, nil
	}

	// ─── ENTRY GATES (only when flat) ─────────────────────────────────

	// Gate 1: Stabilization.
	if bmSt.CalcBarCount < cfg.StabilizationBars {
		bmSt.GateStabilization++
		bmSt.PrevMACDHist = ind.MACDHistogram
		return bmSt, nil, nil
	}

	// Gate 2: Pending entry timeout (5 min).
	if bmSt.PendingEntry != "" && now.Sub(bmSt.PendingEntryAt) > 5*time.Minute {
		bmSt.PendingEntry = ""
		bmSt.PendingEntryAt = time.Time{}
	}
	if bmSt.PendingEntry != "" {
		bmSt.GatePosition++
		bmSt.PrevMACDHist = ind.MACDHistogram
		return bmSt, nil, nil
	}

	// Gate 3: Cooldown + max trades.
	if now.Before(bmSt.CooldownUntil) || bmSt.TradesToday >= cfg.MaxTradesPerDay {
		bmSt.GateCooldown++
		bmSt.PrevMACDHist = ind.MACDHistogram
		return bmSt, nil, nil
	}

	// Gate 4: Trading hours.
	if cfg.AllowedHoursStart != "" && cfg.AllowedHoursEnd != "" {
		loc, err := time.LoadLocation(cfg.AllowedHoursTZ)
		if err == nil {
			hhmm := now.In(loc).Format("15:04")
			if hhmm < cfg.AllowedHoursStart || hhmm >= cfg.AllowedHoursEnd {
				bmSt.GateHours++
				bmSt.PrevMACDHist = ind.MACDHistogram
				return bmSt, nil, nil
			}
		}
	}

	// Gate 5: Regime — price must be above 9 EMA (Trading Rush filter).
	// On 30m bars, EMA9 = ~4.5 hour EMA, acting as intraday trend filter.
	// For daily regime, we also check HTF bias.
	priceAboveEMA9 := ind.EMA9 > 0 && bar.Close > ind.EMA9
	priceBelowEMA9 := ind.EMA9 > 0 && bar.Close < ind.EMA9
	if !priceAboveEMA9 && !priceBelowEMA9 {
		bmSt.GateRegime++
		bmSt.PrevMACDHist = ind.MACDHistogram
		return bmSt, nil, nil
	}

	// ─── ENTRY EVALUATION ─────────────────────────────────────────────

	// Volume confirmation
	volumeOK := cfg.VolumeMult <= 1.0 || ind.VolumeSMA <= 0 || bar.Volume > ind.VolumeSMA*cfg.VolumeMult

	// 200 MA filter (Trading Rush: price above 200 MA on chart timeframe)
	ema200 := ind.EMA200
	if ema200 <= 0 {
		// Fallback to HTF daily EMA200
		if htf, ok := ind.HTF["1d"]; ok {
			ema200 = htf.EMA200
		}
	}

	// HTF bias
	htfBias := ""
	if htf, ok := ind.HTF["1d"]; ok {
		htfBias = htf.Bias
	}

	var sig *start.Signal

	// MACD crossover detection (for macd_only mode):
	// Trading Rush: MACD line crosses above signal line, crossover below zero line
	macdCrossUp := bmSt.PrevMACDLine < bmSt.PrevMACDSignalL && ind.MACDLine > ind.MACDSignal
	macdCrossDown := bmSt.PrevMACDLine > bmSt.PrevMACDSignalL && ind.MACDLine < ind.MACDSignal

	// ── LONG ENTRY ──
	longOK := priceAboveEMA9 &&
		(ema200 <= 0 || bar.Close > ema200) &&
		volumeOK &&
		htfBias != "BEARISH"

	if longOK {
		// MACD crossover: line crosses above signal, within zero band
		longTrigger := macdCrossUp && ind.MACDLine < cfg.MACDZeroBand
		setup := "macd_long"

		if longTrigger {
			// Confluence scoring — replaces individual boolean filters
			conf := ComputeConfluenceScore(bar, ind, bmSt.PrevMACDHists, true)
			if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
				longTrigger = false
			}

			if longTrigger {
				swingLow := swingMin(bmSt.RecentLows)
				if swingLow > 0 && swingLow < bar.Close {
					stopDist := bar.Close - swingLow
					target := bar.Close + cfg.RiskRewardRatio*stopDist
					strength := float64(conf.Score) / 100.0
					if strength < 0.1 {
						strength = 0.1 // minimum strength floor
					}
					s, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideBuy, strength, map[string]string{
						"setup":             setup,
						"bb_percent_b":      fmt.Sprintf("%.4f", ind.BBPercentB),
						"macd_line":         fmt.Sprintf("%.6f", ind.MACDLine),
						"macd_signal":       fmt.Sprintf("%.6f", ind.MACDSignal),
						"macd_hist":         fmt.Sprintf("%.6f", ind.MACDHistogram),
						"ref_price":         fmt.Sprintf("%.10f", bar.Close),
						"stop_price":        fmt.Sprintf("%.4f", swingLow),
						"target_price":      fmt.Sprintf("%.4f", target),
						"rr_ratio":          fmt.Sprintf("%.1f", cfg.RiskRewardRatio),
						"stop_bps":          fmt.Sprintf("%.0f", stopDist/bar.Close*10000),
						"confluence":        fmt.Sprintf("%d", conf.Score),
						"confluence_detail": strings.Join(conf.Factors, "+"),
					})
					if err == nil {
						sig = &s
						bmSt.StopPrice = swingLow
						bmSt.TargetPrice = target
					}
				}
			}
		}
	}

	// ── SHORT ENTRY ──
	shortOK := sig == nil && priceBelowEMA9 &&
		(ema200 <= 0 || bar.Close < ema200) &&
		volumeOK &&
		htfBias != "BULLISH"

	if shortOK {
		// MACD crossover: line crosses below signal, within negative zero band
		shortTrigger := macdCrossDown && ind.MACDLine > -cfg.MACDZeroBand
		setup := "macd_short"

		if shortTrigger {
			conf := ComputeConfluenceScore(bar, ind, bmSt.PrevMACDHists, false)
			if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
				shortTrigger = false
			}

			if shortTrigger {
				swingHigh := swingMax(bmSt.RecentHighs)
				if swingHigh > 0 && swingHigh > bar.Close {
					stopDist := swingHigh - bar.Close
					target := bar.Close - cfg.RiskRewardRatio*stopDist
					strength := float64(conf.Score) / 100.0
					if strength < 0.1 {
						strength = 0.1
					}
					s, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideSell, strength, map[string]string{
						"setup":             setup,
						"bb_percent_b":      fmt.Sprintf("%.4f", ind.BBPercentB),
						"macd_line":         fmt.Sprintf("%.6f", ind.MACDLine),
						"macd_signal":       fmt.Sprintf("%.6f", ind.MACDSignal),
						"macd_hist":         fmt.Sprintf("%.6f", ind.MACDHistogram),
						"ref_price":         fmt.Sprintf("%.10f", bar.Close),
						"stop_price":        fmt.Sprintf("%.4f", swingHigh),
						"target_price":      fmt.Sprintf("%.4f", target),
						"rr_ratio":          fmt.Sprintf("%.1f", cfg.RiskRewardRatio),
						"stop_bps":          fmt.Sprintf("%.0f", stopDist/bar.Close*10000),
						"confluence":        fmt.Sprintf("%d", conf.Score),
						"confluence_detail": strings.Join(conf.Factors, "+"),
					})
					if err == nil {
						sig = &s
						bmSt.StopPrice = swingHigh
						bmSt.TargetPrice = target
					}
				}
			}
		}
	}

	bmSt.PrevMACDHist = ind.MACDHistogram
	bmSt.PrevMACDLine = ind.MACDLine
	bmSt.PrevMACDSignalL = ind.MACDSignal
	bmSt.PrevMACDHists = append(bmSt.PrevMACDHists, ind.MACDHistogram)
	if len(bmSt.PrevMACDHists) > 10 {
		bmSt.PrevMACDHists = bmSt.PrevMACDHists[len(bmSt.PrevMACDHists)-10:]
	}

	if sig != nil {
		bmSt.GatePassedAll++
		bmSt.PendingEntry = sig.Side
		bmSt.PendingEntryAt = now
		bmSt.TradesToday++
		if ctx != nil {
			ctx.Logger().Info("BB+MACD SIGNAL",
				"symbol", symbol, "side", sig.Side,
				"bb_pctb", ind.BBPercentB, "macd_hist", ind.MACDHistogram,
				"stop", bmSt.StopPrice, "target", bmSt.TargetPrice)
		}
		return bmSt, []start.Signal{*sig}, nil
	}

	bmSt.GateNoSetup++
	return bmSt, nil, nil
}

func (s *BollingerMACDStrategy) OnEvent(ctx start.Context, _ string, evt any, st start.State) (start.State, []start.Signal, error) {
	bmSt, ok := st.(*BMState)
	if !ok {
		return st, nil, nil
	}
	switch e := evt.(type) {
	case start.FillConfirmation:
		switch {
		case bmSt.PendingEntry != "":
			// Entry fill — use the pending direction, not the fill side
			// (options fills are always BUY for opening, regardless of direction).
			bmSt.PositionSide = bmSt.PendingEntry
			bmSt.PendingEntry = ""
			bmSt.PendingEntryAt = time.Time{}
		case bmSt.PositionSide != "":
			// Exit fill — flat now.
			bmSt.PositionSide = ""
			bmSt.EntryPrice = 0
			bmSt.StopPrice = 0
			bmSt.TargetPrice = 0
		default:
			// Unexpected fill — reset to flat.
			bmSt.PositionSide = ""
			bmSt.PendingEntry = ""
			bmSt.PendingEntryAt = time.Time{}
			bmSt.EntryPrice = 0
			bmSt.StopPrice = 0
			bmSt.TargetPrice = 0
		}
		if ctx != nil {
			ctx.Logger().Info("BB+MACD Fill",
				"symbol", e.Symbol, "side", e.Side, "price", e.Price,
				"position", bmSt.PositionSide, "entry", bmSt.EntryPrice,
				"stop", bmSt.StopPrice, "target", bmSt.TargetPrice)
		}
	case start.EntryRejection:
		bmSt.PendingEntry = ""
		bmSt.PendingEntryAt = time.Time{}
		bmSt.StopPrice = 0
		bmSt.TargetPrice = 0
	}
	return bmSt, nil, nil
}

func (s *BollingerMACDStrategy) ReplayOnBar(_ start.Context, _ string, bar start.Bar, st start.State, indicators start.IndicatorData) (start.State, error) {
	bmSt, ok := st.(*BMState)
	if !ok {
		return st, fmt.Errorf("BollingerMACDStrategy.ReplayOnBar: expected *BMState, got %T", st)
	}
	bmSt.Indicators = indicators
	bmSt.CalcBarCount++
	bmSt.PrevMACDHist = indicators.MACDHistogram
	bmSt.PrevMACDLine = indicators.MACDLine
	bmSt.PrevMACDSignalL = indicators.MACDSignal
	bmSt.PrevMACDHists = append(bmSt.PrevMACDHists, indicators.MACDHistogram)
	if len(bmSt.PrevMACDHists) > 10 {
		bmSt.PrevMACDHists = bmSt.PrevMACDHists[len(bmSt.PrevMACDHists)-10:]
	}
	bmSt.RecentLows = append(bmSt.RecentLows, bar.Low)
	if len(bmSt.RecentLows) > bmSt.Config.SwingLookback {
		bmSt.RecentLows = bmSt.RecentLows[len(bmSt.RecentLows)-bmSt.Config.SwingLookback:]
	}
	bmSt.RecentHighs = append(bmSt.RecentHighs, bar.High)
	if len(bmSt.RecentHighs) > bmSt.Config.SwingLookback {
		bmSt.RecentHighs = bmSt.RecentHighs[len(bmSt.RecentHighs)-bmSt.Config.SwingLookback:]
	}
	return bmSt, nil
}

// ComputeConfluenceScore evaluates 10 independent factors for entry quality.
// Returns a score 0-100 and a list of contributing factor names.
// isLong: true for long signals, false for short.
func ComputeConfluenceScore(bar start.Bar, ind start.IndicatorData, prevHists []float64, isLong bool) ConfluenceResult {
	var score int
	var factors []string

	// Factor 1: EMA Stack Alignment (0-15)
	// Long: Close > EMA9 > EMA21 > EMA50. Short: Close < EMA9 < EMA21 < EMA50.
	if ind.EMA9 > 0 && ind.EMA21 > 0 && ind.EMA50 > 0 {
		emaCount := 0
		if isLong {
			if bar.Close > ind.EMA9 {
				emaCount++
			}
			if ind.EMA9 > ind.EMA21 {
				emaCount++
			}
			if ind.EMA21 > ind.EMA50 {
				emaCount++
			}
		} else {
			if bar.Close < ind.EMA9 {
				emaCount++
			}
			if ind.EMA9 < ind.EMA21 {
				emaCount++
			}
			if ind.EMA21 < ind.EMA50 {
				emaCount++
			}
		}
		switch emaCount {
		case 3:
			score += 15
			factors = append(factors, "ema_stack")
		case 2:
			score += 10
			factors = append(factors, "ema_partial")
		case 1:
			score += 5
		}
	}

	// Factor 2: ADX Trend Strength (0-15)
	if ind.ADX > 0 {
		switch {
		case ind.ADX >= 30:
			score += 15
			factors = append(factors, "adx_strong")
		case ind.ADX >= 25:
			score += 12
			factors = append(factors, "adx_trend")
		case ind.ADX >= 20:
			score += 8
			factors = append(factors, "adx_ok")
		case ind.ADX >= 15:
			score += 4
		}
	}

	// Factor 3: MACD Histogram Acceleration (0-12)
	// Use CheckHistAccel with different bar counts for graduated scoring
	if len(prevHists) >= 4 {
		switch {
		case CheckHistAccel(prevHists, 3):
			score += 12
			factors = append(factors, "hist_accel_3")
		case CheckHistAccel(prevHists, 2):
			score += 8
			factors = append(factors, "hist_accel_2")
		case CheckHistAccel(prevHists, 1):
			score += 4
		}
	}

	// Factor 4: RSI Position (0-10)
	// Long: 45-65 ideal. Short: 35-55 ideal.
	if ind.RSI > 0 {
		var rsiScore int
		if isLong {
			switch {
			case ind.RSI >= 45 && ind.RSI <= 65:
				rsiScore = 10
			case ind.RSI >= 35 && ind.RSI < 45:
				rsiScore = 5
			case ind.RSI > 65 && ind.RSI <= 75:
				rsiScore = 5
			}
		} else {
			switch {
			case ind.RSI >= 35 && ind.RSI <= 55:
				rsiScore = 10
			case ind.RSI > 55 && ind.RSI <= 65:
				rsiScore = 5
			case ind.RSI >= 25 && ind.RSI < 35:
				rsiScore = 5
			}
		}
		if rsiScore > 0 {
			score += rsiScore
			if rsiScore == 10 {
				factors = append(factors, "rsi_ideal")
			} else {
				factors = append(factors, "rsi_ok")
			}
		}
	}

	// Factor 5: MACD Distance from Zero (0-10)
	// Crossovers near zero catch trend births; far from zero are late entries.
	// Normalize by ATR for cross-symbol comparison.
	if ind.ATR > 0 && ind.MACDLine != 0 {
		dist := math.Abs(ind.MACDLine) / ind.ATR
		switch {
		case dist <= 0.5:
			score += 10
			factors = append(factors, "macd_near_zero")
		case dist <= 1.0:
			score += 6
			factors = append(factors, "macd_mid")
		case dist <= 2.0:
			score += 2
		}
	}

	// Factor 6: Volume Ratio (0-8)
	if ind.VolumeSMA > 0 {
		volRatio := bar.Volume / ind.VolumeSMA
		switch {
		case volRatio >= 1.5:
			score += 8
			factors = append(factors, "vol_surge")
		case volRatio >= 1.2:
			score += 6
			factors = append(factors, "vol_above_avg")
		case volRatio >= 1.0:
			score += 4
		case volRatio >= 0.8:
			score += 2
		}
	}

	// Factor 7: Candle Quality (0-8)
	// Directional close + body ratio combined.
	barRange := bar.High - bar.Low
	if barRange > 0 {
		bodyRatio := math.Abs(bar.Close-bar.Open) / barRange
		directional := (isLong && bar.Close > bar.Open) || (!isLong && bar.Close < bar.Open)
		switch {
		case bodyRatio > 0.7 && directional:
			score += 8
			factors = append(factors, "candle_strong")
		case bodyRatio > 0.5 && directional:
			score += 6
			factors = append(factors, "candle_ok")
		case directional:
			score += 4
			factors = append(factors, "candle_dir")
		}
	}

	// Factor 8: Bollinger Band Position (0-7)
	// Long: %B 0.5-0.8 (trending, not overextended). Short: %B 0.2-0.5.
	if ind.BBPercentB > 0 || ind.BBPercentB < 0 { // BBPercentB is computed
		if isLong {
			switch {
			case ind.BBPercentB >= 0.5 && ind.BBPercentB <= 0.8:
				score += 7
				factors = append(factors, "bb_trend")
			case ind.BBPercentB >= 0.3 && ind.BBPercentB < 0.5:
				score += 4
			}
		} else {
			switch {
			case ind.BBPercentB >= 0.2 && ind.BBPercentB <= 0.5:
				score += 7
				factors = append(factors, "bb_trend")
			case ind.BBPercentB > 0.5 && ind.BBPercentB <= 0.7:
				score += 4
			}
		}
	}

	// Factor 9: HTF Bias Agreement (0-8)
	htfBias := ""
	if htf, ok := ind.HTF["1d"]; ok {
		htfBias = htf.Bias
	}
	if htfBias != "" {
		if (isLong && htfBias == "BULLISH") || (!isLong && htfBias == "BEARISH") {
			score += 8
			factors = append(factors, "htf_agree")
		} else if htfBias == "NEUTRAL" || htfBias == "" {
			score += 4
			factors = append(factors, "htf_neutral")
		}
		// Opposing bias: +0 (no penalty, just no points)
	}

	// Factor 10: VWAP Alignment (0-7)
	if ind.VWAP > 0 {
		if (isLong && bar.Close > ind.VWAP) || (!isLong && bar.Close < ind.VWAP) {
			score += 7
			factors = append(factors, "vwap_aligned")
		}
	}

	return ConfluenceResult{Score: score, Factors: factors}
}

// Deprecated: Use ComputeConfluenceScore instead. Kept for backward compatibility.
//
// CheckHistAccel returns true if the absolute histogram delta has been
// decreasing (converging) for at least requiredBars consecutive bars.
// This indicates momentum is gradually exhausting before the crossover,
// which produces higher-quality signals than abrupt whipsaw crossovers.
func CheckHistAccel(hists []float64, requiredBars int) bool {
	if requiredBars <= 0 || len(hists) < requiredBars+1 {
		return true // disabled or not enough data — pass
	}
	// Compute absolute deltas between consecutive histogram values
	// Then check if the last requiredBars deltas are non-increasing
	deltas := make([]float64, 0, len(hists)-1)
	for i := 1; i < len(hists); i++ {
		deltas = append(deltas, math.Abs(hists[i]-hists[i-1]))
	}
	if len(deltas) < requiredBars {
		return true
	}
	// Check last requiredBars deltas are non-increasing
	start := len(deltas) - requiredBars
	for i := start + 1; i < len(deltas); i++ {
		if deltas[i] > deltas[i-1]+1e-10 { // small epsilon for float comparison
			return false
		}
	}
	return true
}

// Deprecated: Use ComputeConfluenceScore instead. Kept for backward compatibility.
//
// ComputeSignalScore computes a 0-1 composite signal quality score.
// Weights: histogram acceleration 40%, RSI position 30%, volume 30%.
//
// histAccelOK: true if histogram is converging (from CheckHistAccel)
// rsi: current RSI value (0-100)
// isLong: true for long signals, false for short
// volumeRatio: bar volume / volume SMA (e.g., 1.5 means 50% above average)
func ComputeSignalScore(histAccelOK bool, rsi float64, isLong bool, volumeRatio float64) float64 {
	// Component 1: Histogram acceleration (40%)
	var histScore float64
	if histAccelOK {
		histScore = 1.0
	}

	// Component 2: RSI position (30%)
	// For longs: RSI 50-70 is ideal (score 1.0 at 60, 0.0 at 30 and 90)
	// For shorts: RSI 30-50 is ideal (score 1.0 at 40, 0.0 at 10 and 70)
	var rsiScore float64
	if rsi > 0 {
		if isLong {
			// Peak at 60, linearly falls to 0 at 30 and 90
			rsiScore = 1.0 - math.Abs(rsi-60)/30.0
		} else {
			// Peak at 40, linearly falls to 0 at 10 and 70
			rsiScore = 1.0 - math.Abs(rsi-40)/30.0
		}
		rsiScore = math.Max(0, math.Min(1, rsiScore))
	}

	// Component 3: Volume ratio (30%)
	// Score 0 at ratio 0.8, score 1.0 at ratio 1.5+
	var volScore float64
	if volumeRatio > 0 {
		volScore = (volumeRatio - 0.8) / 0.7 // 0 at 0.8, 1 at 1.5
		volScore = math.Max(0, math.Min(1, volScore))
	}

	return 0.4*histScore + 0.3*rsiScore + 0.3*volScore
}

func swingMin(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	m := values[0]
	for _, v := range values[1:] {
		m = math.Min(m, v)
	}
	return m
}

func swingMax(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	m := values[0]
	for _, v := range values[1:] {
		m = math.Max(m, v)
	}
	return m
}
