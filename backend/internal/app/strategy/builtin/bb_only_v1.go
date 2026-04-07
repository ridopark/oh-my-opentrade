package builtin

import (
	"encoding/json"
	"fmt"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// BBOnlyStrategy implements the Trading Rush BB breakout strategy.
// Entry (long): BB %B > threshold (close above upper band), close > EMA9, close > EMA200.
// Entry (short): BB %B < (1 - threshold), close < EMA9, close < EMA200.
// Uses shared confluence scoring for entry quality.
type BBOnlyStrategy struct {
	meta start.Meta
}

// NewBBOnlyStrategy creates a new BB-only breakout strategy.
func NewBBOnlyStrategy() *BBOnlyStrategy {
	id, _ := start.NewStrategyID("bb_only")
	ver, _ := start.NewVersion("1.0.0")
	return &BBOnlyStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "BB Breakout (Trading Rush)",
			Description: "30m Bollinger Band breakout + 200 MA + EMA9 filter, 1.5x R:R with confluence scoring",
			Author:      "system",
		},
	}
}

func (s *BBOnlyStrategy) Meta() start.Meta { return s.meta }
func (s *BBOnlyStrategy) WarmupBars() int  { return 30 }

// BBConfig holds all DNA parameters parsed from TOML [params].
type BBConfig struct {
	BBBreakoutThreshold float64 // default 1.0 (close above upper band)
	RiskRewardRatio     float64
	SwingLookback       int
	VolumeMult          float64
	AllowedHoursStart   string
	AllowedHoursEnd     string
	AllowedHoursTZ      string
	CooldownSeconds     int
	MaxTradesPerDay     int
	StabilizationBars   int
	MinConfluenceScore  int // minimum confluence score for entry. Default 0 (disabled).
}

