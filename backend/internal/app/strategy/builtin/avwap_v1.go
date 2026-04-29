package builtin

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// AVWAPStrategy implements breakout and bounce entries anchored to VWAP levels.
type AVWAPStrategy struct {
	meta start.Meta

	// holdReasons captures the first entry gate that blocked a symbol on
	// the current bar so the dashboard can render "why HOLD" in the last-
	// decision panel. Written by emitEarlyGated / emitEntryGated, cleared
	// on entry signal emission. sync.Map keeps reads lock-free on the
	// liveness snapshot path that races with per-bar OnBar writes across
	// parallel-symbol evaluation.
	holdReasons sync.Map // map[string]*domain.DecisionReason
}

// recordHoldReason stores the current HOLD rationale for symbol. Called by
// every gated early-return so LastHoldReason can surface the blocking gate
// to the liveness telemetry stream.
func (s *AVWAPStrategy) recordHoldReason(symbol, gate, detail string, extraTags map[string]string) {
	if s == nil || symbol == "" {
		return
	}
	tags := map[string]string{"gate": gate}
	for k, v := range extraTags {
		tags[k] = v
	}
	summary := detail
	if summary == "" {
		summary = gate
	}
	s.holdReasons.Store(symbol, &domain.DecisionReason{
		At:      time.Now().UTC(),
		Outcome: "HOLD",
		Summary: summary,
		Tags:    tags,
	})
}

// clearHoldReason drops the per-symbol HOLD rationale on entry-signal fire.
func (s *AVWAPStrategy) clearHoldReason(symbol string) {
	if s == nil {
		return
	}
	s.holdReasons.Delete(symbol)
}

// LastHoldReason implements the HoldReasoner interface used by the runner's
// liveness tracker to populate DecisionReason.Summary on zero-signal bars.
func (s *AVWAPStrategy) LastHoldReason(symbol string) *domain.DecisionReason {
	if s == nil {
		return nil
	}
	v, ok := s.holdReasons.Load(symbol)
	if !ok {
		return nil
	}
	r, _ := v.(*domain.DecisionReason)
	return r
}

// NewAVWAPStrategy creates a new AVWAP Breakout/Bounce strategy.
func NewAVWAPStrategy() *AVWAPStrategy {
	id, _ := start.NewStrategyID("avwap")
	ver, _ := start.NewVersion("1.0.0")
	return &AVWAPStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "AVWAP Breakout/Bounce",
			Description: "Anchored VWAP breakout and bounce strategy with regime gating",
			Author:      "system",
		},
	}
}

func (s *AVWAPStrategy) Meta() start.Meta { return s.meta }
func (s *AVWAPStrategy) WarmupBars() int  { return 30 }
func (s *AVWAPStrategy) ReplayOnBar(_ start.Context, _ string, bar start.Bar, st start.State, indicators start.IndicatorData) (start.State, error) {
	avwapSt, ok := st.(*AVWAPState)
	if !ok {
		return st, fmt.Errorf("AVWAPStrategy.ReplayOnBar: expected *AVWAPState, got %T", st)
	}
	avwapSt.Indicators = indicators
	avwapSt.Calc.Update(bar.Time, bar.High, bar.Low, bar.Close, bar.Volume)
	avwapSt.CalcBarCount++

	cap := avwapSt.Config.HigherLowsBars
	if cap < 2 {
		cap = 3
	}
	avwapSt.RecentLows = append(avwapSt.RecentLows, bar.Low)
	if len(avwapSt.RecentLows) > cap {
		avwapSt.RecentLows = avwapSt.RecentLows[len(avwapSt.RecentLows)-cap:]
	}
	avwapSt.RecentHighs = append(avwapSt.RecentHighs, bar.High)
	if len(avwapSt.RecentHighs) > cap {
		avwapSt.RecentHighs = avwapSt.RecentHighs[len(avwapSt.RecentHighs)-cap:]
	}

	// Shift prev bars ring buffer
	avwapSt.PrevBars[1] = avwapSt.PrevBars[0]
	avwapSt.PrevBars[0] = bar
	if avwapSt.PrevBarCount < 2 {
		avwapSt.PrevBarCount++
	}

	// Rolling 50-bar high/low window for Fibonacci
	avwapSt.BarHighs50 = append(avwapSt.BarHighs50, bar.High)
	if len(avwapSt.BarHighs50) > 50 {
		avwapSt.BarHighs50 = avwapSt.BarHighs50[1:]
	}
	avwapSt.BarLows50 = append(avwapSt.BarLows50, bar.Low)
	if len(avwapSt.BarLows50) > 50 {
		avwapSt.BarLows50 = avwapSt.BarLows50[1:]
	}

	// Update AboveCount/BelowCount so breakout hold_bars are warm after restart.
	avwapValues := avwapSt.Calc.Values()
	if avwapSt.AVWAPDistHistory == nil {
		avwapSt.AVWAPDistHistory = make(map[string][]float64)
	}
	for _, anchorName := range avwapSt.Calc.SortedNames() {
		avwapValue := avwapValues[anchorName]
		switch {
		case bar.Close > avwapValue:
			avwapSt.AboveCount[anchorName]++
			avwapSt.BelowCount[anchorName] = 0
		case bar.Close < avwapValue:
			avwapSt.BelowCount[anchorName]++
			avwapSt.AboveCount[anchorName] = 0
		default:
			avwapSt.AboveCount[anchorName] = 0
			avwapSt.BelowCount[anchorName] = 0
		}
		dist := (bar.Close - avwapValue) / avwapValue * 10000.0
		hist := avwapSt.AVWAPDistHistory[anchorName]
		hist = append(hist, dist)
		if len(hist) > 10 {
			hist = hist[len(hist)-10:]
		}
		avwapSt.AVWAPDistHistory[anchorName] = hist
	}

	return avwapSt, nil
}

// AVWAPConfig holds strategy parameters parsed from DNA.
type AVWAPConfig struct {
	BreakoutEnabled   bool
	HoldBars          int
	VolumeMult        float64
	BounceEnabled     bool
	RSIBounceMax      float64
	RSIBounceMin      float64
	ExitHoldBars      int
	CooldownSeconds   int
	MaxTradesPerDay   int
	AllowRegimes      []string
	Direction         string
	RequireHigherLows bool
	HigherLowsBars    int
	MiddayTrapShield  bool
	MiddayVolumeMult  float64
	AssetClass        string
	PDRangeMode       string // "RTH" (default) or "24H" — controls pd_high/pd_low bar filtering
	Anchors           []string

	MinSlopeBPS                  float64 // minimum AVWAP slope for entries (bps/bar)
	SlopeLookback                int     // number of bars for slope calculation
	RequireCapitulationForShorts bool

	EnforceAVWAPBias     bool
	PullbackEnabled      bool
	PullbackTrendBars    int
	PullbackToleranceBPS int
	PullbackRSIMin       float64
	PullbackRSIMax       float64

	AVWAPStopEnabled   bool
	AVWAPStopBars      int // consecutive bars on wrong side of AVWAP to trigger exit
	AVWAPStopBufferBPS int // buffer in bps before triggering (avoid noise)

	PinchEnabled bool
	PinchMinBPS  int // minimum gap between two AVWAPs (bps)
	PinchMaxBPS  int // maximum gap — if too wide, not a squeeze

	GapReclaimEnabled bool
	GapReclaimBars    int // max bars since crossing below AVWAP to still consider a reclaim

	HandoffEnabled    bool
	HandoffBars       int // consecutive bars of accelerating distance from AVWAP (default 3)
	HandoffMinMomBPS  int // minimum final distance from AVWAP in bps to confirm handoff (default 40)

	MinConfluenceScore        int  // minimum confluence score for entry (0 = no gate)
	FibConfluenceEnabled      bool // enable Fibonacci confluence factor
	KeyLevelConfluenceEnabled bool // enable key level confluence factor
	CandleConfluenceEnabled   bool // enable candlestick pattern confluence factor
	BandConfluenceEnabled     bool // enable AVWAP band zone confluence factor
	DPConfluenceEnabled bool // enable dark pool confluence factor
	DPVetoEnabled      bool // when true, block entries when DP flow opposes direction. Default false.
	DPVetoBuyRatioMin  float64 // block longs when DP buying < this ratio. Default 0.45.
	DPVetoSellRatioMax float64 // block shorts when DP buying > this ratio. Default 0.55.
	DPSizingEnabled    bool    // when true, boost signal strength based on DP alignment. Default false.
	DPSizingMaxBoost   float64 // maximum sizing multiplier when DP strongly confirms. Default 1.5.
	DPStopEnabled      bool    // when true, tighten stops to DP support level. Default false.
	DPLevelLookback    int     // number of 5m DP bars to scan for S/R levels. Default 20.

	AllowedHoursStart string // "HH:MM" entry window start (ET) — fallback when SessionWeightEnabled=false
	AllowedHoursEnd   string // "HH:MM" entry window end (ET)
	AllowedHoursTZ    string // IANA timezone (default America/New_York)

	// Session-time weighting: graduated multiplier on entry strength per time bucket.
	// When enabled, replaces the binary AllowedHours gate. Weight=0.0 blocks entry.
	SessionWeightEnabled      bool
	SessionWeightTZ           string  // IANA timezone (default America/New_York)
	SessionWeightOpen         float64 // 09:30-10:00 opening drive
	SessionWeightExtendedOpen float64 // 10:00-10:30 initial balance extension
	SessionWeightMidMorning   float64 // 10:30-12:00
	SessionWeightLunch        float64 // 12:00-14:00 midday chop
	SessionWeightAfternoon    float64 // 14:00-15:00
	SessionWeightMOC          float64 // 15:00-15:30 MOC imbalance
	SessionWeightClose        float64 // 15:30-16:00
	SessionWeightOutside      float64 // outside RTH

	RegimeBlockedDirections map[string]string // regime -> blocked direction ("LONG" or "SHORT")

	// Late-session DP Z conditioning (mean-reversion alignment for AVWAP).
	// Low Z (<-1.0) = prior day had abnormally low DP buying = bullish reversal tailwind.
	// High Z (>1.0) = prior day had high DP buying = adverse for mean-reversion.
	DPZConditioningEnabled     bool    // Default false.
	DPZFavorableThreshold      float64 // Z below this is favorable. Default -1.0.
	DPZAdverseThreshold        float64 // Z above this is adverse. Default 1.0.
	DPZFavorableTargetMult     float64 // widen premium target on favorable days. Default 1.2.
	DPZAdverseHoldTimeMult     float64 // tighten hold time on adverse days. Default 0.70.
	DPZSuppressAdverseEntries  bool    // block entries when Z > suppress threshold. Default false.
	DPZSuppressThreshold       float64 // Z above this suppresses entries. Default 1.5.

	// Crypto 24/7 session-time weighting buckets (only used when AssetClass=CRYPTO).
	SessionWeightUSPeak     float64 // session_weight_us_peak: 09:30-16:00 ET
	SessionWeightUSEvening  float64 // session_weight_us_evening: 16:00-21:00 ET
	SessionWeightAsiaPeak   float64 // session_weight_asia_peak: 21:00-04:00 ET
	SessionWeightEuropePeak float64 // session_weight_europe_peak: 04:00-09:30 ET

	// Entry priority order: "standard" (default) or "bounce_first".
	EntryPriority string

	// ATR-scaled stops and targets (overrides fixed stop_bps / target when > 0).
	StopATRMult   float64 // stop_atr_mult: 0 = disabled (use stop_bps), >0 = N * ATR stop
	TargetATRMult float64 // target_atr_mult: 0 = disabled, >0 = N * ATR target

	// Inducement detector params (Factor 7 confluence — liquidity sweep).
	// Disabled by default; enabling adds scoring only (no entry blocking).
	InducementEnabled        bool
	InducementSwingN         int
	InducementSwingDepth     int
	InducementMaxAgeBars     int
	InducementBreachMinBps   int
	InducementBreachMaxBps   int
	InducementReversalBars   int
	InducementVolumeMinRatio float64

	// Co-fire veto: block entries on bars where inducement fires AND the
	// climactic-exhaustion pattern (stretch_z, vol_shift) also fires same-bar.
	// Research (omo-signal-corr, IS/OOS/held-out) showed this subset has
	// consistently negative forward returns. Disabled by default. Shadow mode
	// logs would-have-blocked events without enforcing; long-only gate starts
	// conservative per quant recommendation.
	CofireVetoEnabled             bool
	CofireVetoShadow              bool
	CofireVetoLongOnly            bool
	CofireVetoStretchZMin         float64
	CofireVetoStretchZMax         float64
	CofireVetoVolShiftMax         float64
	CofireVetoSessionSigmaMinBars int
}

// AVWAPState is the per-symbol state for the AVWAP strategy.
type AVWAPState struct {
	// parent is a non-serialized back-pointer to the AVWAPStrategy that
	// produced this state. Used by emitEarlyGated / emitEntryGated to
	// record the first blocking gate into the strategy's per-symbol
	// holdReasons map so LastHoldReason can serve it to the liveness
	// tracker. Nil is tolerated (unit tests constructing AVWAPState
	// directly, prior versions restored from snapshot) — recordHoldReason
	// silently no-ops when parent is unset.
	parent *AVWAPStrategy `json:"-"`

	Symbol         string
	Calc           *start.AnchoredVWAPCalc
	Indicators     start.IndicatorData
	AboveCount     map[string]int
	BelowCount     map[string]int
	TradesToday    int
	CooldownUntil  time.Time
	PositionSide   start.Side
	PendingEntry   start.Side // set on signal emission, cleared on fill/rejection
	PendingEntryAt time.Time  // when PendingEntry was set (for timeout recovery)
	Config         AVWAPConfig
	RecentLows     []float64
	RecentHighs    []float64
	CalcBarCount   int // bars fed to Calc since last reset — used for stabilization

	PeakAboveCount map[string]int // tracks the max AboveCount before a pullback started
	PeakBelowCount map[string]int // tracks the max BelowCount before a pullback started

	StopBelowCount  map[string]int // for LONG positions: consecutive bars below AVWAP
	StopAboveCount  map[string]int // for SHORT positions: consecutive bars above AVWAP
	CrossedBelowBar  map[string]int       // tracks how many bars ago price crossed below each AVWAP (gap reclaim)
	AVWAPDistHistory map[string][]float64 // recent close-to-AVWAP distances per anchor (for handoff detection)

	LockedOutSide   start.Side // side that was stopped out; prevents same-direction re-entry

	entryChecks      []domain.EntryCheckResult // transient: reset each bar, not serialized

	LastGatedBarTime time.Time // rate-limit EntryGated events to one per bar

	PrevBars [2]start.Bar // 2-bar lookback for candlestick patterns
	PrevBarCount int                // how many prev bars have been stored (0, 1, or 2)
	BarHighs50   []float64          // rolling 50-bar high window for Fibonacci
	BarLows50    []float64          // rolling 50-bar low window for Fibonacci
	KeyLevels    map[string]float64 // key price levels (pd_high, pd_low, or_high, or_low)

	// Market-tide telemetry. Populated per-bar by the strategy runner via
	// SetTideData just before OnBar. Phase 1 of SPY-tide plumbing — DATA
	// COLLECTION ONLY, never read by any gate or filter.
	SpyTideDevBps  float64 // (last_close - intraday_vwap) / intraday_vwap * 10000
	SpyTideReady   bool    // false until the index tracker has enough warmup bars
	TideIndexName  string  // "SPY" or "QQQ" — which index this stock maps to

	// Inducement detector state (Factor 7 confluence). Populated only when
	// Config.InducementEnabled. InducementSwing is a strategy-local detector
	// to avoid coupling with the shared anchor-resolver's timeframe-specific N.
	InducementSwing      *start.SwingDetector        `json:"-"`
	RecentSwingHighs     []start.SwingLevel          // ring buffer capped at InducementSwingDepth
	RecentSwingLows      []start.SwingLevel          // ring buffer capped at InducementSwingDepth
	PendingInducement    *start.PendingInducement    // in-flight multi-bar reversal candidate
	LastInducementSignal *start.InducementSignal     `json:"-"` // consumed by computeConfluence on current bar

	// Co-fire veto state. Not persisted: on restart, the 10-session warmup
	// during which the veto cannot fire is the conservative default.
	CofireSessionDate    string               `json:"-"`
	CofireSessionVWAPNum float64              `json:"-"`
	CofireSessionVWAPDen float64              `json:"-"`
	CofireSessionReturns []float64            `json:"-"`
	CofireLastClose      float64              `json:"-"`
	CofireTODBuckets     map[string][]float64 `json:"-"`
	CofireBucketedZHist  []float64            `json:"-"`
}

// SetTideData is called by the strategy runner before every OnBar with the
// current SPY/QQQ intraday-VWAP deviation for this symbol. Telemetry only.
func (s *AVWAPState) SetTideData(devBps float64, ready bool, indexName string) {
	s.SpyTideDevBps = devBps
	s.SpyTideReady = ready
	s.TideIndexName = indexName
}

// ResetGatedBarTime clears the dedup guard so the next live bar emits an EntryGated event.
// Called by the runner after warmup completes.
func (s *AVWAPState) ResetGatedBarTime() {
	s.LastGatedBarTime = time.Time{}
}

// recordCheck appends a failed entry check result with proximity=0
// (unknown / not meaningful).
func (s *AVWAPState) recordCheck(name, reason string) {
	s.entryChecks = append(s.entryChecks, domain.EntryCheckResult{
		Name:   name,
		Passed: false,
		Reason: reason,
	})
}

// recordCheckProx appends a failed entry check result with a 0..1
// proximity value indicating how close this check is to passing. Used
// where the blocking condition has a meaningful numeric signal
// (breakout hold_bars vs required, pinch gap vs accepted range).
func (s *AVWAPState) recordCheckProx(name, reason string, proximity float64) {
	if proximity < 0 {
		proximity = 0
	}
	if proximity > 1 {
		proximity = 1
	}
	s.entryChecks = append(s.entryChecks, domain.EntryCheckResult{
		Name:      name,
		Passed:    false,
		Reason:    reason,
		Proximity: proximity,
	})
}

// recordCheckPassed appends a passed entry check result.
func (s *AVWAPState) recordCheckPassed(name string) {
	s.entryChecks = append(s.entryChecks, domain.EntryCheckResult{
		Name:      name,
		Passed:    true,
		Reason:    "fired",
		Proximity: 1.0,
	})
}

// entryContext bundles all per-bar read-only data needed by entry/exit helpers,
// so we don't have to pass ~15 individual parameters through every method call.
type entryContext struct {
	ctx          start.Context
	cfg          AVWAPConfig
	bar          start.Bar
	symbol       string
	instanceID   start.InstanceID
	now          time.Time
	cooldown     time.Duration
	avwapValues  map[string]float64
	sortedAnchors []string
	avwapBias    string
	avwapSlope   float64
	slopeOK      bool
	regimeTag    string
	lockedLong   bool
	lockedShort  bool
	etLocation   *time.Location
	keyLevels    map[string]float64

	// Market-tide snapshot (Phase 1 telemetry only).
	spyTideDevBps float64
	spyTideReady  bool
	tideIndexName string

	// Session-time weighting (populated when SessionWeightEnabled=true).
	sessionBucket string  // e.g. "open", "extended_open", "outside"
	sessionMult   float64 // multiplier on entry strength (default 1.0 when disabled)
}

