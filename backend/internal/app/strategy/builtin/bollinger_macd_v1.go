package builtin

import (
	"encoding/json"
	"fmt"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// BollingerMACDStrategy implements a Bollinger Bands breakout + MACD histogram
// confluence entry, gated by a composite regime score.
type BollingerMACDStrategy struct {
	meta start.Meta
}

func NewBollingerMACDStrategy() *BollingerMACDStrategy {
	id, _ := start.NewStrategyID("bollinger_macd")
	ver, _ := start.NewVersion("1.0.0")
	return &BollingerMACDStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "BB+MACD Confluence with Regime Filter",
			Description: "Bollinger Band breakout confirmed by MACD histogram, gated by composite regime score",
			Author:      "system",
		},
	}
}

func (s *BollingerMACDStrategy) Meta() start.Meta { return s.meta }
func (s *BollingerMACDStrategy) WarmupBars() int  { return 30 }

// BMConfig holds all DNA parameters parsed from TOML [params].
type BMConfig struct {
	BBBreakoutThreshold    float64
	RequireHistogramRising bool
	RegimeThreshold        float64
	VolumeMult             float64
	StopBps                int
	LimitOffsetBps         int
	AllowedHoursStart      string
	AllowedHoursEnd        string
	AllowedHoursTZ         string
	CooldownSeconds        int
	MaxTradesPerDay        int
	StabilizationBars      int
}