// BBState holds per-symbol state.
type BBState struct {
	Symbol         string              `json:"symbol"`
	Indicators     start.IndicatorData `json:"-"`
	Config         BBConfig            `json:"config"`
	CalcBarCount   int                 `json:"calcBarCount"`
	PositionSide   start.Side          `json:"positionSide,omitempty"`
	PendingEntry   start.Side          `json:"pendingEntry,omitempty"`
	PendingEntryAt time.Time           `json:"pendingEntryAt,omitzero"`
	TradesToday    int                 `json:"tradesToday"`
	CooldownUntil  time.Time           `json:"cooldownUntil,omitzero"`
	LastTradeDate  string              `json:"lastTradeDate,omitempty"`

	// Position tracking for R:R exit
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
func (st *BBState) SetIndicators(ind start.IndicatorData) {
	st.Indicators = ind
}

func (st *BBState) Marshal() ([]byte, error)   { return json.Marshal(st) }
func (st *BBState) Unmarshal(data []byte) error { return json.Unmarshal(data, st) }

func parseBBConfig(params map[string]any) BBConfig {
	return BBConfig{
		BBBreakoutThreshold: getFloat64(params, "bb_breakout_threshold", 1.0),
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
	}
}

func (s *BBOnlyStrategy) Init(_ start.Context, symbol string, params map[string]any, prior start.State) (start.State, error) {
	cfg := parseBBConfig(params)
	st := &BBState{
		Symbol: symbol,
		Config: cfg,
	}
	if prior != nil {
		if bbPrior, ok := prior.(*BBState); ok {
			st = bbPrior
			st.Config = cfg
		}
	}
	return st, nil
}

func (s *BBOnlyStrategy) OnBar(ctx start.Context, symbol string, bar start.Bar, st start.State) (start.State, []start.Signal, error) {
	bbSt, ok := st.(*BBState)
	if !ok {
		return st, nil, fmt.Errorf("BBOnlyStrategy.OnBar: expected *BBState, got %T", st)
	}
	cfg := bbSt.Config
	ind := bbSt.Indicators

	now := bar.Time
	if ctx != nil {
		now = ctx.Now()
	}

	bbSt.CalcBarCount++

	// Update rolling bar windows for swing detection.
	bbSt.RecentLows = append(bbSt.RecentLows, bar.Low)
	if len(bbSt.RecentLows) > cfg.SwingLookback {
		bbSt.RecentLows = bbSt.RecentLows[len(bbSt.RecentLows)-cfg.SwingLookback:]
	}
	bbSt.RecentHighs = append(bbSt.RecentHighs, bar.High)
	if len(bbSt.RecentHighs) > cfg.SwingLookback {
		bbSt.RecentHighs = bbSt.RecentHighs[len(bbSt.RecentHighs)-cfg.SwingLookback:]
	}

	// Daily reset.
	todayStr := now.Format("2006-01-02")
	if bbSt.LastTradeDate != todayStr {
		if ctx != nil && bbSt.LastTradeDate != "" && bbSt.DebugBarCount > 0 {
			ctx.Logger().Info("BB-only daily gate summary",
				"symbol", symbol, "date", bbSt.LastTradeDate,
				"bars", bbSt.DebugBarCount,
				"stab", bbSt.GateStabilization, "cd", bbSt.GateCooldown,
				"hrs", bbSt.GateHours, "regime", bbSt.GateRegime,
				"pos", bbSt.GatePosition, "nosetup", bbSt.GateNoSetup,
				"passed", bbSt.GatePassedAll)
		}
		bbSt.TradesToday = 0
		bbSt.CooldownUntil = time.Time{}
		bbSt.LastTradeDate = todayStr
		bbSt.GateStabilization = 0
		bbSt.GateCooldown = 0
		bbSt.GateHours = 0
		bbSt.GateRegime = 0
		bbSt.GatePosition = 0
		bbSt.GateNoSetup = 0
		bbSt.GatePassedAll = 0
		bbSt.DebugBarCount = 0
	}
	bbSt.DebugBarCount++

	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))

	// ─── EXIT EVALUATION (while in position) ─────────────────────────
	if bbSt.PositionSide != "" {
		var exitSig *start.Signal

		switch bbSt.PositionSide {
		case start.SideBuy:
			if bbSt.TargetPrice > 0 && bar.High >= bbSt.TargetPrice {
				sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideSell, 0.9, map[string]string{
					"setup":     "bb_only_target_hit",
					"ref_price": fmt.Sprintf("%.10f", bbSt.TargetPrice),
					"reason":    fmt.Sprintf("%.1fR target: entry=%.2f stop=%.2f target=%.2f", cfg.RiskRewardRatio, bbSt.EntryPrice, bbSt.StopPrice, bbSt.TargetPrice),
				})
				if err == nil {
					exitSig = &sig
				}
			} else if bbSt.StopPrice > 0 && bar.Low <= bbSt.StopPrice {
				sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideSell, 0.9, map[string]string{
					"setup":     "bb_only_stop_hit",
					"ref_price": fmt.Sprintf("%.10f", bbSt.StopPrice),
					"reason":    fmt.Sprintf("swing stop: entry=%.2f stop=%.2f", bbSt.EntryPrice, bbSt.StopPrice),
				})
				if err == nil {
					exitSig = &sig
				}
			}
		case start.SideSell:
			if bbSt.TargetPrice > 0 && bar.Low <= bbSt.TargetPrice {
				sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideBuy, 0.9, map[string]string{
					"setup":     "bb_only_target_hit",
					"ref_price": fmt.Sprintf("%.10f", bbSt.TargetPrice),
					"reason":    fmt.Sprintf("%.1fR target: entry=%.2f stop=%.2f target=%.2f", cfg.RiskRewardRatio, bbSt.EntryPrice, bbSt.StopPrice, bbSt.TargetPrice),
				})
				if err == nil {
					exitSig = &sig
				}
			} else if bbSt.StopPrice > 0 && bar.High >= bbSt.StopPrice {
				sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideBuy, 0.9, map[string]string{
					"setup":     "bb_only_stop_hit",
					"ref_price": fmt.Sprintf("%.10f", bbSt.StopPrice),
					"reason":    fmt.Sprintf("swing stop: entry=%.2f stop=%.2f", bbSt.EntryPrice, bbSt.StopPrice),
				})
				if err == nil {
					exitSig = &sig
				}
			}
		}

		if exitSig != nil {
			bbSt.PositionSide = ""
			bbSt.EntryPrice = 0
			bbSt.StopPrice = 0
			bbSt.TargetPrice = 0
			cooldown := time.Duration(cfg.CooldownSeconds) * time.Second
			bbSt.CooldownUntil = now.Add(cooldown)
			if ctx != nil {
				ctx.Logger().Info("BB-only EXIT", "symbol", symbol, "setup", exitSig.Tags["setup"],
					"reason", exitSig.Tags["reason"])
			}
			return bbSt, []start.Signal{*exitSig}, nil
		}

		return bbSt, nil, nil
	}

	// ─── ENTRY GATES (only when flat) ─────────────────────────────────

	// Gate 1: Stabilization.
	if bbSt.CalcBarCount < cfg.StabilizationBars {
		bbSt.GateStabilization++
		return bbSt, nil, nil
	}

	// Gate 2: Pending entry timeout (5 min).
	if bbSt.PendingEntry != "" && now.Sub(bbSt.PendingEntryAt) > 5*time.Minute {
		bbSt.PendingEntry = ""
		bbSt.PendingEntryAt = time.Time{}
	}
	if bbSt.PendingEntry != "" {
		bbSt.GatePosition++
		return bbSt, nil, nil
	}

	// Gate 3: Cooldown + max trades.
	if now.Before(bbSt.CooldownUntil) || bbSt.TradesToday >= cfg.MaxTradesPerDay {
		bbSt.GateCooldown++
		return bbSt, nil, nil
	}

	// Gate 4: Trading hours.
	if cfg.AllowedHoursStart != "" && cfg.AllowedHoursEnd != "" {
		loc, err := time.LoadLocation(cfg.AllowedHoursTZ)
		if err == nil {
			hhmm := now.In(loc).Format("15:04")
			if hhmm < cfg.AllowedHoursStart || hhmm >= cfg.AllowedHoursEnd {
				bbSt.GateHours++
				return bbSt, nil, nil
			}
		}
	}

	// Gate 5: Regime — price must be above/below EMA9 for direction clarity.
	priceAboveEMA9 := ind.EMA9 > 0 && bar.Close > ind.EMA9
	priceBelowEMA9 := ind.EMA9 > 0 && bar.Close < ind.EMA9
	if !priceAboveEMA9 && !priceBelowEMA9 {
		bbSt.GateRegime++
		return bbSt, nil, nil
	}

	// ─── ENTRY EVALUATION ─────────────────────────────────────────────

	// Volume confirmation
	volumeOK := cfg.VolumeMult <= 1.0 || ind.VolumeSMA <= 0 || bar.Volume > ind.VolumeSMA*cfg.VolumeMult

	// 200 MA filter
	ema200 := ind.EMA200
	if ema200 <= 0 {
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

	// ── LONG ENTRY ──
	longOK := priceAboveEMA9 &&
		(ema200 <= 0 || bar.Close > ema200) &&
		volumeOK &&
		htfBias != "BEARISH"

	if longOK {
		longTrigger := ind.BBPercentB > cfg.BBBreakoutThreshold

		if longTrigger {
			conf := start.ComputeBaseConfluence(bar, ind, true)
			if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
				longTrigger = false
			}

			if longTrigger {
				swingLow := swingMin(bbSt.RecentLows)
				if swingLow > 0 && swingLow < bar.Close {
					stopDist := bar.Close - swingLow
					target := bar.Close + cfg.RiskRewardRatio*stopDist
					strength := float64(conf.Score) / 100.0
					if strength < 0.1 {
						strength = 0.1
					}
					sig2, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideBuy, strength, map[string]string{
						"setup":             "bb_only_long",
						"bb_percent_b":      fmt.Sprintf("%.4f", ind.BBPercentB),
						"ref_price":         fmt.Sprintf("%.10f", bar.Close),
						"stop_price":        fmt.Sprintf("%.4f", swingLow),
						"target_price":      fmt.Sprintf("%.4f", target),
						"rr_ratio":          fmt.Sprintf("%.1f", cfg.RiskRewardRatio),
						"stop_bps":          fmt.Sprintf("%.0f", stopDist/bar.Close*10000),
						"confluence":        fmt.Sprintf("%d", conf.Score),
						"confluence_detail": conf.FormatDetail(),
					})
					if err == nil {
						sig = &sig2
						bbSt.StopPrice = swingLow
						bbSt.TargetPrice = target
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
		shortTrigger := ind.BBPercentB < (1.0 - cfg.BBBreakoutThreshold)

		if shortTrigger {
			conf := start.ComputeBaseConfluence(bar, ind, false)
			if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
				shortTrigger = false
			}

			if shortTrigger {
				swingHigh := swingMax(bbSt.RecentHighs)
				if swingHigh > 0 && swingHigh > bar.Close {
					stopDist := swingHigh - bar.Close
					target := bar.Close - cfg.RiskRewardRatio*stopDist
					strength := float64(conf.Score) / 100.0
					if strength < 0.1 {
						strength = 0.1
					}
					sig2, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideSell, strength, map[string]string{
						"setup":             "bb_only_short",
						"bb_percent_b":      fmt.Sprintf("%.4f", ind.BBPercentB),
						"ref_price":         fmt.Sprintf("%.10f", bar.Close),
						"stop_price":        fmt.Sprintf("%.4f", swingHigh),
						"target_price":      fmt.Sprintf("%.4f", target),
						"rr_ratio":          fmt.Sprintf("%.1f", cfg.RiskRewardRatio),
						"stop_bps":          fmt.Sprintf("%.0f", stopDist/bar.Close*10000),
						"confluence":        fmt.Sprintf("%d", conf.Score),
						"confluence_detail": conf.FormatDetail(),
					})
					if err == nil {
						sig = &sig2
						bbSt.StopPrice = swingHigh
						bbSt.TargetPrice = target
					}
				}
			}
		}
	}

	if sig != nil {
		bbSt.GatePassedAll++
		bbSt.PendingEntry = sig.Side
		bbSt.PendingEntryAt = now
		bbSt.TradesToday++
		if ctx != nil {
			ctx.Logger().Info("BB-only SIGNAL",
				"symbol", symbol, "side", sig.Side,
				"bb_pctb", ind.BBPercentB,
				"stop", bbSt.StopPrice, "target", bbSt.TargetPrice)
		}
		return bbSt, []start.Signal{*sig}, nil
	}

	bbSt.GateNoSetup++
	return bbSt, nil, nil
}