// entryTelemetryTags returns a map of entry-time telemetry fields derived from
// the entry context and current indicator snapshot. These are attached to every
// entry signal for retrospective analysis — they do NOT affect trading behavior.
//
// Fields included:
//   - ext_session_open, ext_pd_high, ext_pd_low: ATR-normalized extension from
//     each AVWAP anchor. Positive = price above AVWAP, negative = below.
//   - atr_pct: ATR as % of close price (volatility normalization).
//   - rsi, bb_pct_b, bb_bandwidth: oscillator / vol regime snapshot.
//   - dp_ratio, dp_buy_ratio: dark pool flow snapshot.
//   - minute_of_day, minute_bucket: session time (ET, 5-min buckets).
//   - avwap_slope_bps: already computed in entryContext, just surfaced.
//
// Keep this function conservative — any added field increases log/storage
// volume. Add fields only when there's a concrete hypothesis to test.
func entryTelemetryTags(ec entryContext, ind start.IndicatorData) map[string]string {
	tags := make(map[string]string, 16)
	bar := ec.bar

	// ATR-normalized extension from each AVWAP anchor
	if ind.ATR > 0 && bar.Close > 0 {
		if v, ok := ec.avwapValues["session_open"]; ok && v > 0 {
			tags["ext_session_open"] = fmt.Sprintf("%.3f", (bar.Close-v)/ind.ATR)
		}
		if v, ok := ec.avwapValues["pd_high"]; ok && v > 0 {
			tags["ext_pd_high"] = fmt.Sprintf("%.3f", (bar.Close-v)/ind.ATR)
		}
		if v, ok := ec.avwapValues["pd_low"]; ok && v > 0 {
			tags["ext_pd_low"] = fmt.Sprintf("%.3f", (bar.Close-v)/ind.ATR)
		}
		tags["atr_pct"] = fmt.Sprintf("%.3f", ind.ATR/bar.Close*100)
	}

	// Oscillator / vol regime
	if ind.RSI != 0 {
		tags["rsi"] = fmt.Sprintf("%.2f", ind.RSI)
	}
	if ind.BBPercentB != 0 {
		tags["bb_pct_b"] = fmt.Sprintf("%.3f", ind.BBPercentB)
	}
	if ind.BBBandwidth != 0 {
		tags["bb_bandwidth"] = fmt.Sprintf("%.5f", ind.BBBandwidth)
	}

	// Dark pool flow snapshot
	if ind.DPRatio != 0 {
		tags["dp_ratio"] = fmt.Sprintf("%.3f", ind.DPRatio)
	}
	if ind.DPBuyRatio != 0 {
		tags["dp_buy_ratio"] = fmt.Sprintf("%.3f", ind.DPBuyRatio)
	}

	// Session time bucketing (ET)
	if ec.etLocation != nil {
		tet := bar.Time.In(ec.etLocation)
		tags["minute_of_day"] = fmt.Sprintf("%d", tet.Hour()*60+tet.Minute())
		tags["minute_bucket"] = fmt.Sprintf("%02d:%02d", tet.Hour(), (tet.Minute()/5)*5)
	}

	// AVWAP slope (already computed, surfaced for uniform access)
	if ec.slopeOK {
		tags["avwap_slope_bps"] = fmt.Sprintf("%.3f", ec.avwapSlope)
	}

	// Market-tide deviation (SPY or QQQ intraday-VWAP basis, in bps).
	// Emitted ONLY when the tracker is warmed up, so an absent tag means
	// "no reading" rather than "tide is exactly zero" — important for
	// post-hoc bucketing. Phase 1 of SPY-tide plumbing (telemetry only).
	if ec.spyTideReady {
		switch ec.tideIndexName {
		case "QQQ":
			tags["qqq_tide_dev_bps"] = fmt.Sprintf("%.1f", ec.spyTideDevBps)
		case "SPY":
			tags["spy_tide_dev_bps"] = fmt.Sprintf("%.1f", ec.spyTideDevBps)
		}
	}

	return tags
}

// mergeTelemetry copies telemetry fields into dst without overwriting existing
// keys — setup-specific fields (setup, mode, confluence, etc.) always win.
func mergeTelemetry(dst, src map[string]string) {
	for k, v := range src {
		if _, exists := dst[k]; !exists {
			dst[k] = v
		}
	}
}

// newEntrySignal wraps start.NewSignal for AVWAP entry signals, attaching
// entry-time telemetry (extension, oscillators, session bucketing, etc.)
// to the tag map. This is a DATA COLLECTION ONLY function — it does not
// filter or modify trading behavior, it just enriches the tags written to
// the trade log for later retrospective analysis.
//
// The helper consolidates what used to be 12 inline start.NewSignal call sites
// across the AVWAP entry evaluators.
func (s *AVWAPState) newEntrySignal(ec entryContext, side start.Side, strength float64, tags map[string]string) (start.Signal, error) {
	mergeTelemetry(tags, entryTelemetryTags(ec, s.Indicators))
	tags["late_session_dp_z"] = fmt.Sprintf("%.3f", s.Indicators.LateSessionDPZ)

	// Session-time weighting tags for post-hoc P&L analysis by bucket.
	if ec.sessionBucket != "" {
		tags["session_bucket"] = ec.sessionBucket
		tags["session_mult"] = fmt.Sprintf("%.2f", ec.sessionMult)
	}

	// Z-conditioned exit multipliers: modulate PREMIUM_TARGET and STAGNATION_EXIT
	// based on late-session dark pool Z-score. Tags flow through OrderIntent.Meta
	// into the position monitor's CustomState for per-trade exit adjustments.
	cfg := ec.cfg
	if cfg.DPZConditioningEnabled && s.Indicators.LateSessionDPZ != 0 {
		z := s.Indicators.LateSessionDPZ
		if z <= cfg.DPZFavorableThreshold {
			// Favorable: wider premium target, longer stagnation timeout
			tags["dp_z_premium_target_mult"] = fmt.Sprintf("%.3f", cfg.DPZFavorableTargetMult)
			tags["dp_z_stagnation_mult"] = fmt.Sprintf("%.3f", 1.0/cfg.DPZAdverseHoldTimeMult)
		} else if z >= cfg.DPZAdverseThreshold {
			// Adverse: tighter premium target, shorter stagnation timeout
			tags["dp_z_premium_target_mult"] = fmt.Sprintf("%.3f", 1.0/cfg.DPZFavorableTargetMult)
			tags["dp_z_stagnation_mult"] = fmt.Sprintf("%.3f", cfg.DPZAdverseHoldTimeMult)
		}
	}

	// ATR-scaled stop (overrides fixed stop_bps when enabled).
	if cfg.StopATRMult > 0 && s.Indicators.ATR > 0 {
		atr := s.Indicators.ATR
		if side == start.SideBuy {
			stopPrice := ec.bar.Close - cfg.StopATRMult*atr
			tags["stop_price"] = fmt.Sprintf("%.4f", stopPrice)
			tags["stop_bps"] = fmt.Sprintf("%.0f", (ec.bar.Close-stopPrice)/ec.bar.Close*10000)
		} else {
			stopPrice := ec.bar.Close + cfg.StopATRMult*atr
			tags["stop_price"] = fmt.Sprintf("%.4f", stopPrice)
			tags["stop_bps"] = fmt.Sprintf("%.0f", (stopPrice-ec.bar.Close)/ec.bar.Close*10000)
		}
	}
	// ATR-scaled target (overrides fixed target when enabled).
	if cfg.TargetATRMult > 0 && s.Indicators.ATR > 0 {
		atr := s.Indicators.ATR
		if side == start.SideBuy {
			tags["target_price"] = fmt.Sprintf("%.4f", ec.bar.Close+cfg.TargetATRMult*atr)
		} else {
			tags["target_price"] = fmt.Sprintf("%.4f", ec.bar.Close-cfg.TargetATRMult*atr)
		}
	}

	return start.NewSignal(ec.instanceID, ec.symbol, start.SignalEntry, side, strength, tags)
}

// logShortGate emits a structured debug log when a short entry is blocked at a gate.
func logShortGate(ctx start.Context, symbol, gate, anchor string, kvs ...string) {
	if ctx == nil || ctx.Logger() == nil {
		return
	}
	args := []any{"symbol", symbol, "gate", gate, "anchor", anchor}
	for i := 0; i+1 < len(kvs); i += 2 {
		args = append(args, kvs[i], kvs[i+1])
	}
	ctx.Logger().Info("AVWAP short gate blocked", args...)
}

// SetIndicators implements the indicatorSetter interface.
func (s *AVWAPState) SetIndicators(ind start.IndicatorData) {
	s.Indicators = ind
}

// updateInducement advances the inducement detector one bar. Safe to call
// when cfg.InducementEnabled is false — it is a no-op. Maintains the per-
// strategy SwingDetector, ages and prunes swing ring buffers, and stores
// the detected signal on LastInducementSignal for computeConfluence to read.
func (s *AVWAPState) updateInducement(bar start.Bar, cfg AVWAPConfig) {
	s.LastInducementSignal = nil
	if !cfg.InducementEnabled {
		return
	}
	if s.InducementSwing == nil {
		n := cfg.InducementSwingN
		if n < 1 {
			n = 3
		}
		s.InducementSwing = start.NewSwingDetector(n, "5m")
	}
	// Age existing swings by one bar before adding new confirmed ones.
	for i := range s.RecentSwingHighs {
		s.RecentSwingHighs[i].BarAge++
	}
	for i := range s.RecentSwingLows {
		s.RecentSwingLows[i].BarAge++
	}
	// Push this bar; SwingDetector emits confirmed pivots lagged by N.
	for _, ca := range s.InducementSwing.Push(bar) {
		lvl := start.SwingLevel{
			Time:   ca.Time,
			Price:  ca.Price,
			BarAge: s.InducementSwing.N(), // confirmed bar is N bars back
		}
		switch ca.Type {
		case start.AnchorSwingHigh:
			lvl.Side = start.InducementSwingHigh
			s.RecentSwingHighs = appendSwingLevel(s.RecentSwingHighs, lvl, cfg.InducementSwingDepth)
		case start.AnchorSwingLow:
			lvl.Side = start.InducementSwingLow
			s.RecentSwingLows = appendSwingLevel(s.RecentSwingLows, lvl, cfg.InducementSwingDepth)
		}
	}
	// Prune stale swings beyond the age cap.
	s.RecentSwingHighs = pruneStaleSwings(s.RecentSwingHighs, cfg.InducementMaxAgeBars)
	s.RecentSwingLows = pruneStaleSwings(s.RecentSwingLows, cfg.InducementMaxAgeBars)

	// Detect.
	icfg := start.InducementConfig{
		BreachMinBPS:   float64(cfg.InducementBreachMinBps),
		BreachMaxBPS:   float64(cfg.InducementBreachMaxBps),
		ReversalBars:   cfg.InducementReversalBars,
		VolumeMinRatio: cfg.InducementVolumeMinRatio,
		MaxAgeBars:     cfg.InducementMaxAgeBars,
	}
	sig, pending := start.DetectInducement(
		bar, s.RecentSwingHighs, s.RecentSwingLows,
		s.PendingInducement, icfg, s.Indicators.VolumeSMA,
	)
	s.PendingInducement = pending
	s.LastInducementSignal = sig
}

// updateCofireVetoState maintains the per-symbol session VWAP, rolling session
// sigma, and time-of-day bucketed volume state needed to evaluate the co-fire
// veto on each RTH bar. Runs on every bar regardless of whether the veto is
// enabled, so enabling the flag does not need a warmup restart. Computation is
// a handful of float ops plus one map lookup per bar — trivial on the hot path.
//
// RTH-only: extended hours bars are skipped because session VWAP has no
// meaningful anchor outside RTH and the research validated the veto on RTH 5m
// bars only.
func (s *AVWAPState) updateCofireVetoState(bar start.Bar) {
	if !domain.IsEquityMarketOpen(bar.Time) {
		return
	}
	if etLocation == nil {
		return
	}
	et := bar.Time.In(etLocation)

	date := et.Format("2006-01-02")
	if date != s.CofireSessionDate {
		s.CofireSessionDate = date
		s.CofireSessionVWAPNum = 0
		s.CofireSessionVWAPDen = 0
		s.CofireSessionReturns = s.CofireSessionReturns[:0]
		s.CofireLastClose = 0
	}

	// Skip zero- or negative-volume bars (halts, illiquid prints) so they
	// don't displace valid samples from the 20-session TOD bucket ring and
	// don't pollute session VWAP. Log-return append already guards bar.Close.
	if bar.Volume <= 0 {
		return
	}

	typical := (bar.High + bar.Low + bar.Close) / 3.0
	s.CofireSessionVWAPNum += typical * bar.Volume
	s.CofireSessionVWAPDen += bar.Volume

	if s.CofireLastClose > 0 && bar.Close > 0 {
		s.CofireSessionReturns = append(s.CofireSessionReturns, math.Log(bar.Close/s.CofireLastClose))
	}
	s.CofireLastClose = bar.Close

	if s.CofireTODBuckets == nil {
		s.CofireTODBuckets = make(map[string][]float64)
	}
	key := et.Format("15:04")
	hist := s.CofireTODBuckets[key]

	// Compute bucketed_z from hist BEFORE appending current bar so the current
	// observation doesn't self-bias the z-score.
	var bucketedZ float64
	bucketReady := false
	if len(hist) >= 10 {
		med := cofireMedian(hist)
		if med > 0 && bar.Volume > 0 {
			logRatios := make([]float64, 0, len(hist))
			for _, v := range hist {
				if v > 0 {
					logRatios = append(logRatios, math.Log(v/med))
				}
			}
			sd := cofireStdev(logRatios)
			if sd > 0 {
				bucketedZ = math.Log(bar.Volume/med) / sd
				bucketReady = true
			}
		}
	}

	hist = append(hist, bar.Volume)
	const todBucketCap = 20
	if len(hist) > todBucketCap {
		hist = hist[len(hist)-todBucketCap:]
	}
	s.CofireTODBuckets[key] = hist

	if bucketReady {
		s.CofireBucketedZHist = append(s.CofireBucketedZHist, bucketedZ)
		const zHistCap = 8
		if len(s.CofireBucketedZHist) > zHistCap {
			s.CofireBucketedZHist = s.CofireBucketedZHist[len(s.CofireBucketedZHist)-zHistCap:]
		}
	}
}

// computeCofireVeto returns (veto, stretchZ, volShift) for the current bar.
// The veto fires when:
//   (1) inducement fired on this bar (s.LastInducementSignal != nil)
//   (2) session sigma has enough samples
//   (3) bucketed vol_shift has 8 prior z-scores
//   (4) |stretch_z| in [min, max] AND vol_shift < max
//
// Direction-neutral: the caller checks the long-only gate against the
// evaluated entry's Side.
func (s *AVWAPState) computeCofireVeto(bar start.Bar, cfg AVWAPConfig) (bool, float64, float64) {
	if s.LastInducementSignal == nil {
		return false, 0, 0
	}
	minBars := cfg.CofireVetoSessionSigmaMinBars
	if minBars <= 0 {
		minBars = 6
	}
	if len(s.CofireSessionReturns) < minBars {
		return false, 0, 0
	}
	if len(s.CofireBucketedZHist) < 8 {
		return false, 0, 0
	}
	if s.CofireSessionVWAPDen <= 0 {
		return false, 0, 0
	}
	sessionVWAP := s.CofireSessionVWAPNum / s.CofireSessionVWAPDen
	// Session sigma in price units: stdev of log-returns * current close is a
	// first-order approximation sufficient for a threshold gate. Matches the
	// formulation validated in the signal-corr research.
	sigma := cofireStdev(s.CofireSessionReturns) * bar.Close
	if sigma <= 0 {
		return false, 0, 0
	}
	stretchZ := (bar.Close - sessionVWAP) / sigma
	absZ := math.Abs(stretchZ)
	if absZ < cfg.CofireVetoStretchZMin || absZ > cfg.CofireVetoStretchZMax {
		return false, stretchZ, 0
	}
	hist := s.CofireBucketedZHist
	fast := hist[len(hist)-3:]
	slow := hist[len(hist)-8 : len(hist)-3]
	volShift := cofireMean(fast) - cofireMean(slow)
	if volShift >= cfg.CofireVetoVolShiftMax {
		return false, stretchZ, volShift
	}
	return true, stretchZ, volShift
}

// applyCofireVeto wraps an entry signal with the co-fire veto. Returns the
// original signal unchanged when the veto is disabled, long-only gate rejects
// the side, or the veto conditions don't fire. In shadow mode, logs a
// would-have-blocked event and returns the signal. In enforced mode, records
// a hold reason and returns nil.
func (s *AVWAPState) applyCofireVeto(ec entryContext, sig *start.Signal, err error) (*start.Signal, error) {
	if err != nil || sig == nil {
		return sig, err
	}
	if !ec.cfg.CofireVetoEnabled && !ec.cfg.CofireVetoShadow {
		return sig, err
	}
	if ec.cfg.CofireVetoLongOnly && sig.Side != start.SideBuy {
		return sig, err
	}
	veto, stretchZ, volShift := s.computeCofireVeto(ec.bar, ec.cfg)
	if !veto {
		return sig, err
	}
	if ec.cfg.CofireVetoShadow && !ec.cfg.CofireVetoEnabled {
		if ec.ctx != nil && ec.ctx.Logger() != nil {
			ec.ctx.Logger().Info("cofire veto SHADOW would have blocked entry",
				"symbol", ec.symbol,
				"side", string(sig.Side),
				"stretch_z", stretchZ,
				"vol_shift", volShift,
			)
		}
		return sig, err
	}
	if s.parent != nil {
		s.parent.recordHoldReason(ec.symbol, "cofire_veto",
			fmt.Sprintf("stretch_z=%.2f vol_shift=%.2f ind=%s", stretchZ, volShift, s.LastInducementSignal.Tag),
			map[string]string{
				"veto_stretch_z": fmt.Sprintf("%.2f", stretchZ),
				"veto_vol_shift": fmt.Sprintf("%.2f", volShift),
			})
	}
	if ec.ctx != nil && ec.ctx.Logger() != nil {
		ec.ctx.Logger().Info("cofire veto blocked entry",
			"symbol", ec.symbol,
			"side", string(sig.Side),
			"stretch_z", stretchZ,
			"vol_shift", volShift,
		)
	}
	return nil, nil
}

// cofireMean/Stdev/Median are local helpers kept unexported to avoid widening
// the package surface. Inline to avoid pulling a new stats dependency.
func cofireMean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func cofireStdev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := cofireMean(xs)
	var ss float64
	for _, x := range xs {
		d := x - m
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(xs)-1))
}

func cofireMedian(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := make([]float64, len(xs))
	copy(cp, xs)
	// Simple insertion sort — xs is small (<=20).
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j-1] > cp[j]; j-- {
			cp[j-1], cp[j] = cp[j], cp[j-1]
		}
	}
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

// appendSwingLevel pushes lvl onto buf and trims from the front when the
// buffer exceeds depth. depth<=0 disables capping.
func appendSwingLevel(buf []start.SwingLevel, lvl start.SwingLevel, depth int) []start.SwingLevel {
	buf = append(buf, lvl)
	if depth > 0 && len(buf) > depth {
		buf = buf[len(buf)-depth:]
	}
	return buf
}

// pruneStaleSwings drops swings older than maxAge. maxAge<=0 disables pruning.
func pruneStaleSwings(buf []start.SwingLevel, maxAge int) []start.SwingLevel {
	if maxAge <= 0 {
		return buf
	}
	out := buf[:0]
	for _, sw := range buf {
		if sw.BarAge <= maxAge {
			out = append(out, sw)
		}
	}
	return out
}

func (s *AVWAPState) AnchorNames() []string { return s.Config.Anchors }

// HasAnchor reports whether the named anchor exists in the AVWAP calc.
// Used to detect anchors that were skipped during pre-market startup
// (zero-time session_open) so the runner can re-resolve on first RTH bar.
func (s *AVWAPState) HasAnchor(name string) bool {
	if s.Calc == nil {
		return false
	}
	_, exists := s.Calc.AnchorPoints()[name]
	return exists
}

// SetKeyLevels stores key price levels (pd_high, pd_low, or_high, or_low) for confluence scoring.
func (s *AVWAPState) SetKeyLevels(levels map[string]float64) {
	s.KeyLevels = levels
}

