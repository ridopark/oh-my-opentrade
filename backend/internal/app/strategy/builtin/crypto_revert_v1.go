package builtin

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// CryptoRevertStrategy implements a 5m mean-reversion strategy for crypto
// spot majors gated on trade-flow imbalance and a recent liquidity sweep.
// It is the phase-1 implementation of the MFT `crypto_revert_v1` doc
// (long-only). See docs/MFT-crypto-strategies/01-crypto-revert-v1.md.
//
// The strategy bundles three internal indicators per symbol:
//
//   - SessionVWAP — UTC-anchored VWAP with a rolling-sigma deviation z-score.
//   - TFI — trade-flow imbalance over the last tfi_lookback_min minutes. Phase
//     1 has no tick feed into the strategy layer, so the bar-sign fallback is
//     used; it is lossy but deterministic and identical in backtest/live.
//   - CryptoInducement — liquidity-sweep detector; entries require a recent
//     bullish sweep (stops below cleared then price recovered).
//
// All three are purely in-memory and rebuilt from bar history on ReplayOnBar.
// The strategy does NOT persist them (State marshals only scalar fields) —
// restart-replay reconstructs the state from bars as usual.
//
// Short entries are deferred (phase 2, perps only).
type CryptoRevertStrategy struct {
	meta start.Meta
}

// NewCryptoRevertStrategy constructs the built-in strategy singleton. The
// implementation ID "crypto_revert" is what TOML specs reference via
// `[hooks] signals = { engine = "builtin", name = "crypto_revert" }`.
func NewCryptoRevertStrategy() *CryptoRevertStrategy {
	id, _ := start.NewStrategyID("crypto_revert")
	ver, _ := start.NewVersion("0.1.0")
	return &CryptoRevertStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "Crypto Mean Reversion (5m)",
			Description: "VWAP-fade on BTC/ETH gated by TFI and inducement sweep; phase-1 long-only.",
			Author:      "system",
		},
	}
}

func (s *CryptoRevertStrategy) Meta() start.Meta { return s.meta }

// WarmupBars is sized for the largest indicator requirement: CryptoInducement
// lookback (60 bars = 5h at 5m) plus headroom for the swing-window scan.
// 96 bars = 8h is comfortably above the floor and matches the default
// rolling-sigma window so SessionVWAP is ready at first OnBar.
func (s *CryptoRevertStrategy) WarmupBars() int { return 96 }

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

type CryptoRevertConfig struct {
	// Entry thresholds (dev_z is signed; we only long phase 1, so
	// EntryDevZ is negative).
	EntryDevZ    float64 // default -2.0
	ExitDevZ     float64 // default -0.3 — |dev_z| < |this| closes
	HardStopDevZ float64 // default -3.5

	// TFI window and floor. TFIMin > 0 rejects weak-magnitude flow.
	TFILookbackMin int     // default 15
	TFIMin         float64 // default 0.15 (quant recal — sign-only gate is too loose)

	// Sigma method controls the SessionVWAP deviation estimator.
	// "rolling" is the default after the quant review; "session" is retained
	// for A/B.
	SigmaMethod       string // "rolling" | "session" | "ewma"; default "rolling"
	SigmaLookbackBars int    // default 96

	// Session reset hour (UTC); crypto defaults to 0.
	SessionResetUTCHour int // default 0

	// Time & concurrency.
	TimeStopMin   int // default 90
	MaxConcurrent int // default 2 — strategy-level sanity cap; the risk sizer
	// is the real book-wide gate.

	// Gate toggles.
	RequireTFI             bool // default true
	RequireInducement      bool // default true
	InducementLookbackBars int  // default 2 (quant recal — crypto sweeps resolve faster)

	// Fallback: use sign(close-open)*volume when taker side is missing.
	UseBarSignTFI bool // default true

	// TFISource controls which TFI ingestion path is active:
	//   "trade_tick" — only UpdateTrade (tick events); never call UpdateBar.
	//   "bar_sign"   — only UpdateBar (bar-sign fallback); ignore ticks.
	//   "auto"       — prefer ticks when we've seen any in the last
	//                  2*tfi_lookback_min window; otherwise fall back to
	//                  bar-sign. This is the default so backtests (no tick
	//                  feed yet) keep working while live/paper uses real
	//                  taker-side data without any config flip.
	TFISource string // default "auto"

	// Phase-2 gates — disabled by default.
	RequireXVFlow  bool // default false
	RequireSkewOK  bool // default false

	// Session-time weighting multipliers applied to signal strength.
	// Defaults preserve existing behavior (all 1.0).
	WeightUSHours   float64 // default 1.0
	WeightAsiaHours float64 // default 1.0 (doc suggests 0.7; keep neutral until wired)
	WeightWeekend   float64 // default 1.0 (doc suggests 0.5)

	// When true, position-monitor skips price-based exit rules and trusts the
	// strategy's OnBar exits (reversion_complete, hard_stop, time_stop,
	// inducement_flip). Time-only rules (MAX_HOLDING_TIME) still fire as a
	// safety net. Default false preserves legacy behavior.
	StrategyExitsPriority bool
}

