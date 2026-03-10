package builtin

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// BreakRetestStrategy implements the Break & Retest pattern: detect structural
// breakouts with momentum, wait for a retest of the broken level with Fibonacci
// confluence, and enter on engulfing confirmation. Uses market structure (HH/HL
// or LH/LL) for trend direction gating.
type BreakRetestStrategy struct {
	meta start.Meta
}

// NewBreakRetestStrategy creates a new Break & Retest strategy.
func NewBreakRetestStrategy() *BreakRetestStrategy {
	id, _ := start.NewStrategyID("break_retest_v1")
	ver, _ := start.NewVersion("1.0.0")
	return &BreakRetestStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "Break & Retest",
			Description: "Structural breakout with retest confirmation, Fibonacci confluence, and market structure trend filter",
			Author:      "system",
		},
	}
}

func (s *BreakRetestStrategy) Meta() start.Meta { return s.meta }
func (s *BreakRetestStrategy) WarmupBars() int  { return 50 }

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// BreakRetestConfig holds strategy parameters parsed from DNA.
type BreakRetestConfig struct {
	// Pivot detection
	PivotLookback int

	// Breakout momentum
	VolSurgeMult      float64
	BodyRangeRatio    float64
	ATRBreakoutMult   float64
	BreakoutBufferATR float64
	MaxWickRatio      float64

	// Retest
	RetestBandATR    float64
	RetestExpiryBars int
	InvalidationATR  float64

	// Fibonacci
	FibConfluenceATR float64

	// Engulfing
	EngulfBodyMult float64

	// Risk
	StopMode   string
	TP1Mode    string
	TP2Mode    string
	MinRRRatio float64

	// AI
	AIEnabled        bool
	AITimeoutSeconds int
	AIMinConfidence  float64
	SizeMultMin      float64
	SizeMultBase     float64
	SizeMultMax      float64

	// Trade management
	CooldownSeconds int
	MaxTradesPerDay int
}