func (s *BBOnlyStrategy) OnEvent(ctx start.Context, _ string, evt any, st start.State) (start.State, []start.Signal, error) {
	bbSt, ok := st.(*BBState)
	if !ok {
		return st, nil, nil
	}
	switch e := evt.(type) {
	case start.FillConfirmation:
		switch {
		case bbSt.PendingEntry != "":
			bbSt.PositionSide = bbSt.PendingEntry
			bbSt.PendingEntry = ""
			bbSt.PendingEntryAt = time.Time{}
		case bbSt.PositionSide != "":
			bbSt.PositionSide = ""
			bbSt.EntryPrice = 0
			bbSt.StopPrice = 0
			bbSt.TargetPrice = 0
		default:
			bbSt.PositionSide = ""
			bbSt.PendingEntry = ""
			bbSt.PendingEntryAt = time.Time{}
			bbSt.EntryPrice = 0
			bbSt.StopPrice = 0
			bbSt.TargetPrice = 0
		}
		if ctx != nil {
			ctx.Logger().Info("BB-only Fill",
				"symbol", e.Symbol, "side", e.Side, "price", e.Price,
				"position", bbSt.PositionSide, "entry", bbSt.EntryPrice,
				"stop", bbSt.StopPrice, "target", bbSt.TargetPrice)
		}
	case start.EntryRejection:
		bbSt.PendingEntry = ""
		bbSt.PendingEntryAt = time.Time{}
		bbSt.StopPrice = 0
		bbSt.TargetPrice = 0
	}
	return bbSt, nil, nil
}

func (s *BBOnlyStrategy) ReplayOnBar(_ start.Context, _ string, bar start.Bar, st start.State, indicators start.IndicatorData) (start.State, error) {
	bbSt, ok := st.(*BBState)
	if !ok {
		return st, fmt.Errorf("BBOnlyStrategy.ReplayOnBar: expected *BBState, got %T", st)
	}
	bbSt.Indicators = indicators
	bbSt.CalcBarCount++
	bbSt.RecentLows = append(bbSt.RecentLows, bar.Low)
	if len(bbSt.RecentLows) > bbSt.Config.SwingLookback {
		bbSt.RecentLows = bbSt.RecentLows[len(bbSt.RecentLows)-bbSt.Config.SwingLookback:]
	}
	bbSt.RecentHighs = append(bbSt.RecentHighs, bar.High)
	if len(bbSt.RecentHighs) > bbSt.Config.SwingLookback {
		bbSt.RecentHighs = bbSt.RecentHighs[len(bbSt.RecentHighs)-bbSt.Config.SwingLookback:]
	}
	return bbSt, nil
}
