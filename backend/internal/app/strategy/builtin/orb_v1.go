// Package builtin contains compiled-in strategy implementations
// that wrap existing trading logic behind the Strategy interface.
package builtin

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// ORBStrategy wraps the existing ORBTracker as a Strategy implementation.
// It delegates bar processing to ORBTracker and converts SetupConditions
// into strategy Signals.
type ORBStrategy struct {
	meta start.Meta
}

// NewORBStrategy creates a new ORB Break & Retest strategy.
func NewORBStrategy() *ORBStrategy {
	id, _ := start.NewStrategyID("orb_break_retest")
	ver, _ := start.NewVersion("1.0.0")
	return &ORBStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "ORB Break & Retest",
			Description: "Opening Range Breakout — Break & Retest with volume confirmation",
			Author:      "system",
		},
	}
}

func (s *ORBStrategy) Meta() start.Meta { return s.meta }
func (s *ORBStrategy) WarmupBars() int  { return 0 } // replay handles state recovery

// Init creates initial state for a symbol. If prior state is provided and
// compatible, it restores from that state (restart recovery).
func (s *ORBStrategy) Init(ctx start.Context, symbol string, params map[string]any, prior start.State) (start.State, error) {
	cfg := monitor.NewORBConfigFromDNA(params)
	tracker := monitor.NewORBTrackerWithSource("strategy")

	// Read bar_timeframe from DNA params, default to "5m".
	barTF := "5m"
	if v, ok := params["bar_timeframe"]; ok {
		if s, ok := v.(string); ok && s != "" {
			barTF = s
		}
	}

	minConf := 0
	if v, ok := params["min_confluence_score"]; ok {
		switch n := v.(type) {
		case int:
			minConf = n
		case int64:
			minConf = int(n)
		case float64:
			minConf = int(n)
		}
	}

	st := &ORBState{
		Tracker:            tracker,
		Config:             cfg,
		Symbol:             symbol,
		Timeframe:          barTF,
		MinConfluenceScore: minConf,
	}

	// Attempt to restore from prior state if available.
	if prior != nil {
		if orbPrior, ok := prior.(*ORBState); ok {
			// Reuse the tracker with its session state intact.
			st.Tracker = orbPrior.Tracker
			st.Config = cfg // Use new config (may have been updated).
		} else if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Warn("ORBStrategy: incompatible prior state, starting fresh",
				"symbol", symbol)
		}
	}

	return st, nil
}