func parseBreakRetestConfig(params map[string]any) BreakRetestConfig {
	return BreakRetestConfig{
		PivotLookback:     getInt(params, "pivot_lookback", 5),
		VolSurgeMult:      getFloat64(params, "vol_surge_mult", 2.0),
		BodyRangeRatio:    getFloat64(params, "body_range_ratio", 0.7),
		ATRBreakoutMult:   getFloat64(params, "atr_breakout_mult", 1.5),
		BreakoutBufferATR: getFloat64(params, "breakout_buffer_atr", 0.2),
		MaxWickRatio:      getFloat64(params, "max_wick_ratio", 0.3),
		RetestBandATR:     getFloat64(params, "retest_band_atr", 0.15),
		RetestExpiryBars:  getInt(params, "retest_expiry_bars", 20),
		InvalidationATR:   getFloat64(params, "invalidation_atr", 0.5),
		FibConfluenceATR:  getFloat64(params, "fib_confluence_atr", 0.1),
		EngulfBodyMult:    getFloat64(params, "engulf_body_mult", 2.0),
		StopMode:          getString(params, "stop_mode", "retest_low"),
		TP1Mode:           getString(params, "tp1_mode", "breakout_peak"),
		TP2Mode:           getString(params, "tp2_mode", "fib_1618_ext"),
		MinRRRatio:        getFloat64(params, "min_rr_ratio", 2.0),
		AIEnabled:         getBool(params, "ai_enabled", false),
		AITimeoutSeconds:  getInt(params, "ai_timeout_seconds", 5),
		AIMinConfidence:   getFloat64(params, "ai_min_confidence", 0.65),
		SizeMultMin:       getFloat64(params, "size_mult_min", 0.5),
		SizeMultBase:      getFloat64(params, "size_mult_base", 1.0),
		SizeMultMax:       getFloat64(params, "size_mult_max", 1.5),
		CooldownSeconds:   getInt(params, "cooldown_seconds", 300),
		MaxTradesPerDay:   getInt(params, "max_trades_per_day", 5),
	}
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

// BRPhase represents the strategy state machine phase.
type BRPhase int

const (
	BRPhaseIdle              BRPhase = iota // Scanning for structure
	BRPhaseTrending                         // Market structure established
	BRPhaseLevelDetected                    // Potential breakout level identified
	BRPhaseBreakoutConfirmed                // Momentum breakout confirmed
	BRPhaseWaitingRetest                    // Waiting for price to return to level
)

// SwingPoint records a detected pivot high or low.
type SwingPoint struct {
	Price  float64   `json:"price"`
	Time   time.Time `json:"time"`
	IsHigh bool      `json:"is_high"`
}

// BreakRetestState is the per-symbol state for the Break & Retest strategy.
type BreakRetestState struct {
	Symbol     string              `json:"-"`
	Config     BreakRetestConfig   `json:"-"`
	Indicators start.IndicatorData `json:"-"`

	// State machine
	Phase BRPhase

	// Bar history for pivot detection: last (2*N+1) bars.
	RecentBars []start.Bar

	// Market structure: last detected swing points.
	SwingPoints    []SwingPoint
	TrendDirection string // "bullish", "bearish", ""

	// Previous bar (for engulfing detection).
	PrevBar    start.Bar
	HasPrevBar bool

	// Active setup
	BreakoutLevel     float64
	BreakoutSide      start.Side
	BreakoutBar       start.Bar
	BreakoutVolume    float64
	SwingLowOfMove    float64 // swing low before breakout (bullish Fib anchor)
	SwingHighOfMove   float64 // swing high before breakout (bearish Fib anchor)
	BarsSinceBreakout int

	// Risk targets (computed on entry signal)
	StopPrice float64
	TP1Price  float64
	TP2Price  float64

	// Position management
	PositionSide       start.Side
	PendingEntry       start.Side
	PendingEntryAt     time.Time
	PendingAIRequestID string
	LastAIVerdict      string
	LastAIConfidence   float64
	LastAIAt           time.Time
	LastSizeMult       float64
	LastBarClose       float64

	// Trade management
	TradesToday   int
	CooldownUntil time.Time
}

// SetIndicators implements the indicatorSetter interface used by the runner.
func (s *BreakRetestState) SetIndicators(ind start.IndicatorData) {
	s.Indicators = ind
}

// ClearPendingEntry implements the pendingClearer interface used by the runner.
func (s *BreakRetestState) ClearPendingEntry() {
	s.PendingEntry = ""
	s.PendingEntryAt = time.Time{}
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func (s *BreakRetestStrategy) Init(ctx start.Context, symbol string, params map[string]any, prior start.State) (start.State, error) {
	cfg := parseBreakRetestConfig(params)
	st := &BreakRetestState{
		Symbol: symbol,
		Config: cfg,
	}

	if prior != nil {
		if brPrior, ok := prior.(*BreakRetestState); ok {
			// Preserve runtime state, reload config.
			*st = *brPrior
			st.Config = cfg
		} else if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Warn("BreakRetestStrategy: incompatible prior state, starting fresh", "symbol", symbol)
		}
	}

	return st, nil
}

// ---------------------------------------------------------------------------
// ReplayOnBar (warmup without signals)
// ---------------------------------------------------------------------------

func (s *BreakRetestStrategy) ReplayOnBar(_ start.Context, _ string, bar start.Bar, st start.State, indicators start.IndicatorData) (start.State, error) {
	brSt, ok := st.(*BreakRetestState)
	if !ok {
		return st, fmt.Errorf("BreakRetestStrategy.ReplayOnBar: expected *BreakRetestState, got %T", st)
	}
	brSt.Indicators = indicators
	updateBarBuffer(brSt, bar)
	detectPivots(brSt)
	classifyTrend(brSt)
	brSt.PrevBar = bar
	brSt.HasPrevBar = true
	return brSt, nil
}

// ---------------------------------------------------------------------------
// OnBar — main decision logic
// ---------------------------------------------------------------------------

func (s *BreakRetestStrategy) OnBar(ctx start.Context, symbol string, bar start.Bar, st start.State) (start.State, []start.Signal, error) {
	brSt, ok := st.(*BreakRetestState)
	if !ok {
		return st, nil, fmt.Errorf("BreakRetestStrategy.OnBar: expected *BreakRetestState, got %T", st)
	}
	cfg := brSt.Config

	now := bar.Time
	if ctx != nil {
		now = ctx.Now()
	}

	// Pending-entry timeout (5 min, same as other strategies).
	if brSt.PendingEntry != "" && now.Sub(brSt.PendingEntryAt) > 5*time.Minute {
		if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Warn("BreakRetestStrategy: pending entry timed out, resetting",
				"symbol", symbol, "side", brSt.PendingEntry)
		}
		brSt.PendingEntry = ""
		brSt.PendingEntryAt = time.Time{}
	}

	// Cooldown / max trades gate.
	if now.Before(brSt.CooldownUntil) {
		updateBarBuffer(brSt, bar)
		detectPivots(brSt)
		classifyTrend(brSt)
		brSt.PrevBar = bar
		brSt.HasPrevBar = true
		return brSt, nil, nil
	}
	if brSt.TradesToday >= cfg.MaxTradesPerDay {
		updateBarBuffer(brSt, bar)
		detectPivots(brSt)
		classifyTrend(brSt)
		brSt.PrevBar = bar
		brSt.HasPrevBar = true
		return brSt, nil, nil
	}

	// Always update structure (pivots, trend) even if gated.
	updateBarBuffer(brSt, bar)
	detectPivots(brSt)
	classifyTrend(brSt)

	atr := brSt.Indicators.ATR
	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
	cooldown := time.Duration(cfg.CooldownSeconds) * time.Second

	// Regime tag for signal metadata.
	regimeTag := "none"
	if ar, ok2 := brSt.Indicators.AnchorRegimes["5m"]; ok2 {
		regimeTag = ar.Type
	}

	// ----- Exit logic (always checked) -----
	if brSt.PositionSide != "" && brSt.PendingEntry == "" {
		if shouldExit(brSt, bar) {
			exitSide := start.SideSell
			if brSt.PositionSide == start.SideSell {
				exitSide = start.SideBuy
			}
			sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, exitSide, 0.8, map[string]string{
				"ref_price": fmt.Sprintf("%.10f", bar.Close),
				"setup":     "break_retest_exit",
				"reason":    "trend_reversal_or_stop",
				"regime_5m": regimeTag,
			})
			if err != nil {
				return brSt, nil, err
			}
			brSt.PositionSide = ""
			brSt.CooldownUntil = now.Add(cooldown)
			brSt.Phase = BRPhaseIdle
			brSt.PrevBar = bar
			brSt.HasPrevBar = true
			return brSt, []start.Signal{sig}, nil
		}
	}

	// Only entries if flat and no pending entry.
	if brSt.PositionSide != "" || brSt.PendingEntry != "" {
		// Still advance state machine for structure tracking.
		advanceStateMachine(brSt, bar, atr, cfg)
		brSt.PrevBar = bar
		brSt.HasPrevBar = true
		return brSt, nil, nil
	}

	// ----- State machine -----
	advanceStateMachine(brSt, bar, atr, cfg)

	// Check if we've reached entry signal condition.
	if brSt.Phase == BRPhaseWaitingRetest && atr > 0 && brSt.HasPrevBar {
		inRetestZone := isInRetestZone(brSt, bar, atr, cfg)
		hasFibConfluence := checkFibConfluence(brSt, atr, cfg)
		hasEngulfing := checkEngulfing(brSt, bar, cfg)

		if inRetestZone && hasFibConfluence && hasEngulfing {
			// Compute risk targets.
			stopPrice, tp1, tp2 := computeRiskTargets(brSt, bar, atr, cfg)

			// Check minimum R:R ratio.
			risk := math.Abs(bar.Close - stopPrice)
			reward := math.Abs(tp1 - bar.Close)
			if risk > 0 && reward/risk >= cfg.MinRRRatio {
				// Compute signal strength.
				strength := computeStrength(brSt, bar, cfg)
				brSt.StopPrice = stopPrice
				brSt.TP1Price = tp1
				brSt.TP2Price = tp2

				tags := map[string]string{
					"ref_price":      fmt.Sprintf("%.10f", bar.Close),
					"setup":          "break_retest",
					"trend":          brSt.TrendDirection,
					"level":          fmt.Sprintf("%.4f", brSt.BreakoutLevel),
					"fib_confluence": "true",
					"breakout_vol":   fmt.Sprintf("%.1fx", brSt.BreakoutVolume/math.Max(brSt.Indicators.VolumeSMA, 1)),
					"retest_bar":     fmt.Sprintf("%d", brSt.BarsSinceBreakout),
					"stop_price":     fmt.Sprintf("%.10f", stopPrice),
					"tp1_price":      fmt.Sprintf("%.10f", tp1),
					"tp2_price":      fmt.Sprintf("%.10f", tp2),
					"regime_5m":      regimeTag,
				}

				if cfg.AIEnabled {
					reqID := fmt.Sprintf("%s:%s:%s:%d", s.meta.ID, s.meta.Version, symbol, now.UnixNano())
					brSt.PendingAIRequestID = reqID
					tags["ai_requested"] = "true"
					tags["ai_request_id"] = reqID
				}

				sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, brSt.BreakoutSide, strength, tags)
				if err != nil {
					return brSt, nil, err
				}

				brSt.PendingEntry = brSt.BreakoutSide
				brSt.PendingEntryAt = now
				brSt.TradesToday++
				brSt.CooldownUntil = now.Add(cooldown)
				brSt.LastBarClose = bar.Close
				brSt.Phase = BRPhaseIdle // Reset after signal emission.
				brSt.PrevBar = bar
				brSt.HasPrevBar = true
				return brSt, []start.Signal{sig}, nil
			}
		}
	}

	brSt.PrevBar = bar
	brSt.HasPrevBar = true
	return brSt, nil, nil
}