// ResetAnchors performs a partial update: anchors with unchanged times
// preserve their running VWAP state (CumPV/CumV/M2). New or changed
// anchors start fresh. Removed anchors are dropped.
func (s *AVWAPState) ResetAnchors(anchorTimes map[string]time.Time) {
	if s.Calc == nil {
		s.Calc = start.NewAnchoredVWAPCalc()
		for name, t := range anchorTimes {
			if t.IsZero() {
				continue
			}
			rthOnly := (name == "pd_high" || name == "pd_low") && s.Config.PDRangeMode != "24H"
			s.Calc.AddAnchor(start.AnchorPoint{Name: name, AnchorTime: t, RTHOnly: rthOnly})
		}
		s.AboveCount = make(map[string]int)
		s.BelowCount = make(map[string]int)
		s.PeakAboveCount = make(map[string]int)
		s.PeakBelowCount = make(map[string]int)
		s.StopBelowCount = make(map[string]int)
		s.StopAboveCount = make(map[string]int)
		s.CrossedBelowBar = make(map[string]int)
		s.TradesToday = 0
		s.CalcBarCount = 0
		s.LockedOutSide = ""
		return
	}

	existingStates := s.Calc.States()
	existingPoints := s.Calc.AnchorPoints()

	newCalc := start.NewAnchoredVWAPCalc()
	newAbove := make(map[string]int)
	newBelow := make(map[string]int)

	for name, t := range anchorTimes {
		if t.IsZero() {
			continue
		}

		rthOnly := (name == "pd_high" || name == "pd_low") && s.Config.PDRangeMode != "24H"
		ap := start.AnchorPoint{Name: name, AnchorTime: t, RTHOnly: rthOnly}

		if oldAP, exists := existingPoints[name]; exists && oldAP.AnchorTime.Equal(t) {
			if oldState, hasState := existingStates[name]; hasState {
				newCalc.AddAnchor(ap)
				newCalc.Restore([]start.AnchorPoint{ap}, map[string]start.AnchoredVWAPState{name: oldState})
				newAbove[name] = s.AboveCount[name]
				newBelow[name] = s.BelowCount[name]
				continue
			}
		}

		newCalc.AddAnchor(ap)
	}

	s.Calc = newCalc
	s.AboveCount = newAbove
	s.BelowCount = newBelow
	s.PeakAboveCount = make(map[string]int)
	s.PeakBelowCount = make(map[string]int)
	s.StopBelowCount = make(map[string]int)
	s.StopAboveCount = make(map[string]int)
	s.CrossedBelowBar = make(map[string]int)
	s.TradesToday = 0
	s.CalcBarCount = 0 // reset stabilization counter
	s.LockedOutSide = ""
}

func (s *AVWAPState) ClearPendingEntry() {
	s.PendingEntry = ""
	s.PendingEntryAt = time.Time{}
	// PositionSide is intentionally not cleared here — it represents
	// a real position tracked from fill confirmations.
}

// UpdateCalcAnchor feeds a bar into a single named anchor in the AVWAP calculator.
// Used to replay previous-day bars into individual anchors (pd_high, pd_low)
// without affecting other anchors or the lastBarTime dedup guard.
func (s *AVWAPState) UpdateCalcAnchor(name string, bar start.Bar) {
	if s.Calc != nil {
		s.Calc.UpdateSingleAnchor(name, bar.Time, bar.High, bar.Low, bar.Close, bar.Volume)
		s.CalcBarCount++ // count replayed bars toward stabilization threshold
	}
}

// UpdateCalc feeds a 1m bar into the AVWAP calculator for smooth chart
// rendering. Does not trigger any signal logic.
func (s *AVWAPState) UpdateCalc(bar start.Bar) {
	if s.Calc != nil {
		s.Calc.Update(bar.Time, bar.High, bar.Low, bar.Close, bar.Volume)
		s.CalcBarCount++
	}
}

// CheckExitsOn1m evaluates exit-only logic on 1m bars for faster reaction.
// Per Brian Shannon: use short-term chart (1m) to fine-tune exits while
// entries remain on the strategy timeframe (5m). Returns nil if no exit.
func (s *AVWAPState) CheckExitsOn1m(symbol string, bar start.Bar) []start.Signal {
	if s.PositionSide == "" || s.PendingEntry != "" {
		return nil
	}
	cfg := s.Config
	avwapValues := s.Calc.Values()
	if len(avwapValues) == 0 {
		return nil
	}
	sortedAnchors := s.Calc.SortedNames()
	instanceID, _ := start.NewInstanceID(fmt.Sprintf("avwap:1.0.0:%s", symbol))
	cooldown := time.Duration(cfg.CooldownSeconds) * time.Second
	now := bar.Time

	// Update recent lows/highs with 1m data for swing trail precision
	s.RecentLows = append(s.RecentLows, bar.Low)
	cap := cfg.HigherLowsBars
	if cap < 2 {
		cap = 3
	}
	if len(s.RecentLows) > cap {
		s.RecentLows = s.RecentLows[len(s.RecentLows)-cap:]
	}
	s.RecentHighs = append(s.RecentHighs, bar.High)
	if len(s.RecentHighs) > cap {
		s.RecentHighs = s.RecentHighs[len(s.RecentHighs)-cap:]
	}

	// --- AVWAP stop on 1m ---
	if cfg.AVWAPStopEnabled {
		if s.StopBelowCount == nil {
			s.StopBelowCount = make(map[string]int)
		}
		if s.StopAboveCount == nil {
			s.StopAboveCount = make(map[string]int)
		}
		for _, anchorName := range sortedAnchors {
			avwapValue := avwapValues[anchorName]
			bufferAbs := avwapValue * float64(cfg.AVWAPStopBufferBPS) / 10000.0
			switch s.PositionSide {
			case start.SideBuy:
				if bar.Close < avwapValue-bufferAbs {
					s.StopBelowCount[anchorName]++
				} else {
					s.StopBelowCount[anchorName] = 0
				}
			case start.SideSell:
				if bar.Close > avwapValue+bufferAbs {
					s.StopAboveCount[anchorName]++
				} else {
					s.StopAboveCount[anchorName] = 0
				}
			}
		}
		switch s.PositionSide {
		case start.SideBuy:
			for _, anchorName := range sortedAnchors {
				if s.StopBelowCount[anchorName] >= cfg.AVWAPStopBars {
					sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideSell, 0.9, map[string]string{
						"ref_price": fmt.Sprintf("%.10f", bar.Close),
						"setup":     "avwap_stop",
						"anchor":    anchorName,
						"source":    "1m",
					})
					if err != nil {
						return nil
					}
					s.PositionSide = ""
					s.CooldownUntil = now.Add(cooldown)
					for an := range s.StopBelowCount {
						s.StopBelowCount[an] = 0
					}
					return []start.Signal{sig}
				}
			}
		case start.SideSell:
			for _, anchorName := range sortedAnchors {
				if s.StopAboveCount[anchorName] >= cfg.AVWAPStopBars {
					sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideBuy, 0.9, map[string]string{
						"ref_price": fmt.Sprintf("%.10f", bar.Close),
						"setup":     "avwap_stop",
						"anchor":    anchorName,
						"source":    "1m",
					})
					if err != nil {
						return nil
					}
					s.PositionSide = ""
					s.CooldownUntil = now.Add(cooldown)
					for an := range s.StopAboveCount {
						s.StopAboveCount[an] = 0
					}
					return []start.Signal{sig}
				}
			}
		}
	}

	return nil
}

// AVWAPValues returns the current anchored VWAP values for chart rendering.
// Suppresses output for the first 10 bars after a reset to avoid noisy
// values from an under-accumulated calculator.
func (s *AVWAPState) AVWAPValues() map[string]float64 {
	if s.Calc == nil || s.CalcBarCount < 10 {
		return nil
	}
	return s.Calc.Values()
}

// --- param helpers (shared by strategies in this package) ---

func getFloat64(m map[string]any, key string, def float64) float64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		}
	}
	return def
}

func getInt(m map[string]any, key string, def int) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return def
}

func getBool(m map[string]any, key string, def bool) bool {
	if v, ok := m[key]; ok {
		if b, ok2 := v.(bool); ok2 {
			return b
		}
	}
	return def
}

func getStringSlice(m map[string]any, key string, def []string) []string {
	v, ok := m[key]
	if !ok {
		return def
	}
	switch sl := v.(type) {
	case []string:
		return sl
	case []any:
		out := make([]string, 0, len(sl))
		for _, item := range sl {
			if s, ok2 := item.(string); ok2 {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return def
}
func getString(m map[string]any, key string, def string) string {
	if v, ok := m[key]; ok {
		if s, ok2 := v.(string); ok2 {
			return s
		}
	}
	return def
}

var etLocation *time.Location

func init() {
	var err error
	etLocation, err = time.LoadLocation("America/New_York")
	if err != nil {
		log.Fatalf("failed to load America/New_York timezone: %v", err)
	}
}

func hasHigherLows(lows []float64) bool {
	if len(lows) < 2 {
		return false
	}
	// Allow ties (<=) but not lower lows (<). Equal lows still represent
	// a valid "higher lows" pattern in real markets.
	for i := 1; i < len(lows); i++ {
		if lows[i] < lows[i-1] {
			return false
		}
	}
	return true
}

func parseAVWAPConfig(params map[string]any) AVWAPConfig {
	cfg := AVWAPConfig{
		BreakoutEnabled:   getBool(params, "breakout_enabled", true),
		HoldBars:          getInt(params, "hold_bars", 2),
		VolumeMult:        getFloat64(params, "volume_mult", 1.5),
		BounceEnabled:     getBool(params, "bounce_enabled", true),
		RSIBounceMax:      getFloat64(params, "rsi_bounce_max", 30),
		ExitHoldBars:      getInt(params, "exit_hold_bars", 2),
		CooldownSeconds:   getInt(params, "cooldown_seconds", 120),
		MaxTradesPerDay:   getInt(params, "max_trades_per_day", 3),
		AllowRegimes:      getStringSlice(params, "allow_regimes", []string{"BALANCE", "REVERSAL"}),
		Direction:         getString(params, "direction", ""),
		RequireHigherLows: getBool(params, "require_higher_lows", false),
		HigherLowsBars:    getInt(params, "higher_lows_bars", 3),
		MiddayTrapShield:  getBool(params, "midday_trap_shield", false),
		MiddayVolumeMult:  getFloat64(params, "midday_volume_mult", 2.0),
		AssetClass:        getString(params, "asset_class", ""),
		PDRangeMode:       strings.ToUpper(getString(params, "pd_range_mode", "RTH")),
		Anchors:           getStringSlice(params, "anchors", []string{"session_open"}),

		MinSlopeBPS:                  getFloat64(params, "min_slope_bps", 0.0),
		SlopeLookback:                getInt(params, "slope_lookback", 10),
		RequireCapitulationForShorts: getBool(params, "require_capitulation_for_shorts", false),

		EnforceAVWAPBias:     getBool(params, "enforce_avwap_bias", true),
		PullbackEnabled:      getBool(params, "pullback_enabled", true),
		PullbackTrendBars:    getInt(params, "pullback_trend_bars", 10),
		PullbackToleranceBPS: getInt(params, "pullback_tolerance_bps", 20),
		PullbackRSIMin:       getFloat64(params, "pullback_rsi_min", 40.0),
		PullbackRSIMax:       getFloat64(params, "pullback_rsi_max", 60.0),

		AVWAPStopEnabled:   getBool(params, "avwap_stop_enabled", true),
		AVWAPStopBars:      getInt(params, "avwap_stop_bars", 3),
		AVWAPStopBufferBPS: getInt(params, "avwap_stop_buffer_bps", 10),

		PinchEnabled: getBool(params, "pinch_enabled", false),
		PinchMinBPS:  getInt(params, "pinch_min_bps", 5),
		PinchMaxBPS:  getInt(params, "pinch_max_bps", 50),

		GapReclaimEnabled: getBool(params, "gap_reclaim_enabled", false),
		GapReclaimBars:    getInt(params, "gap_reclaim_bars", 5),

		HandoffEnabled:   getBool(params, "handoff_enabled", false),
		HandoffBars:      getInt(params, "handoff_bars", 3),
		HandoffMinMomBPS: getInt(params, "handoff_min_mom_bps", 40),

		MinConfluenceScore:        getInt(params, "min_confluence_score", 0),
		FibConfluenceEnabled:      getBool(params, "fib_confluence_enabled", true),
		KeyLevelConfluenceEnabled: getBool(params, "key_level_confluence_enabled", true),
		CandleConfluenceEnabled:   getBool(params, "candle_confluence_enabled", true),
		BandConfluenceEnabled:     getBool(params, "band_confluence_enabled", true),
		DPConfluenceEnabled: getBool(params, "dp_confluence_enabled", false),
		DPVetoEnabled:      getBool(params, "dp_veto_enabled", false),
		DPVetoBuyRatioMin:  getFloat64(params, "dp_veto_buy_ratio_min", 0.45),
		DPVetoSellRatioMax: getFloat64(params, "dp_veto_sell_ratio_max", 0.55),
		DPSizingEnabled:    getBool(params, "dp_sizing_enabled", false),
		DPSizingMaxBoost:   getFloat64(params, "dp_sizing_max_boost", 1.5),
		DPStopEnabled:      getBool(params, "dp_stop_enabled", false),
		DPLevelLookback:    getInt(params, "dp_level_lookback", 20),

		AllowedHoursStart: getString(params, "allowed_hours_start", ""),
		AllowedHoursEnd:   getString(params, "allowed_hours_end", ""),
		AllowedHoursTZ:    getString(params, "allowed_hours_tz", "America/New_York"),

		SessionWeightEnabled:      getBool(params, "session_weight_enabled", false),
		SessionWeightTZ:           getString(params, "session_weight_tz", "America/New_York"),
		SessionWeightOpen:         getFloat64(params, "session_weight_open", 1.15),
		SessionWeightExtendedOpen: getFloat64(params, "session_weight_extended_open", 1.10),
		SessionWeightMidMorning:   getFloat64(params, "session_weight_mid_morning", 1.00),
		SessionWeightLunch:        getFloat64(params, "session_weight_lunch", 0.85),
		SessionWeightAfternoon:    getFloat64(params, "session_weight_afternoon", 1.00),
		SessionWeightMOC:          getFloat64(params, "session_weight_moc", 1.10),
		SessionWeightClose:        getFloat64(params, "session_weight_close", 0.95),
		SessionWeightOutside:      getFloat64(params, "session_weight_outside", 0.0),

		DPZConditioningEnabled: getBool(params, "dp_z_conditioning_enabled", false),
		DPZFavorableThreshold:  getFloat64(params, "dp_z_favorable_threshold", -1.0),
		DPZAdverseThreshold:    getFloat64(params, "dp_z_adverse_threshold", 1.0),
		DPZFavorableTargetMult: getFloat64(params, "dp_z_favorable_target_mult", 1.2),
		DPZAdverseHoldTimeMult:    getFloat64(params, "dp_z_adverse_hold_time_mult", 0.70),
		DPZSuppressAdverseEntries: getBool(params, "dp_z_suppress_adverse_entries", false),
		DPZSuppressThreshold:      getFloat64(params, "dp_z_suppress_threshold", 1.5),

		SessionWeightUSPeak:     getFloat64(params, "session_weight_us_peak", 0),
		SessionWeightUSEvening:  getFloat64(params, "session_weight_us_evening", 0),
		SessionWeightAsiaPeak:   getFloat64(params, "session_weight_asia_peak", 0),
		SessionWeightEuropePeak: getFloat64(params, "session_weight_europe_peak", 0),

		EntryPriority: getString(params, "entry_priority", "standard"),

		StopATRMult:   getFloat64(params, "stop_atr_mult", 0),
		TargetATRMult: getFloat64(params, "target_atr_mult", 0),

		InducementEnabled:        getBool(params, "inducement_enabled", false),
		InducementSwingN:         getInt(params, "inducement_swing_n", 3),
		InducementSwingDepth:     getInt(params, "inducement_swing_depth", 8),
		InducementMaxAgeBars:     getInt(params, "inducement_max_age_bars", 60),
		InducementBreachMinBps:   getInt(params, "inducement_breach_min_bps", 5),
		InducementBreachMaxBps:   getInt(params, "inducement_breach_max_bps", 80),
		InducementReversalBars:   getInt(params, "inducement_reversal_bars", 3),
		InducementVolumeMinRatio: getFloat64(params, "inducement_volume_min_ratio", 1.2),

		CofireVetoEnabled:             getBool(params, "cofire_veto_enabled", false),
		CofireVetoShadow:              getBool(params, "cofire_veto_shadow", false),
		CofireVetoLongOnly:            getBool(params, "cofire_veto_long_only", true),
		CofireVetoStretchZMin:         getFloat64(params, "cofire_veto_stretch_z_min", 2.0),
		CofireVetoStretchZMax:         getFloat64(params, "cofire_veto_stretch_z_max", 3.0),
		CofireVetoVolShiftMax:         getFloat64(params, "cofire_veto_vol_shift_max", -0.5),
		CofireVetoSessionSigmaMinBars: getInt(params, "cofire_veto_session_sigma_min_bars", 6),
	}
	cfg.RSIBounceMin = 100 - cfg.RSIBounceMax

	// Parse regime_blocked_directions nested map: regime -> "LONG" or "SHORT"
	if rbd, ok := params["regime_blocked_directions"]; ok {
		if m, ok2 := rbd.(map[string]any); ok2 {
			cfg.RegimeBlockedDirections = make(map[string]string, len(m))
			for k, v := range m {
				if s, ok3 := v.(string); ok3 {
					cfg.RegimeBlockedDirections[k] = s
				}
			}
		}
	}

	return cfg
}

// Init creates initial state for a symbol.
func (s *AVWAPStrategy) Init(ctx start.Context, symbol string, params map[string]any, prior start.State) (start.State, error) {
	cfg := parseAVWAPConfig(params)
	// Auto-detect crypto symbols and enable 24h pd range
	if strings.Contains(symbol, "/") && cfg.PDRangeMode == "RTH" {
		cfg.PDRangeMode = "24H"
	}
	calc := start.NewAnchoredVWAPCalc()

	anchorNames := getStringSlice(params, "anchors", []string{"session_open"})
	added := 0
	for _, name := range anchorNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var anchorTime time.Time
		if name == "session_open" {
			if ctx != nil {
				anchorTime = ctx.Now()
			}
		}
		rthOnly := (name == "pd_high" || name == "pd_low") && cfg.PDRangeMode != "24H"
		calc.AddAnchor(start.AnchorPoint{Name: name, AnchorTime: anchorTime, RTHOnly: rthOnly})
		added++
	}
	if added == 0 {
		var anchorTime time.Time
		if ctx != nil {
			anchorTime = ctx.Now()
		}
		calc.AddAnchor(start.AnchorPoint{Name: "session_open", AnchorTime: anchorTime})
	}

	st := &AVWAPState{
		parent:          s,
		Symbol:          symbol,
		Calc:            calc,
		AboveCount:      make(map[string]int),
		BelowCount:      make(map[string]int),
		PeakAboveCount:  make(map[string]int),
		PeakBelowCount:  make(map[string]int),
		StopBelowCount:  make(map[string]int),
		StopAboveCount:  make(map[string]int),
		CrossedBelowBar: make(map[string]int),
		Config:          cfg,
	}

	if prior != nil {
		if avwapPrior, ok := prior.(*AVWAPState); ok {
			st.Calc = avwapPrior.Calc
			st.AboveCount = avwapPrior.AboveCount
			st.BelowCount = avwapPrior.BelowCount
			st.PeakAboveCount = avwapPrior.PeakAboveCount
			st.PeakBelowCount = avwapPrior.PeakBelowCount
			st.StopBelowCount = avwapPrior.StopBelowCount
			st.StopAboveCount = avwapPrior.StopAboveCount
			st.CrossedBelowBar = avwapPrior.CrossedBelowBar
			st.TradesToday = avwapPrior.TradesToday
			st.CooldownUntil = avwapPrior.CooldownUntil
			st.PositionSide = avwapPrior.PositionSide
			st.PendingEntry = avwapPrior.PendingEntry
			st.PendingEntryAt = avwapPrior.PendingEntryAt
			st.Config = cfg
		} else if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Warn("AVWAPStrategy: incompatible prior state, starting fresh", "symbol", symbol)
		}
	}

	return st, nil
}

// --- Exit methods ---

// evaluateBasicExit checks whether the current position should exit based on
// the ExitHoldBars rule (price held against position for N consecutive bars).
func (s *AVWAPState) evaluateBasicExit(ec entryContext) (*start.Signal, error) {
	if s.PendingEntry != "" {
		return nil, nil
	}
	cfg := ec.cfg
	instanceID := ec.instanceID
	now := ec.now
	cooldown := ec.cooldown
	regimeTag := ec.regimeTag

	// Z conditioning: adjust exit hold bars based on late-session DP Z.
	// Favorable Z (low) = bullish tailwind → be more patient (increase hold bars).
	// Adverse Z (high) = headwind → tighten exit (decrease hold bars).
	effectiveHoldBars := cfg.ExitHoldBars
	if cfg.DPZConditioningEnabled && s.Indicators.LateSessionDPZ != 0 {
		if s.Indicators.LateSessionDPZ <= cfg.DPZFavorableThreshold {
			effectiveHoldBars = int(float64(effectiveHoldBars) / cfg.DPZAdverseHoldTimeMult)
		} else if s.Indicators.LateSessionDPZ >= cfg.DPZAdverseThreshold {
			effectiveHoldBars = int(float64(effectiveHoldBars) * cfg.DPZAdverseHoldTimeMult)
		}
		if effectiveHoldBars < 1 {
			effectiveHoldBars = 1
		}
	}

	if s.PositionSide == start.SideBuy {
		for _, belowCnt := range s.BelowCount {
			if belowCnt >= effectiveHoldBars {
				sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalExit, start.SideSell, 0.8, map[string]string{
					"ref_price": fmt.Sprintf("%.10f", ec.bar.Close),
					"setup":     "avwap_exit",
					"regime_5m": regimeTag,
				})
				if err != nil {
					return nil, err
				}
				s.PositionSide = ""
				s.CooldownUntil = now.Add(cooldown)
				for anchorName := range s.AboveCount {
					s.AboveCount[anchorName] = 0
					s.BelowCount[anchorName] = 0
				}
				return &sig, nil
			}
		}
	}
	if s.PositionSide == start.SideSell {
		for _, aboveCnt := range s.AboveCount {
			if aboveCnt >= effectiveHoldBars {
				sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalExit, start.SideBuy, 0.8, map[string]string{
					"ref_price": fmt.Sprintf("%.10f", ec.bar.Close),
					"setup":     "avwap_exit",
					"regime_5m": regimeTag,
				})
				if err != nil {
					return nil, err
				}
				s.PositionSide = ""
				s.CooldownUntil = now.Add(cooldown)
				for anchorName := range s.AboveCount {
					s.AboveCount[anchorName] = 0
					s.BelowCount[anchorName] = 0
				}
				return &sig, nil
			}
		}
	}
	return nil, nil
}

