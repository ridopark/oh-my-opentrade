// Package builtin contains compiled-in strategy implementations
// that wrap existing trading logic behind the Strategy interface.
package builtin

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// ORBStrategy wraps the existing ORBTracker as a Strategy implementation.
// It delegates bar processing to ORBTracker and converts SetupConditions
// into strategy Signals.
type ORBStrategy struct {
	meta strat.Meta
}

// NewORBStrategy creates a new ORB Break & Retest strategy.
func NewORBStrategy() *ORBStrategy {
	id, _ := strat.NewStrategyID("orb_break_retest")
	ver, _ := strat.NewVersion("1.0.0")
	return &ORBStrategy{
		meta: strat.Meta{
			ID:          id,
			Version:     ver,
			Name:        "ORB Break & Retest",
			Description: "Opening Range Breakout — Break & Retest with volume confirmation",
			Author:      "system",
		},
	}
}

func (s *ORBStrategy) Meta() strat.Meta { return s.meta }
func (s *ORBStrategy) WarmupBars() int  { return 30 } // ORB window minutes

// Init creates initial state for a symbol. If prior state is provided and
// compatible, it restores from that state (restart recovery).
func (s *ORBStrategy) Init(ctx strat.Context, symbol string, params map[string]any, prior strat.State) (strat.State, error) {
	cfg := monitor.NewORBConfigFromDNA(params)
	tracker := monitor.NewORBTracker()

	st := &ORBState{
		Tracker: tracker,
		Config:  cfg,
		Symbol:  symbol,
	}

	// Attempt to restore from prior state if available.
	if prior != nil {
		if orbPrior, ok := prior.(*ORBState); ok {
			// Reuse the tracker with its session state intact.
			st.Tracker = orbPrior.Tracker
			st.Config = cfg // Use new config (may have been updated).
		} else {
			// Incompatible state type — start fresh. Log if possible.
			if ctx != nil && ctx.Logger() != nil {
				ctx.Logger().Warn("ORBStrategy: incompatible prior state, starting fresh",
					"symbol", symbol)
			}
		}
	}

	return st, nil
}

// OnBar processes a market bar through the ORBTracker and converts any
// detected setup condition into a Signal.
func (s *ORBStrategy) OnBar(ctx strat.Context, symbol string, bar strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
	orbState, ok := st.(*ORBState)
	if !ok {
		return st, nil, fmt.Errorf("ORBStrategy.OnBar: expected *ORBState, got %T", st)
	}

	// Convert strategy.Bar → domain.MarketBar for the ORBTracker.
	sym, err := domain.NewSymbol(symbol)
	if err != nil {
		return st, nil, fmt.Errorf("ORBStrategy.OnBar: invalid symbol: %w", err)
	}
	domBar, err := domain.NewMarketBar(bar.Time, sym, "1m", bar.Open, bar.High, bar.Low, bar.Close, bar.Volume)
	if err != nil {
		return st, nil, fmt.Errorf("ORBStrategy.OnBar: invalid bar: %w", err)
	}

	// Build indicator snapshot from ORBState's cached indicators.
	// The ORBTracker primarily needs Volume and VolumeSMA for RVOL calculation.
	snap := orbState.BuildSnapshot(sym, bar.Time)

	// Delegate to the underlying ORBTracker.
	setup, detected := orbState.Tracker.OnBar(domBar, snap, orbState.Config, false)
	if !detected || setup == nil {
		return orbState, nil, nil
	}

	// Anchor regime gating: suppress entry if 5m anchor regime is REVERSAL.
	// TREND and BALANCE allow signals; nil/empty AnchorRegimes = no gating (backward compat).
	anchorTag := "none"
	if ar, ok := orbState.Indicators.AnchorRegimes["5m"]; ok {
		anchorTag = ar.Type
		if ar.Type == "REVERSAL" {
			return orbState, nil, nil
		}
	}

	// Convert SetupCondition → Signal.
	instanceID, _ := strat.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
	side := strat.SideBuy
	if setup.Direction == domain.DirectionShort {
		side = strat.SideSell
	}

	sig, err := strat.NewSignal(
		instanceID,
		symbol,
		strat.SignalEntry,
		side,
		clampStrength(setup.Confidence),
		map[string]string{
			"ref_price":    fmt.Sprintf("%.4f", setup.BarClose),
			"trigger":      setup.Trigger,
			"orb_high":     fmt.Sprintf("%.4f", setup.ORBHigh),
			"orb_low":      fmt.Sprintf("%.4f", setup.ORBLow),
			"rvol":         fmt.Sprintf("%.2f", setup.RVOL),
			"bar_close":    fmt.Sprintf("%.4f", setup.BarClose),
			"regime_anchor": anchorTag,
		},
	)
	if err != nil {
		return orbState, nil, fmt.Errorf("ORBStrategy.OnBar: signal creation failed: %w", err)
	}

	return orbState, []strat.Signal{sig}, nil
}

// OnEvent is a no-op for the ORB strategy — it only reacts to bars.
func (s *ORBStrategy) OnEvent(ctx strat.Context, symbol string, evt any, st strat.State) (strat.State, []strat.Signal, error) {
	return st, nil, nil
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
	Tracker    *monitor.ORBTracker
	Config     monitor.ORBConfig
	Symbol     string
	Indicators strat.IndicatorData // cached from last bar
}

// SetIndicators updates the cached indicator data. Called by the runner
// before each OnBar to provide pre-computed indicators.
func (s *ORBState) SetIndicators(ind strat.IndicatorData) {
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
	Indicators strat.IndicatorData `json:"indicators"`
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
	s.Tracker = monitor.NewORBTracker()
	// Session restoration: the tracker manages sessions internally.
	// If we had a session snapshot, we'd need to inject it.
	// For now, the ORB range is recoverable via bar replay (existing warmup path).
	return nil
}