// ---------------------------------------------------------------------------
// OnEvent
// ---------------------------------------------------------------------------

func (s *BreakRetestStrategy) OnEvent(ctx start.Context, symbol string, evt any, st start.State) (start.State, []start.Signal, error) {
	brSt, ok := st.(*BreakRetestState)
	if !ok {
		return st, nil, fmt.Errorf("BreakRetestStrategy.OnEvent: expected *BreakRetestState, got %T", st)
	}

	switch e := evt.(type) {
	case start.FillConfirmation:
		if brSt.PendingEntry != "" {
			brSt.PositionSide = brSt.PendingEntry
			brSt.PendingEntry = ""
			brSt.PendingEntryAt = time.Time{}
			if ctx != nil && ctx.Logger() != nil {
				ctx.Logger().Info("BreakRetestStrategy: fill confirmed, position active",
					"symbol", symbol, "side", brSt.PositionSide, "price", e.Price)
			}
		}
		return brSt, nil, nil

	case start.EntryRejection:
		if brSt.PendingEntry != "" {
			if ctx != nil && ctx.Logger() != nil {
				ctx.Logger().Warn("BreakRetestStrategy: entry rejected, clearing pending",
					"symbol", symbol, "side", brSt.PendingEntry, "reason", e.Reason)
			}
			brSt.PendingEntry = ""
			brSt.PendingEntryAt = time.Time{}
		}
		return brSt, nil, nil

	case AIDebateResult:
		return s.handleAIDebate(ctx, symbol, e, brSt)

	default:
		return brSt, nil, nil
	}
}