// evaluateAVWAPStop checks the AVWAP-based stop: exit when price breaks significantly
// past AVWAP against position for AVWAPStopBars consecutive bars.
func (s *AVWAPState) evaluateAVWAPStop(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	if !cfg.AVWAPStopEnabled || s.PositionSide == "" || s.PendingEntry != "" {
		return nil, nil
	}
	if s.StopBelowCount == nil {
		s.StopBelowCount = make(map[string]int)
	}
	if s.StopAboveCount == nil {
		s.StopAboveCount = make(map[string]int)
	}

	instanceID := ec.instanceID
	now := ec.now
	cooldown := ec.cooldown
	regimeTag := ec.regimeTag
	sortedAnchors := ec.sortedAnchors
	avwapValues := ec.avwapValues
	bar := ec.bar

	// Update stop counters for each anchor.
	for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
		bufferAbs := avwapValue * float64(cfg.AVWAPStopBufferBPS) / 10000.0
		switch s.PositionSide {
		case start.SideBuy:
			if bar.Close < avwapValue-bufferAbs {
				s.StopBelowCount[anchorName]++
			} else {
				s.StopBelowCount[anchorName] = 0
			}
		case start.SideSell:
			if bar.Close > avwapValue+bufferAbs {
				s.StopAboveCount[anchorName]++
			} else {
				s.StopAboveCount[anchorName] = 0
			}
		}
	}

	// Check if any anchor triggered the stop.
	switch s.PositionSide {
	case start.SideBuy:
		for _, anchorName := range sortedAnchors {
			if s.StopBelowCount[anchorName] >= cfg.AVWAPStopBars {
				sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalExit, start.SideSell, 0.9, map[string]string{
					"ref_price": fmt.Sprintf("%.10f", bar.Close),
					"setup":     "avwap_stop",
					"anchor":    anchorName,
					"regime_5m": regimeTag,
				})
				if err != nil {
					return nil, err
				}
				s.PositionSide = ""
				s.LockedOutSide = start.SideBuy
				s.CooldownUntil = now.Add(cooldown)
				for an := range s.StopBelowCount {
					s.StopBelowCount[an] = 0
				}
				return &sig, nil
			}
		}
	case start.SideSell:
		for _, anchorName := range sortedAnchors {
			if s.StopAboveCount[anchorName] >= cfg.AVWAPStopBars {
				sig, err := start.NewSignal(instanceID, ec.symbol, start.SignalExit, start.SideBuy, 0.9, map[string]string{
					"ref_price": fmt.Sprintf("%.10f", bar.Close),
					"setup":     "avwap_stop",
					"anchor":    anchorName,
					"regime_5m": regimeTag,
				})
				if err != nil {
					return nil, err
				}
				s.PositionSide = ""
				s.LockedOutSide = start.SideSell
				s.CooldownUntil = now.Add(cooldown)
				for an := range s.StopAboveCount {
					s.StopAboveCount[an] = 0
				}
				return &sig, nil
			}
		}
	}
	return nil, nil
}

// evaluateExits runs all exit checks in order and returns the first triggered signal.
func (s *AVWAPState) evaluateExits(ec entryContext) (*start.Signal, error) {
	if sig, err := s.evaluateBasicExit(ec); err != nil || sig != nil {
		return sig, err
	}
	if sig, err := s.evaluateAVWAPStop(ec); err != nil || sig != nil {
		return sig, err
	}
	return nil, nil
}

// --- Entry methods ---

// evaluateCapitulationReclaim checks section 6e: capitulation AVWAP reclaim long entry.
func (s *AVWAPState) evaluateCapitulationReclaim(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	bar := ec.bar
	now := ec.now
	cooldown := ec.cooldown
	regimeTag := ec.regimeTag
	avwapValues := ec.avwapValues
	sortedAnchors := ec.sortedAnchors

	reason := "no capitulation anchor"
	for _, anchorName := range sortedAnchors {
		if !strings.HasPrefix(anchorName, "capitulation") {
			continue
		}
		avwapValue, ok := avwapValues[anchorName]
		if !ok || avwapValue == 0 {
			reason = "AVWAP value missing"
			continue
		}
		volumeOK := cfg.VolumeMult == 0 || (s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA)
		if ec.lockedLong {
			reason = "locked LONG"
			continue
		}
		if s.AboveCount[anchorName] >= 1 && s.AboveCount[anchorName] <= cfg.HoldBars &&
			bar.Close > avwapValue && bar.Close > bar.Open && volumeOK {
			conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
				s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50, s.LastInducementSignal)
			if conf.Vetoed || (cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore) {
				reason = fmt.Sprintf("confluence %d < %d", conf.Score, cfg.MinConfluenceScore)
				continue
			}
			adjustedStrength := conf.applyDPSizing(math.Min(1.0, 0.9+float64(conf.Score)*0.03)) * ec.sessionMult
			volRatio := 0.0
			if s.Indicators.VolumeSMA > 0 {
				volRatio = bar.Volume / s.Indicators.VolumeSMA
			}
			sig, err := s.newEntrySignal(ec, start.SideBuy, adjustedStrength, map[string]string{
				"ref_price":         fmt.Sprintf("%.10f", bar.Close),
				"setup":             "avwap_capitulation_reclaim",
				"anchor":            anchorName,
				"avwap":             fmt.Sprintf("%.4f", avwapValue),
				"vol_ratio":         fmt.Sprintf("%.2f", volRatio),
				"regime_5m":         regimeTag,
				"mode":              "capitulation_reclaim",
				"confluence":        fmt.Sprintf("%d", conf.Score),
				"confluence_detail":     strings.Join(conf.Factors, "+"),
				"confluence_components": conf.ComponentsJSON(),
			})
			if err != nil {
				return nil, err
			}
			s.PendingEntry = start.SideBuy
			s.PendingEntryAt = now
			s.TradesToday++
			s.CooldownUntil = now.Add(cooldown)
			return &sig, nil
		}
		// Got a capitulation anchor but conditions not met — track closest miss
		if reason == "no capitulation anchor" || reason == "AVWAP value missing" {
			reason = "reclaim conditions not met"
		}
	}
	s.recordCheck("cap_reclaim", reason)
	return nil, nil
}

// evaluateBreakout checks section 7: AVWAP breakout entries (long and short).
func (s *AVWAPState) evaluateBreakout(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	if !cfg.BreakoutEnabled {
		s.recordCheck("breakout", "disabled")
		return nil, nil
	}
	bar := ec.bar
	now := ec.now
	cooldown := ec.cooldown
	regimeTag := ec.regimeTag
	avwapValues := ec.avwapValues
	sortedAnchors := ec.sortedAnchors
	ctx := ec.ctx

	reason := fmt.Sprintf("hold bars %d < %d", maxAboveCount(s.AboveCount, sortedAnchors), cfg.HoldBars)

	// Long breakouts
	for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
		volRatio := 0.0
		if s.Indicators.VolumeSMA > 0 {
			volRatio = bar.Volume / s.Indicators.VolumeSMA
		}
		volumeOK := cfg.VolumeMult == 0 || (s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA)

		// Split outer gate into individual checks so the UI reports the
		// specific blocker instead of the default "hold bars N < M" when N >= M.
		if s.AboveCount[anchorName] >= cfg.HoldBars {
			if ec.lockedLong {
				reason = "locked long (cooldown/position)"
				continue
			}
			if !volumeOK {
				reason = fmt.Sprintf("vol %.2fx < %.1fx min", volRatio, cfg.VolumeMult)
				continue
			}
			if cfg.RequireHigherLows && !hasHigherLows(s.RecentLows) {
				reason = "higher lows not met"
				continue
			}
			if cfg.MinSlopeBPS > 0 && ec.slopeOK && ec.avwapSlope < cfg.MinSlopeBPS {
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP slope: blocking long breakout", "symbol", ec.symbol, "slope_bps", ec.avwapSlope, "min", cfg.MinSlopeBPS)
				}
				reason = fmt.Sprintf("slope %.1f bps < %.1f min", ec.avwapSlope, cfg.MinSlopeBPS)
				continue
			}
			conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
				s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50, s.LastInducementSignal)
			if conf.Vetoed || (cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore) {
				reason = fmt.Sprintf("confluence %d < %d", conf.Score, cfg.MinConfluenceScore)
				continue
			}
			adjustedStrength := conf.applyDPSizing(math.Min(1.0, 0.7+float64(conf.Score)*0.03)) * ec.sessionMult
			sig, err := s.newEntrySignal(ec, start.SideBuy, adjustedStrength, map[string]string{
				"ref_price":         fmt.Sprintf("%.10f", bar.Close),
				"setup":             "avwap_breakout",
				"anchor":            anchorName,
				"avwap":             fmt.Sprintf("%.4f", avwapValue),
				"vol_ratio":         fmt.Sprintf("%.2f", volRatio),
				"hold_bars":         fmt.Sprintf("%d", s.AboveCount[anchorName]),
				"mode":              "breakout",
				"regime_5m":         regimeTag,
				"confluence":        fmt.Sprintf("%d", conf.Score),
				"confluence_detail":     strings.Join(conf.Factors, "+"),
				"confluence_components": conf.ComponentsJSON(),
			})
			if err != nil {
				return nil, err
			}
			s.PendingEntry = start.SideBuy
			s.PendingEntryAt = now
			s.TradesToday++
			s.CooldownUntil = now.Add(cooldown)
			return &sig, nil
		}
	}

	// Short breakouts
	if !strings.EqualFold(cfg.Direction, "LONG") {
		for _, anchorName := range sortedAnchors {
			avwapValue := avwapValues[anchorName]
			volRatio := 0.0
			if s.Indicators.VolumeSMA > 0 {
				volRatio = bar.Volume / s.Indicators.VolumeSMA
			}
			volumeOK := cfg.VolumeMult == 0 || (s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA)

			// Split outer gate so short-breakout blockers surface specifically
			// rather than the default "hold bars N < M" fallback (see long path).
			if s.BelowCount[anchorName] >= cfg.HoldBars {
				if ec.lockedShort {
					reason = "locked short (cooldown/position)"
					continue
				}
				if !volumeOK {
					reason = fmt.Sprintf("vol %.2fx < %.1fx min", volRatio, cfg.VolumeMult)
					continue
				}
				// Regime gating handled by evaluateEntries — breakout only called in REVERSAL
				if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "SHORT" {
					logShortGate(ctx, ec.symbol, "enforce_avwap_bias", anchorName, "bias", ec.avwapBias)
					if ctx != nil && ctx.Logger() != nil {
						ctx.Logger().Info("AVWAP bias: blocking short breakout", "symbol", ec.symbol, "bias", ec.avwapBias, "anchor", anchorName)
					}
					reason = "bias blocks SHORT"
					continue
				}
				// Note: require_higher_lows is NOT applied to shorts — the bias gate
				// + slope gate already confirm downtrend structure. Requiring strict
				// lower highs blocked 9000+ short attempts in backtests.
				if cfg.MiddayTrapShield && strings.EqualFold(cfg.AssetClass, "EQUITY") && ec.etLocation != nil {
					barET := bar.Time.In(ec.etLocation)
					hour := barET.Hour()
					if hour >= 11 && hour < 13 {
						middayVolOK := s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.MiddayVolumeMult*s.Indicators.VolumeSMA
						if !middayVolOK {
							logShortGate(ctx, ec.symbol, "midday_trap_shield", anchorName)
							reason = "midday trap shield (11-13 ET)"
							continue
						}
					}
				}
				if cfg.MinSlopeBPS > 0 && ec.slopeOK && ec.avwapSlope > -cfg.MinSlopeBPS {
					logShortGate(ctx, ec.symbol, "min_slope_bps", anchorName, "slope_bps", fmt.Sprintf("%.2f", ec.avwapSlope))
					if ctx != nil && ctx.Logger() != nil {
						ctx.Logger().Info("AVWAP slope: blocking short breakout", "symbol", ec.symbol, "slope_bps", ec.avwapSlope, "min", -cfg.MinSlopeBPS)
					}
					reason = fmt.Sprintf("slope %.1f bps < %.1f min", ec.avwapSlope, cfg.MinSlopeBPS)
					continue
				}
				if cfg.RequireCapitulationForShorts && ec.avwapBias == "LONG" {
					logShortGate(ctx, ec.symbol, "require_capitulation", anchorName, "bias", ec.avwapBias)
					reason = "capitulation required for short"
					continue
				}
				conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
					s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50, s.LastInducementSignal)
				if conf.Vetoed || (cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore) {
					reason = fmt.Sprintf("confluence %d < %d", conf.Score, cfg.MinConfluenceScore)
					continue
				}
				adjustedStrength := conf.applyDPSizing(math.Min(1.0, 0.7+float64(conf.Score)*0.03)) * ec.sessionMult
				sig, err := s.newEntrySignal(ec, start.SideSell, adjustedStrength, map[string]string{
					"ref_price":         fmt.Sprintf("%.10f", bar.Close),
					"setup":             "avwap_breakout",
					"anchor":            anchorName,
					"avwap":             fmt.Sprintf("%.4f", avwapValue),
					"vol_ratio":         fmt.Sprintf("%.2f", volRatio),
					"hold_bars":         fmt.Sprintf("%d", s.BelowCount[anchorName]),
					"mode":              "breakout",
					"regime_5m":         regimeTag,
					"confluence":            fmt.Sprintf("%d", conf.Score),
					"confluence_detail":     strings.Join(conf.Factors, "+"),
					"confluence_components": conf.ComponentsJSON(),
				})
				if err != nil {
					return nil, err
				}
				s.PendingEntry = start.SideSell
				s.PendingEntryAt = now
				s.TradesToday++
				s.CooldownUntil = now.Add(cooldown)
				return &sig, nil
			}
		}
	}
	var breakoutProx float64
	if cfg.HoldBars > 0 {
		// Whichever side is closest to triggering — long uses AboveCount,
		// short uses BelowCount. Take the max so the bar reflects the
		// nearest-to-firing direction.
		above := maxAboveCount(s.AboveCount, sortedAnchors)
		below := maxAboveCount(s.BelowCount, sortedAnchors)
		best := above
		if below > best {
			best = below
		}
		breakoutProx = math.Min(1.0, float64(best)/float64(cfg.HoldBars))
	}
	s.recordCheckProx("breakout", reason, breakoutProx)
	return nil, nil
}

// evaluatePullback checks section 7b: pullback-to-AVWAP entries.
func (s *AVWAPState) evaluatePullback(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	if !cfg.PullbackEnabled {
		s.recordCheck("pullback", "disabled")
		return nil, nil
	}
	bar := ec.bar
	now := ec.now
	cooldown := ec.cooldown
	regimeTag := ec.regimeTag
	avwapValues := ec.avwapValues
	sortedAnchors := ec.sortedAnchors
	ctx := ec.ctx

	requiredTrendBars := cfg.PullbackTrendBars
	if regimeTag == "BALANCE" {
		requiredTrendBars += 3
	}
	bestPeak := 0
	for _, a := range sortedAnchors {
		if s.PeakAboveCount[a] > bestPeak {
			bestPeak = s.PeakAboveCount[a]
		}
		if s.PeakBelowCount[a] > bestPeak {
			bestPeak = s.PeakBelowCount[a]
		}
	}
	reason := fmt.Sprintf("trend bars %d < %d", bestPeak, requiredTrendBars)

	for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
		if avwapValue == 0 {
			continue
		}
		toleranceFrac := float64(cfg.PullbackToleranceBPS) / 10000.0
		toleranceAbs := avwapValue * toleranceFrac

		volRatio := 0.0
		if s.Indicators.VolumeSMA > 0 {
			volRatio = bar.Volume / s.Indicators.VolumeSMA
		}
		volumeOK := cfg.VolumeMult == 0 || (s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA)
		rsiOK := s.Indicators.RSI >= cfg.PullbackRSIMin && s.Indicators.RSI <= cfg.PullbackRSIMax

		// Long pullback: was above AVWAP for trend bars, low touches AVWAP, closes above, RSI mid-range.
		reqLong := cfg.PullbackTrendBars
		if regimeTag == "BALANCE" {
			reqLong += 3
		}
		if !ec.lockedLong && s.PeakAboveCount[anchorName] >= reqLong &&
			bar.Low <= avwapValue+toleranceAbs &&
			bar.Close > avwapValue &&
			rsiOK && volumeOK {
			if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "LONG" {
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP bias: blocking long pullback", "symbol", ec.symbol, "bias", ec.avwapBias, "anchor", anchorName)
				}
				reason = "bias blocks LONG"
				continue
			}
			if cfg.MinSlopeBPS > 0 && ec.slopeOK && ec.avwapSlope < cfg.MinSlopeBPS {
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP slope: blocking long pullback", "symbol", ec.symbol, "slope_bps", ec.avwapSlope, "min", cfg.MinSlopeBPS)
				}
				reason = fmt.Sprintf("slope %.1f bps < %.1f min", ec.avwapSlope, cfg.MinSlopeBPS)
				continue
			}
			conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
				s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50, s.LastInducementSignal)
			if conf.Vetoed || (cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore) {
				reason = fmt.Sprintf("confluence %d < %d", conf.Score, cfg.MinConfluenceScore)
				continue
			}
			adjustedStrength := conf.applyDPSizing(math.Min(1.0, 0.85+float64(conf.Score)*0.03)) * ec.sessionMult
			sig, err := s.newEntrySignal(ec, start.SideBuy, adjustedStrength, map[string]string{
				"ref_price":         fmt.Sprintf("%.10f", bar.Close),
				"setup":             "avwap_pullback",
				"regime_5m":         regimeTag,
				"anchor":            anchorName,
				"avwap":             fmt.Sprintf("%.4f", avwapValue),
				"vol_ratio":         fmt.Sprintf("%.2f", volRatio),
				"peak_above":        fmt.Sprintf("%d", s.PeakAboveCount[anchorName]),
				"mode":              "pullback",
				"confluence":        fmt.Sprintf("%d", conf.Score),
				"confluence_detail":     strings.Join(conf.Factors, "+"),
				"confluence_components": conf.ComponentsJSON(),
			})
			if err != nil {
				return nil, err
			}
			s.PendingEntry = start.SideBuy
			s.PendingEntryAt = now
			s.TradesToday++
			s.CooldownUntil = now.Add(cooldown)
			s.PeakAboveCount[anchorName] = 0
			return &sig, nil
		}

		// Short pullback: was below AVWAP for trend bars, high reaches AVWAP, closes below, RSI mid-range.
		reqShort := cfg.PullbackTrendBars
		if regimeTag == "BALANCE" {
			reqShort += 3
		}
		if !ec.lockedShort && !strings.EqualFold(cfg.Direction, "LONG") &&
			s.PeakBelowCount[anchorName] >= reqShort &&
			bar.High >= avwapValue-toleranceAbs &&
			bar.Close < avwapValue &&
			rsiOK && volumeOK {
			if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "SHORT" {
				logShortGate(ctx, ec.symbol, "enforce_avwap_bias", anchorName, "bias", ec.avwapBias)
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP bias: blocking short pullback", "symbol", ec.symbol, "bias", ec.avwapBias, "anchor", anchorName)
				}
				reason = "bias blocks SHORT"
				continue
			}
			if cfg.MinSlopeBPS > 0 && ec.slopeOK && ec.avwapSlope > -cfg.MinSlopeBPS {
				logShortGate(ctx, ec.symbol, "min_slope_bps", anchorName, "slope_bps", fmt.Sprintf("%.2f", ec.avwapSlope))
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP slope: blocking short pullback", "symbol", ec.symbol, "slope_bps", ec.avwapSlope, "min", -cfg.MinSlopeBPS)
				}
				reason = fmt.Sprintf("slope %.1f bps < %.1f min", ec.avwapSlope, cfg.MinSlopeBPS)
				continue
			}
			if cfg.RequireCapitulationForShorts && ec.avwapBias == "LONG" {
				logShortGate(ctx, ec.symbol, "require_capitulation", anchorName, "bias", ec.avwapBias)
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP gate: capitulation required for short pullback (above AVWAP)", "symbol", ec.symbol, "anchor", anchorName)
				}
				reason = "capitulation required for short"
				continue
			}
			conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
				s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50, s.LastInducementSignal)
			if conf.Vetoed || (cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore) {
				reason = fmt.Sprintf("confluence %d < %d", conf.Score, cfg.MinConfluenceScore)
				continue
			}
			adjustedStrength := conf.applyDPSizing(math.Min(1.0, 0.85+float64(conf.Score)*0.03)) * ec.sessionMult
			sig, err := s.newEntrySignal(ec, start.SideSell, adjustedStrength, map[string]string{
				"ref_price":         fmt.Sprintf("%.10f", bar.Close),
				"setup":             "avwap_pullback",
				"regime_5m":         regimeTag,
				"anchor":            anchorName,
				"avwap":             fmt.Sprintf("%.4f", avwapValue),
				"vol_ratio":         fmt.Sprintf("%.2f", volRatio),
				"peak_below":        fmt.Sprintf("%d", s.PeakBelowCount[anchorName]),
				"mode":              "pullback",
				"confluence":        fmt.Sprintf("%d", conf.Score),
				"confluence_detail":     strings.Join(conf.Factors, "+"),
				"confluence_components": conf.ComponentsJSON(),
			})
			if err != nil {
				return nil, err
			}
			s.PendingEntry = start.SideSell
			s.PendingEntryAt = now
			s.TradesToday++
			s.CooldownUntil = now.Add(cooldown)
			s.PeakBelowCount[anchorName] = 0
			return &sig, nil
		}
	}
	s.recordCheck("pullback", reason)
	return nil, nil
}

