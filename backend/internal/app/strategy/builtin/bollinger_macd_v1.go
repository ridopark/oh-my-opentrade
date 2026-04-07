package builtin

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// BollingerMACDStrategy implements the Trading Rush BB breakout + MACD
// confluence strategy on 30-minute bars.
//
// Entry (long): close > upper BB (%B > 1.0), MACD histogram > 0,
// close > 200 MA, price trending (close > 9 EMA on chart timeframe).
// Stop: below swing low. Target: 1.5x stop distance (dynamic R:R).
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
	BBBreakoutThreshold float64
	MACDBelow0Required  bool    // require MACD crossover below zero line
	RiskRewardRatio     float64 // target = entry + ratio * (entry - stop)
	SwingLookback       int     // bars to look back for swing low/high
	VolumeMult          float64
	AllowedHoursStart   string
	AllowedHoursEnd     string
	AllowedHoursTZ      string
	CooldownSeconds     int
	MaxTradesPerDay     int
	StabilizationBars   int
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
	PrevMACDHist   float64             `json:"prevMACDHist"`
	LastTradeDate  string              `json:"lastTradeDate,omitempty"`

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
		BBBreakoutThreshold: getFloat64(params, "bb_breakout_threshold", 1.0),
		MACDBelow0Required:  getBool(params, "macd_below_zero_required", false),
		RiskRewardRatio:     getFloat64(params, "risk_reward_ratio", 1.5),
		SwingLookback:       getInt(params, "swing_lookback", 10),
		VolumeMult:          getFloat64(params, "volume_mult", 1.0),
		AllowedHoursStart:   getString(params, "allowed_hours_start", "09:35"),
		AllowedHoursEnd:     getString(params, "allowed_hours_end", "15:45"),
		AllowedHoursTZ:      getString(params, "allowed_hours_tz", "America/New_York"),
		CooldownSeconds:     getInt(params, "cooldown_seconds", 1800),
		MaxTradesPerDay:     getInt(params, "max_trades_per_day", 2),
		StabilizationBars:   getInt(params, "stabilization_bars", 30),
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

	// ── LONG: close > upper BB, MACD histogram > 0, close > 200 MA, price > 9 EMA ──
	if priceAboveEMA9 &&
		ind.BBPercentB > cfg.BBBreakoutThreshold &&
		ind.MACDHistogram > 0 &&
		(!cfg.MACDBelow0Required || ind.MACDLine < 0) &&
		(ema200 <= 0 || bar.Close > ema200) &&
		volumeOK &&
		htfBias != "BEARISH" {

		// Stop = lowest low of lookback window
		swingLow := swingMin(bmSt.RecentLows)
		if swingLow > 0 && swingLow < bar.Close {
			stopDist := bar.Close - swingLow
			target := bar.Close + cfg.RiskRewardRatio*stopDist
			strength := 0.8
			s, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideBuy, strength, map[string]string{
				"setup":        "bb_macd_long",
				"bb_percent_b": fmt.Sprintf("%.4f", ind.BBPercentB),
				"macd_hist":    fmt.Sprintf("%.6f", ind.MACDHistogram),
				"ref_price":    fmt.Sprintf("%.10f", bar.Close),
				"stop_price":   fmt.Sprintf("%.4f", swingLow),
				"target_price": fmt.Sprintf("%.4f", target),
				"rr_ratio":     fmt.Sprintf("%.1f", cfg.RiskRewardRatio),
				"stop_bps":     fmt.Sprintf("%.0f", stopDist/bar.Close*10000),
			})
			if err == nil {
				sig = &s
				bmSt.StopPrice = swingLow
				bmSt.TargetPrice = target
			}
		}
	}

	// ── SHORT: close < lower BB, MACD histogram < 0, close < 200 MA, price < 9 EMA ──
	if sig == nil && priceBelowEMA9 &&
		ind.BBPercentB < (1.0-cfg.BBBreakoutThreshold) && // %B < 0.0 when threshold=1.0
		ind.MACDHistogram < 0 &&
		(!cfg.MACDBelow0Required || ind.MACDLine > 0) &&
		(ema200 <= 0 || bar.Close < ema200) &&
		volumeOK &&
		htfBias != "BULLISH" {

		swingHigh := swingMax(bmSt.RecentHighs)
		if swingHigh > 0 && swingHigh > bar.Close {
			stopDist := swingHigh - bar.Close
			target := bar.Close - cfg.RiskRewardRatio*stopDist
			strength := 0.8
			s, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideSell, strength, map[string]string{
				"setup":        "bb_macd_short",
				"bb_percent_b": fmt.Sprintf("%.4f", ind.BBPercentB),
				"macd_hist":    fmt.Sprintf("%.6f", ind.MACDHistogram),
				"ref_price":    fmt.Sprintf("%.10f", bar.Close),
				"stop_price":   fmt.Sprintf("%.4f", swingHigh),
				"target_price": fmt.Sprintf("%.4f", target),
				"rr_ratio":     fmt.Sprintf("%.1f", cfg.RiskRewardRatio),
				"stop_bps":     fmt.Sprintf("%.0f", stopDist/bar.Close*10000),
			})
			if err == nil {
				sig = &s
				bmSt.StopPrice = swingHigh
				bmSt.TargetPrice = target
			}
		}
	}

	bmSt.PrevMACDHist = ind.MACDHistogram

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
		case bmSt.PendingEntry != "" && e.Side == bmSt.PendingEntry:
			// Entry fill — record entry price for R:R tracking.
			bmSt.PositionSide = e.Side
			bmSt.EntryPrice = e.Price
			bmSt.PendingEntry = ""
			bmSt.PendingEntryAt = time.Time{}
			// Recalculate target based on actual fill price
			if bmSt.PositionSide == start.SideBuy && bmSt.StopPrice > 0 {
				stopDist := e.Price - bmSt.StopPrice
				bmSt.TargetPrice = e.Price + bmSt.Config.RiskRewardRatio*stopDist
			} else if bmSt.PositionSide == start.SideSell && bmSt.StopPrice > 0 {
				stopDist := bmSt.StopPrice - e.Price
				bmSt.TargetPrice = e.Price - bmSt.Config.RiskRewardRatio*stopDist
			}
		case bmSt.PositionSide != "" && e.Side != bmSt.PositionSide:
			// Exit fill — flat now.
			bmSt.PositionSide = ""
			bmSt.EntryPrice = 0
			bmSt.StopPrice = 0
			bmSt.TargetPrice = 0
		default:
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