// BMState holds per-symbol state.
type BMState struct {
	Symbol         string             `json:"symbol"`
	Indicators     start.IndicatorData `json:"-"`
	Config         BMConfig           `json:"config"`
	CalcBarCount   int                `json:"calcBarCount"`
	PositionSide   start.Side         `json:"positionSide,omitempty"`
	PendingEntry   start.Side         `json:"pendingEntry,omitempty"`
	PendingEntryAt time.Time          `json:"pendingEntryAt,omitzero"`
	TradesToday    int                `json:"tradesToday"`
	CooldownUntil  time.Time          `json:"cooldownUntil,omitzero"`
	PrevMACDHist   float64            `json:"prevMACDHist"`
	LastTradeDate  string             `json:"lastTradeDate,omitempty"`

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

func (st *BMState) Marshal() ([]byte, error)      { return json.Marshal(st) }
func (st *BMState) Unmarshal(data []byte) error    { return json.Unmarshal(data, st) }

func parseBMConfig(params map[string]any) BMConfig {
	return BMConfig{
		BBBreakoutThreshold:    getFloat64(params, "bb_breakout_threshold", 1.0),
		RequireHistogramRising: getBool(params, "require_histogram_rising", true),
		RegimeThreshold:        getFloat64(params, "regime_threshold", 0.67),
		VolumeMult:             getFloat64(params, "volume_mult", 1.5),
		StopBps:                getInt(params, "stop_bps", 50),
		LimitOffsetBps:         getInt(params, "limit_offset_bps", 10),
		AllowedHoursStart:      getString(params, "allowed_hours_start", "09:35"),
		AllowedHoursEnd:        getString(params, "allowed_hours_end", "15:45"),
		AllowedHoursTZ:         getString(params, "allowed_hours_tz", "America/New_York"),
		CooldownSeconds:        getInt(params, "cooldown_seconds", 1800),
		MaxTradesPerDay:        getInt(params, "max_trades_per_day", 3),
		StabilizationBars:      getInt(params, "stabilization_bars", 30),
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
			st.Config = cfg // always use latest config
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

	// Daily reset: clear TradesToday, CooldownUntil, and debug counters.
	todayStr := now.Format("2006-01-02")
	if bmSt.LastTradeDate != todayStr {
		// Log previous day's gate summary before reset.
		if ctx != nil && bmSt.LastTradeDate != "" && bmSt.DebugBarCount > 0 {
			ctx.Logger().Info("BB+MACD daily gate summary",
				"symbol", symbol,
				"date", bmSt.LastTradeDate,
				"bars", bmSt.DebugBarCount,
				"gate_stabilization", bmSt.GateStabilization,
				"gate_cooldown", bmSt.GateCooldown,
				"gate_hours", bmSt.GateHours,
				"gate_regime", bmSt.GateRegime,
				"gate_position", bmSt.GatePosition,
				"gate_no_setup", bmSt.GateNoSetup,
				"gate_passed", bmSt.GatePassedAll,
			)
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

	// Gate 1: Stabilization — need enough bars for indicators to settle.
	if bmSt.CalcBarCount < cfg.StabilizationBars {
		bmSt.GateStabilization++
		bmSt.PrevMACDHist = ind.MACDHistogram
		return bmSt, nil, nil
	}

	// Gate 2: Pending entry timeout (5 min).
	if bmSt.PendingEntry != "" && now.Sub(bmSt.PendingEntryAt) > 5*time.Minute {
		if ctx != nil {
			ctx.Logger().Info("BB+MACD clearing PendingEntry timeout",
				"symbol", symbol, "side", bmSt.PendingEntry, "age", now.Sub(bmSt.PendingEntryAt).String())
		}
		bmSt.PendingEntry = ""
		bmSt.PendingEntryAt = time.Time{}
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

	// Gate 5: Regime score — the key filter.
	if ind.RegimeScore < cfg.RegimeThreshold {
		bmSt.GateRegime++
		bmSt.PrevMACDHist = ind.MACDHistogram
		return bmSt, nil, nil
	}

	// Gate 6: Only entries if flat and no pending entry.
	if bmSt.PositionSide != "" || bmSt.PendingEntry != "" {
		bmSt.GatePosition++
		if ctx != nil {
			ctx.Logger().Info("BB+MACD gate6 position block",
				"symbol", symbol, "position_side", bmSt.PositionSide, "pending_entry", bmSt.PendingEntry)
		}
		bmSt.PrevMACDHist = ind.MACDHistogram
		return bmSt, nil, nil
	}

	// Entry evaluation
	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
	cooldown := time.Duration(cfg.CooldownSeconds) * time.Second

	// Volume confirmation
	volumeOK := ind.VolumeSMA <= 0 || bar.Volume > ind.VolumeSMA*cfg.VolumeMult

	// MACD histogram direction
	histRising := ind.MACDHistogram > bmSt.PrevMACDHist
	histFalling := ind.MACDHistogram < bmSt.PrevMACDHist

	// HTF bias
	htfBias := ""
	if htf, ok := ind.HTF["1d"]; ok {
		htfBias = htf.Bias
	}

	var sig *start.Signal

	// Long entry: BB %B > threshold + MACD histogram > 0 & rising + price > EMA200
	if ind.BBPercentB > cfg.BBBreakoutThreshold &&
		ind.MACDHistogram > 0 &&
		(!cfg.RequireHistogramRising || histRising) &&
		(ind.EMA200 <= 0 || bar.Close > ind.EMA200) &&
		volumeOK &&
		htfBias != "BEARISH" {

		strength := ind.RegimeScore * 0.85
		if strength > 1.0 {
			strength = 1.0
		}
		s, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideBuy, strength, map[string]string{
			"setup":          "bb_macd_long",
			"bb_percent_b":   fmt.Sprintf("%.4f", ind.BBPercentB),
			"macd_histogram": fmt.Sprintf("%.6f", ind.MACDHistogram),
			"regime_score":   fmt.Sprintf("%.2f", ind.RegimeScore),
			"adx":            fmt.Sprintf("%.2f", ind.ADX),
			"ref_price":      fmt.Sprintf("%.10f", bar.Close),
			"stop_bps":       fmt.Sprintf("%d", cfg.StopBps),
		})
		if err != nil {
			return bmSt, nil, err
		}
		sig = &s
	}

	// Short entry: BB %B < 0 + MACD histogram < 0 & falling + price < EMA200
	if sig == nil &&
		ind.BBPercentB < 0.0 &&
		ind.MACDHistogram < 0 &&
		(!cfg.RequireHistogramRising || histFalling) &&
		(ind.EMA200 <= 0 || bar.Close < ind.EMA200) &&
		volumeOK &&
		htfBias != "BULLISH" {

		strength := ind.RegimeScore * 0.85
		if strength > 1.0 {
			strength = 1.0
		}
		s, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideSell, strength, map[string]string{
			"setup":          "bb_macd_short",
			"bb_percent_b":   fmt.Sprintf("%.4f", ind.BBPercentB),
			"macd_histogram": fmt.Sprintf("%.6f", ind.MACDHistogram),
			"regime_score":   fmt.Sprintf("%.2f", ind.RegimeScore),
			"adx":            fmt.Sprintf("%.2f", ind.ADX),
			"ref_price":      fmt.Sprintf("%.10f", bar.Close),
			"stop_bps":       fmt.Sprintf("%d", cfg.StopBps),
		})
		if err != nil {
			return bmSt, nil, err
		}
		sig = &s
	}

	bmSt.PrevMACDHist = ind.MACDHistogram

	if sig != nil {
		bmSt.GatePassedAll++
		if ctx != nil {
			ctx.Logger().Info("BB+MACD SIGNAL EMITTED",
				"symbol", symbol, "side", sig.Side, "strength", sig.Strength,
				"bb_pctb", ind.BBPercentB, "macd_hist", ind.MACDHistogram,
				"regime", ind.RegimeScore, "adx", ind.ADX, "volume_ok", volumeOK)
		}
		bmSt.PendingEntry = sig.Side
		bmSt.PendingEntryAt = now
		bmSt.TradesToday++
		bmSt.CooldownUntil = now.Add(cooldown)
		return bmSt, []start.Signal{*sig}, nil
	}

	// Passed all gates but no entry condition met — log why.
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
		prevSide := bmSt.PositionSide
		switch {
		case bmSt.PendingEntry != "" && e.Side == bmSt.PendingEntry:
			// Entry fill — we're now in a position.
			bmSt.PositionSide = e.Side
			bmSt.PendingEntry = ""
			bmSt.PendingEntryAt = time.Time{}
		case bmSt.PositionSide != "" && e.Side != bmSt.PositionSide:
			// Exit fill (opposite side) — we're flat now.
			bmSt.PositionSide = ""
		default:
			// Unexpected fill — reset to flat to avoid permanent lockout.
			bmSt.PositionSide = ""
			bmSt.PendingEntry = ""
			bmSt.PendingEntryAt = time.Time{}
		}
		if ctx != nil {
			ctx.Logger().Info("BB+MACD FillConfirmation",
				"symbol", e.Symbol, "side", e.Side, "price", e.Price,
				"prev_position", prevSide, "position_side_now", bmSt.PositionSide)
		}
	case start.EntryRejection:
		bmSt.PendingEntry = ""
		bmSt.PendingEntryAt = time.Time{}
		if ctx != nil {
			ctx.Logger().Info("BB+MACD EntryRejection",
				"symbol", e.Symbol, "side", e.Side, "reason", e.Reason)
		}
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
	return bmSt, nil
}
