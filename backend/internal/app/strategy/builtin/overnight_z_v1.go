package builtin

import (
	"encoding/json"
	"fmt"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

type OvernightZStrategy struct {
	meta start.Meta
}

func NewOvernightZStrategy() *OvernightZStrategy {
	id, _ := start.NewStrategyID("overnight_z")
	ver, _ := start.NewVersion("1.0.0")
	return &OvernightZStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "Overnight Z-Score Bias",
			Description: "Late-session DP buy_ratio Z predicts next-day mean-reversion; long equity shares at open, exit MOC",
			Author:      "system",
		},
	}
}

func (s *OvernightZStrategy) Meta() start.Meta { return s.meta }
func (s *OvernightZStrategy) WarmupBars() int  { return 5 }

// OZConfig holds DNA parameters parsed from TOML [params].
type OZConfig struct {
	LateZLongThreshold  float64 // Z below this triggers long entry (default -1.5)
	LateZShortThreshold float64 // Z above this triggers short entry (default 1.5)
	LongOnly            bool    // when true, only take long entries (default true)

	EntryTime string // ET time for entry, e.g. "09:35"
	ExitTime  string // ET time for MOC exit, e.g. "15:45"
	TimezoneTZ string

	HardStopBPS float64 // max adverse move before stop (default 200)

	RiskPerTradePct float64 // % of equity per signal (default 2.0)
	MaxPositions    int     // max concurrent positions (default 6)
	MaxPerSector    int     // max per sector group (default 2)

	// Kill switch
	RollingWRKillThreshold float64 // disable entries below this WR (default 0.38)
	RollingWRKillWindow    int     // number of trades for rolling WR (default 20)
	RollingWRCooldownDays  int     // trading days to disable after kill switch (default 5)
}

// OZState holds per-symbol state.
type OZState struct {
	Symbol     string              `json:"symbol"`
	Indicators start.IndicatorData `json:"-"`
	Config     OZConfig            `json:"config"`

	PositionSide   start.Side `json:"positionSide,omitempty"`
	PendingEntry   start.Side `json:"pendingEntry,omitempty"`
	PendingEntryAt time.Time  `json:"pendingEntryAt,omitzero"`
	EntryPrice     float64    `json:"entryPrice,omitempty"`
	EntryFillPrice float64    `json:"entryFillPrice,omitempty"`

	LastLateZ     float64 `json:"lastLateZ"`
	LastTradeDate string  `json:"lastTradeDate,omitempty"`
	TradesToday   int     `json:"tradesToday"`

	// Rolling performance for kill switch
	TradeOutcomes    []int8 `json:"tradeOutcomes,omitempty"`
	KillSwitchUntil  string `json:"killSwitchUntil,omitempty"` // YYYY-MM-DD when kill switch expires
	KillSwitchDaysLeft int  `json:"killSwitchDaysLeft,omitempty"`

	// Signal progress tracking
	LastGatedBarTime time.Time `json:"-"`
	CalcBarCount     int       `json:"calcBarCount"`
}

func (st *OZState) SetIndicators(ind start.IndicatorData) {
	st.Indicators = ind
}

func (st *OZState) ResetGatedBarTime() {
	st.LastGatedBarTime = time.Time{}
}

func (st *OZState) Marshal() ([]byte, error)   { return json.Marshal(st) }
func (st *OZState) Unmarshal(data []byte) error { return json.Unmarshal(data, st) }

func parseOZConfig(params map[string]any) OZConfig {
	return OZConfig{
		LateZLongThreshold:     getFloat64(params, "late_z_long_threshold", -1.5),
		LateZShortThreshold:    getFloat64(params, "late_z_short_threshold", 1.5),
		LongOnly:               getBool(params, "long_only", true),
		EntryTime:              getString(params, "entry_time", "09:35"),
		ExitTime:               getString(params, "exit_time", "15:45"),
		TimezoneTZ:             getString(params, "allowed_hours_tz", "America/New_York"),
		HardStopBPS:            getFloat64(params, "hard_stop_bps", 200),
		RiskPerTradePct:        getFloat64(params, "risk_per_trade_pct", 2.0),
		MaxPositions:           getInt(params, "max_positions", 6),
		MaxPerSector:           getInt(params, "max_per_sector", 2),
		RollingWRKillThreshold: getFloat64(params, "rolling_wr_kill_threshold", 0.38),
		RollingWRKillWindow:    getInt(params, "rolling_wr_kill_window", 20),
		RollingWRCooldownDays:  getInt(params, "rolling_wr_cooldown_days", 5),
	}
}