// OnBar processes a market bar through the ORBTracker and converts any
// detected setup condition into a Signal.
func (s *ORBStrategy) OnBar(ctx start.Context, symbol string, bar start.Bar, st start.State) (start.State, []start.Signal, error) {
	orbState, ok := st.(*ORBState)
	if !ok {
		return st, nil, fmt.Errorf("ORBStrategy.OnBar: expected *ORBState, got %T", st)
	}

	// Convert strategy.Bar → domain.MarketBar for the ORBTracker.
	sym, err := domain.NewSymbol(symbol)
	if err != nil {
		return st, nil, fmt.Errorf("ORBStrategy.OnBar: invalid symbol: %w", err)
	}
	tf := orbState.Timeframe
	if tf == "" {
		tf = "1m"
	}
	domBar, err := domain.NewMarketBar(bar.Time, sym, domain.Timeframe(tf), bar.Open, bar.High, bar.Low, bar.Close, bar.Volume)
	if err != nil {
		return st, nil, fmt.Errorf("ORBStrategy.OnBar: invalid bar: %w", err)
	}

	// Build indicator snapshot from ORBState's cached indicators.
	// The ORBTracker primarily needs Volume and VolumeSMA for RVOL calculation.
	snap := orbState.BuildSnapshot(sym, bar.Time)

	// Delegate to the underlying ORBTracker.
	setup, detected := orbState.Tracker.OnBar(domBar, snap, orbState.Config, false)

	// Emit ORBPhaseUpdate on state transitions and during active phases
	// (FORMING_RANGE to show range building, AWAITING_RETEST to show countdown).
	if ctx != nil {
		if sess := orbState.Tracker.GetSession(symbol); sess != nil {
			phaseChanged := sess.State != orbState.PrevPhase
			activePhase := sess.State == "FORMING_RANGE" || sess.State == "AWAITING_RETEST"
			if phaseChanged || activePhase {
				orbState.PrevPhase = sess.State
				orbState.emitPhaseUpdate(ctx, sess, domBar, snap)
			}
		}
	}

	if !detected || setup == nil {
		return orbState, nil, nil
	}

	anchorTag := "none"
	if ar, ok := orbState.Indicators.AnchorRegimes["5m"]; ok {
		anchorTag = ar.Type
		if ar.Type == "REVERSAL" {
			return orbState, nil, nil
		}
	}

	htfBiasTag := "none"
	if orbState.Config.HTFBiasEnabled {
		daily, ok := orbState.Indicators.HTF["1d"]
		if !ok || daily.Bias == "" {
			// Fail-closed: no HTF data → block signal for safety.
			// This prevents unfiltered trades on symbols that haven't
			// completed HTF warmup (e.g., dynamically screened symbols).
			return orbState, nil, nil
		}
		htfBiasTag = daily.Bias
		switch {
		case setup.Direction == domain.DirectionLong && daily.Bias == "BEARISH":
			return orbState, nil, nil
		case setup.Direction == domain.DirectionShort && daily.Bias == "BULLISH":
			return orbState, nil, nil
		}
	}

	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
	side := start.SideBuy
	isLong := setup.Direction == domain.DirectionLong
	if !isLong {
		side = start.SideSell
	}

	// Confluence scoring and gating.
	conf := start.ComputeBaseConfluence(
		start.Bar{Time: bar.Time, Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume},
		orbState.Indicators, isLong,
	)
	if orbState.MinConfluenceScore > 0 && conf.Score < orbState.MinConfluenceScore {
		return orbState, nil, nil
	}

	// Signal strength: higher of tracker confidence and confluence/100.
	confStrength := float64(conf.Score) / 100.0
	if confStrength < 0.1 {
		confStrength = 0.1
	}
	signalStrength := setup.Confidence
	if confStrength > signalStrength {
		signalStrength = confStrength
	}

	tags := map[string]string{
		"ref_price":         fmt.Sprintf("%.10f", setup.BarClose),
		"setup":             setup.Trigger,
		"trigger":           setup.Trigger,
		"orb_high":          fmt.Sprintf("%.4f", setup.ORBHigh),
		"orb_low":           fmt.Sprintf("%.4f", setup.ORBLow),
		"rvol":              fmt.Sprintf("%.2f", setup.RVOL),
		"bar_close":         fmt.Sprintf("%.4f", setup.BarClose),
		"regime_anchor":     anchorTag,
		"htf_bias":          htfBiasTag,
		"confluence":        fmt.Sprintf("%d", conf.Score),
		"confluence_detail": conf.FormatDetail(),
	}

	if orbState.Config.SignalATRStopMult > 0 && orbState.Indicators.ATR > 0 {
		atrStop := orbState.Indicators.ATR * orbState.Config.SignalATRStopMult
		var stopPrice float64
		if isLong {
			stopPrice = setup.BarClose - atrStop
		} else {
			stopPrice = setup.BarClose + atrStop
		}
		tags["stop_price"] = fmt.Sprintf("%.10f", stopPrice)
		tags["atr_stop_distance"] = fmt.Sprintf("%.4f", atrStop)
	}

	sig, err := start.NewSignal(
		instanceID,
		symbol,
		start.SignalEntry,
		side,
		clampStrength(signalStrength),
		tags,
	)
	if err != nil {
		return orbState, nil, fmt.Errorf("ORBStrategy.OnBar: signal creation failed: %w", err)
	}

	return orbState, []start.Signal{sig}, nil
}

