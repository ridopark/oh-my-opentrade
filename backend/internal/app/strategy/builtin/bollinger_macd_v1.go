package builtin

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
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
	MinConfluenceScore  int     // minimum confluence score (0-100) for entry. Default 0 (disabled).
	DPConfluenceEnabled bool    // when true, include dark pool scoring in confluence. Default false.
	DPVetoEnabled       bool    // when true, block entries when DP flow opposes direction. Default false.
	DPVetoBuyRatioMin   float64 // block longs when DP buying < this ratio. Default 0.45.
	DPVetoSellRatioMax  float64 // block shorts when DP buying > this ratio. Default 0.55.
	DPSizingEnabled     bool    // when true, boost signal strength based on DP alignment. Default false.
	DPSizingMaxBoost    float64 // maximum sizing multiplier when DP strongly confirms. Default 1.5.
	DPStopEnabled       bool    // when true, tighten stops to DP support level. Default false.
	DPLevelLookback     int     // number of 5m DP bars to scan for S/R levels. Default 20.
	RollingWRMin        float64 // minimum rolling win rate (0-1) to allow entries. Default 0 (disabled).
	RollingWRWindow     int     // number of recent trades for rolling WR calculation. Default 20.
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

	// Rolling performance gate — tracks recent trade outcomes per symbol
	EntryFillPrice float64 `json:"entryFillPrice,omitempty"` // premium price at entry fill
	TradeOutcomes  []int8  `json:"tradeOutcomes,omitempty"`  // rolling window: 1=win, -1=loss

	// Signal progress tracking
	LastGatedBarTime time.Time `json:"-"`

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
		MinConfluenceScore:  getInt(params, "min_confluence_score", 0),
		DPConfluenceEnabled: getBool(params, "dp_confluence_enabled", false),
		DPVetoEnabled:       getBool(params, "dp_veto_enabled", false),
		DPVetoBuyRatioMin:   getFloat64(params, "dp_veto_buy_ratio_min", 0.45),
		DPVetoSellRatioMax:  getFloat64(params, "dp_veto_sell_ratio_max", 0.55),
		DPSizingEnabled:     getBool(params, "dp_sizing_enabled", false),
		DPSizingMaxBoost:    getFloat64(params, "dp_sizing_max_boost", 1.5),
		DPStopEnabled:       getBool(params, "dp_stop_enabled", false),
		DPLevelLookback:     getInt(params, "dp_level_lookback", 20),
		RollingWRMin:        getFloat64(params, "rolling_wr_min", 0),
		RollingWRWindow:     getInt(params, "rolling_wr_window", 20),
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
		emitMACDEntryGated(ctx, symbol, bmSt, bar, ind, "warmup", fmt.Sprintf("bar %d/%d", bmSt.CalcBarCount, cfg.StabilizationBars))
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
		emitMACDEntryGated(ctx, symbol, bmSt, bar, ind, "filters", "pending entry awaiting fill")
		return bmSt, nil, nil
	}

	// Gate 3: Cooldown + max trades.
	if now.Before(bmSt.CooldownUntil) || bmSt.TradesToday >= cfg.MaxTradesPerDay {
		bmSt.GateCooldown++
		bmSt.PrevMACDHist = ind.MACDHistogram
		emitMACDEntryGated(ctx, symbol, bmSt, bar, ind, "filters", "cooldown or max trades reached")
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
				emitMACDEntryGated(ctx, symbol, bmSt, bar, ind, "filters", "outside trading hours")
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
		emitMACDEntryGated(ctx, symbol, bmSt, bar, ind, "regime", "no EMA9 directional bias")
		return bmSt, nil, nil
	}

	// Gate 6: Rolling performance gate — skip entries if recent WR too low.
	if cfg.RollingWRMin > 0 && len(bmSt.TradeOutcomes) >= cfg.RollingWRWindow {
		wins := 0
		window := bmSt.TradeOutcomes
		if len(window) > cfg.RollingWRWindow {
			window = window[len(window)-cfg.RollingWRWindow:]
		}
		for _, outcome := range window {
			if outcome > 0 {
				wins++
			}
		}
		wr := float64(wins) / float64(len(window))
		if wr < cfg.RollingWRMin {
			bmSt.PrevMACDHist = ind.MACDHistogram
			emitMACDEntryGated(ctx, symbol, bmSt, bar, ind, "filters", fmt.Sprintf("rolling WR %.0f%% < %.0f%% min", wr*100, cfg.RollingWRMin*100))
			return bmSt, nil, nil
		}
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

	// Compute regime tag for signal enrichment (matches AVWAP pattern)
	regimeTag := "none"
	if ar, ok := ind.AnchorRegimes["5m"]; ok && ar.Type != "" {
		regimeTag = ar.Type
	}

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
			// DP veto gate — block entries when institutional flow opposes direction.
			if cfg.DPVetoEnabled {
				if blocked, reason := start.DPVeto(ind, true, cfg.DPVetoBuyRatioMin, cfg.DPVetoSellRatioMax); blocked {
					ctx.Logger().Debug("long entry vetoed by dark pool",
						"symbol", symbol, "reason", reason)
					longTrigger = false
				}
			}
		}
		if longTrigger {
			// Confluence scoring — replaces individual boolean filters
			conf := ComputeConfluenceScore(bar, ind, bmSt.PrevMACDHists, true, cfg.DPConfluenceEnabled)
			if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
				longTrigger = false
			}

			if longTrigger {
				swingLow := swingMin(bmSt.RecentLows)
				if swingLow > 0 && swingLow < bar.Close {
					// Optionally tighten stop to DP support level
					stopPrice := swingLow
					if cfg.DPStopEnabled && ind.DPSupportLevel > swingLow && ind.DPSupportLevel < bar.Close {
						stopPrice = ind.DPSupportLevel - ind.ATR*0.1
					}
					stopDist := bar.Close - stopPrice
					target := bar.Close + cfg.RiskRewardRatio*stopDist
					strength := float64(conf.Score) / 100.0
					if strength < 0.1 {
						strength = 0.1 // minimum strength floor
					}
					// Apply DP sizing multiplier when enabled
					if cfg.DPSizingEnabled {
						strength *= start.DPSizingMultiplier(ind, true, cfg.DPSizingMaxBoost)
						if strength > 1.0 {
							strength = 1.0
						}
					}
					s, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideBuy, strength, map[string]string{
						"setup":             setup,
						"bb_percent_b":      fmt.Sprintf("%.4f", ind.BBPercentB),
						"macd_line":         fmt.Sprintf("%.6f", ind.MACDLine),
						"macd_signal":       fmt.Sprintf("%.6f", ind.MACDSignal),
						"macd_hist":         fmt.Sprintf("%.6f", ind.MACDHistogram),
						"ref_price":         fmt.Sprintf("%.10f", bar.Close),
						"stop_price":        fmt.Sprintf("%.4f", stopPrice),
						"target_price":      fmt.Sprintf("%.4f", target),
						"rr_ratio":          fmt.Sprintf("%.1f", cfg.RiskRewardRatio),
						"stop_bps":          fmt.Sprintf("%.0f", stopDist/bar.Close*10000),
						"confluence":        fmt.Sprintf("%d", conf.Score),
						"confluence_detail": conf.FormatDetail(),
						"regime_5m":         regimeTag,
						"htf_bias":          htfBias,
						"adx":               fmt.Sprintf("%.1f", ind.ADX),
						"rsi":               fmt.Sprintf("%.1f", ind.RSI),
						"vwap_dist_pct":     fmt.Sprintf("%.2f", (bar.Close-ind.VWAP)/ind.VWAP*100),
					})
					if err == nil {
						sig = &s
						bmSt.StopPrice = stopPrice
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
			// DP veto gate — block entries when institutional flow opposes direction.
			if cfg.DPVetoEnabled {
				if blocked, reason := start.DPVeto(ind, false, cfg.DPVetoBuyRatioMin, cfg.DPVetoSellRatioMax); blocked {
					ctx.Logger().Debug("short entry vetoed by dark pool",
						"symbol", symbol, "reason", reason)
					shortTrigger = false
				}
			}
		}
		if shortTrigger {
			conf := ComputeConfluenceScore(bar, ind, bmSt.PrevMACDHists, false, cfg.DPConfluenceEnabled)
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
					// Apply DP sizing multiplier when enabled
					if cfg.DPSizingEnabled {
						strength *= start.DPSizingMultiplier(ind, false, cfg.DPSizingMaxBoost)
						if strength > 1.0 {
							strength = 1.0
						}
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
						"confluence_detail": conf.FormatDetail(),
						"regime_5m":         regimeTag,
						"htf_bias":          htfBias,
						"adx":               fmt.Sprintf("%.1f", ind.ADX),
						"rsi":               fmt.Sprintf("%.1f", ind.RSI),
						"vwap_dist_pct":     fmt.Sprintf("%.2f", (bar.Close-ind.VWAP)/ind.VWAP*100),
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
	emitMACDEntryGated(ctx, symbol, bmSt, bar, ind, "crossover", "no MACD crossover detected")
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
			bmSt.EntryFillPrice = e.Price // track for rolling WR
		case bmSt.PositionSide != "":
			// Exit fill — record win/loss for rolling performance gate.
			if bmSt.EntryFillPrice > 0 {
				if e.Price > bmSt.EntryFillPrice {
					bmSt.TradeOutcomes = append(bmSt.TradeOutcomes, 1) // win
				} else {
					bmSt.TradeOutcomes = append(bmSt.TradeOutcomes, -1) // loss
				}
				// Cap rolling window
				maxWindow := bmSt.Config.RollingWRWindow
				if maxWindow <= 0 {
					maxWindow = 20
				}
				if len(bmSt.TradeOutcomes) > maxWindow*2 {
					bmSt.TradeOutcomes = bmSt.TradeOutcomes[len(bmSt.TradeOutcomes)-maxWindow:]
				}
			}
			bmSt.PositionSide = ""
			bmSt.EntryPrice = 0
			bmSt.EntryFillPrice = 0
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

// ComputeConfluenceScore evaluates 10+ factors for MACD entry quality:
// 8 universal factors (from domain/strategy/confluence.go) + 2 MACD-specific,
// plus optional dark pool scoring when dpEnabled is true.
// Returns a score 0-100+ and a list of contributing factor names.
func ComputeConfluenceScore(bar start.Bar, ind start.IndicatorData, prevHists []float64, isLong bool, dpEnabled bool) start.ConfluenceResult {
	// 8 universal factors (max 78 points)
	base := start.ComputeBaseConfluence(bar, ind, isLong)

	// MACD-specific Factor 1: Histogram Acceleration (0-12)
	var macdExtra start.ConfluenceResult
	if len(prevHists) >= 4 {
		switch {
		case CheckHistAccel(prevHists, 3):
			macdExtra.Score += 12
			macdExtra.Factors = append(macdExtra.Factors, "hist_accel_3")
		case CheckHistAccel(prevHists, 2):
			macdExtra.Score += 8
			macdExtra.Factors = append(macdExtra.Factors, "hist_accel_2")
		case CheckHistAccel(prevHists, 1):
			macdExtra.Score += 4
		}
	}

	// MACD-specific Factor 2: Distance from Zero (0-10)
	if ind.ATR > 0 && ind.MACDLine != 0 {
		dist := math.Abs(ind.MACDLine) / ind.ATR
		switch {
		case dist <= 0.5:
			macdExtra.Score += 10
			macdExtra.Factors = append(macdExtra.Factors, "macd_near_zero")
		case dist <= 1.0:
			macdExtra.Score += 6
			macdExtra.Factors = append(macdExtra.Factors, "macd_mid")
		case dist <= 2.0:
			macdExtra.Score += 2
		}
	}

	result := start.MergeConfluence(base, macdExtra)
	if dpEnabled {
		result = start.MergeConfluence(result, start.ScoreDarkPool(ind, isLong))
	}
	return result
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

// ---------------------------------------------------------------------------
// Signal progress (EntryGated) emission
// ---------------------------------------------------------------------------

// macdGateOrder defines the 4 display gates for the dashboard readiness bar.
var macdGateOrder = []string{"warmup", "regime", "crossover", "filters"}

func macdGateIndex(gate string) int {
	for i, g := range macdGateOrder {
		if g == gate {
			return i
		}
	}
	return len(macdGateOrder)
}

// emitMACDEntryGated publishes an EntryGated event showing how close
// this symbol is to triggering a MACD entry signal.
func emitMACDEntryGated(ctx start.Context, symbol string, bmSt *BMState, bar start.Bar, ind start.IndicatorData, blockingGate, blockingDetail string) {
	if ctx == nil {
		return
	}
	if bar.Time.Equal(bmSt.LastGatedBarTime) {
		return
	}
	bmSt.LastGatedBarTime = bar.Time

	gatesTotal := len(macdGateOrder)
	blockIdx := macdGateIndex(blockingGate)
	gatesPassed := blockIdx
	if blockingGate == "" {
		gatesPassed = gatesTotal
	}

	// MACD proximity score: how close MACD line is to crossing signal line (0-10).
	score := 0
	if ind.MACDSignal != 0 {
		dist := (ind.MACDLine - ind.MACDSignal) / math.Abs(ind.MACDSignal)
		score = int(math.Min(10, math.Max(0, (dist+1)*5)))
	}

	volRatio := 0.0
	if ind.VolumeSMA > 0 {
		volRatio = bar.Volume / ind.VolumeSMA
	}

	payload := domain.EntryGatedPayload{
		Symbol:         symbol,
		Strategy:       "bollinger_macd",
		SetupType:      "macd_cross",
		GatesPassed:    gatesPassed,
		GatesTotal:     gatesTotal,
		BlockingGate:   blockingGate,
		BlockingDetail: blockingDetail,
		Confluence: domain.EntryGatedConfluence{
			Score:    score,
			MaxScore: 10,
		},
		Indicators: domain.EntryGatedIndicators{
			RSI:         ind.RSI,
			VolumeRatio: volRatio,
		},
		Bar: domain.BarSnapshot{
			Open:   bar.Open,
			High:   bar.High,
			Low:    bar.Low,
			Close:  bar.Close,
			Volume: bar.Volume,
		},
	}
	_ = ctx.EmitDomainEvent(payload)
}

// EmitSignalProgress implements start.SignalProgressEmitter for post-warmup
// SSE cache seeding.
func (st *BMState) EmitSignalProgress() []any {
	if st.CalcBarCount < 2 {
		return nil
	}
	blockingGate := "crossover"
	if st.CalcBarCount < st.Config.StabilizationBars {
		blockingGate = "warmup"
	}
	return []any{domain.EntryGatedPayload{
		Symbol:       st.Symbol,
		Strategy:     "bollinger_macd",
		SetupType:    "macd_cross",
		GatesPassed:  2,
		GatesTotal:   4,
		BlockingGate: blockingGate,
		Confluence:   domain.EntryGatedConfluence{Score: 0, MaxScore: 10},
		Bar:          domain.BarSnapshot{},
	}}
}