func parseCryptoRevertConfig(params map[string]any) CryptoRevertConfig {
	return CryptoRevertConfig{
		EntryDevZ:              getFloat64(params, "entry_dev_z", -2.0),
		ExitDevZ:               getFloat64(params, "exit_dev_z", -0.3),
		HardStopDevZ:           getFloat64(params, "hard_stop_dev_z", -3.5),
		TFILookbackMin:         getInt(params, "tfi_lookback_min", 15),
		TFIMin:                 getFloat64(params, "tfi_min", 0.15),
		SigmaMethod:            getString(params, "sigma_method", "rolling"),
		SigmaLookbackBars:      getInt(params, "sigma_lookback_bars", 96),
		SessionResetUTCHour:    getInt(params, "session_reset_utc", 0),
		TimeStopMin:            getInt(params, "time_stop_min", 90),
		MaxConcurrent:          getInt(params, "max_concurrent", 2),
		RequireTFI:             getBool(params, "require_tfi", true),
		RequireInducement:      getBool(params, "require_inducement", true),
		InducementLookbackBars: getInt(params, "inducement_lookback_bars", 2),
		UseBarSignTFI:          getBool(params, "use_bar_sign_tfi", true),
		TFISource:              getString(params, "tfi_source", "auto"),
		RequireXVFlow:          getBool(params, "require_xv_flow", false),
		RequireSkewOK:          getBool(params, "require_skew_ok", false),
		WeightUSHours:          getFloat64(params, "weight_us_hours", 1.0),
		WeightAsiaHours:        getFloat64(params, "weight_asia_hours", 1.0),
		WeightWeekend:          getFloat64(params, "weight_weekend", 1.0),
		StrategyExitsPriority:  getBool(params, "strategy_exits_priority", false),
	}
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

// CryptoRevertState is the per-symbol state. Only scalar fields marshal;
// the internal indicator objects and the bar history buffer are rebuilt from
// ReplayOnBar on restart, so they are JSON-excluded.
type CryptoRevertState struct {
	Symbol string             `json:"symbol"`
	Config CryptoRevertConfig `json:"config"`

	// Position state.
	PositionSide   start.Side `json:"positionSide,omitempty"`
	PendingEntry   start.Side `json:"pendingEntry,omitempty"`
	PendingEntryAt time.Time  `json:"pendingEntryAt,omitzero"`
	EntryPrice     float64    `json:"entryPrice,omitempty"`
	EntryTime      time.Time  `json:"entryTime,omitzero"`

	// Indicators — not persisted; rebuilt via ReplayOnBar.
	vwap       *start.SessionVWAP      `json:"-"`
	tfi        *start.TFI              `json:"-"`
	inducement *start.CryptoInducement `json:"-"`

	// Cached read from the last ingest() — OnBar consumes these without
	// calling Update again (which would double-advance the VWAP cumulative
	// state and rolling-sigma window).
	lastVWAP  float64 `json:"-"`
	lastSigma float64 `json:"-"`
	lastDevZ  float64 `json:"-"`
	lastOK    bool    `json:"-"`

	// Rolling bar buffer for inducement detection. Capped at the max of the
	// inducement lookback and the warmup window.
	bars []start.Bar `json:"-"`

	// Most recent trade-tick timestamp observed via OnEvent(TradeTick).
	// Used by the "auto" tfi_source mode to decide whether the bar-sign
	// fallback is still needed. Zero value means "no tick ever seen".
	lastTradeAt time.Time `json:"-"`

	// Count of UpdateTrade calls — exposed for tests and telemetry so we
	// can confirm the event-driven path is actually being exercised.
	tradeTickCount int `json:"-"`

	// Most recent bullish sweep timestamp; -1 if none yet. We store the bar
	// index into `bars` and compare against current index for the N-bar gate.
	lastBullishSweepIdx int `json:"-"`
	// Most recent bearish sweep index — used for the opposite-regime-flip
	// exit trigger.
	lastBearishSweepIdx int `json:"-"`
	barIdx              int `json:"-"`

	// Diagnostic counters — track drop-off across OnBar gates. These are
	// emitted periodically via ctx.Logger() at low verbosity to diagnose
	// backtest signal scarcity. They are NOT persisted (json:"-").
	diagBarCount             int `json:"-"`
	diagVWAPOKCount          int `json:"-"`
	diagWarmupSkipCount      int `json:"-"`
	diagEntryGatePassCount   int `json:"-"`
	diagEmitCount            int `json:"-"`
	diagPositionLockSkipCount int `json:"-"`
	diagTFIRejectCount       int `json:"-"`
	diagInducementRejectCount int `json:"-"`
	diagDevZRejectCount      int `json:"-"`

	Indicators start.IndicatorData `json:"-"`
}

func (st *CryptoRevertState) SetIndicators(ind start.IndicatorData) { st.Indicators = ind }

// Debug accessors (used by tests and operator telemetry).

func (st *CryptoRevertState) DebugSnapshot() (barIdx, bullSweepIdx, bearSweepIdx int, vwap, sigma, devZ, tfi float64, ok bool) {
	if st == nil {
		return
	}
	tfi = 0
	if st.tfi != nil {
		tfi, _ = st.tfi.Value()
	}
	return st.barIdx, st.lastBullishSweepIdx, st.lastBearishSweepIdx,
		st.lastVWAP, st.lastSigma, st.lastDevZ, tfi, st.lastOK
}

func (st *CryptoRevertState) Marshal() ([]byte, error)    { return json.Marshal(st) }
func (st *CryptoRevertState) Unmarshal(data []byte) error { return json.Unmarshal(data, st) }

// ensureIndicators lazily constructs indicator instances from the current
// config. Safe to call repeatedly.
func (st *CryptoRevertState) ensureIndicators() {
	if st.vwap == nil {
		st.vwap = start.NewSessionVWAP(start.SessionVWAPConfig{
			SessionResetUTCHour: st.Config.SessionResetUTCHour,
			SigmaMethod:         st.Config.SigmaMethod,
			SigmaLookbackBars:   st.Config.SigmaLookbackBars,
		})
	}
	if st.tfi == nil {
		st.tfi = start.NewTFI(start.TFIConfig{
			WindowMinutes:   st.Config.TFILookbackMin,
			FallbackBarSign: st.Config.UseBarSignTFI,
		})
	}
	if st.inducement == nil {
		cfg := start.DefaultCryptoInducementConfig()
		cfg.ReversalBars = st.Config.InducementLookbackBars
		st.inducement = start.NewCryptoInducement(cfg)
	}
	if st.lastBullishSweepIdx == 0 && st.lastBearishSweepIdx == 0 {
		// Zero-initialized slice — sentinel to "no sweep seen".
		st.lastBullishSweepIdx = -1
		st.lastBearishSweepIdx = -1
	}
}

// TradeTickCount returns how many trade ticks the strategy has ingested via
// OnEvent(TradeTick). Exposed for tests and operator telemetry so the
// event-driven TFI path can be verified end-to-end.
func (st *CryptoRevertState) TradeTickCount() int {
	if st == nil {
		return 0
	}
	return st.tradeTickCount
}

// tfiUsingTradeTicks reports whether the TFI indicator should rely on
// live trade-tick events rather than bar-sign fallback. For "auto" mode we
// require a tick within 2*tfi_lookback_min of the current bar — long enough
// to survive brief gaps but short enough that a stalled tick feed degrades
// to bar-sign on its own.
func (st *CryptoRevertState) tfiUsingTradeTicks(now time.Time) bool {
	switch st.Config.TFISource {
	case "trade_tick":
		return true
	case "bar_sign":
		return false
	default: // "auto" and any unrecognized value
		if st.lastTradeAt.IsZero() {
			return false
		}
		window := 2 * time.Duration(st.Config.TFILookbackMin) * time.Minute
		if window <= 0 {
			window = 30 * time.Minute
		}
		return now.Sub(st.lastTradeAt) <= window
	}
}

// ingest updates all indicators and the bar buffer for a single bar.
func (st *CryptoRevertState) ingest(bar start.Bar) {
	st.ensureIndicators()
	v, s, z, ok := st.vwap.Update(bar)
	st.lastVWAP, st.lastSigma, st.lastDevZ, st.lastOK = v, s, z, ok
	// Only feed the bar-sign path into TFI when we don't have fresh tick
	// data. Under live/paper with a healthy trade-tick feed this is skipped
	// and TFI reflects real taker-side flow; backtests without tick feeds
	// fall through and keep the deterministic bar-sign behavior.
	if !st.tfiUsingTradeTicks(bar.Time) {
		st.tfi.UpdateBar(bar)
	}

	st.bars = append(st.bars, bar)
	// Cap the buffer: 120 bars is ~2x CryptoInducement lookback — ample
	// history for swing detection without unbounded growth.
	const maxBars = 120
	if len(st.bars) > maxBars {
		st.bars = st.bars[len(st.bars)-maxBars:]
		// barIdx is absolute; it does NOT reset when the buffer rolls.
	}
	st.barIdx++

	// Run the inducement detector against the current bar window. Taker
	// trades aren't available in phase 1; pass nil and rely on volume + level
	// gates inside the detector.
	res := st.inducement.Detect(st.Symbol, st.bars, nil)
	if res.Detected {
		switch res.Direction {
		case start.CryptoInducementBullish:
			st.lastBullishSweepIdx = st.barIdx
		case start.CryptoInducementBearish:
			st.lastBearishSweepIdx = st.barIdx
		}
	}
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func (s *CryptoRevertStrategy) Init(_ start.Context, symbol string, params map[string]any, prior start.State) (start.State, error) {
	cfg := parseCryptoRevertConfig(params)
	st := &CryptoRevertState{
		Symbol:              symbol,
		Config:              cfg,
		lastBullishSweepIdx: -1,
		lastBearishSweepIdx: -1,
	}
	if prior != nil {
		if p, ok := prior.(*CryptoRevertState); ok {
			st = p
			st.Config = cfg
			if st.lastBullishSweepIdx == 0 {
				st.lastBullishSweepIdx = -1
			}
			if st.lastBearishSweepIdx == 0 {
				st.lastBearishSweepIdx = -1
			}
		}
	}
	st.ensureIndicators()
	return st, nil
}

// ---------------------------------------------------------------------------
// ReplayOnBar — warmup without signals
// ---------------------------------------------------------------------------

func (s *CryptoRevertStrategy) ReplayOnBar(_ start.Context, _ string, bar start.Bar, st start.State, indicators start.IndicatorData) (start.State, error) {
	rst, ok := st.(*CryptoRevertState)
	if !ok {
		return st, fmt.Errorf("CryptoRevertStrategy.ReplayOnBar: expected *CryptoRevertState, got %T", st)
	}
	rst.Indicators = indicators
	rst.ingest(bar)
	return rst, nil
}

// ---------------------------------------------------------------------------
// OnBar — decision loop
// ---------------------------------------------------------------------------

func (s *CryptoRevertStrategy) OnBar(ctx start.Context, symbol string, bar start.Bar, st start.State) (start.State, []start.Signal, error) {
	rst, ok := st.(*CryptoRevertState)
	if !ok {
		return st, nil, fmt.Errorf("CryptoRevertStrategy.OnBar: expected *CryptoRevertState, got %T", st)
	}
	rst.ensureIndicators()

	// Ingest before decisioning so VWAP/TFI/inducement see the current bar.
	priorBullishSweepIdx := rst.lastBullishSweepIdx
	priorBearishSweepIdx := rst.lastBearishSweepIdx
	rst.ingest(bar)

	cfg := rst.Config
	now := bar.Time
	if ctx != nil {
		now = ctx.Now()
	}

	// Consume cached indicator snapshot from ingest — must NOT call
	// Update again or we'd double-advance the VWAP state.
	vwap, sigma, devZ, vwapOK := rst.lastVWAP, rst.lastSigma, rst.lastDevZ, rst.lastOK
	tfi, _ := rst.tfi.Value()

	rst.diagBarCount++
	if vwapOK {
		rst.diagVWAPOKCount++
	}
	// Periodic diagnostic log: every 5000 bars. Cheap at backtest scale,
	// invisible at live scale (symbol emits ~288 bars/day on 5m → log fires
	// every ~17 days). Also logs at bar #1 so we confirm OnBar is called at all.
	if ctx != nil && (rst.diagBarCount == 1 || rst.diagBarCount%5000 == 0) {
		if lg := ctx.Logger(); lg != nil {
			lg.Info("crypto_revert diag",
				"symbol", symbol,
				"bar_count", rst.diagBarCount,
				"vwap_ok_count", rst.diagVWAPOKCount,
				"warmup_skip_count", rst.diagWarmupSkipCount,
				"entry_gate_pass_count", rst.diagEntryGatePassCount,
				"emit_count", rst.diagEmitCount,
				"position_lock_skip_count", rst.diagPositionLockSkipCount,
				"tfi_reject_count", rst.diagTFIRejectCount,
				"inducement_reject_count", rst.diagInducementRejectCount,
				"devz_reject_count", rst.diagDevZRejectCount,
				"last_devz", devZ,
				"last_tfi", tfi,
				"pos_side", string(rst.PositionSide),
				"bar_time", bar.Time.Format(time.RFC3339),
			)
		}
	}

	// Not enough data yet — let the system keep warming up.
	if !vwapOK {
		rst.diagWarmupSkipCount++
		return rst, nil, nil
	}

	// Pending-entry timeout: in the backtest slice pipeline, OnBar for all
	// bars runs to completion BEFORE any fills are published (fills replay
	// in phase B). Without a timeout the very first emitted entry would
	// latch PendingEntry and short-circuit every subsequent OnBar. Mirrors
	// AVWAP's 5-minute recovery path. In live mode the fill confirmation
	// arrives within seconds so the timeout never fires.
	if rst.PendingEntry != "" && !rst.PendingEntryAt.IsZero() &&
		now.Sub(rst.PendingEntryAt) > 5*time.Minute {
		if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Warn("crypto_revert: pending entry timed out, resetting",
				"symbol", symbol,
				"side", string(rst.PendingEntry),
				"age", now.Sub(rst.PendingEntryAt).String(),
			)
		}
		rst.PendingEntry = ""
		rst.PendingEntryAt = time.Time{}
	}

	var signals []start.Signal

	// -----------------------------------------------------------------------
	// EXIT LOGIC
	// -----------------------------------------------------------------------
	if rst.PositionSide == start.SideBuy {
		exitReason := ""

		// Reversion complete: |dev_z| below |ExitDevZ|.
		if math.Abs(devZ) < math.Abs(cfg.ExitDevZ) {
			exitReason = "reversion_complete"
		}

		// Hard stop: dev_z worse than HardStopDevZ (further below VWAP).
		if exitReason == "" && devZ <= cfg.HardStopDevZ {
			exitReason = "hard_stop"
		}

		// Time stop.
		if exitReason == "" && !rst.EntryTime.IsZero() {
			if int(now.Sub(rst.EntryTime).Minutes()) >= cfg.TimeStopMin {
				exitReason = "time_stop"
			}
		}

		// Opposite inducement re-fire (regime flip).
		if exitReason == "" && rst.lastBearishSweepIdx != priorBearishSweepIdx {
			exitReason = "inducement_flip"
		}

		if exitReason != "" {
			instanceID, _ := start.NewInstanceID(symbol)
			exitTags := map[string]string{
				"reason":    exitReason,
				"ref_price": fmt.Sprintf("%.10f", bar.Close),
				"dev_z":     fmt.Sprintf("%.3f", devZ),
				"vwap":      fmt.Sprintf("%.4f", vwap),
				"sigma":     fmt.Sprintf("%.4f", sigma),
				"tfi":       fmt.Sprintf("%.3f", tfi),
				"strategy":  "crypto_revert",
			}
			if cfg.StrategyExitsPriority {
				exitTags["exit_origin"] = "strategy"
			}
			sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideSell, 1.0, exitTags)
			if err == nil {
				signals = append(signals, sig)
			}
			rst.PositionSide = ""
			rst.EntryPrice = 0
			rst.EntryTime = time.Time{}
			return rst, signals, nil
		}
	}

	// -----------------------------------------------------------------------
	// ENTRY LOGIC (long only, phase 1)
	// -----------------------------------------------------------------------
	if rst.PositionSide != "" || rst.PendingEntry != "" {
		rst.diagPositionLockSkipCount++
		return rst, nil, nil
	}

	// Gate 1: price extended below VWAP.
	if devZ > cfg.EntryDevZ {
		rst.diagDevZRejectCount++
		return rst, nil, nil
	}

	// Gate 2: TFI magnitude floor. For a long entry we want buy-pressure
	// despite the drop — tfi > TFIMin (positive).
	if cfg.RequireTFI {
		if tfi < cfg.TFIMin {
			rst.diagTFIRejectCount++
			return rst, nil, nil
		}
	}

	// Gate 3: recent bullish inducement sweep within InducementLookbackBars.
	// Fresh sweep only — we ignore sweeps that landed before this OnBar call
	// if InducementLookbackBars==0 would allow stale triggers. A sweep
	// registered by the *current* bar counts (priorBullishSweepIdx <
	// lastBullishSweepIdx means "ingest saw one this bar").
	_ = priorBullishSweepIdx // kept for symmetry / future telemetry
	if cfg.RequireInducement {
		if rst.lastBullishSweepIdx < 0 {
			rst.diagInducementRejectCount++
			return rst, nil, nil
		}
		age := rst.barIdx - rst.lastBullishSweepIdx
		if age > cfg.InducementLookbackBars {
			rst.diagInducementRejectCount++
			return rst, nil, nil
		}
	}
	rst.diagEntryGatePassCount++

	// Phase-2 gates — flipped off by default.
	if cfg.RequireXVFlow {
		// TODO: cross-venue flow aggregator (phase 2).
		return rst, nil, nil
	}
	if cfg.RequireSkewOK {
		// TODO: Deribit skew regime gate (phase 2).
		return rst, nil, nil
	}

	// Strength: clamp |dev_z| / |entry_dev_z| to [0.3, 1.0].
	denom := math.Abs(cfg.EntryDevZ)
	if denom == 0 {
		denom = 1
	}
	strength := clampF(math.Abs(devZ)/denom, 0.3, 1.0)

	// Session-time weighting. Neutral (1.0) by default; operators can dial
	// asia/weekend down once the memory-project policy is wired in.
	strength *= sessionWeight(now, cfg)
	strength = clampF(strength, 0.0, 1.0)

	instanceID, _ := start.NewInstanceID(symbol)
	entryTags := map[string]string{
		"reason":    "mean_revert_long",
		"ref_price": fmt.Sprintf("%.10f", bar.Close),
		"dev_z":     fmt.Sprintf("%.3f", devZ),
		"vwap":      fmt.Sprintf("%.4f", vwap),
		"sigma":     fmt.Sprintf("%.4f", sigma),
		"tfi":       fmt.Sprintf("%.3f", tfi),
		"strategy":  "crypto_revert",
	}
	if cfg.StrategyExitsPriority {
		entryTags["strategy_exits_priority"] = "true"
	}
	sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideBuy, strength, entryTags)
	if err != nil {
		return rst, nil, err
	}
	signals = append(signals, sig)
	rst.diagEmitCount++

	// Eagerly mark the position as open on signal emission. The backtest
	// slice-dispatch pipeline processes ALL bars in Phase A and only replays
	// fills (OnEvent) afterward — if we wait for FillConfirmation to set
	// PositionSide, every subsequent OnBar in Phase A still sees an empty
	// position and the strategy's own exit rules (reversion_complete,
	// hard_stop, time_stop, inducement_flip) never fire. Treating the emitted
	// entry signal as good-as-filled lets exit evaluation track the hold.
	// OnEvent still refines EntryPrice with the actual fill price when it
	// arrives; EntryRejection rolls the position back to flat.
	rst.PendingEntry = start.SideBuy
	rst.PendingEntryAt = now
	rst.PositionSide = start.SideBuy
	rst.EntryPrice = bar.Close
	rst.EntryTime = now
	return rst, signals, nil
}