func (s *BreakRetestStrategy) handleAIDebate(ctx start.Context, symbol string, result AIDebateResult, brSt *BreakRetestState) (start.State, []start.Signal, error) {
	if result.RequestID != brSt.PendingAIRequestID {
		return brSt, nil, nil
	}

	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
	now := time.Now()
	if ctx != nil {
		now = ctx.Now()
	}

	brSt.PendingAIRequestID = ""
	brSt.LastAIVerdict = result.Verdict
	brSt.LastAIConfidence = result.Confidence
	brSt.LastAIAt = now

	switch result.Verdict {
	case "veto":
		exitSide := start.SideSell
		if brSt.PositionSide == start.SideSell {
			exitSide = start.SideBuy
		}
		sig, err := start.NewSignal(instanceID, symbol, start.SignalFlat, exitSide, 0.9, map[string]string{
			"ref_price":  fmt.Sprintf("%.10f", brSt.LastBarClose),
			"setup":      "break_retest_ai_veto",
			"ai_verdict": "veto",
			"ai_conf":    fmt.Sprintf("%.2f", result.Confidence),
		})
		if err != nil {
			return brSt, nil, err
		}
		brSt.PositionSide = ""
		return brSt, []start.Signal{sig}, nil

	case "bull", "bear":
		agrees := (result.Verdict == "bull" && brSt.PositionSide == start.SideBuy) ||
			(result.Verdict == "bear" && brSt.PositionSide == start.SideSell)
		var sizeMult float64
		if agrees {
			sizeMult = brSt.Config.SizeMultMax
		} else {
			sizeMult = brSt.Config.SizeMultMin
		}
		brSt.LastSizeMult = sizeMult

		sig, err := start.NewSignal(instanceID, symbol, start.SignalAdjust, brSt.PositionSide, 0.7, map[string]string{
			"ref_price":  fmt.Sprintf("%.10f", brSt.LastBarClose),
			"setup":      "break_retest_ai_adjust",
			"ai_verdict": result.Verdict,
			"ai_conf":    fmt.Sprintf("%.2f", result.Confidence),
			"size_mult":  fmt.Sprintf("%.2f", sizeMult),
		})
		if err != nil {
			return brSt, nil, err
		}
		return brSt, []start.Signal{sig}, nil

	default:
		return brSt, nil, nil
	}
}

// ---------------------------------------------------------------------------
// Internal algorithms
// ---------------------------------------------------------------------------

// updateBarBuffer appends a bar and trims to the required window size.
func updateBarBuffer(st *BreakRetestState, bar start.Bar) {
	st.RecentBars = append(st.RecentBars, bar)
	maxBars := 2*st.Config.PivotLookback + 1
	if maxBars < 20 {
		maxBars = 20 // keep a reasonable history
	}
	if len(st.RecentBars) > maxBars {
		st.RecentBars = st.RecentBars[len(st.RecentBars)-maxBars:]
	}
}

// detectPivots uses N-bar pivot detection on the bar buffer.
// A swing high at index i requires: bar[i].High > all bars within N bars left and right.
func detectPivots(st *BreakRetestState) {
	n := st.Config.PivotLookback
	bars := st.RecentBars
	if len(bars) < 2*n+1 {
		return
	}

	// Check the bar at position len-n-1 (the center of the lookback window).
	idx := len(bars) - n - 1

	// Check swing high.
	if isPivotHigh(bars, idx, n) {
		sp := SwingPoint{Price: bars[idx].High, Time: bars[idx].Time, IsHigh: true}
		// Avoid duplicate (same time).
		if len(st.SwingPoints) == 0 || !st.SwingPoints[len(st.SwingPoints)-1].Time.Equal(sp.Time) {
			st.SwingPoints = append(st.SwingPoints, sp)
		}
	}

	// Check swing low.
	if isPivotLow(bars, idx, n) {
		sp := SwingPoint{Price: bars[idx].Low, Time: bars[idx].Time, IsHigh: false}
		if len(st.SwingPoints) == 0 || !st.SwingPoints[len(st.SwingPoints)-1].Time.Equal(sp.Time) {
			st.SwingPoints = append(st.SwingPoints, sp)
		}
	}

	// Trim to last 10 swing points.
	if len(st.SwingPoints) > 10 {
		st.SwingPoints = st.SwingPoints[len(st.SwingPoints)-10:]
	}
}