// evaluatePinch checks section 7c: dual-AVWAP pinch breakout entries.
func (s *AVWAPState) evaluatePinch(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	avwapValues := ec.avwapValues
	sortedAnchors := ec.sortedAnchors
	if !cfg.PinchEnabled {
		s.recordCheck("pinch", "disabled")
		return nil, nil
	}
	if len(avwapValues) < 2 {
		s.recordCheck("pinch", "need 2+ AVWAPs")
		return nil, nil
	}
	bar := ec.bar
	now := ec.now
	cooldown := ec.cooldown
	regimeTag := ec.regimeTag
	ctx := ec.ctx

	var minAVWAP, maxAVWAP float64
	first := true
	for _, anchorName := range sortedAnchors {
		v := avwapValues[anchorName]
		if first || v < minAVWAP {
			minAVWAP = v
		}
		if first || v > maxAVWAP {
			maxAVWAP = v
		}
		first = false
	}

	volRatio := 0.0
	if s.Indicators.VolumeSMA > 0 {
		volRatio = bar.Volume / s.Indicators.VolumeSMA
	}
	volumeOK := cfg.VolumeMult == 0 || (s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA)
	// Use first anchor's AVWAP value for confluence scoring in pinch
	pinchAVWAPValue := avwapValues[sortedAnchors[0]]
	gapBPS := (maxAVWAP - minAVWAP) / minAVWAP * 10000.0
	reason := fmt.Sprintf("gap %.0f bps outside [%d, %d]", gapBPS, cfg.PinchMinBPS, cfg.PinchMaxBPS)
	if gapBPS >= float64(cfg.PinchMinBPS) && gapBPS <= float64(cfg.PinchMaxBPS) {
		reason = "price not breaking pinch band"
		// Long pinch breakout: price breaks above maxAVWAP.
		if bar.Close > maxAVWAP && volumeOK && !ec.lockedLong {
			if !cfg.EnforceAVWAPBias || ec.avwapBias == "" || ec.avwapBias == "LONG" {
				conf := computeConfluence(cfg, bar, pinchAVWAPValue, ec.avwapValues, s.Indicators,
					s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50, s.LastInducementSignal)
				if conf.Vetoed || (cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore) {
					reason = fmt.Sprintf("confluence %d < %d", conf.Score, cfg.MinConfluenceScore)
				} else {
					adjustedStrength := conf.applyDPSizing(math.Min(1.0, 0.9+float64(conf.Score)*0.03)) * ec.sessionMult
					sig, err := s.newEntrySignal(ec, start.SideBuy, adjustedStrength, map[string]string{
						"ref_price":         fmt.Sprintf("%.10f", bar.Close),
						"setup":             "avwap_pinch",
						"regime_5m":         regimeTag,
						"confluence":        fmt.Sprintf("%d", conf.Score),
						"confluence_detail":     strings.Join(conf.Factors, "+"),
						"confluence_components": conf.ComponentsJSON(),
					})
					if err != nil {
						return nil, err
					}
					s.PendingEntry = start.SideBuy
					s.PendingEntryAt = now
					s.TradesToday++
					s.CooldownUntil = now.Add(cooldown)
					return &sig, nil
				}
			}
		}
		if !volumeOK && (bar.Close > maxAVWAP || bar.Close < minAVWAP) {
			reason = fmt.Sprintf("vol %.1fx < %.1fx min", volRatio, cfg.VolumeMult)
		}

		// Short pinch breakout: price breaks below minAVWAP.
		if bar.Close < minAVWAP && volumeOK && !ec.lockedShort && !strings.EqualFold(cfg.Direction, "LONG") {
			switch {
			case cfg.RequireCapitulationForShorts && ec.avwapBias == "LONG":
				logShortGate(ctx, ec.symbol, "require_capitulation", "pinch", "bias", ec.avwapBias)
				reason = "capitulation required for short"
			case !cfg.EnforceAVWAPBias || ec.avwapBias == "" || ec.avwapBias == "SHORT":
				conf := computeConfluence(cfg, bar, pinchAVWAPValue, ec.avwapValues, s.Indicators,
					s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50, s.LastInducementSignal)
				if conf.Vetoed || (cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore) {
					reason = fmt.Sprintf("confluence %d < %d", conf.Score, cfg.MinConfluenceScore)
				} else {
					adjustedStrength := conf.applyDPSizing(math.Min(1.0, 0.9+float64(conf.Score)*0.03)) * ec.sessionMult
					sig, err := s.newEntrySignal(ec, start.SideSell, adjustedStrength, map[string]string{
						"ref_price":         fmt.Sprintf("%.10f", bar.Close),
						"setup":             "avwap_pinch",
						"regime_5m":         regimeTag,
						"confluence":        fmt.Sprintf("%d", conf.Score),
						"confluence_detail":     strings.Join(conf.Factors, "+"),
						"confluence_components": conf.ComponentsJSON(),
					})
					if err != nil {
						return nil, err
					}
					s.PendingEntry = start.SideSell
					s.PendingEntryAt = now
					s.TradesToday++
					s.CooldownUntil = now.Add(cooldown)
					return &sig, nil
				}
			default:
				logShortGate(ctx, ec.symbol, "enforce_avwap_bias", "pinch", "bias", ec.avwapBias)
				reason = "bias blocks SHORT"
			}
		}
	}
	// Pinch proximity: if gap is inside the acceptable band, we're already
	// at 1.0 (blocked by some inner condition — bias, breakout, confluence).
	// If gap is outside, map distance-from-band to a 0..1 bar, with one
	// band-width of overflow mapping to 0.
	var pinchProx float64
	lo, hi := float64(cfg.PinchMinBPS), float64(cfg.PinchMaxBPS)
	rangeW := hi - lo
	if gapBPS >= lo && gapBPS <= hi {
		pinchProx = 1.0
	} else if rangeW > 0 {
		var dist float64
		if gapBPS < lo {
			dist = lo - gapBPS
		} else {
			dist = gapBPS - hi
		}
		pinchProx = math.Max(0, 1.0-dist/rangeW)
	}
	s.recordCheckProx("pinch", reason, pinchProx)
	return nil, nil
}

// evaluateGapReclaim checks section 7d: gap reclaim long entry.
func (s *AVWAPState) evaluateGapReclaim(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	if !cfg.GapReclaimEnabled {
		s.recordCheck("gap_reclaim", "disabled")
		return nil, nil
	}
	if s.CrossedBelowBar == nil {
		s.CrossedBelowBar = make(map[string]int)
	}
	bar := ec.bar
	now := ec.now
	cooldown := ec.cooldown
	regimeTag := ec.regimeTag
	avwapValues := ec.avwapValues
	sortedAnchors := ec.sortedAnchors
	ctx := ec.ctx

	reason := "no recent cross-below"
	volRatio := 0.0
	if s.Indicators.VolumeSMA > 0 {
		volRatio = bar.Volume / s.Indicators.VolumeSMA
	}
	volumeOK := cfg.VolumeMult == 0 || (s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA)
	for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
		prev := s.CrossedBelowBar[anchorName]
		switch {
		case bar.Close < avwapValue:
			if prev == 0 {
				s.CrossedBelowBar[anchorName] = 1 // just crossed below
			} else {
				s.CrossedBelowBar[anchorName]++
			}
		case prev > 0 && prev <= cfg.GapReclaimBars && bar.Close > avwapValue:
			// Reclaim! Price was below for 1-N bars, now closed above.
			s.CrossedBelowBar[anchorName] = 0
			if !volumeOK {
				reason = fmt.Sprintf("vol %.1fx < %.1fx min", volRatio, cfg.VolumeMult)
				continue
			}
			if ec.lockedLong {
				reason = "locked LONG"
				continue
			}
			if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "LONG" {
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP bias: blocking long gap reclaim", "symbol", ec.symbol, "bias", ec.avwapBias, "anchor", anchorName)
				}
				reason = "bias blocks LONG"
				continue
			}
			conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
				s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50, s.LastInducementSignal)
			if conf.Vetoed || (cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore) {
				reason = fmt.Sprintf("confluence %d < %d", conf.Score, cfg.MinConfluenceScore)
				continue
			}
			adjustedStrength := conf.applyDPSizing(math.Min(1.0, 0.85+float64(conf.Score)*0.03)) * ec.sessionMult
			sig, err := s.newEntrySignal(ec, start.SideBuy, adjustedStrength, map[string]string{
				"ref_price":         fmt.Sprintf("%.10f", bar.Close),
				"setup":             "avwap_gap_reclaim",
				"regime_5m":         regimeTag,
				"anchor":            anchorName,
				"confluence":        fmt.Sprintf("%d", conf.Score),
				"confluence_detail":     strings.Join(conf.Factors, "+"),
				"confluence_components": conf.ComponentsJSON(),
			})
			if err != nil {
				return nil, err
			}
			s.PendingEntry = start.SideBuy
			s.PendingEntryAt = now
			s.TradesToday++
			s.CooldownUntil = now.Add(cooldown)
			return &sig, nil
		default:
			s.CrossedBelowBar[anchorName] = 0
		}
	}
	s.recordCheck("gap_reclaim", reason)
	return nil, nil
}

// evaluateHandoff checks section 7e: momentum handoff entries.
// Per Brian Shannon: a "handoff point" occurs when price accelerates away from
// the AVWAP after nearly touching it.
func (s *AVWAPState) evaluateHandoff(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	if !cfg.HandoffEnabled {
		s.recordCheck("handoff", "disabled")
		return nil, nil
	}
	bar := ec.bar
	now := ec.now
	cooldown := ec.cooldown
	regimeTag := ec.regimeTag
	avwapValues := ec.avwapValues
	sortedAnchors := ec.sortedAnchors
	ctx := ec.ctx

	handoffBars := cfg.HandoffBars
	if handoffBars < 2 {
		handoffBars = 3
	}

	reason := fmt.Sprintf("history %d < %d bars", maxHistLen(s.AVWAPDistHistory, sortedAnchors), handoffBars+1)

	for _, anchorName := range sortedAnchors {
		hist := s.AVWAPDistHistory[anchorName]
		if len(hist) < handoffBars+1 {
			continue
		}
		recent := hist[len(hist)-handoffBars:]
		minMom := float64(cfg.HandoffMinMomBPS)
		nearAVWAP := hist[len(hist)-handoffBars-1]

		// Long handoff: consecutive bars with increasing positive distance from AVWAP.
		if !ec.lockedLong && nearAVWAP >= 0 && nearAVWAP <= 30 {
			allIncreasing := true
			for i := 1; i < len(recent); i++ {
				if recent[i] <= recent[i-1] || recent[i] <= 0 {
					allIncreasing = false
					break
				}
			}
			if !allIncreasing || recent[len(recent)-1] < minMom {
				reason = "no sustained momentum"
			} else {
				volRatio := 0.0
				if s.Indicators.VolumeSMA > 0 {
					volRatio = bar.Volume / s.Indicators.VolumeSMA
				}
				volumeOK := cfg.VolumeMult == 0 || (s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA)
				if !volumeOK {
					reason = fmt.Sprintf("vol %.1fx < %.1fx min", volRatio, cfg.VolumeMult)
				} else {
					if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "LONG" {
						reason = "bias blocks LONG"
						continue
					}
					if cfg.RequireHigherLows && !hasHigherLows(s.RecentLows) {
						reason = "higher lows not met"
						continue
					}
					if cfg.MinSlopeBPS > 0 && ec.slopeOK && ec.avwapSlope < cfg.MinSlopeBPS {
						reason = fmt.Sprintf("slope %.1f bps < %.1f min", ec.avwapSlope, cfg.MinSlopeBPS)
						continue
					}
					handoffAVWAP := avwapValues[anchorName]
					conf := computeConfluence(cfg, bar, handoffAVWAP, ec.avwapValues, s.Indicators,
						s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50, s.LastInducementSignal)
					if conf.Vetoed || (cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore) {
						reason = fmt.Sprintf("confluence %d < %d", conf.Score, cfg.MinConfluenceScore)
						continue
					}
					adjustedStrength := conf.applyDPSizing(math.Min(1.0, 0.85+float64(conf.Score)*0.03)) * ec.sessionMult
					sig, err := s.newEntrySignal(ec, start.SideBuy, adjustedStrength, map[string]string{
						"ref_price":         fmt.Sprintf("%.10f", bar.Close),
						"setup":             "avwap_handoff",
						"anchor":            anchorName,
						"avwap":             fmt.Sprintf("%.4f", handoffAVWAP),
						"momentum_bps":      fmt.Sprintf("%.1f", recent[len(recent)-1]),
						"regime_5m":         regimeTag,
						"mode":              "handoff",
						"confluence":        fmt.Sprintf("%d", conf.Score),
						"confluence_detail":     strings.Join(conf.Factors, "+"),
						"confluence_components": conf.ComponentsJSON(),
					})
					if err != nil {
						return nil, err
					}
					s.PendingEntry = start.SideBuy
					s.PendingEntryAt = now
					s.TradesToday++
					s.CooldownUntil = now.Add(cooldown)
					return &sig, nil
				}
			}
		} else if nearAVWAP >= 0 {
			distBPS := nearAVWAP
			reason = fmt.Sprintf("dist %.0f bps outside range", distBPS)
		}

		// Short handoff: consecutive bars with increasing negative distance from AVWAP.
		if !ec.lockedShort && !strings.EqualFold(cfg.Direction, "LONG") && nearAVWAP <= 0 && nearAVWAP >= -30 {
			allDecreasing := true
			for i := 1; i < len(recent); i++ {
				if recent[i] >= recent[i-1] || recent[i] >= 0 {
					allDecreasing = false
					break
				}
			}
			if !allDecreasing || recent[len(recent)-1] > -minMom {
				reason = "no sustained momentum"
			} else {
				volRatio := 0.0
				if s.Indicators.VolumeSMA > 0 {
					volRatio = bar.Volume / s.Indicators.VolumeSMA
				}
				volumeOK := cfg.VolumeMult == 0 || (s.Indicators.VolumeSMA > 0 && bar.Volume > cfg.VolumeMult*s.Indicators.VolumeSMA)
				if !volumeOK {
					reason = fmt.Sprintf("vol %.1fx < %.1fx min", volRatio, cfg.VolumeMult)
				} else {
					if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "SHORT" {
						logShortGate(ctx, ec.symbol, "enforce_avwap_bias", anchorName, "bias", ec.avwapBias)
						reason = "bias blocks SHORT"
						continue
					}
					if cfg.RequireCapitulationForShorts && ec.avwapBias == "LONG" {
						logShortGate(ctx, ec.symbol, "require_capitulation", anchorName, "bias", ec.avwapBias)
						reason = "capitulation required for short"
						continue
					}
					// lower-highs gate removed for shorts — bias + slope are sufficient
					if cfg.MinSlopeBPS > 0 && ec.slopeOK && ec.avwapSlope > -cfg.MinSlopeBPS {
						logShortGate(ctx, ec.symbol, "min_slope_bps", anchorName, "slope_bps", fmt.Sprintf("%.2f", ec.avwapSlope))
						reason = fmt.Sprintf("slope %.1f bps < %.1f min", ec.avwapSlope, cfg.MinSlopeBPS)
						continue
					}
					shortHandoffAVWAP := avwapValues[anchorName]
					conf := computeConfluence(cfg, bar, shortHandoffAVWAP, ec.avwapValues, s.Indicators,
						s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50, s.LastInducementSignal)
					if conf.Vetoed || (cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore) {
						reason = fmt.Sprintf("confluence %d < %d", conf.Score, cfg.MinConfluenceScore)
						continue
					}
					adjustedStrength := conf.applyDPSizing(math.Min(1.0, 0.85+float64(conf.Score)*0.03)) * ec.sessionMult
					sig, err := s.newEntrySignal(ec, start.SideSell, adjustedStrength, map[string]string{
						"ref_price":         fmt.Sprintf("%.10f", bar.Close),
						"setup":             "avwap_handoff",
						"anchor":            anchorName,
						"avwap":             fmt.Sprintf("%.4f", shortHandoffAVWAP),
						"momentum_bps":      fmt.Sprintf("%.1f", recent[len(recent)-1]),
						"regime_5m":         regimeTag,
						"mode":              "handoff",
						"confluence":        fmt.Sprintf("%d", conf.Score),
						"confluence_detail":     strings.Join(conf.Factors, "+"),
						"confluence_components": conf.ComponentsJSON(),
					})
					if err != nil {
						return nil, err
					}
					s.PendingEntry = start.SideSell
					s.PendingEntryAt = now
					s.TradesToday++
					s.CooldownUntil = now.Add(cooldown)
					return &sig, nil
				}
			}
		}
	}
	s.recordCheck("handoff", reason)
	return nil, nil
}