func (s *OvernightZStrategy) Init(_ start.Context, symbol string, params map[string]any, prior start.State) (start.State, error) {
	cfg := parseOZConfig(params)
	st := &OZState{
		Symbol: symbol,
		Config: cfg,
	}
	if prior != nil {
		if ozPrior, ok := prior.(*OZState); ok {
			st = ozPrior
			st.Config = cfg
		}
	}
	return st, nil
}

func (s *OvernightZStrategy) OnBar(ctx start.Context, symbol string, bar start.Bar, st start.State) (start.State, []start.Signal, error) {
	ozSt, ok := st.(*OZState)
	if !ok {
		return st, nil, fmt.Errorf("OvernightZStrategy.OnBar: expected *OZState, got %T", st)
	}

	cfg := ozSt.Config
	ind := ozSt.Indicators
	ozSt.CalcBarCount++

	now := bar.Time
	loc := cachedLocation(cfg.TimezoneTZ)
	if loc == nil {
		return ozSt, nil, nil
	}
	etNow := now.In(loc)
	hhmm := etNow.Format("15:04")
	today := etNow.Format("2006-01-02")

	// Reset daily counters on new day.
	if today != ozSt.LastTradeDate {
		ozSt.TradesToday = 0
		ozSt.LastTradeDate = today
	}

	// Cache yesterday's late Z from indicator pipeline.
	if ind.LateSessionDPZ != 0 {
		ozSt.LastLateZ = ind.LateSessionDPZ
	}

	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))

	// ─── EXIT EVALUATION ─────────────────────────────────────────────

	if ozSt.PositionSide != "" {
		// Hard stop: 200 bps adverse move.
		if ozSt.EntryPrice > 0 {
			moveBPS := (bar.Close - ozSt.EntryPrice) / ozSt.EntryPrice * 10000
			if ozSt.PositionSide == start.SideSell {
				moveBPS = -moveBPS
			}
			if moveBPS <= -cfg.HardStopBPS {
				sig, err := start.NewSignal(instanceID, symbol, start.SignalExit,
					exitSide(ozSt.PositionSide), 0.95,
					map[string]string{
						"setup":     "oz_hard_stop",
						"ref_price": fmt.Sprintf("%.10f", bar.Close),
						"reason":    fmt.Sprintf("hard stop: %.0f bps against entry", -moveBPS),
					})
				if err != nil {
					return ozSt, nil, err
				}
				ozSt.PositionSide = ""
				ozSt.EntryPrice = 0
				return ozSt, []start.Signal{sig}, nil
			}
		}

		// MOC exit at configured time.
		if hhmm >= cfg.ExitTime {
			sig, err := start.NewSignal(instanceID, symbol, start.SignalExit,
				exitSide(ozSt.PositionSide), 0.90,
				map[string]string{
					"setup":     "oz_moc_exit",
					"ref_price": fmt.Sprintf("%.10f", bar.Close),
					"reason":    fmt.Sprintf("MOC exit at %s ET", cfg.ExitTime),
				})
			if err != nil {
				return ozSt, nil, err
			}
			ozSt.PositionSide = ""
			ozSt.EntryPrice = 0
			return ozSt, []start.Signal{sig}, nil
		}

		return ozSt, nil, nil
	}

	// ─── ENTRY EVALUATION ────────────────────────────────────────────

	// Pending entry timeout (5 min).
	if ozSt.PendingEntry != "" {
		if now.Sub(ozSt.PendingEntryAt) > 5*time.Minute {
			ozSt.PendingEntry = ""
			ozSt.PendingEntryAt = time.Time{}
		}
		return ozSt, nil, nil
	}

	// Only enter at the configured entry time bar.
	if hhmm != cfg.EntryTime {
		return ozSt, nil, nil
	}

	// Max trades per day (1 per symbol for this strategy).
	if ozSt.TradesToday >= 1 {
		return ozSt, nil, nil
	}

	// Kill switch: check rolling WR.
	if cfg.RollingWRKillThreshold > 0 && len(ozSt.TradeOutcomes) >= cfg.RollingWRKillWindow {
		window := ozSt.TradeOutcomes
		if len(window) > cfg.RollingWRKillWindow {
			window = window[len(window)-cfg.RollingWRKillWindow:]
		}
		wins := 0
		for _, o := range window {
			if o > 0 {
				wins++
			}
		}
		wr := float64(wins) / float64(len(window))
		if wr < cfg.RollingWRKillThreshold {
			if ozSt.KillSwitchDaysLeft <= 0 {
				ozSt.KillSwitchDaysLeft = cfg.RollingWRCooldownDays
			}
		}
	}
	if ozSt.KillSwitchDaysLeft > 0 {
		// Decrement on new trading day.
		if ozSt.KillSwitchUntil != today {
			ozSt.KillSwitchUntil = today
			ozSt.KillSwitchDaysLeft--
		}
		if ozSt.KillSwitchDaysLeft > 0 {
			return ozSt, nil, nil
		}
	}

	// Need valid late Z signal.
	lateZ := ozSt.LastLateZ
	if lateZ == 0 {
		return ozSt, nil, nil
	}

	var side start.Side
	if lateZ <= cfg.LateZLongThreshold {
		side = start.SideBuy
	} else if !cfg.LongOnly && lateZ >= cfg.LateZShortThreshold {
		side = start.SideSell
	}

	if side == "" {
		return ozSt, nil, nil
	}

	sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, side, 0.80,
		map[string]string{
			"setup":     "overnight_z_entry",
			"ref_price": fmt.Sprintf("%.10f", bar.Close),
			"reason":    fmt.Sprintf("late Z=%.2f %s threshold=%.2f", lateZ, side, cfg.LateZLongThreshold),
			"late_z":    fmt.Sprintf("%.3f", lateZ),
		})
	if err != nil {
		return ozSt, nil, err
	}

	ozSt.PendingEntry = side
	ozSt.PendingEntryAt = now
	ozSt.EntryPrice = bar.Close
	ozSt.TradesToday++

	return ozSt, []start.Signal{sig}, nil
}