func isPivotHigh(bars []start.Bar, idx, n int) bool {
	if idx < n || idx >= len(bars)-n {
		return false
	}
	target := bars[idx].High
	for j := 1; j <= n; j++ {
		if bars[idx-j].High >= target || bars[idx+j].High > target {
			return false
		}
	}
	return true
}

func isPivotLow(bars []start.Bar, idx, n int) bool {
	if idx < n || idx >= len(bars)-n {
		return false
	}
	target := bars[idx].Low
	for j := 1; j <= n; j++ {
		if bars[idx-j].Low <= target || bars[idx+j].Low < target {
			return false
		}
	}
	return true
}

// classifyTrend determines market structure from swing points.
// Bullish: ascending swing lows AND ascending swing highs.
// Bearish: descending swing highs AND descending swing lows.
func classifyTrend(st *BreakRetestState) {
	highs, lows := separateSwings(st.SwingPoints)
	if len(highs) >= 2 && len(lows) >= 2 {
		hh := highs[len(highs)-1].Price > highs[len(highs)-2].Price
		hl := lows[len(lows)-1].Price > lows[len(lows)-2].Price
		lh := highs[len(highs)-1].Price < highs[len(highs)-2].Price
		ll := lows[len(lows)-1].Price < lows[len(lows)-2].Price

		if hh && hl {
			st.TrendDirection = "bullish"
			return
		}
		if lh && ll {
			st.TrendDirection = "bearish"
			return
		}
	}
	st.TrendDirection = ""
}

func separateSwings(points []SwingPoint) (highs, lows []SwingPoint) {
	for _, p := range points {
		if p.IsHigh {
			highs = append(highs, p)
		} else {
			lows = append(lows, p)
		}
	}
	return
}

// advanceStateMachine handles phase transitions.
func advanceStateMachine(st *BreakRetestState, bar start.Bar, atr float64, cfg BreakRetestConfig) {
	switch st.Phase {
	case BRPhaseIdle:
		if st.TrendDirection != "" {
			st.Phase = BRPhaseTrending
		}

	case BRPhaseTrending:
		if st.TrendDirection == "" {
			st.Phase = BRPhaseIdle
			return
		}
		// Look for the latest relevant swing point as a potential breakout level.
		_, lows := separateSwings(st.SwingPoints)
		highs, _ := separateSwings(st.SwingPoints)
		if st.TrendDirection == "bullish" && len(highs) > 0 {
			st.BreakoutLevel = highs[len(highs)-1].Price
			st.BreakoutSide = start.SideBuy
			if len(lows) > 0 {
				st.SwingLowOfMove = lows[len(lows)-1].Price
			}
			st.Phase = BRPhaseLevelDetected
		} else if st.TrendDirection == "bearish" && len(lows) > 0 {
			st.BreakoutLevel = lows[len(lows)-1].Price
			st.BreakoutSide = start.SideSell
			if len(highs) > 0 {
				st.SwingHighOfMove = highs[len(highs)-1].Price
			}
			st.Phase = BRPhaseLevelDetected
		}

	case BRPhaseLevelDetected:
		if st.TrendDirection == "" {
			st.Phase = BRPhaseIdle
			return
		}
		if atr <= 0 {
			return
		}
		// Check for momentum breakout.
		if checkMomentumBreakout(st, bar, atr, cfg) {
			st.BreakoutBar = bar
			st.BreakoutVolume = bar.Volume
			st.BarsSinceBreakout = 0
			st.Phase = BRPhaseBreakoutConfirmed
		}

	case BRPhaseBreakoutConfirmed:
		st.BarsSinceBreakout++
		// Transition to waiting once price has moved away from level.
		if st.BreakoutSide == start.SideBuy && bar.Close > st.BreakoutLevel {
			st.Phase = BRPhaseWaitingRetest
		} else if st.BreakoutSide == start.SideSell && bar.Close < st.BreakoutLevel {
			st.Phase = BRPhaseWaitingRetest
		}
		// Invalidation: if price immediately reverses back.
		if atr > 0 {
			if st.BreakoutSide == start.SideBuy && bar.Close < st.BreakoutLevel-cfg.InvalidationATR*atr {
				st.Phase = BRPhaseIdle
			} else if st.BreakoutSide == start.SideSell && bar.Close > st.BreakoutLevel+cfg.InvalidationATR*atr {
				st.Phase = BRPhaseIdle
			}
		}

	case BRPhaseWaitingRetest:
		st.BarsSinceBreakout++
		// Expiry check.
		if st.BarsSinceBreakout > cfg.RetestExpiryBars {
			st.Phase = BRPhaseIdle
			return
		}
		// Invalidation: deep retrace past level.
		if atr > 0 {
			if st.BreakoutSide == start.SideBuy && bar.Close < st.BreakoutLevel-cfg.InvalidationATR*atr {
				st.Phase = BRPhaseIdle
			} else if st.BreakoutSide == start.SideSell && bar.Close > st.BreakoutLevel+cfg.InvalidationATR*atr {
				st.Phase = BRPhaseIdle
			}
		}
		// Entry condition is checked in OnBar after advanceStateMachine.
	}
}