// evaluateBounce checks section 8: AVWAP bounce entries (long and short).
func (s *AVWAPState) evaluateBounce(ec entryContext) (*start.Signal, error) {
	cfg := ec.cfg
	if !cfg.BounceEnabled {
		s.recordCheck("bounce", "disabled")
		return nil, nil
	}
	bar := ec.bar
	now := ec.now
	cooldown := ec.cooldown
	regimeTag := ec.regimeTag
	avwapValues := ec.avwapValues
	sortedAnchors := ec.sortedAnchors
	ctx := ec.ctx

	reason := "price not touching AVWAP"

	for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
		touchesAVWAP := bar.Low <= avwapValue && avwapValue <= bar.High

		// Long bounce: touches AVWAP + RSI < max + bullish candle.
		if !ec.lockedLong && touchesAVWAP && s.Indicators.RSI > 0 && s.Indicators.RSI < cfg.RSIBounceMax {
			if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "LONG" {
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP bias: blocking long bounce", "symbol", ec.symbol, "bias", ec.avwapBias, "anchor", anchorName)
				}
				reason = "bias blocks LONG bounce"
				continue
			}
			// Regime gating is handled by evaluateEntries (bounce only called in TREND)
			if bar.Close <= bar.Open {
				reason = "candle bearish (need bullish)"
				continue
			}
			conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
				s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50, s.LastInducementSignal)
			if conf.Vetoed || (cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore) {
				reason = fmt.Sprintf("confluence %d < %d", conf.Score, cfg.MinConfluenceScore)
				continue
			}
			adjustedStrength := conf.applyDPSizing(math.Min(1.0, 0.6+float64(conf.Score)*0.03)) * ec.sessionMult
			sig, err := s.newEntrySignal(ec, start.SideBuy, adjustedStrength, map[string]string{
				"ref_price":         fmt.Sprintf("%.10f", bar.Close),
				"setup":             "avwap_bounce",
				"anchor":            anchorName,
				"avwap":             fmt.Sprintf("%.4f", avwapValue),
				"rsi":               fmt.Sprintf("%.2f", s.Indicators.RSI),
				"mode":              "bounce",
				"regime_5m":         regimeTag,
				"confluence":        fmt.Sprintf("%d", conf.Score),
				"confluence_detail":     strings.Join(conf.Factors, "+"),
				"confluence_components": conf.ComponentsJSON(),
			})
			if err != nil {
				return nil, err
			}
			s.PendingEntry = start.SideBuy
			s.PendingEntryAt = now
			s.TradesToday++
			s.CooldownUntil = now.Add(cooldown)
			return &sig, nil
		}

		// Direction guard: skip short entries in long-only mode (e.g. crypto).
		if strings.EqualFold(cfg.Direction, "LONG") {
			continue
		}

		// Short bounce: touches AVWAP + RSI > min + bearish candle.
		if !ec.lockedShort && touchesAVWAP && s.Indicators.RSI > cfg.RSIBounceMin {
			if cfg.EnforceAVWAPBias && ec.avwapBias != "" && ec.avwapBias != "SHORT" {
				logShortGate(ctx, ec.symbol, "enforce_avwap_bias", anchorName, "bias", ec.avwapBias)
				if ctx != nil && ctx.Logger() != nil {
					ctx.Logger().Info("AVWAP bias: blocking short bounce", "symbol", ec.symbol, "bias", ec.avwapBias, "anchor", anchorName)
				}
				reason = "bias blocks SHORT bounce"
				continue
			}
			// Regime gating is handled by evaluateEntries (bounce only called in TREND)
			if bar.Close >= bar.Open {
				logShortGate(ctx, ec.symbol, "bearish_candle", anchorName)
				reason = "candle bullish (need bearish)"
				continue
			}
			if cfg.RequireCapitulationForShorts && ec.avwapBias == "LONG" {
				logShortGate(ctx, ec.symbol, "require_capitulation", anchorName, "bias", ec.avwapBias)
				reason = "capitulation required for short"
				continue // block short bounces above AVWAP without capitulation
			}
			conf := computeConfluence(cfg, bar, avwapValue, ec.avwapValues, s.Indicators,
				s.PrevBars, s.PrevBarCount, ec.keyLevels, s.BarHighs50, s.BarLows50, s.LastInducementSignal)
			if conf.Vetoed || (cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore) {
				reason = fmt.Sprintf("confluence %d < %d", conf.Score, cfg.MinConfluenceScore)
				continue
			}
			adjustedStrength := conf.applyDPSizing(math.Min(1.0, 0.6+float64(conf.Score)*0.03)) * ec.sessionMult
			sig, err := s.newEntrySignal(ec, start.SideSell, adjustedStrength, map[string]string{
				"ref_price":         fmt.Sprintf("%.10f", bar.Close),
				"setup":             "avwap_bounce",
				"anchor":            anchorName,
				"avwap":             fmt.Sprintf("%.4f", avwapValue),
				"rsi":               fmt.Sprintf("%.2f", s.Indicators.RSI),
				"mode":              "bounce",
				"regime_5m":         regimeTag,
				"confluence":        fmt.Sprintf("%d", conf.Score),
				"confluence_detail":     strings.Join(conf.Factors, "+"),
				"confluence_components": conf.ComponentsJSON(),
			})
			if err != nil {
				return nil, err
			}
			s.PendingEntry = start.SideSell
			s.PendingEntryAt = now
			s.TradesToday++
			s.CooldownUntil = now.Add(cooldown)
			return &sig, nil
		}
	}
	s.recordCheck("bounce", reason)
	return nil, nil
}

// evaluateEntries runs all entry checks in conviction-priority order.
// Confluence scoring handles quality filtering — no regime gating needed.
// Priority (per Brian Shannon research):
//   1. Pinch — massive pent-up energy from AVWAP convergence
//   2. Capitulation Reclaim — institutional turnover / exhaustion reversal
//   3. Gap Reclaim — immediate response to a fresh catalyst
//   4. Pullback — confluence entry (AVWAP + trend structure)
//   5. Handoff — riding an accelerating "fast trend"
//   6. Breakout — sustained above/below AVWAP with volume
//   7. Bounce — lowest conviction, mean-reversion at AVWAP
func (s *AVWAPState) evaluateEntries(ec entryContext) (*start.Signal, error) {
	s.entryChecks = s.entryChecks[:0]

	// Z conditioning: suppress entries on extreme adverse Z days.
	if ec.cfg.DPZConditioningEnabled && ec.cfg.DPZSuppressAdverseEntries &&
		s.Indicators.LateSessionDPZ >= ec.cfg.DPZSuppressThreshold {
		return nil, nil
	}

	var sig *start.Signal
	var err error
	if ec.cfg.EntryPriority == "bounce_first" {
		sig, err = s.evaluateEntriesBounceFirst(ec)
	} else {
		sig, err = s.evaluateEntriesStandard(ec)
	}
	return s.applyCofireVeto(ec, sig, err)
}

// evaluateEntriesStandard is the default entry priority order:
// pinch > cap_reclaim > gap_reclaim > pullback > handoff > breakout > bounce.
func (s *AVWAPState) evaluateEntriesStandard(ec entryContext) (*start.Signal, error) {
	if sig, err := s.evaluatePinch(ec); err != nil || sig != nil {
		if sig != nil {
			s.recordCheckPassed("pinch")
		}
		return sig, err
	}
	if sig, err := s.evaluateCapitulationReclaim(ec); err != nil || sig != nil {
		if sig != nil {
			s.recordCheckPassed("cap_reclaim")
		}
		return sig, err
	}
	if sig, err := s.evaluateGapReclaim(ec); err != nil || sig != nil {
		if sig != nil {
			s.recordCheckPassed("gap_reclaim")
		}
		return sig, err
	}
	if sig, err := s.evaluatePullback(ec); err != nil || sig != nil {
		if sig != nil {
			s.recordCheckPassed("pullback")
		}
		return sig, err
	}
	if sig, err := s.evaluateHandoff(ec); err != nil || sig != nil {
		if sig != nil {
			s.recordCheckPassed("handoff")
		}
		return sig, err
	}
	if sig, err := s.evaluateBreakout(ec); err != nil || sig != nil {
		if sig != nil {
			s.recordCheckPassed("breakout")
		}
		return sig, err
	}
	if sig, err := s.evaluateBounce(ec); err != nil || sig != nil {
		if sig != nil {
			s.recordCheckPassed("bounce")
		}
		return sig, err
	}
	return nil, nil
}

// evaluateEntriesBounceFirst reorders entries to prioritize pullback-to-AVWAP
// setups, which tend to perform better for crypto's mean-reverting microstructure.
// Order: bounce > pullback > pinch > cap_reclaim > gap_reclaim > handoff > breakout.
func (s *AVWAPState) evaluateEntriesBounceFirst(ec entryContext) (*start.Signal, error) {
	if sig, err := s.evaluateBounce(ec); err != nil || sig != nil {
		if sig != nil {
			s.recordCheckPassed("bounce")
		}
		return sig, err
	}
	if sig, err := s.evaluatePullback(ec); err != nil || sig != nil {
		if sig != nil {
			s.recordCheckPassed("pullback")
		}
		return sig, err
	}
	if sig, err := s.evaluatePinch(ec); err != nil || sig != nil {
		if sig != nil {
			s.recordCheckPassed("pinch")
		}
		return sig, err
	}
	if sig, err := s.evaluateCapitulationReclaim(ec); err != nil || sig != nil {
		if sig != nil {
			s.recordCheckPassed("cap_reclaim")
		}
		return sig, err
	}
	if sig, err := s.evaluateGapReclaim(ec); err != nil || sig != nil {
		if sig != nil {
			s.recordCheckPassed("gap_reclaim")
		}
		return sig, err
	}
	if sig, err := s.evaluateHandoff(ec); err != nil || sig != nil {
		if sig != nil {
			s.recordCheckPassed("handoff")
		}
		return sig, err
	}
	if sig, err := s.evaluateBreakout(ec); err != nil || sig != nil {
		if sig != nil {
			s.recordCheckPassed("breakout")
		}
		return sig, err
	}
	return nil, nil
}

// EmitSignalProgress returns the current AVWAP confluence state as a domain event payload.
// Implements start.SignalProgressEmitter for post-warmup SSE cache seeding.
func (s *AVWAPState) EmitSignalProgress() []any {
	if s.Calc == nil || s.CalcBarCount < 2 {
		return nil
	}
	cfg := s.Config
	avwapValues := s.Calc.Values()
	if len(avwapValues) == 0 {
		return nil
	}

	// Compute confluence for first anchor.
	var conf confluenceResult
	if len(cfg.Anchors) > 0 {
		if avwapVal, ok := avwapValues[cfg.Anchors[0]]; ok {
			conf = computeConfluence(
				cfg, s.PrevBars[0], avwapVal, avwapValues,
				s.Indicators, s.PrevBars, s.PrevBarCount,
				s.KeyLevels, s.BarHighs50, s.BarLows50,
				s.LastInducementSignal,
			)
		}
	}

	// Determine bias from first anchor.
	avwapBias := ""
	if len(cfg.Anchors) > 0 && s.CalcBarCount >= 10 {
		if firstAVWAP, ok := avwapValues[cfg.Anchors[0]]; ok {
			bar := s.PrevBars[0]
			if bar.Close > firstAVWAP {
				avwapBias = "LONG"
			} else if bar.Close < firstAVWAP {
				avwapBias = "SHORT"
			}
		}
	}

	var slopeBPS float64
	if cfg.MinSlopeBPS > 0 && len(cfg.Anchors) > 0 {
		slopeBPS, _ = s.Calc.Slope(cfg.Anchors[0], cfg.SlopeLookback)
	}

	volRatio := 0.0
	if s.Indicators.VolumeSMA > 0 {
		volRatio = s.PrevBars[0].Volume / s.Indicators.VolumeSMA
	}

	factorSet := make(map[string]bool)
	for _, f := range conf.Factors {
		factorSet[f] = true
	}

	// Evaluate gates the same way as emitEntryGated.
	gatesTotal := 5 // regime, position_flat, bias, slope, confluence
	gatesPassed := 2 // regime and position_flat always passed
	biasOK := avwapBias != ""
	if biasOK {
		gatesPassed++
	}
	slopeOK := cfg.MinSlopeBPS == 0 || math.Abs(slopeBPS) >= cfg.MinSlopeBPS
	if slopeOK {
		gatesPassed++
	}
	confluenceOK := !conf.Vetoed && conf.Score >= cfg.MinConfluenceScore
	if confluenceOK {
		gatesPassed++
	}
	var blockingGate, blockingDetail string
	switch {
	case confluenceOK:
		blockingGate = "entry_specific"
		blockingDetail = "confluence met but no entry type conditions satisfied"
	case !biasOK:
		blockingGate = "bias"
		blockingDetail = "no directional bias established"
	case !slopeOK:
		blockingGate = "slope"
		blockingDetail = fmt.Sprintf("%.2f bps < %.2f min", slopeBPS, cfg.MinSlopeBPS)
	default:
		blockingGate = "confluence"
		blockingDetail = fmt.Sprintf("score %d < %d min", conf.Score, cfg.MinConfluenceScore)
	}

	// Populate entryChecks when at entry_specific gate (confluence passed).
	if blockingGate == "entry_specific" && s.PrevBarCount > 0 && s.Calc != nil {
		ec := entryContext{
			cfg:           cfg,
			bar:           s.PrevBars[0],
			symbol:        s.Symbol,
			now:           s.PrevBars[0].Time,
			avwapValues:   avwapValues,
			sortedAnchors: s.Calc.SortedNames(),
			avwapBias:     avwapBias,
			avwapSlope:    slopeBPS,
			slopeOK:       slopeOK,
			keyLevels:     s.KeyLevels,
			spyTideDevBps: s.SpyTideDevBps,
			spyTideReady:  s.SpyTideReady,
			tideIndexName: s.TideIndexName,
			sessionMult:   1.0, // preview context — no session gating
		}
		_, _ = s.evaluateEntries(ec)
	}

	indicators := indicatorsFromData(s.Indicators)
	indicators.VolumeRatio = volRatio
	indicators.AVWAPBias = avwapBias
	indicators.SlopeBPS = slopeBPS
	indicators.AboveCount = copyIntMap(s.AboveCount)
	indicators.BelowCount = copyIntMap(s.BelowCount)
	indicators.Volume = s.PrevBars[0].Volume

	payload := domain.EntryGatedPayload{
		Symbol:       s.Symbol,
		Strategy:     "avwap",
		SetupType:    "multi",
		GatesPassed:  gatesPassed,
		GatesTotal:   gatesTotal,
		BlockingGate: blockingGate,
		BlockingDetail: blockingDetail,
		EntryChecks:  s.entryChecks,
		Confluence: domain.EntryGatedConfluence{
			Score:          conf.Score,
			MaxScore:       cfg.MinConfluenceScore,
			Fib:            factorSet["fib_38.2"] || factorSet["fib_50"] || factorSet["fib_61.8"],
			FibDetail:      extractFactor(conf.Factors, "fib_"),
			KeyLevel:       extractFactor(conf.Factors, "key_") != "",
			KeyLevelDetail: extractFactor(conf.Factors, "key_"),
			Candle:         factorSet["inside_bar"] || factorSet["strength_candle"] || factorSet["morning_star"],
			CandleDetail:   extractCandleFactor(conf.Factors),
			Band:           factorSet["band_zone"],
			Components:     toEntryGatedComponents(conf.Components),
		},
		Indicators: indicators,
		AVWAPState: avwapStateFromCalc(s.Calc, cfg.SlopeLookback),
		Bar: domain.BarSnapshot{
			Time:   s.PrevBars[0].Time,
			Open:   s.PrevBars[0].Open,
			High:   s.PrevBars[0].High,
			Low:    s.PrevBars[0].Low,
			Close:  s.PrevBars[0].Close,
			Volume: s.PrevBars[0].Volume,
		},
	}
	return []any{payload}
}

// emitEarlyGated publishes an EntryGated event for early gate returns (cooldown,
// hours, position, regime) where the full entryContext is not yet available.
func (s *AVWAPState) emitEarlyGated(ctx start.Context, symbol string, bar start.Bar, blockingGate, blockingDetail string) {
	// Record the HOLD rationale regardless of ProgressEventsSuppressed — the
	// liveness tracker surfaces it via LastHoldReason on the generic
	// DecisionReason path and that runs in both live and backtest modes.
	if s != nil && s.parent != nil {
		s.parent.recordHoldReason(symbol, blockingGate, blockingDetail, nil)
	}
	if ctx == nil {
		return
	}
	if ctx.ProgressEventsSuppressed() {
		return
	}
	if bar.Time.Equal(s.LastGatedBarTime) {
		return
	}
	s.LastGatedBarTime = bar.Time

	cfg := s.Config
	var score int
	if s.Calc != nil && len(cfg.Anchors) > 0 {
		if avwapVal, ok := s.Calc.Values()[cfg.Anchors[0]]; ok {
			conf := computeConfluence(cfg, bar, avwapVal, s.Calc.Values(),
				s.Indicators, s.PrevBars, s.PrevBarCount,
				s.KeyLevels, s.BarHighs50, s.BarLows50, s.LastInducementSignal)
			score = conf.Score
		}
	}

	avwapBias := ""
	if s.Calc != nil && len(cfg.Anchors) > 0 {
		if firstAVWAP, ok := s.Calc.Values()[cfg.Anchors[0]]; ok {
			if bar.Close > firstAVWAP {
				avwapBias = "LONG"
			} else if bar.Close < firstAVWAP {
				avwapBias = "SHORT"
			}
		}
	}

	var slopeBPS float64
	if cfg.MinSlopeBPS > 0 && s.Calc != nil && len(cfg.Anchors) > 0 {
		slopeBPS, _ = s.Calc.Slope(cfg.Anchors[0], cfg.SlopeLookback)
	}

	indicators := indicatorsFromData(s.Indicators)
	indicators.AVWAPBias = avwapBias
	indicators.SlopeBPS = slopeBPS
	indicators.Volume = bar.Volume

	payload := domain.EntryGatedPayload{
		Symbol:         symbol,
		Strategy:       "avwap",
		SetupType:      "multi",
		GatesPassed:    0,
		GatesTotal:     5,
		BlockingGate:   blockingGate,
		BlockingDetail: blockingDetail,
		Confluence: domain.EntryGatedConfluence{
			Score:    score,
			MaxScore: cfg.MinConfluenceScore,
		},
		Indicators: indicators,
		AVWAPState: avwapStateFromCalc(s.Calc, cfg.SlopeLookback),
		Bar: domain.BarSnapshot{
			Time: bar.Time, Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume,
		},
	}
	_ = ctx.EmitDomainEvent(payload)
}