// ReplayOnBar processes a historical bar for state recovery.
// It feeds the bar through the ORBTracker with replay=true, which reconstructs
// the opening range and state machine without firing live signals.
func (s *ORBStrategy) ReplayOnBar(ctx start.Context, symbol string, bar start.Bar, st start.State, indicators start.IndicatorData) (start.State, error) {
	orbState, ok := st.(*ORBState)
	if !ok {
		return st, fmt.Errorf("ORBStrategy.ReplayOnBar: expected *ORBState, got %T", st)
	}

	// Inject indicators for snapshot building.
	orbState.SetIndicators(indicators)

	// Convert strategy.Bar → domain.MarketBar for the ORBTracker.
	sym, err := domain.NewSymbol(symbol)
	if err != nil {
		return st, fmt.Errorf("ORBStrategy.ReplayOnBar: invalid symbol: %w", err)
	}
	tf := orbState.Timeframe
	if tf == "" {
		tf = "1m"
	}
	domBar, err := domain.NewMarketBar(bar.Time, sym, domain.Timeframe(tf), bar.Open, bar.High, bar.Low, bar.Close, bar.Volume)
	if err != nil {
		return st, fmt.Errorf("ORBStrategy.ReplayOnBar: invalid bar: %w", err)
	}

	snap := orbState.BuildSnapshot(sym, bar.Time)

	// Delegate to tracker with replay=true — state advances but no signal fires.
	orbState.Tracker.OnBar(domBar, snap, orbState.Config, true)

	// Track phase changes during replay so the first live OnBar emits the snapshot.
	if sess := orbState.Tracker.GetSession(symbol); sess != nil {
		orbState.PrevPhase = "" // Force emit on first live bar by keeping PrevPhase stale
	}

	return orbState, nil
}

// EmitSignalProgress returns the current ORB phase as a domain event payload.
// Implements start.SignalProgressEmitter for post-warmup SSE cache seeding.
func (s *ORBState) EmitSignalProgress() []any {
	sess := s.Tracker.GetSession(s.Symbol)
	if sess == nil || sess.State == "PRE_OPEN" {
		return nil
	}
	breakoutDir := ""
	if sess.Breakout.Confirmed {
		breakoutDir = string(sess.Breakout.Direction)
	}
	breakoutTime := ""
	if !sess.Breakout.BreakBar.IsZero() {
		breakoutTime = sess.Breakout.BreakBar.Format(time.RFC3339)
	}

	payload := domain.ORBPhaseUpdatePayload{
		Symbol: s.Symbol,
		Phase:  string(sess.State),
		Range: domain.ORBPhaseRange{
			High:          sess.OrbHigh,
			Low:           sess.OrbLow,
			Valid:         !sess.RangeInvalid,
			BarCount:      sess.RangeBarCount,
			ExpectedBars:  s.Config.WindowMinutes / max(barDurFromTF(s.Timeframe), 1),
			WindowMinutes: s.Config.WindowMinutes,
		},
		Breakout: domain.ORBPhaseBreakout{
			Direction:  breakoutDir,
			RVOL:       sess.Breakout.RVOL,
			BreakClose: sess.Breakout.BreakClose,
			BreakTime:  breakoutTime,
		},
		Retest: domain.ORBPhaseRetest{
			Touched:        sess.Retest.Touched,
			TouchPrice:     sess.Retest.TouchPrice,
			BarsSinceBreak: sess.BarsSinceBreakout,
			MaxRetestBars:  s.Config.MaxRetestBars,
			HoldConfirmed:  sess.Retest.Confirmed,
		},
		FVG: domain.ORBPhaseFVG{
			Active: sess.ActiveFVG != nil,
		},
	}
	if sess.ActiveFVG != nil {
		payload.FVG.High = sess.ActiveFVG.High
		payload.FVG.Low = sess.ActiveFVG.Low
	}
	return []any{payload}
}

// OnEvent is a no-op for the ORB strategy — it only reacts to bars.
func (s *ORBStrategy) OnEvent(ctx start.Context, symbol string, evt any, st start.State) (start.State, []start.Signal, error) {
	return st, nil, nil
}