// checkMomentumBreakout validates a breakout candle has sufficient energy.
func checkMomentumBreakout(st *BreakRetestState, bar start.Bar, atr float64, cfg BreakRetestConfig) bool {
	bodySize := math.Abs(bar.Close - bar.Open)
	barRange := bar.High - bar.Low
	if barRange <= 0 {
		return false
	}

	// Body-to-range ratio.
	if bodySize/barRange < cfg.BodyRangeRatio {
		return false
	}

	// ATR multiple.
	if barRange < cfg.ATRBreakoutMult*atr {
		return false
	}

	// Volume surge.
	if st.Indicators.VolumeSMA > 0 && bar.Volume < cfg.VolSurgeMult*st.Indicators.VolumeSMA {
		return false
	}

	// Wick ratio filter (liquidity grab protection).
	wickSize := barRange - bodySize
	if wickSize/barRange > cfg.MaxWickRatio {
		return false
	}

	// Direction-specific close check.
	if st.BreakoutSide == start.SideBuy {
		if bar.Close <= st.BreakoutLevel+cfg.BreakoutBufferATR*atr {
			return false
		}
		// Must be a bullish candle.
		if bar.Close <= bar.Open {
			return false
		}
	} else {
		if bar.Close >= st.BreakoutLevel-cfg.BreakoutBufferATR*atr {
			return false
		}
		// Must be a bearish candle.
		if bar.Close >= bar.Open {
			return false
		}
	}

	return true
}

// isInRetestZone checks if price has returned to the breakout level proximity band.
func isInRetestZone(st *BreakRetestState, bar start.Bar, atr float64, cfg BreakRetestConfig) bool {
	band := cfg.RetestBandATR * atr
	if st.BreakoutSide == start.SideBuy {
		// For bullish: price should dip near the level (old resistance, now support).
		return bar.Low <= st.BreakoutLevel+band && bar.Low >= st.BreakoutLevel-band
	}
	// For bearish: price should rally near the level (old support, now resistance).
	return bar.High >= st.BreakoutLevel-band && bar.High <= st.BreakoutLevel+band
}

// checkFibConfluence checks if the Fibonacci golden pocket aligns with the breakout level.
func checkFibConfluence(st *BreakRetestState, atr float64, cfg BreakRetestConfig) bool {
	tolerance := cfg.FibConfluenceATR * atr
	if tolerance <= 0 {
		return false
	}

	if st.BreakoutSide == start.SideBuy {
		swingLow := st.SwingLowOfMove
		swingHigh := st.BreakoutBar.High
		if swingHigh <= swingLow {
			return false
		}
		fib50 := swingHigh - (swingHigh-swingLow)*0.5
		fib618 := swingHigh - (swingHigh-swingLow)*0.618
		// Golden pocket: either 0.5 or 0.618 near the breakout level.
		if math.Abs(fib50-st.BreakoutLevel) <= tolerance || math.Abs(fib618-st.BreakoutLevel) <= tolerance {
			return true
		}
	} else {
		swingHigh := st.SwingHighOfMove
		swingLow := st.BreakoutBar.Low
		if swingHigh <= swingLow {
			return false
		}
		fib50 := swingLow + (swingHigh-swingLow)*0.5
		fib618 := swingLow + (swingHigh-swingLow)*0.618
		if math.Abs(fib50-st.BreakoutLevel) <= tolerance || math.Abs(fib618-st.BreakoutLevel) <= tolerance {
			return true
		}
	}

	return false
}

// checkEngulfing detects bullish or bearish engulfing pattern.
func checkEngulfing(st *BreakRetestState, bar start.Bar, cfg BreakRetestConfig) bool {
	if !st.HasPrevBar {
		return false
	}
	prev := st.PrevBar

	prevBody := math.Abs(prev.Close - prev.Open)
	currBody := math.Abs(bar.Close - bar.Open)

	if prevBody <= 0 {
		return currBody > 0 // Allow if previous was a doji.
	}

	// Minimum body size requirement.
	if currBody < cfg.EngulfBodyMult*prevBody {
		return false
	}

	if st.BreakoutSide == start.SideBuy {
		// Bullish engulfing: prev bearish, curr bullish, curr engulfs prev.
		prevBearish := prev.Close < prev.Open
		currBullish := bar.Close > bar.Open
		engulfs := bar.Close > prev.Open && bar.Open < prev.Close
		return prevBearish && currBullish && engulfs
	}

	// Bearish engulfing: prev bullish, curr bearish, curr engulfs prev.
	prevBullish := prev.Close > prev.Open
	currBearish := bar.Close < bar.Open
	engulfs := bar.Close < prev.Open && bar.Open > prev.Close
	return prevBullish && currBearish && engulfs
}