// emitEntryGated publishes an EntryGated domain event showing how close this
// symbol is to triggering an AVWAP entry signal. Called once per bar when all
// entry types failed.
func (s *AVWAPState) emitEntryGated(ec entryContext) {
	if ec.ctx == nil {
		return
	}

	// Determine blocking gate and count passed gates.
	gatesTotal := 5 // regime, position_flat, bias, slope, confluence
	gatesPassed := 2 // regime and position_flat always passed here (we're past those gates in OnBar)

	biasOK := ec.avwapBias != ""
	if biasOK {
		gatesPassed++
	}

	slopeOK := !ec.slopeOK || ec.cfg.MinSlopeBPS == 0 || math.Abs(ec.avwapSlope) >= ec.cfg.MinSlopeBPS
	if slopeOK {
		gatesPassed++
	}

	// Compute confluence for first anchor to show the score.
	var conf confluenceResult
	if len(ec.cfg.Anchors) > 0 {
		firstAnchor := ec.cfg.Anchors[0]
		if avwapVal, ok := ec.avwapValues[firstAnchor]; ok {
			conf = computeConfluence(
				ec.cfg, ec.bar, avwapVal, ec.avwapValues,
				s.Indicators, s.PrevBars, s.PrevBarCount,
				s.KeyLevels, s.BarHighs50, s.BarLows50,
				s.LastInducementSignal,
			)
		}
	}

	confluenceOK := !conf.Vetoed && conf.Score >= ec.cfg.MinConfluenceScore
	if confluenceOK {
		gatesPassed++
	}

	// Determine the most relevant blocking gate (last failing gate in priority order).
	var blockingGate, blockingDetail string
	switch {
	case confluenceOK:
		blockingGate = "entry_specific"
		blockingDetail = "confluence met but no entry type conditions satisfied"
	case !biasOK:
		blockingGate = "bias"
		blockingDetail = "no directional bias established"
	case !slopeOK:
		blockingGate = "slope"
		blockingDetail = fmt.Sprintf("%.2f bps < %.2f min", ec.avwapSlope, ec.cfg.MinSlopeBPS)
	default:
		blockingGate = "confluence"
		blockingDetail = fmt.Sprintf("score %d < %d min", conf.Score, ec.cfg.MinConfluenceScore)
	}

	// Build factor breakdown for confluence.
	factorSet := make(map[string]bool)
	for _, f := range conf.Factors {
		factorSet[f] = true
	}

	volRatio := 0.0
	if s.Indicators.VolumeSMA > 0 {
		volRatio = ec.bar.Volume / s.Indicators.VolumeSMA
	}

	// Record the HOLD rationale for the liveness tracker so the dashboard
	// can show "why HOLD" with the actual score/threshold numbers, not just
	// a generic blocking-gate tag.
	if s != nil && s.parent != nil {
		extra := map[string]string{
			"score":       fmt.Sprintf("%d", conf.Score),
			"threshold":   fmt.Sprintf("%d", ec.cfg.MinConfluenceScore),
			"avwap_bias":  ec.avwapBias,
			"slope_bps":   fmt.Sprintf("%.2f", ec.avwapSlope),
			"regime":      ec.regimeTag,
		}
		s.parent.recordHoldReason(ec.symbol, blockingGate, blockingDetail, extra)
	}

	indicators := indicatorsFromData(s.Indicators)
	indicators.VolumeRatio = volRatio
	indicators.AVWAPBias = ec.avwapBias
	indicators.SlopeBPS = ec.avwapSlope
	indicators.AboveCount = copyIntMap(s.AboveCount)
	indicators.BelowCount = copyIntMap(s.BelowCount)
	indicators.Volume = ec.bar.Volume

	payload := domain.EntryGatedPayload{
		Symbol:        ec.symbol,
		Strategy:      "avwap",
		SetupType:     "multi", // evaluated all entry types
		GatesPassed:   gatesPassed,
		GatesTotal:    gatesTotal,
		BlockingGate:  blockingGate,
		BlockingDetail: blockingDetail,
		EntryChecks:   s.entryChecks,
		Confluence: domain.EntryGatedConfluence{
			Score:    conf.Score,
			MaxScore: ec.cfg.MinConfluenceScore,
			Fib:      factorSet["fib_38.2"] || factorSet["fib_50"] || factorSet["fib_61.8"],
			FibDetail: extractFactor(conf.Factors, "fib_"),
			KeyLevel: extractFactor(conf.Factors, "key_") != "",
			KeyLevelDetail: extractFactor(conf.Factors, "key_"),
			Candle:   factorSet["inside_bar"] || factorSet["strength_candle"] || factorSet["morning_star"],
			CandleDetail: extractCandleFactor(conf.Factors),
			Band:     factorSet["band_zone"],
			Components: toEntryGatedComponents(conf.Components),
		},
		Indicators: indicators,
		AVWAPState: avwapStateFromCalc(s.Calc, ec.cfg.SlopeLookback),
		Bar: domain.BarSnapshot{
			Time:   ec.bar.Time,
			Open:   ec.bar.Open,
			High:   ec.bar.High,
			Low:    ec.bar.Low,
			Close:  ec.bar.Close,
			Volume: ec.bar.Volume,
		},
	}

	_ = ec.ctx.EmitDomainEvent(payload)
}

func extractFactor(factors []string, prefix string) string {
	for _, f := range factors {
		if strings.HasPrefix(f, prefix) {
			return f
		}
	}
	return ""
}

func extractCandleFactor(factors []string) string {
	for _, f := range factors {
		if f == "inside_bar" || f == "strength_candle" || f == "morning_star" {
			return f
		}
	}
	return ""
}

// maxAboveCount returns the maximum AboveCount across anchors for entry check reasons.
func maxAboveCount(counts map[string]int, anchors []string) int {
	best := 0
	for _, a := range anchors {
		if counts[a] > best {
			best = counts[a]
		}
	}
	return best
}

// maxHistLen returns the maximum history length across anchors for entry check reasons.
func maxHistLen(hist map[string][]float64, anchors []string) int {
	best := 0
	for _, a := range anchors {
		if len(hist[a]) > best {
			best = len(hist[a])
		}
	}
	return best
}