// emitPhaseUpdate publishes an ORBPhaseUpdate domain event with the current session snapshot.
func (s *ORBState) emitPhaseUpdate(ctx start.Context, sess *monitor.ORBSession, bar domain.MarketBar, snap domain.IndicatorSnapshot) {
	breakoutDir := ""
	if sess.Breakout.Confirmed {
		breakoutDir = string(sess.Breakout.Direction)
	}
	breakoutTime := ""
	if !sess.Breakout.BreakBar.IsZero() {
		breakoutTime = sess.Breakout.BreakBar.Format(time.RFC3339)
	}

	conf := 0.0
	if sess.Breakout.Confirmed {
		conf = monitor.ORBConfidence(sess, bar, snap, s.Config)
	}

	payload := domain.ORBPhaseUpdatePayload{
		Symbol: s.Symbol,
		Phase:  string(sess.State),
		Range: domain.ORBPhaseRange{
			High:          sess.OrbHigh,
			Low:           sess.OrbLow,
			Valid:         !sess.RangeInvalid,
			BarCount:      sess.RangeBarCount,
			ExpectedBars:  s.Config.WindowMinutes / max(barDurFromTF(s.Timeframe), 1),
			WindowMinutes: s.Config.WindowMinutes,
		},
		Breakout: domain.ORBPhaseBreakout{
			Direction:  breakoutDir,
			RVOL:       sess.Breakout.RVOL,
			BreakClose: sess.Breakout.BreakClose,
			BreakTime:  breakoutTime,
		},
		Retest: domain.ORBPhaseRetest{
			Touched:        sess.Retest.Touched,
			TouchPrice:     sess.Retest.TouchPrice,
			BarsSinceBreak: sess.BarsSinceBreakout,
			MaxRetestBars:  s.Config.MaxRetestBars,
			HoldConfirmed:  sess.Retest.Confirmed,
		},
		Confidence: conf,
		FVG: domain.ORBPhaseFVG{
			Active: sess.ActiveFVG != nil,
		},
		Bar: domain.BarSnapshot{
			Open:   bar.Open,
			High:   bar.High,
			Low:    bar.Low,
			Close:  bar.Close,
			Volume: bar.Volume,
		},
	}
	if sess.ActiveFVG != nil {
		payload.FVG.High = sess.ActiveFVG.High
		payload.FVG.Low = sess.ActiveFVG.Low
	}

	_ = ctx.EmitDomainEvent(payload)
}

// barDurFromTF returns bar duration in minutes for common timeframes.
func barDurFromTF(tf string) int {
	switch tf {
	case "1m":
		return 1
	case "5m":
		return 5
	case "15m":
		return 15
	default:
		return 1
	}
}

// clampStrength ensures confidence is in [0,1].
func clampStrength(c float64) float64 {
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}

// ORBState wraps the ORBTracker and its config as a serializable State.
type ORBState struct {
	Tracker            *monitor.ORBTracker
	Config             monitor.ORBConfig
	Symbol             string
	Timeframe          string              // "1m" or "5m" — set by runner based on instance assignment
	Indicators         start.IndicatorData // cached from last bar
	PrevPhase          monitor.ORBState    // previous phase for change detection (EntryGated events)
	MinConfluenceScore int                 // minimum confluence score for entry. Default 0 (disabled).
}

// SetIndicators updates the cached indicator data. Called by the runner
// before each OnBar to provide pre-computed indicators.
func (s *ORBState) SetIndicators(ind start.IndicatorData) {
	s.Indicators = ind
}

// BuildSnapshot converts cached IndicatorData into a domain.IndicatorSnapshot.
func (s *ORBState) BuildSnapshot(sym domain.Symbol, t time.Time) domain.IndicatorSnapshot {
	snap, _ := domain.NewIndicatorSnapshot(
		t, sym, "1m",
		s.Indicators.RSI,
		s.Indicators.StochK,
		s.Indicators.StochD,
		s.Indicators.EMA9,
		s.Indicators.EMA21,
		s.Indicators.VWAP,
		s.Indicators.Volume,
		s.Indicators.VolumeSMA,
	)
	return snap
}

// orbStateJSON is the JSON wire format for ORBState persistence.
type orbStateJSON struct {
	Symbol     string              `json:"symbol"`
	Config     monitor.ORBConfig   `json:"config"`
	Session    *monitor.ORBSession `json:"session,omitempty"`
	Indicators start.IndicatorData `json:"indicators"`
}

// Marshal serializes the ORBState for persistence/recovery.
func (s *ORBState) Marshal() ([]byte, error) {
	j := orbStateJSON{
		Symbol:     s.Symbol,
		Config:     s.Config,
		Session:    s.Tracker.GetSession(s.Symbol),
		Indicators: s.Indicators,
	}
	return json.Marshal(j)
}

// Unmarshal restores ORBState from persisted bytes.
func (s *ORBState) Unmarshal(data []byte) error {
	var j orbStateJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return fmt.Errorf("ORBState.Unmarshal: %w", err)
	}
	s.Symbol = j.Symbol
	s.Config = j.Config
	s.Indicators = j.Indicators
	s.Tracker = monitor.NewORBTrackerWithSource("strategy")
	// Session restoration: the tracker manages sessions internally.
	// If we had a session snapshot, we'd need to inject it.
	// For now, the ORB range is recoverable via bar replay (existing warmup path).
	return nil
}
