package builtin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

type WhalePullbackStrategy struct {
	meta start.Meta
}

func NewWhalePullbackStrategy() *WhalePullbackStrategy {
	id, _ := start.NewStrategyID("whale_pullback_v1")
	ver, _ := start.NewVersion("1.0.0")
	return &WhalePullbackStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "Whale Pullback v1",
			Description: "VWAP-side trend with large-candle break, EMA(N) pullback with body confirm, and a clear-path veto from the prior-session HVN volume profile.",
			Author:      "system",
		},
	}
}

func (s *WhalePullbackStrategy) Meta() start.Meta { return s.meta }
func (s *WhalePullbackStrategy) WarmupBars() int  { return 60 }

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

type WhalePullbackConfig struct {
	EMAPeriod         int
	PullbackTouchATR  float64
	MinTrendBars      int
	VWAPBreakATR      float64
	VPLookbackDays    int
	VPBinBps          float64
	VPHVNThresholdPct float64
	VPClearATR        float64
	VPRequired        bool
	VPRTHOnly         bool
	ATRStopMult       float64
	ExitBodyCloses    int
	CooldownSeconds   int
	MaxTradesPerDay   int
	AllowedHoursStart string
	AllowedHoursEnd   string
	AllowedHoursTZ    string
}

func parseWhalePullbackConfig(params map[string]any) WhalePullbackConfig {
	return WhalePullbackConfig{
		EMAPeriod:         getInt(params, "ema_period", 9),
		PullbackTouchATR:  getFloat64(params, "pullback_touch_atr", 0.15),
		MinTrendBars:      getInt(params, "min_trend_bars", 3),
		VWAPBreakATR:      getFloat64(params, "vwap_break_atr", 0.5),
		VPLookbackDays:    getInt(params, "vp_lookback_days", 5),
		VPBinBps:          getFloat64(params, "vp_bin_bps", 10),
		VPHVNThresholdPct: getFloat64(params, "vp_hvn_threshold_pct", 80.0),
		VPClearATR:        getFloat64(params, "vp_clear_atr", 0.6),
		VPRequired:        getBool(params, "vp_required", true),
		VPRTHOnly:         getBool(params, "vp_rth_only", true),
		ATRStopMult:       getFloat64(params, "atr_stop_mult", 1.75),
		ExitBodyCloses:    getInt(params, "exit_body_closes", 2),
		CooldownSeconds:   getInt(params, "cooldown_seconds", 1800),
		MaxTradesPerDay:   getInt(params, "max_trades_per_day", 3),
		AllowedHoursStart: getString(params, "allowed_hours_start", "09:35"),
		AllowedHoursEnd:   getString(params, "allowed_hours_end", "15:30"),
		AllowedHoursTZ:    getString(params, "allowed_hours_tz", "America/New_York"),
	}
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

type WPPhase int

const (
	WPPhaseIdle WPPhase = iota
	WPPhaseTrending
	WPPhasePullbackArmed
)

type WhalePullbackState struct {
	Symbol     string              `json:"-"`
	Config     WhalePullbackConfig `json:"-"`
	Indicators start.IndicatorData `json:"-"`

	Phase          WPPhase
	TrendDirection string
	TrendBars      int
	QualifiedBreak bool

	EMAValue float64
	EMAReady bool
	ema      *start.EMARolling `json:"-"`

	SessionDate string
	sessions    []*sessionHist   `json:"-"`
	merged      map[int]float64  `json:"-"`
	hvnSet      map[int]struct{} `json:"-"`
	currentAnchor float64        `json:"-"`
	currentBinBps float64        `json:"-"`

	OppositeBodyCount int

	PositionSide   start.Side
	PendingEntry   start.Side
	PendingEntryAt time.Time
	EntryPrice     float64

	PrevBar    start.Bar
	HasPrevBar bool

	TradesToday   int
	CooldownUntil time.Time
}

type sessionHist struct {
	hist   *start.VolumeHistogram
	anchor float64
}

func (s *WhalePullbackState) SetIndicators(ind start.IndicatorData) { s.Indicators = ind }

func (s *WhalePullbackState) ClearPendingEntry() {
	s.PendingEntry = ""
	s.PendingEntryAt = time.Time{}
}

// HVNContainsPrice reports whether the merged HVN set covers any bin in the
// inclusive price range [low, high]. Used by tests to assert window-roll
// re-keying behavior without exposing internal histogram structure.
func (s *WhalePullbackState) HVNContainsPrice(low, high, thresholdPct float64) bool {
	if len(s.hvnSet) == 0 || s.currentBinBps <= 0 {
		return false
	}
	tmp := start.NewVolumeHistogram(s.currentBinBps, s.currentAnchor)
	loIdx := tmp.BinIndex(low)
	hiIdx := tmp.BinIndex(high)
	for idx := range s.hvnSet {
		if idx >= loIdx && idx <= hiIdx {
			return true
		}
	}
	return false
}

// HVNFingerprint returns a deterministic hash over the merged HVN bin set,
// used by parity tests to assert byte-equal state across replay paths.
func (s *WhalePullbackState) HVNFingerprint() string {
	if s.hvnSet == nil {
		return ""
	}
	keys := make([]int, 0, len(s.hvnSet))
	for k := range s.hvnSet {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	h := sha256.New()
	for _, k := range keys {
		_, _ = h.Write([]byte(strconv.Itoa(k)))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func (s *WhalePullbackStrategy) Init(_ start.Context, symbol string, params map[string]any, prior start.State) (start.State, error) {
	cfg := parseWhalePullbackConfig(params)
	st := &WhalePullbackState{
		Symbol:        symbol,
		Config:        cfg,
		ema:           start.NewEMARolling(cfg.EMAPeriod),
		currentBinBps: cfg.VPBinBps,
	}
	if prior != nil {
		if wp, ok := prior.(*WhalePullbackState); ok {
			scalars := *wp
			scalars.Config = cfg
			scalars.ema = start.NewEMARolling(cfg.EMAPeriod)
			scalars.currentBinBps = cfg.VPBinBps
			scalars.sessions = nil
			scalars.merged = nil
			scalars.hvnSet = nil
			scalars.currentAnchor = 0
			scalars.EMAReady = false
			scalars.EMAValue = 0
			st = &scalars
		}
	}
	return st, nil
}

// ---------------------------------------------------------------------------
// ReplayOnBar
// ---------------------------------------------------------------------------

func (s *WhalePullbackStrategy) ReplayOnBar(_ start.Context, _ string, bar start.Bar, st start.State, indicators start.IndicatorData) (start.State, error) {
	wp, ok := st.(*WhalePullbackState)
	if !ok {
		return st, fmt.Errorf("WhalePullbackStrategy.ReplayOnBar: expected *WhalePullbackState, got %T", st)
	}
	wp.Indicators = indicators
	updateStructure(wp, bar)
	wp.PrevBar = bar
	wp.HasPrevBar = true
	return wp, nil
}

// ---------------------------------------------------------------------------
// OnBar
// ---------------------------------------------------------------------------

func (s *WhalePullbackStrategy) OnBar(ctx start.Context, symbol string, bar start.Bar, st start.State) (start.State, []start.Signal, error) {
	wp, ok := st.(*WhalePullbackState)
	if !ok {
		return st, nil, fmt.Errorf("WhalePullbackStrategy.OnBar: expected *WhalePullbackState, got %T", st)
	}
	cfg := wp.Config

	now := bar.Time
	if ctx != nil {
		now = ctx.Now()
	}

	if wp.PendingEntry != "" && now.Sub(wp.PendingEntryAt) > 5*time.Minute {
		wp.PendingEntry = ""
		wp.PendingEntryAt = time.Time{}
	}

	emaPrev := wp.EMAValue
	emaReadyPrev := wp.EMAReady
	updateStructure(wp, bar)

	if wp.PositionSide != "" && emaReadyPrev {
		opposite := false
		if wp.PositionSide == start.SideBuy && bar.Close < emaPrev {
			opposite = true
		}
		if wp.PositionSide == start.SideSell && bar.Close > emaPrev {
			opposite = true
		}
		if opposite {
			wp.OppositeBodyCount++
		} else {
			wp.OppositeBodyCount = 0
		}
	}

	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
	cooldown := time.Duration(cfg.CooldownSeconds) * time.Second
	atr := wp.Indicators.ATR

	if wp.PositionSide != "" && wp.PendingEntry == "" {
		exit, reason := evalExit(wp, bar, atr, emaReadyPrev)
		if exit {
			exitSide := start.SideSell
			if wp.PositionSide == start.SideSell {
				exitSide = start.SideBuy
			}
			sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, exitSide, 0.8, map[string]string{
				"ref_price": fmt.Sprintf("%.10f", bar.Close),
				"setup":     "whale_pullback_exit",
				"reason":    reason,
			})
			if err != nil {
				return wp, nil, err
			}
			wp.PositionSide = ""
			wp.OppositeBodyCount = 0
			wp.CooldownUntil = now.Add(cooldown)
			wp.PrevBar = bar
			wp.HasPrevBar = true
			return wp, []start.Signal{sig}, nil
		}
	}

	wp.PrevBar = bar
	wp.HasPrevBar = true

	if wp.PositionSide != "" || wp.PendingEntry != "" {
		return wp, nil, nil
	}
	if now.Before(wp.CooldownUntil) {
		return wp, nil, nil
	}
	if wp.TradesToday >= cfg.MaxTradesPerDay {
		return wp, nil, nil
	}
	if !inAllowedHours(now, cfg) {
		return wp, nil, nil
	}
	if !emaReadyPrev || atr <= 0 {
		return wp, nil, nil
	}
	if wp.Phase != WPPhasePullbackArmed {
		return wp, nil, nil
	}

	confirmed, side := evalPullbackConfirm(wp, bar, atr, emaPrev)
	if !confirmed {
		return wp, nil, nil
	}

	if cfg.VPRequired && vetoByVP(wp, bar, atr, side) {
		return wp, nil, nil
	}

	isLong := side == start.SideBuy
	conf := start.ComputeBaseConfluence(bar, wp.Indicators, isLong)
	tags := map[string]string{
		"ref_price":             fmt.Sprintf("%.10f", bar.Close),
		"setup":                 "whale_pullback",
		"trend":                 wp.TrendDirection,
		"ema_period":            strconv.Itoa(cfg.EMAPeriod),
		"ema_value":             fmt.Sprintf("%.6f", emaPrev),
		"confluence":            fmt.Sprintf("%d", conf.Score),
		"confluence_detail":     conf.FormatDetail(),
		"confluence_components": conf.ComponentsJSON(),
	}
	sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, side, 0.7, tags)
	if err != nil {
		return wp, nil, err
	}
	wp.PendingEntry = side
	wp.PendingEntryAt = now
	wp.EntryPrice = bar.Close
	wp.OppositeBodyCount = 0
	wp.TradesToday++
	wp.CooldownUntil = now.Add(cooldown)
	// Backtest harness fills at bar close and FillReceived may not reach
	// OnEvent before the next OnBar (sharded slice pipeline defers signal
	// publication until replayFlat). Transition optimistically so OnBar's
	// body-close / ATR-stop guards run; EntryRejection rolls back if the
	// engine vetoes (e.g. RTH suppression). Live keeps the handshake.
	if ctx != nil && ctx.IsBacktest() {
		wp.PositionSide = side
		wp.PendingEntry = ""
		wp.PendingEntryAt = time.Time{}
	}
	return wp, []start.Signal{sig}, nil
}

// ---------------------------------------------------------------------------
// OnEvent
// ---------------------------------------------------------------------------

func (s *WhalePullbackStrategy) OnEvent(_ start.Context, _ string, evt any, st start.State) (start.State, []start.Signal, error) {
	wp, ok := st.(*WhalePullbackState)
	if !ok {
		return st, nil, fmt.Errorf("WhalePullbackStrategy.OnEvent: expected *WhalePullbackState, got %T", st)
	}
	switch evt.(type) {
	case start.FillConfirmation:
		if wp.PendingEntry != "" {
			wp.PositionSide = wp.PendingEntry
			wp.PendingEntry = ""
			wp.PendingEntryAt = time.Time{}
		}
	case start.EntryRejection:
		// In backtest the optimistic emit-time path also sets PositionSide
		// (see OnBar). On rejection roll both back and refund the daily-cap
		// counter so the cap reflects accepted trades, not attempted ones.
		// In live PositionSide remains empty here so the rollback is a
		// no-op for the live handshake; only PendingEntry needs clearing.
		if wp.PositionSide != "" && wp.TradesToday > 0 {
			wp.TradesToday--
		}
		wp.PositionSide = ""
		wp.PendingEntry = ""
		wp.PendingEntryAt = time.Time{}
	}
	return wp, nil, nil
}

// ---------------------------------------------------------------------------
// Shared structure update — called by OnBar and ReplayOnBar
// ---------------------------------------------------------------------------

func updateStructure(wp *WhalePullbackState, bar start.Bar) {
	rolloverIfNewSession(wp, bar)
	accumulateInCurrentSession(wp, bar)

	advanceTrend(wp, bar)
	advancePullback(wp, bar)

	if wp.ema != nil {
		wp.ema.Update(bar.Close)
		wp.EMAValue = wp.ema.Value()
		wp.EMAReady = wp.ema.IsReady()
	}
}

// ---------------------------------------------------------------------------
// Trend / pullback / exit collaborators
// ---------------------------------------------------------------------------

func advanceTrend(wp *WhalePullbackState, bar start.Bar) {
	vwap := wp.Indicators.VWAP
	atr := wp.Indicators.ATR
	if vwap <= 0 || atr <= 0 {
		return
	}
	dir := ""
	if bar.Close > vwap {
		dir = "bullish"
	} else if bar.Close < vwap {
		dir = "bearish"
	}
	if dir == "" {
		wp.TrendDirection = ""
		wp.TrendBars = 0
		wp.QualifiedBreak = false
		wp.Phase = WPPhaseIdle
		return
	}
	if dir != wp.TrendDirection {
		wp.TrendDirection = dir
		wp.TrendBars = 1
		wp.QualifiedBreak = false
		wp.Phase = WPPhaseIdle
		return
	}
	wp.TrendBars++
	dist := math.Abs(bar.Close - vwap)
	if dist >= wp.Config.VWAPBreakATR*atr {
		wp.QualifiedBreak = true
	}
	if wp.TrendBars >= wp.Config.MinTrendBars && wp.QualifiedBreak {
		if wp.Phase == WPPhaseIdle {
			wp.Phase = WPPhaseTrending
		}
	}
}

func advancePullback(wp *WhalePullbackState, bar start.Bar) {
	if wp.Phase < WPPhaseTrending {
		return
	}
	if !wp.EMAReady {
		return
	}
	atr := wp.Indicators.ATR
	if atr <= 0 {
		return
	}
	ema := wp.EMAValue
	band := wp.Config.PullbackTouchATR * atr
	touched := false
	switch wp.TrendDirection {
	case "bullish":
		touched = bar.Low <= ema+band
	case "bearish":
		touched = bar.High >= ema-band
	}
	if touched {
		wp.Phase = WPPhasePullbackArmed
	}
}

// evalPullbackConfirm runs against EMA(t-1) — the value computed before
// updateStructure ingested the current bar. Returns side on confirmation.
func evalPullbackConfirm(wp *WhalePullbackState, bar start.Bar, atr, emaPrev float64) (bool, start.Side) {
	if atr <= 0 || emaPrev <= 0 {
		return false, ""
	}
	band := wp.Config.PullbackTouchATR * atr
	switch wp.TrendDirection {
	case "bullish":
		if bar.Low > emaPrev+band {
			return false, ""
		}
		if bar.Close <= emaPrev {
			return false, ""
		}
		return true, start.SideBuy
	case "bearish":
		if bar.High < emaPrev-band {
			return false, ""
		}
		if bar.Close >= emaPrev {
			return false, ""
		}
		return true, start.SideSell
	}
	return false, ""
}

func evalExit(wp *WhalePullbackState, bar start.Bar, atr float64, emaReadyPrev bool) (bool, string) {
	if wp.PositionSide == "" {
		return false, ""
	}
	if emaReadyPrev && wp.OppositeBodyCount >= wp.Config.ExitBodyCloses {
		return true, "ema_body_close_opposite"
	}
	if atr > 0 && wp.EntryPrice > 0 {
		stopDist := wp.Config.ATRStopMult * atr
		if wp.PositionSide == start.SideBuy && bar.Close <= wp.EntryPrice-stopDist {
			return true, "atr_stop"
		}
		if wp.PositionSide == start.SideSell && bar.Close >= wp.EntryPrice+stopDist {
			return true, "atr_stop"
		}
	}
	return false, ""
}

// ---------------------------------------------------------------------------
// Volume profile collaborator
// ---------------------------------------------------------------------------

func rolloverIfNewSession(wp *WhalePullbackState, bar start.Bar) {
	loc := cachedLocation(wp.Config.AllowedHoursTZ)
	if loc == nil {
		loc = etLocation
	}
	dateStr := bar.Time.In(loc).Format("2006-01-02")
	if wp.SessionDate == dateStr {
		return
	}
	wp.SessionDate = dateStr
	wp.TradesToday = 0

	anchor := bar.Close
	newSession := &sessionHist{
		hist:   start.NewVolumeHistogram(wp.Config.VPBinBps, anchor),
		anchor: anchor,
	}
	wp.sessions = append(wp.sessions, newSession)

	for len(wp.sessions) > wp.Config.VPLookbackDays {
		wp.sessions = wp.sessions[1:]
	}

	rebuildMerged(wp)
}

func accumulateInCurrentSession(wp *WhalePullbackState, bar start.Bar) {
	if len(wp.sessions) == 0 {
		return
	}
	if wp.Config.VPRTHOnly && !isRTHBar(bar.Time, wp.Config.AllowedHoursTZ) {
		return
	}
	cur := wp.sessions[len(wp.sessions)-1]
	cur.hist.Accumulate(bar)
}

func isRTHBar(t time.Time, tz string) bool {
	loc := cachedLocation(tz)
	if loc == nil {
		loc = etLocation
	}
	hhmm := t.In(loc).Format("15:04")
	return hhmm >= "09:30" && hhmm < "16:00"
}

// rebuildMerged recomputes the anchor from the oldest kept LOOKBACK session
// (excluding the current in-progress session) and re-keys all kept session
// bin maps under the new anchor before merging. The merged HVN set is frozen
// at this snapshot — current-session accumulation does not influence today's
// veto query.
func rebuildMerged(wp *WhalePullbackState) {
	keep := wp.sessions
	if len(keep) <= 1 {
		wp.merged = nil
		wp.hvnSet = nil
		wp.currentAnchor = 0
		return
	}
	prior := keep[:len(keep)-1]

	if len(prior) > wp.Config.VPLookbackDays {
		prior = prior[len(prior)-wp.Config.VPLookbackDays:]
	}

	anchor := prior[0].anchor
	wp.currentAnchor = anchor

	merged := start.NewVolumeHistogram(wp.Config.VPBinBps, anchor)
	for _, sess := range prior {
		if sess.hist == nil {
			continue
		}
		for oldIdx, v := range sess.hist.Bins() {
			price := sess.hist.BinCenter(oldIdx)
			newIdx := merged.BinIndex(price)
			merged.Bins()[newIdx] += v
		}
	}
	wp.merged = merged.Bins()

	hvnIdx := merged.HVNBins(wp.Config.VPHVNThresholdPct)
	wp.hvnSet = make(map[int]struct{}, len(hvnIdx))
	for _, idx := range hvnIdx {
		wp.hvnSet[idx] = struct{}{}
	}
}

// vetoByVP is only called when cfg.VPRequired is true. With no prior-session
// profile yet, the conservative answer is to veto — explicitly documented as
// expected first-day behavior on a freshly added symbol in the plan.
func vetoByVP(wp *WhalePullbackState, bar start.Bar, atr float64, side start.Side) bool {
	if wp.merged == nil {
		return true
	}
	if atr <= 0 {
		return true
	}
	if len(wp.hvnSet) == 0 {
		return false
	}
	span := wp.Config.VPClearATR * atr
	var lo, hi float64
	if side == start.SideBuy {
		lo, hi = bar.Close, bar.Close+span
	} else {
		lo, hi = bar.Close-span, bar.Close
	}
	tmp := start.NewVolumeHistogram(wp.Config.VPBinBps, wp.currentAnchor)
	loIdx := tmp.BinIndex(lo)
	hiIdx := tmp.BinIndex(hi)
	for idx := range wp.hvnSet {
		if idx >= loIdx && idx <= hiIdx {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Allowed hours
// ---------------------------------------------------------------------------

func inAllowedHours(now time.Time, cfg WhalePullbackConfig) bool {
	if cfg.AllowedHoursStart == "" || cfg.AllowedHoursEnd == "" {
		return true
	}
	loc := etLocation
	if cfg.AllowedHoursTZ != "" {
		if parsed := cachedLocation(cfg.AllowedHoursTZ); parsed != nil {
			loc = parsed
		}
	}
	hhmm := now.In(loc).Format("15:04")
	return hhmm >= cfg.AllowedHoursStart && hhmm < cfg.AllowedHoursEnd
}

// ---------------------------------------------------------------------------
// Marshal / Unmarshal — only marshal scalar state; primitives rebuild via warmup
// ---------------------------------------------------------------------------

type whalePullbackStateJSON struct {
	Symbol            string     `json:"symbol"`
	Phase             WPPhase    `json:"phase"`
	TrendDirection    string     `json:"trend_direction"`
	TrendBars         int        `json:"trend_bars"`
	QualifiedBreak    bool       `json:"qualified_break"`
	OppositeBodyCount int        `json:"opposite_body_count"`
	PositionSide      start.Side `json:"position_side"`
	PendingEntry      start.Side `json:"pending_entry"`
	PendingEntryAt    time.Time  `json:"pending_entry_at"`
	EntryPrice        float64    `json:"entry_price"`
	PrevBar           barJSON    `json:"prev_bar"`
	HasPrevBar        bool       `json:"has_prev_bar"`
	SessionDate       string     `json:"session_date"`
	TradesToday       int        `json:"trades_today"`
	CooldownUntil     time.Time  `json:"cooldown_until"`
}

func (s *WhalePullbackState) Marshal() ([]byte, error) {
	j := whalePullbackStateJSON{
		Symbol:            s.Symbol,
		Phase:             s.Phase,
		TrendDirection:    s.TrendDirection,
		TrendBars:         s.TrendBars,
		QualifiedBreak:    s.QualifiedBreak,
		OppositeBodyCount: s.OppositeBodyCount,
		PositionSide:      s.PositionSide,
		PendingEntry:      s.PendingEntry,
		PendingEntryAt:    s.PendingEntryAt,
		EntryPrice:        s.EntryPrice,
		PrevBar:           barToJSON(s.PrevBar),
		HasPrevBar:        s.HasPrevBar,
		SessionDate:       s.SessionDate,
		TradesToday:       s.TradesToday,
		CooldownUntil:     s.CooldownUntil,
	}
	return json.Marshal(j)
}

func (s *WhalePullbackState) Unmarshal(data []byte) error {
	var j whalePullbackStateJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return fmt.Errorf("WhalePullbackState.Unmarshal: %w", err)
	}
	s.Symbol = j.Symbol
	s.Phase = j.Phase
	s.TrendDirection = j.TrendDirection
	s.TrendBars = j.TrendBars
	s.QualifiedBreak = j.QualifiedBreak
	s.OppositeBodyCount = j.OppositeBodyCount
	s.PositionSide = j.PositionSide
	s.PendingEntry = j.PendingEntry
	s.PendingEntryAt = j.PendingEntryAt
	s.EntryPrice = j.EntryPrice
	s.PrevBar = jsonToBar(j.PrevBar)
	s.HasPrevBar = j.HasPrevBar
	s.SessionDate = j.SessionDate
	s.TradesToday = j.TradesToday
	s.CooldownUntil = j.CooldownUntil
	return nil
}