// shouldExit checks if an open position should be closed.
func shouldExit(st *BreakRetestState, bar start.Bar) bool {
	// Exit if trend direction flipped.
	if st.PositionSide == start.SideBuy && st.TrendDirection == "bearish" {
		return true
	}
	if st.PositionSide == start.SideSell && st.TrendDirection == "bullish" {
		return true
	}
	// Exit if price hit stop level.
	if st.StopPrice > 0 {
		if st.PositionSide == start.SideBuy && bar.Close <= st.StopPrice {
			return true
		}
		if st.PositionSide == start.SideSell && bar.Close >= st.StopPrice {
			return true
		}
	}
	return false
}

// computeRiskTargets calculates stop loss, TP1, and TP2.
func computeRiskTargets(st *BreakRetestState, bar start.Bar, atr float64, cfg BreakRetestConfig) (stop, tp1, tp2 float64) {
	if st.BreakoutSide == start.SideBuy {
		// Stop: below retest candle low (or Fib 0.786 or breakout origin).
		switch cfg.StopMode {
		case "fib_786":
			swingHigh := st.BreakoutBar.High
			swingLow := st.SwingLowOfMove
			if swingHigh > swingLow {
				stop = swingHigh - (swingHigh-swingLow)*0.786
			} else {
				stop = bar.Low - 0.5*atr
			}
		case "breakout_origin":
			stop = st.SwingLowOfMove
		default: // "retest_low"
			stop = bar.Low - 0.1*atr
		}

		// TP1: breakout peak.
		tp1 = st.BreakoutBar.High

		// TP2: 1.618 Fibonacci extension.
		moveSize := st.BreakoutBar.High - st.SwingLowOfMove
		tp2 = st.BreakoutBar.High + moveSize*0.618
	} else {
		switch cfg.StopMode {
		case "fib_786":
			swingHigh := st.SwingHighOfMove
			swingLow := st.BreakoutBar.Low
			if swingHigh > swingLow {
				stop = swingLow + (swingHigh-swingLow)*0.786
			} else {
				stop = bar.High + 0.5*atr
			}
		case "breakout_origin":
			stop = st.SwingHighOfMove
		default: // "retest_low"
			stop = bar.High + 0.1*atr
		}

		tp1 = st.BreakoutBar.Low

		moveSize := st.SwingHighOfMove - st.BreakoutBar.Low
		tp2 = st.BreakoutBar.Low - moveSize*0.618
	}

	return stop, tp1, tp2
}

// computeStrength calculates signal confidence based on confluence factors.
func computeStrength(st *BreakRetestState, bar start.Bar, cfg BreakRetestConfig) float64 {
	strength := 0.6

	// Bonus for strong volume.
	if st.Indicators.VolumeSMA > 0 && st.BreakoutVolume > 3*st.Indicators.VolumeSMA {
		strength += 0.1
	}

	// Bonus for strong engulfing.
	if st.HasPrevBar {
		prevBody := math.Abs(st.PrevBar.Close - st.PrevBar.Open)
		currBody := math.Abs(bar.Close - bar.Open)
		if prevBody > 0 && currBody > 3*prevBody {
			strength += 0.1
		}
	}

	// Bonus for quick retest (within 10 bars).
	if st.BarsSinceBreakout <= 10 {
		strength += 0.1
	}

	if strength > 1.0 {
		strength = 1.0
	}
	return strength
}

// ---------------------------------------------------------------------------
// Serialization
// ---------------------------------------------------------------------------

type breakRetestStateJSON struct {
	Symbol             string              `json:"symbol"`
	Phase              BRPhase             `json:"phase"`
	RecentBars         []barJSON           `json:"recent_bars"`
	SwingPoints        []SwingPoint        `json:"swing_points"`
	TrendDirection     string              `json:"trend_direction"`
	PrevBar            barJSON             `json:"prev_bar"`
	HasPrevBar         bool                `json:"has_prev_bar"`
	BreakoutLevel      float64             `json:"breakout_level"`
	BreakoutSide       start.Side          `json:"breakout_side"`
	BreakoutBar        barJSON             `json:"breakout_bar"`
	BreakoutVolume     float64             `json:"breakout_volume"`
	SwingLowOfMove     float64             `json:"swing_low_of_move"`
	SwingHighOfMove    float64             `json:"swing_high_of_move"`
	BarsSinceBreakout  int                 `json:"bars_since_breakout"`
	StopPrice          float64             `json:"stop_price"`
	TP1Price           float64             `json:"tp1_price"`
	TP2Price           float64             `json:"tp2_price"`
	PositionSide       start.Side          `json:"position_side"`
	PendingEntry       start.Side          `json:"pending_entry"`
	PendingEntryAt     time.Time           `json:"pending_entry_at"`
	PendingAIRequestID string              `json:"pending_ai_request_id"`
	LastAIVerdict      string              `json:"last_ai_verdict"`
	LastAIConfidence   float64             `json:"last_ai_confidence"`
	LastAIAt           time.Time           `json:"last_ai_at"`
	LastSizeMult       float64             `json:"last_size_mult"`
	LastBarClose       float64             `json:"last_bar_close"`
	TradesToday        int                 `json:"trades_today"`
	CooldownUntil      time.Time           `json:"cooldown_until"`
	Indicators         start.IndicatorData `json:"indicators"`
}