// ---------------------------------------------------------------------------
// OnEvent — fills and rejections
// ---------------------------------------------------------------------------

func (s *CryptoRevertStrategy) OnEvent(_ start.Context, _ string, evt any, st start.State) (start.State, []start.Signal, error) {
	rst, ok := st.(*CryptoRevertState)
	if !ok {
		return st, nil, nil
	}

	switch e := evt.(type) {
	case start.TradeTick:
		// Ingest the tick into TFI only if the active source allows it.
		// This matches tfiUsingTradeTicks so "bar_sign" mode cannot
		// accidentally poison TFI with stray tick events.
		rst.ensureIndicators()
		if rst.Config.TFISource != "bar_sign" {
			rst.tfi.UpdateTrade(start.MarketTradeLike{
				Time:      e.Time,
				Size:      e.Size,
				TakerSide: e.TakerSide,
			})
			rst.tradeTickCount++
			rst.lastTradeAt = e.Time
		}
		return rst, nil, nil
	case start.FillConfirmation:
		switch e.Side {
		case start.SideBuy:
			// OnBar already marked the position open eagerly (see the entry
			// block). Refine EntryPrice with the actual fill price so exit
			// rules evaluate against the realized cost basis, and stamp
			// EntryTime to the bar that originated the signal.
			rst.PendingEntry = ""
			rst.PositionSide = start.SideBuy
			if e.Price > 0 {
				rst.EntryPrice = e.Price
			}
			if !rst.PendingEntryAt.IsZero() {
				rst.EntryTime = rst.PendingEntryAt
			}
		case start.SideSell:
			// Exit fill confirmed — clear all position state. The sell may
			// have been initiated by our OnBar exit logic OR by an external
			// exit rule (MAX_HOLDING_TIME, MAX_LOSS) from position_monitor.
			// Either way we're flat and must be ready to re-enter.
			rst.PendingEntry = ""
			rst.PositionSide = ""
			rst.EntryPrice = 0
			rst.EntryTime = time.Time{}
		}
	case start.EntryRejection:
		// Signal was rejected downstream (risk, exposure, etc.). Roll the
		// eagerly-set position state back to flat so the next bar can retry.
		rst.PendingEntry = ""
		rst.PositionSide = ""
		rst.EntryPrice = 0
		rst.EntryTime = time.Time{}
	}

	return rst, nil, nil
}

// sessionWeight returns the strength multiplier for the bar's UTC hour.
// US hours: 13:00-21:00 UTC (09:00-17:00 ET approx). Asia: 00:00-08:00 UTC.
// Weekend: Sat/Sun UTC. Overlaps default to US then Asia.
func sessionWeight(t time.Time, cfg CryptoRevertConfig) float64 {
	u := t.UTC()
	wd := u.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return cfg.WeightWeekend
	}
	h := u.Hour()
	switch {
	case h >= 13 && h < 21:
		return cfg.WeightUSHours
	case h >= 0 && h < 8:
		return cfg.WeightAsiaHours
	default:
		// Europe / transitional hours — neutral.
		return 1.0
	}
}