func (s *OvernightZStrategy) OnEvent(_ start.Context, _ string, evt any, st start.State) (start.State, []start.Signal, error) {
	ozSt, ok := st.(*OZState)
	if !ok {
		return st, nil, nil
	}
	switch e := evt.(type) {
	case start.FillConfirmation:
		switch {
		case ozSt.PendingEntry != "":
			ozSt.PositionSide = ozSt.PendingEntry
			ozSt.PendingEntry = ""
			ozSt.PendingEntryAt = time.Time{}
			ozSt.EntryFillPrice = e.Price
		case ozSt.PositionSide != "":
			if ozSt.EntryFillPrice > 0 {
				if e.Price > ozSt.EntryFillPrice {
					ozSt.TradeOutcomes = append(ozSt.TradeOutcomes, 1)
				} else {
					ozSt.TradeOutcomes = append(ozSt.TradeOutcomes, -1)
				}
				maxWin := ozSt.Config.RollingWRKillWindow
				if maxWin <= 0 {
					maxWin = 20
				}
				if len(ozSt.TradeOutcomes) > maxWin*2 {
					ozSt.TradeOutcomes = ozSt.TradeOutcomes[len(ozSt.TradeOutcomes)-maxWin:]
				}
			}
			ozSt.PositionSide = ""
			ozSt.EntryPrice = 0
			ozSt.EntryFillPrice = 0
		default:
			ozSt.PositionSide = ""
			ozSt.PendingEntry = ""
			ozSt.PendingEntryAt = time.Time{}
			ozSt.EntryPrice = 0
		}
	case start.EntryRejection:
		ozSt.PendingEntry = ""
		ozSt.PendingEntryAt = time.Time{}
		_ = e
	}
	return ozSt, nil, nil
}

func (s *OvernightZStrategy) ReplayOnBar(_ start.Context, _ string, _ start.Bar, st start.State, indicators start.IndicatorData) (start.State, error) {
	ozSt, ok := st.(*OZState)
	if !ok {
		return st, fmt.Errorf("OvernightZStrategy.ReplayOnBar: expected *OZState, got %T", st)
	}
	ozSt.Indicators = indicators
	ozSt.CalcBarCount++
	if indicators.LateSessionDPZ != 0 {
		ozSt.LastLateZ = indicators.LateSessionDPZ
	}
	return ozSt, nil
}

func exitSide(positionSide start.Side) start.Side {
	if positionSide == start.SideBuy {
		return start.SideSell
	}
	return start.SideBuy
}