type barJSON struct {
	Time   time.Time `json:"time"`
	Open   float64   `json:"open"`
	High   float64   `json:"high"`
	Low    float64   `json:"low"`
	Close  float64   `json:"close"`
	Volume float64   `json:"volume"`
}

func barToJSON(b start.Bar) barJSON {
	return barJSON{Time: b.Time, Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: b.Volume}
}

func jsonToBar(j barJSON) start.Bar {
	return start.Bar{Time: j.Time, Open: j.Open, High: j.High, Low: j.Low, Close: j.Close, Volume: j.Volume}
}

func (s *BreakRetestState) Marshal() ([]byte, error) {
	recentBars := make([]barJSON, len(s.RecentBars))
	for i, b := range s.RecentBars {
		recentBars[i] = barToJSON(b)
	}

	j := breakRetestStateJSON{
		Symbol:             s.Symbol,
		Phase:              s.Phase,
		RecentBars:         recentBars,
		SwingPoints:        s.SwingPoints,
		TrendDirection:     s.TrendDirection,
		PrevBar:            barToJSON(s.PrevBar),
		HasPrevBar:         s.HasPrevBar,
		BreakoutLevel:      s.BreakoutLevel,
		BreakoutSide:       s.BreakoutSide,
		BreakoutBar:        barToJSON(s.BreakoutBar),
		BreakoutVolume:     s.BreakoutVolume,
		SwingLowOfMove:     s.SwingLowOfMove,
		SwingHighOfMove:    s.SwingHighOfMove,
		BarsSinceBreakout:  s.BarsSinceBreakout,
		StopPrice:          s.StopPrice,
		TP1Price:           s.TP1Price,
		TP2Price:           s.TP2Price,
		PositionSide:       s.PositionSide,
		PendingEntry:       s.PendingEntry,
		PendingEntryAt:     s.PendingEntryAt,
		PendingAIRequestID: s.PendingAIRequestID,
		LastAIVerdict:      s.LastAIVerdict,
		LastAIConfidence:   s.LastAIConfidence,
		LastAIAt:           s.LastAIAt,
		LastSizeMult:       s.LastSizeMult,
		LastBarClose:       s.LastBarClose,
		TradesToday:        s.TradesToday,
		CooldownUntil:      s.CooldownUntil,
		Indicators:         s.Indicators,
	}
	return json.Marshal(j)
}

func (s *BreakRetestState) Unmarshal(data []byte) error {
	var j breakRetestStateJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return fmt.Errorf("BreakRetestState.Unmarshal: %w", err)
	}

	s.Symbol = j.Symbol
	s.Phase = j.Phase
	s.SwingPoints = j.SwingPoints
	s.TrendDirection = j.TrendDirection
	s.PrevBar = jsonToBar(j.PrevBar)
	s.HasPrevBar = j.HasPrevBar
	s.BreakoutLevel = j.BreakoutLevel
	s.BreakoutSide = j.BreakoutSide
	s.BreakoutBar = jsonToBar(j.BreakoutBar)
	s.BreakoutVolume = j.BreakoutVolume
	s.SwingLowOfMove = j.SwingLowOfMove
	s.SwingHighOfMove = j.SwingHighOfMove
	s.BarsSinceBreakout = j.BarsSinceBreakout
	s.StopPrice = j.StopPrice
	s.TP1Price = j.TP1Price
	s.TP2Price = j.TP2Price
	s.PositionSide = j.PositionSide
	s.PendingEntry = j.PendingEntry
	s.PendingEntryAt = j.PendingEntryAt
	s.PendingAIRequestID = j.PendingAIRequestID
	s.LastAIVerdict = j.LastAIVerdict
	s.LastAIConfidence = j.LastAIConfidence
	s.LastAIAt = j.LastAIAt
	s.LastSizeMult = j.LastSizeMult
	s.LastBarClose = j.LastBarClose
	s.TradesToday = j.TradesToday
	s.CooldownUntil = j.CooldownUntil
	s.Indicators = j.Indicators

	s.RecentBars = make([]start.Bar, len(j.RecentBars))
	for i, bj := range j.RecentBars {
		s.RecentBars[i] = jsonToBar(bj)
	}

	return nil
}