func copyIntMap(m map[string]int) map[string]int {
	if m == nil {
		return nil
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// toEntryGatedComponents copies a strategy ComponentScore slice into the
// JSON DTO domain.EntryGatedComponent expected by EntryGatedConfluence.
// Used by both AVWAP and MACD EntryGated emit paths so live and backtest
// blocked rows carry the same per-factor breakdown for SQL diffing.
func toEntryGatedComponents(in []start.ComponentScore) []domain.EntryGatedComponent {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.EntryGatedComponent, len(in))
	for i, c := range in {
		out[i] = domain.EntryGatedComponent{
			Name:     c.Name,
			Group:    c.Group,
			Weight:   c.Weight,
			Value:    c.Value,
			Fired:    c.Fired,
			SubScore: c.SubScore,
			Inputs:   c.Inputs,
		}
	}
	return out
}

// indicatorsFromData copies the raw diagnostic indicator inputs from an
// IndicatorData snapshot into an EntryGatedIndicators payload. Site-specific
// fields (VolumeRatio, AVWAPBias, SlopeBPS, AboveCount/BelowCount, Volume)
// are filled in by the caller because each emit site reads bar.Volume from a
// different local variable (s.PrevBars[0] / bar / ec.bar) and the AVWAP-only
// fields don't apply to MACD.
func indicatorsFromData(ind start.IndicatorData) domain.EntryGatedIndicators {
	return domain.EntryGatedIndicators{
		RSI:           ind.RSI,
		VolumeSMA:     ind.VolumeSMA,
		MACDLine:      ind.MACDLine,
		MACDSignal:    ind.MACDSignal,
		MACDHistogram: ind.MACDHistogram,
		EMA21:         ind.EMA21,
		EMA50:         ind.EMA50,
		StochK:        ind.StochK,
		StochD:        ind.StochD,
	}
}

// avwapStateFromCalc snapshots the AnchoredVWAPCalc into the JSON DTO
// expected by EntryGatedPayload.AVWAPState. Returns nil when calc is
// nil; the pointer field on EntryGatedPayload then drops the key from
// the emitted JSON. slopeLookback must match the strategy's gate
// configuration so the diagnostic slope and the gate slope agree.
func avwapStateFromCalc(c *start.AnchoredVWAPCalc, slopeLookback int) *domain.EntryGatedAVWAPState {
	if c == nil {
		return nil
	}
	snap := c.Snapshot(slopeLookback)
	if len(snap) == 0 {
		return &domain.EntryGatedAVWAPState{LastBarTime: c.LastBarTime()}
	}
	anchors := make(map[string]domain.EntryGatedAnchor, len(snap))
	for name, a := range snap {
		anchors[name] = domain.EntryGatedAnchor{
			VWAP:      a.VWAP,
			SlopeBPS:  a.SlopeBPS,
			BarCount:  a.BarCount,
			VWAPCount: a.VWAPCount,
			Active:    a.Active,
		}
	}
	return &domain.EntryGatedAVWAPState{
		LastBarTime: c.LastBarTime(),
		AnchorCount: len(anchors),
		Anchors:     anchors,
	}
}

// --- Confluence scoring ---

type confluenceResult struct {
	Score      int
	Factors    []string
	Components []start.ComponentScore
	Vetoed     bool    // true when DP veto blocked this entry
	dpIsLong   bool    // cached direction for DP sizing
	dpCfg      *AVWAPConfig // reference to config for sizing
	dpInd      *start.IndicatorData // reference to indicators for sizing
}

// ComponentsJSON returns the Components serialized as a compact JSON string
// for inclusion in signal tags. Returns "" if no components.
func (cr confluenceResult) ComponentsJSON() string {
	if len(cr.Components) == 0 {
		return ""
	}
	type comp struct {
		Name   string  `json:"n"`
		Group  string  `json:"g"`
		Weight int     `json:"w"`
		Value  float64 `json:"v,omitempty"`
		Fired  bool    `json:"f"`
	}
	out := make([]comp, len(cr.Components))
	for i, c := range cr.Components {
		out[i] = comp{Name: c.Name, Group: c.Group, Weight: c.Weight, Value: c.Value, Fired: c.Fired}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// SessionBucket maps a time in the given location to a session bucket name.
func SessionBucket(t time.Time, loc *time.Location) string {
	lt := t.In(loc)
	h, m := lt.Hour(), lt.Minute()
	mins := h*60 + m // minutes since midnight

	switch {
	case mins >= 570 && mins < 600: // 09:30-10:00
		return "open"
	case mins >= 600 && mins < 630: // 10:00-10:30
		return "extended_open"
	case mins >= 630 && mins < 720: // 10:30-12:00
		return "mid_morning"
	case mins >= 720 && mins < 840: // 12:00-14:00
		return "lunch"
	case mins >= 840 && mins < 900: // 14:00-15:00
		return "afternoon"
	case mins >= 900 && mins < 930: // 15:00-15:30
		return "moc"
	case mins >= 930 && mins < 960: // 15:30-16:00
		return "close"
	default:
		return "outside"
	}
}

// CryptoSessionBucket maps a time in the given location to a crypto-specific
// 24/7 session bucket. Unlike equity RTH buckets, crypto never returns "outside"
// — the entire day is covered by four regional liquidity windows.
func CryptoSessionBucket(t time.Time, loc *time.Location) string {
	lt := t.In(loc)
	h, m := lt.Hour(), lt.Minute()
	mins := h*60 + m
	switch {
	case mins >= 570 && mins < 960: // 09:30-16:00 ET
		return "us_peak"
	case mins >= 960 && mins < 1260: // 16:00-21:00 ET
		return "us_evening"
	case mins >= 1260 || mins < 240: // 21:00-04:00 ET (crosses midnight)
		return "asia_peak"
	default: // 04:00-09:30 ET
		return "europe_peak"
	}
}

// SessionWeight returns the session bucket name and its weight multiplier for the given time.
func (cfg AVWAPConfig) SessionWeight(t time.Time) (string, float64) {
	// Crypto uses 24/7 buckets instead of equity RTH buckets,
	// but only when at least one crypto weight is configured.
	if strings.EqualFold(cfg.AssetClass, "CRYPTO") && cfg.hasCryptoWeights() {
		return cfg.cryptoSessionWeight(t)
	}

	tz := cfg.SessionWeightTZ
	if tz == "" {
		tz = "America/New_York"
	}
	loc := cachedLocation(tz)
	if loc == nil {
		loc = etLocation
	}
	bucket := SessionBucket(t, loc)
	switch bucket {
	case "open":
		return bucket, cfg.SessionWeightOpen
	case "extended_open":
		return bucket, cfg.SessionWeightExtendedOpen
	case "mid_morning":
		return bucket, cfg.SessionWeightMidMorning
	case "lunch":
		return bucket, cfg.SessionWeightLunch
	case "afternoon":
		return bucket, cfg.SessionWeightAfternoon
	case "moc":
		return bucket, cfg.SessionWeightMOC
	case "close":
		return bucket, cfg.SessionWeightClose
	default:
		return bucket, cfg.SessionWeightOutside
	}
}

// hasCryptoWeights returns true if any crypto session weight is configured (non-zero).
func (cfg AVWAPConfig) hasCryptoWeights() bool {
	return cfg.SessionWeightUSPeak > 0 || cfg.SessionWeightUSEvening > 0 ||
		cfg.SessionWeightAsiaPeak > 0 || cfg.SessionWeightEuropePeak > 0
}

// cryptoSessionWeight resolves the crypto session bucket and its configured weight.
func (cfg AVWAPConfig) cryptoSessionWeight(t time.Time) (string, float64) {
	tz := cfg.SessionWeightTZ
	if tz == "" {
		tz = "America/New_York"
	}
	loc := cachedLocation(tz)
	if loc == nil {
		loc = etLocation
	}
	bucket := CryptoSessionBucket(t, loc)
	switch bucket {
	case "us_peak":
		return bucket, cfg.SessionWeightUSPeak
	case "us_evening":
		return bucket, cfg.SessionWeightUSEvening
	case "asia_peak":
		return bucket, cfg.SessionWeightAsiaPeak
	case "europe_peak":
		return bucket, cfg.SessionWeightEuropePeak
	default:
		return bucket, 0
	}
}

// applyDPSizing adjusts strength with DP sizing multiplier when configured.
func (cr confluenceResult) applyDPSizing(strength float64) float64 {
	if cr.dpCfg == nil || !cr.dpCfg.DPSizingEnabled {
		return strength
	}
	strength *= start.DPSizingMultiplier(*cr.dpInd, cr.dpIsLong, cr.dpCfg.DPSizingMaxBoost)
	if strength > 1.0 {
		strength = 1.0
	}
	return strength
}

func computeConfluence(
	cfg AVWAPConfig,
	bar start.Bar,
	avwapValue float64,
	avwapValues map[string]float64,
	indicators start.IndicatorData,
	prevBars [2]start.Bar,
	prevBarCount int,
	keyLevels map[string]float64,
	barHighs50, barLows50 []float64,
	inducementSig *start.InducementSignal,
) confluenceResult {
	var res confluenceResult

	// DP veto gate — block entry when institutional flow opposes direction.
	isLongEntry := bar.Close > avwapValue
	if cfg.DPVetoEnabled {
		if blocked, _ := start.DPVeto(indicators, isLongEntry, cfg.DPVetoBuyRatioMin, cfg.DPVetoSellRatioMax); blocked {
			res.Vetoed = true
			return res
		}
	}

	// Cache DP sizing references for downstream applyDPSizing calls.
	res.dpIsLong = isLongEntry
	cfgCopy := cfg
	res.dpCfg = &cfgCopy
	indCopy := indicators
	res.dpInd = &indCopy

	// Tolerance: ATR/2, fallback to avwapValue*0.002
	tolerance := avwapValue * 0.002
	if indicators.ATR > 0 {
		tolerance = indicators.ATR / 2
	}

	// Factor 1: Fibonacci (+3)
	fibComp := start.ComponentScore{Name: "fib", Group: "structure", Weight: 3}
	if cfg.FibConfluenceEnabled && len(barHighs50) >= 20 && len(barLows50) >= 20 {
		maxH := barHighs50[0]
		for _, h := range barHighs50[1:] {
			if h > maxH {
				maxH = h
			}
		}
		minL := barLows50[0]
		for _, l := range barLows50[1:] {
			if l < minL {
				minL = l
			}
		}
		rng := maxH - minL
		if rng > 0 {
			fib382 := maxH - rng*0.382
			fib500 := maxH - rng*0.500
			fib618 := maxH - rng*0.618
			for _, fib := range []struct {
				level float64
				name  string
			}{
				{fib382, "fib_38.2"},
				{fib500, "fib_50"},
				{fib618, "fib_61.8"},
			} {
				if math.Abs(avwapValue-fib.level) <= tolerance {
					res.Score += 3
					res.Factors = append(res.Factors, fib.name)
					fibComp.Fired = true
					fibComp.Value = fib.level
					break // only count once
				}
			}
		}
	}
	res.Components = append(res.Components, fibComp)

	// Factor 2: Key Level (+3)
	keyComp := start.ComponentScore{Name: "key_level", Group: "structure", Weight: 3}
	if cfg.KeyLevelConfluenceEnabled && len(keyLevels) > 0 {
		for name, level := range keyLevels {
			if math.Abs(avwapValue-level) <= tolerance {
				res.Score += 3
				res.Factors = append(res.Factors, "key_"+name)
				keyComp.Fired = true
				keyComp.Value = level
				break // only count once
			}
		}
	}
	res.Components = append(res.Components, keyComp)

	// Factor 3: Candlestick (+2)
	candleComp := start.ComponentScore{Name: "candle_pattern", Group: "price_action", Weight: 2}
	if cfg.CandleConfluenceEnabled && prevBarCount >= 1 {
		matched := false

		// Inside Bar: current bar range significantly smaller than previous bar
		// (must be contained AND range < 70% of previous range — filters noise)
		prevRange := prevBars[0].High - prevBars[0].Low
		curRange := bar.High - bar.Low
		if bar.High <= prevBars[0].High && bar.Low >= prevBars[0].Low &&
			prevRange > 0 && curRange < 0.7*prevRange {
			res.Score += 2
			res.Factors = append(res.Factors, "inside_bar")
			candleComp.Fired = true
			matched = true
		}

		// Strength candle: body > 60% of range
		if !matched && bar.High != bar.Low {
			body := bar.Close - bar.Open
			if body < 0 {
				body = -body
			}
			rng := bar.High - bar.Low
			if body > 0.6*rng {
				res.Score += 2
				res.Factors = append(res.Factors, "strength_candle")
				candleComp.Fired = true
				matched = true
			}
		}

		// Morning Star: 3-bar pattern (prevBars[1] bearish, prevBars[0] small body, bar bullish)
		if !matched && prevBarCount >= 2 {
			prev2 := prevBars[1]
			prev1 := prevBars[0]
			prev2Bearish := prev2.Close < prev2.Open
			prev1Range := prev1.High - prev1.Low
			prev1Body := prev1.Close - prev1.Open
			if prev1Body < 0 {
				prev1Body = -prev1Body
			}
			prev1Small := prev1Range > 0 && prev1Body < 0.3*prev1Range
			curBullish := bar.Close > bar.Open
			if prev2Bearish && prev1Small && curBullish {
				res.Score += 2
				res.Factors = append(res.Factors, "morning_star")
				candleComp.Fired = true
			}
		}
	}
	res.Components = append(res.Components, candleComp)

	// Factor 4: Band Zone (+2)
	bandComp := start.ComponentScore{Name: "band_zone", Group: "structure", Weight: 2}
	if cfg.BandConfluenceEnabled && len(avwapValues) >= 2 {
		var minAVWAP, maxAVWAP float64
		first := true
		for _, v := range avwapValues {
			if first || v < minAVWAP {
				minAVWAP = v
			}
			if first || v > maxAVWAP {
				maxAVWAP = v
			}
			first = false
		}
		if bar.Close >= minAVWAP && bar.Close <= maxAVWAP {
			res.Score += 2
			res.Factors = append(res.Factors, "band_zone")
			bandComp.Fired = true
		}
	}
	res.Components = append(res.Components, bandComp)

	// Factor 5: Dark Pool (+10 max, opt-in via config)
	if cfg.DPConfluenceEnabled {
		isLong := bar.Close > avwapValue
		dp := start.ScoreDarkPool(indicators, isLong)
		res.Score += dp.Score
		res.Factors = append(res.Factors, dp.Factors...)
		res.Components = append(res.Components, dp.Components...)
	}

	// Factor 6: Whale Accumulation (+3 max, from 13F filings)
	whaleComp := start.ComponentScore{Name: "whale", Group: "flow", Weight: 3, Value: float64(indicators.WhaleScore)}
	if indicators.WhaleScore >= 6 {
		res.Score += 3
		res.Factors = append(res.Factors, "whale_strong")
		whaleComp.Fired = true
	} else if indicators.WhaleScore >= 3 {
		res.Score += 2
		res.Factors = append(res.Factors, "whale_moderate")
		whaleComp.Fired = true
	}
	res.Components = append(res.Components, whaleComp)

	// Factor 7: Inducement (liquidity sweep, +5 max, opt-in).
	// Only fires when the detected sweep direction matches the entry direction
	// implied by price vs AVWAP. Sweep of a swing HIGH (SideSell) aligns with
	// short entries (isLongEntry=false); sweep of a swing LOW (SideBuy) aligns
	// with longs.
	inducementComp := start.ComponentScore{Name: "inducement", Group: "microstructure", Weight: 5}
	if cfg.InducementEnabled && inducementSig != nil {
		sigIsLong := inducementSig.Direction == start.SideBuy
		if sigIsLong == isLongEntry {
			res.Score += inducementSig.Score
			res.Factors = append(res.Factors, inducementSig.Tag)
			inducementComp.Fired = true
			inducementComp.Value = float64(inducementSig.Score)
		}
	}
	res.Components = append(res.Components, inducementComp)

	return res
}

// OnBar processes a bar and emits breakout/bounce/exit signals.
func (s *AVWAPStrategy) OnBar(ctx start.Context, symbol string, bar start.Bar, st start.State) (start.State, []start.Signal, error) {
	avwapSt, ok := st.(*AVWAPState)
	if !ok {
		return st, nil, fmt.Errorf("AVWAPStrategy.OnBar: expected *AVWAPState, got %T", st)
	}
	cfg := avwapSt.Config

	now := bar.Time
	if ctx != nil {
		now = ctx.Now()
	}

	// Pending-entry timeout: if entry signal was emitted but no fill/rejection after 5 min, reset.
	if avwapSt.PendingEntry != "" && now.Sub(avwapSt.PendingEntryAt) > 5*time.Minute {
		if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Warn("AVWAPStrategy: pending entry timed out, resetting", "symbol", symbol, "side", avwapSt.PendingEntry)
		}
		avwapSt.PendingEntry = ""
		avwapSt.PendingEntryAt = time.Time{}
	}

	// 1. Cooldown / max trades gate.
	if now.Before(avwapSt.CooldownUntil) {
		avwapSt.emitEarlyGated(ctx, symbol, bar, "cooldown", "cooldown active")
		return avwapSt, nil, nil
	}
	if avwapSt.TradesToday >= cfg.MaxTradesPerDay {
		avwapSt.emitEarlyGated(ctx, symbol, bar, "max_trades", fmt.Sprintf("%d/%d trades", avwapSt.TradesToday, cfg.MaxTradesPerDay))
		return avwapSt, nil, nil
	}

	// 1b. Trading window gate.
	// When session-time weighting is enabled, use graduated multipliers per bucket.
	// When disabled, fall back to the binary AllowedHours gate.
	var sessionBucket string
	sessionMult := 1.0
	if cfg.SessionWeightEnabled {
		sessionBucket, sessionMult = cfg.SessionWeight(now)
		if sessionMult <= 0 {
			avwapSt.emitEarlyGated(ctx, symbol, bar, "hours", fmt.Sprintf("session bucket %s (weight 0)", sessionBucket))
			return avwapSt, nil, nil
		}
	} else if cfg.AllowedHoursStart != "" && cfg.AllowedHoursEnd != "" {
		loc := etLocation
		if cfg.AllowedHoursTZ != "" {
			if parsed := cachedLocation(cfg.AllowedHoursTZ); parsed != nil {
				loc = parsed
			}
		}
		localNow := now.In(loc)
		hhmm := localNow.Format("15:04")
		if hhmm < cfg.AllowedHoursStart || hhmm >= cfg.AllowedHoursEnd {
			avwapSt.emitEarlyGated(ctx, symbol, bar, "hours", "outside trading hours")
			return avwapSt, nil, nil
		}
	}

	// 2. Update AVWAP calculator with this bar.
	// In backtests, 1m bars already feed the calculator via UpdateCalc() in the
	// runner. This 5m bar update is needed for tests and live trading where
	// UpdateCalc may not be called separately.
	avwapSt.Calc.Update(bar.Time, bar.High, bar.Low, bar.Close, bar.Volume)
	avwapValues := avwapSt.Calc.Values()

	// Get sorted anchor names for deterministic iteration order.
	// Go map iteration is random — without this, backtest results
	// vary between runs as different anchors trigger first.
	sortedAnchors := avwapSt.Calc.SortedNames()

	// 2b. Update recent lows/highs sliding window for higher-lows filter.
	avwapSt.RecentLows = append(avwapSt.RecentLows, bar.Low)
	if len(avwapSt.RecentLows) > cfg.HigherLowsBars {
		avwapSt.RecentLows = avwapSt.RecentLows[len(avwapSt.RecentLows)-cfg.HigherLowsBars:]
	}
	avwapSt.RecentHighs = append(avwapSt.RecentHighs, bar.High)
	if len(avwapSt.RecentHighs) > cfg.HigherLowsBars {
		avwapSt.RecentHighs = avwapSt.RecentHighs[len(avwapSt.RecentHighs)-cfg.HigherLowsBars:]
	}

	// 2c. Shift prev bars ring buffer for candlestick patterns.
	avwapSt.PrevBars[1] = avwapSt.PrevBars[0]
	avwapSt.PrevBars[0] = bar
	if avwapSt.PrevBarCount < 2 {
		avwapSt.PrevBarCount++
	}

	// 2d. Rolling 50-bar high/low window for Fibonacci.
	avwapSt.BarHighs50 = append(avwapSt.BarHighs50, bar.High)
	if len(avwapSt.BarHighs50) > 50 {
		avwapSt.BarHighs50 = avwapSt.BarHighs50[1:]
	}
	avwapSt.BarLows50 = append(avwapSt.BarLows50, bar.Low)
	if len(avwapSt.BarLows50) > 50 {
		avwapSt.BarLows50 = avwapSt.BarLows50[1:]
	}

	// 2e. Inducement detector (Factor 7) — updates swing ring buffers and
	// detects same-bar / multi-bar liquidity sweeps. Disabled by default.
	avwapSt.updateInducement(bar, cfg)

	// 2f. Co-fire veto state: session VWAP, sigma, bucketed volume history.
	// Runs every bar; veto itself is applied at entry-evaluation time only
	// when cfg.CofireVetoEnabled / cfg.CofireVetoShadow is true.
	avwapSt.updateCofireVetoState(bar)

	// 3. Regime gating.
	regimeAllowed := false
	regimeTag := "none"
	if ar, ok2 := avwapSt.Indicators.AnchorRegimes["5m"]; ok2 {
		regimeTag = ar.Type
		for _, allowed := range cfg.AllowRegimes {
			if ar.Type == allowed || (allowed == "TREND" && (ar.Type == "TREND_UP" || ar.Type == "TREND_DOWN")) {
				regimeAllowed = true
				break
			}
		}
	} else {
		regimeAllowed = true
	}

	// 4. Update AboveCount/BelowCount and distance history for each active anchor.
	if avwapSt.AVWAPDistHistory == nil {
		avwapSt.AVWAPDistHistory = make(map[string][]float64)
	}
	for _, anchorName := range sortedAnchors {
		avwapValue := avwapValues[anchorName]
		switch {
		case bar.Close > avwapValue:
			avwapSt.AboveCount[anchorName]++
			avwapSt.BelowCount[anchorName] = 0
		case bar.Close < avwapValue:
			avwapSt.BelowCount[anchorName]++
			avwapSt.AboveCount[anchorName] = 0
		default:
			avwapSt.AboveCount[anchorName] = 0
			avwapSt.BelowCount[anchorName] = 0
		}
		// Track distance from AVWAP for handoff detection (signed: positive = above)
		dist := (bar.Close - avwapValue) / avwapValue * 10000.0 // in bps
		hist := avwapSt.AVWAPDistHistory[anchorName]
		hist = append(hist, dist)
		maxHist := 10
		if len(hist) > maxHist {
			hist = hist[len(hist)-maxHist:]
		}
		avwapSt.AVWAPDistHistory[anchorName] = hist
	}

	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
	cooldown := time.Duration(cfg.CooldownSeconds) * time.Second

	ec := entryContext{
		ctx:           ctx,
		cfg:           cfg,
		bar:           bar,
		symbol:        symbol,
		instanceID:    instanceID,
		now:           now,
		cooldown:      cooldown,
		avwapValues:   avwapValues,
		sortedAnchors: sortedAnchors,
		regimeTag:     regimeTag,
		etLocation:    etLocation,
		keyLevels:     avwapSt.KeyLevels,
		spyTideDevBps: avwapSt.SpyTideDevBps,
		spyTideReady:  avwapSt.SpyTideReady,
		tideIndexName: avwapSt.TideIndexName,
		sessionBucket: sessionBucket,
		sessionMult:   sessionMult,
	}

	// 5. Exit signals (check even if cooldown would block new entries).
	if sig, err := avwapSt.evaluateExits(ec); err != nil {
		return avwapSt, nil, err
	} else if sig != nil {
		s.clearHoldReason(symbol)
		return avwapSt, []start.Signal{*sig}, nil
	}

	// 6. Only entries if flat and regime allowed.
	if avwapSt.PositionSide != "" || avwapSt.PendingEntry != "" {
		avwapSt.emitEarlyGated(ctx, symbol, bar, "position", "position/pending active")
		return avwapSt, nil, nil
	}
	if !regimeAllowed {
		avwapSt.emitEarlyGated(ctx, symbol, bar, "regime", regimeTag+" not allowed")
		return avwapSt, nil, nil
	}

	// 6a1. Regime-direction blocking — block a specific direction in a specific regime.
	// e.g. block LONG in BALANCE because AVWAP breakout longs underperform in ranges.
	if blocked, ok2 := cfg.RegimeBlockedDirections[regimeTag]; ok2 {
		if strings.EqualFold(blocked, "LONG") {
			ec.lockedLong = true
		}
		if strings.EqualFold(blocked, "SHORT") {
			ec.lockedShort = true
		}
	}

	// 6a2. Session lockout — once stopped out on a side, no re-entry in the
	// same direction until next session. A long stop-out doesn't block shorts.
	if avwapSt.LockedOutSide == start.SideBuy {
		ec.lockedLong = true
	}
	if avwapSt.LockedOutSide == start.SideSell {
		ec.lockedShort = true
	}

	// 6b. Directional bias gate — determine bias from first anchor's AVWAP value.
	// Require at least minBarsForBias bars before trusting the AVWAP for directional
	// decisions. At session open, the AVWAP has too few data points to be reliable
	// (e.g. 09:44 = only ~9 one-minute bars). This matches the chart stabilization
	// gate (CalcBarCount >= 10) so bias and chart stay aligned.
	const minBarsForBias = 10
	if cfg.EnforceAVWAPBias && len(cfg.Anchors) > 0 && avwapSt.CalcBarCount >= minBarsForBias {
		firstAnchor := cfg.Anchors[0]
		if firstAVWAP, ok2 := avwapValues[firstAnchor]; ok2 {
			if bar.Close > firstAVWAP {
				ec.avwapBias = "LONG"
			} else if bar.Close < firstAVWAP {
				ec.avwapBias = "SHORT"
			}
		}
	}

	// 6b2. AVWAP slope gate — check slope of first anchor for trend confirmation.
	if cfg.MinSlopeBPS > 0 && len(cfg.Anchors) > 0 {
		ec.avwapSlope, ec.slopeOK = avwapSt.Calc.Slope(cfg.Anchors[0], cfg.SlopeLookback)
	}

	// 6c. Update PeakAboveCount/PeakBelowCount for pullback detection.
	if avwapSt.PeakAboveCount == nil {
		avwapSt.PeakAboveCount = make(map[string]int)
	}
	if avwapSt.PeakBelowCount == nil {
		avwapSt.PeakBelowCount = make(map[string]int)
	}
	for _, anchorName := range sortedAnchors {
		if avwapSt.AboveCount[anchorName] > avwapSt.PeakAboveCount[anchorName] {
			avwapSt.PeakAboveCount[anchorName] = avwapSt.AboveCount[anchorName]
		}
		// NOTE: when AboveCount==0 && PeakAboveCount>0, peak is frozen for pullback check (no-op).
		if avwapSt.BelowCount[anchorName] > avwapSt.PeakBelowCount[anchorName] {
			avwapSt.PeakBelowCount[anchorName] = avwapSt.BelowCount[anchorName]
		}
		// NOTE: when BelowCount==0 && PeakBelowCount>0, peak is frozen for pullback check (no-op).
		// Reset peaks when price re-establishes a trend (AboveCount or BelowCount exceeds prior peak)
		if avwapSt.AboveCount[anchorName] >= cfg.PullbackTrendBars {
			avwapSt.PeakAboveCount[anchorName] = avwapSt.AboveCount[anchorName]
		}
		if avwapSt.BelowCount[anchorName] >= cfg.PullbackTrendBars {
			avwapSt.PeakBelowCount[anchorName] = avwapSt.BelowCount[anchorName]
		}
	}

	// 6e–8. Entry evaluation.
	if sig, err := avwapSt.evaluateEntries(ec); err != nil {
		return avwapSt, nil, err
	} else if sig != nil {
		s.clearHoldReason(symbol)
		return avwapSt, []start.Signal{*sig}, nil
	}

	// 9. Emit EntryGated event — we reached entry evaluation but no signal fired.
	// Rate-limit to one event per symbol per bar.
	if ctx != nil && !bar.Time.Equal(avwapSt.LastGatedBarTime) {
		avwapSt.LastGatedBarTime = bar.Time
		avwapSt.emitEntryGated(ec)
	}

	return avwapSt, nil, nil
}

// OnEvent handles fill confirmations and entry rejections for AVWAP strategy.
func (s *AVWAPStrategy) OnEvent(ctx start.Context, symbol string, evt any, st start.State) (start.State, []start.Signal, error) {
	avwapSt, ok := st.(*AVWAPState)
	if !ok {
		return st, nil, fmt.Errorf("AVWAPStrategy.OnEvent: expected *AVWAPState, got %T", st)
	}

	switch e := evt.(type) {
	case start.FillConfirmation:
		if avwapSt.PendingEntry != "" {
			// Entry fill: promote PendingEntry to confirmed position.
			avwapSt.PositionSide = avwapSt.PendingEntry
			avwapSt.PendingEntry = ""
			avwapSt.PendingEntryAt = time.Time{}
			if ctx != nil && ctx.Logger() != nil {
				ctx.Logger().Info("AVWAPStrategy: fill confirmed, position active", "symbol", symbol, "side", avwapSt.PositionSide, "price", e.Price)
			}
		} else if avwapSt.PositionSide != "" {
			// Exit fill: position was closed by position monitor exit rules.
			// Clear PositionSide so the strategy can re-enter.
			exitSide := avwapSt.PositionSide
			avwapSt.PositionSide = ""
			if ctx != nil && ctx.Logger() != nil {
				ctx.Logger().Info("AVWAPStrategy: exit fill confirmed, position cleared", "symbol", symbol, "prev_side", exitSide, "price", e.Price)
			}
		}
		return avwapSt, nil, nil

	case start.EntryRejection:
		if avwapSt.PendingEntry != "" {
			if ctx != nil && ctx.Logger() != nil {
				ctx.Logger().Warn("AVWAPStrategy: entry rejected, clearing pending", "symbol", symbol, "side", avwapSt.PendingEntry, "reason", e.Reason)
			}
			avwapSt.PendingEntry = ""
			avwapSt.PendingEntryAt = time.Time{}
			avwapSt.CooldownUntil = time.Time{}
			if avwapSt.TradesToday > 0 {
				avwapSt.TradesToday--
			}
		}
		return avwapSt, nil, nil

	default:
		return avwapSt, nil, nil
	}
}

// --- Serialization ---

type avwapStateJSON struct {
	Symbol         string                             `json:"symbol"`
	Config         AVWAPConfig                        `json:"config"`
	CalcStates     map[string]start.AnchoredVWAPState `json:"calc_states"`
	Anchors        []start.AnchorPoint                `json:"anchors"`
	AboveCount     map[string]int                     `json:"above_count"`
	BelowCount     map[string]int                     `json:"below_count"`
	PeakAboveCount  map[string]int                     `json:"peak_above_count,omitempty"`
	PeakBelowCount  map[string]int                     `json:"peak_below_count,omitempty"`
	StopBelowCount  map[string]int                     `json:"stop_below_count,omitempty"`
	StopAboveCount  map[string]int                     `json:"stop_above_count,omitempty"`
	CrossedBelowBar map[string]int                     `json:"crossed_below_bar,omitempty"`
	TradesToday     int                                `json:"trades_today"`
	CooldownUntil  time.Time                          `json:"cooldown_until"`
	PositionSide   start.Side                         `json:"position_side"`
	PendingEntry   start.Side                         `json:"pending_entry"`
	PendingEntryAt time.Time                          `json:"pending_entry_at"`
	Indicators     start.IndicatorData                `json:"indicators"`
	RecentLows     []float64                          `json:"recent_lows,omitempty"`
	RecentHighs    []float64                          `json:"recent_highs,omitempty"`
	PrevBarCount   int                                `json:"prev_bar_count,omitempty"`
	BarHighs50     []float64                          `json:"bar_highs_50,omitempty"`
	BarLows50      []float64                          `json:"bar_lows_50,omitempty"`
	KeyLevels      map[string]float64                 `json:"key_levels,omitempty"`
}

func (s *AVWAPState) Marshal() ([]byte, error) {
	// Extract anchor points for serialization.
	avwapValues := s.Calc.Values()
	marshalSorted := s.Calc.SortedNames()
	anchors := make([]start.AnchorPoint, 0, len(avwapValues))
	for _, name := range marshalSorted {
		anchors = append(anchors, start.AnchorPoint{Name: name})
	}

	j := avwapStateJSON{
		Symbol:         s.Symbol,
		Config:         s.Config,
		CalcStates:     s.Calc.States(),
		Anchors:        anchors,
		AboveCount:     s.AboveCount,
		BelowCount:     s.BelowCount,
		PeakAboveCount:  s.PeakAboveCount,
		PeakBelowCount:  s.PeakBelowCount,
		StopBelowCount:  s.StopBelowCount,
		StopAboveCount:  s.StopAboveCount,
		CrossedBelowBar: s.CrossedBelowBar,
		TradesToday:     s.TradesToday,
		CooldownUntil:  s.CooldownUntil,
		PositionSide:   s.PositionSide,
		PendingEntry:   s.PendingEntry,
		PendingEntryAt: s.PendingEntryAt,
		Indicators:     s.Indicators,
		RecentLows:     s.RecentLows,
		RecentHighs:    s.RecentHighs,
		PrevBarCount:   s.PrevBarCount,
		BarHighs50:     s.BarHighs50,
		BarLows50:      s.BarLows50,
		KeyLevels:      s.KeyLevels,
	}
	return json.Marshal(j)
}

func (s *AVWAPState) Unmarshal(data []byte) error {
	var j avwapStateJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return fmt.Errorf("AVWAPState.Unmarshal: %w", err)
	}
	s.Symbol = j.Symbol
	s.Config = j.Config
	s.AboveCount = j.AboveCount
	s.BelowCount = j.BelowCount
	s.PeakAboveCount = j.PeakAboveCount
	s.PeakBelowCount = j.PeakBelowCount
	s.StopBelowCount = j.StopBelowCount
	s.StopAboveCount = j.StopAboveCount
	s.CrossedBelowBar = j.CrossedBelowBar
	s.TradesToday = j.TradesToday
	s.CooldownUntil = j.CooldownUntil
	s.PositionSide = j.PositionSide
	s.PendingEntry = j.PendingEntry
	s.PendingEntryAt = j.PendingEntryAt
	s.Indicators = j.Indicators
	s.RecentLows = j.RecentLows
	s.RecentHighs = j.RecentHighs
	s.PrevBarCount = j.PrevBarCount
	s.BarHighs50 = j.BarHighs50
	s.BarLows50 = j.BarLows50
	s.KeyLevels = j.KeyLevels

	s.Calc = start.NewAnchoredVWAPCalc()
	s.Calc.Restore(j.Anchors, j.CalcStates)

	if s.AboveCount == nil {
		s.AboveCount = make(map[string]int)
	}
	if s.BelowCount == nil {
		s.BelowCount = make(map[string]int)
	}
	if s.PeakAboveCount == nil {
		s.PeakAboveCount = make(map[string]int)
	}
	if s.PeakBelowCount == nil {
		s.PeakBelowCount = make(map[string]int)
	}
	if s.StopBelowCount == nil {
		s.StopBelowCount = make(map[string]int)
	}
	if s.StopAboveCount == nil {
		s.StopAboveCount = make(map[string]int)
	}
	if s.CrossedBelowBar == nil {
		s.CrossedBelowBar = make(map[string]int)
	}
	return nil
}
