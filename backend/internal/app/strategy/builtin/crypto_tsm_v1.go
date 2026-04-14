package builtin

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// CryptoTSMStrategy implements Volume-Weighted Time-Series Momentum for
// 24/7 crypto markets on daily bars.
//
// Academic basis: Huang, Sangiorgi & Urquhart (2024) — "Cryptocurrency
// Volume-Weighted Time Series Momentum" (SSRN). Reported Sharpe 1.51
// with 28-day lookback and 5-day holding period.
//
// Signal: VWTSM = Σ w(k) * r(t-k) * V(t-k) / V̄, z-scored over a
// trailing window. Entries require positive z-score, expanding volume
// regime, and realized vol below a cap. Exits via trailing stop, signal
// reversal, time stop, or volatility spike.
type CryptoTSMStrategy struct {
	meta start.Meta
}

func NewCryptoTSMStrategy() *CryptoTSMStrategy {
	id, _ := start.NewStrategyID("crypto_tsm")
	ver, _ := start.NewVersion("1.0.0")
	return &CryptoTSMStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "Crypto Time-Series Momentum",
			Description: "Volume-weighted daily TS momentum for crypto spot (Huang et al. 2024)",
			Author:      "system",
		},
	}
}

func (s *CryptoTSMStrategy) Meta() start.Meta { return s.meta }

// WarmupBars returns bars needed before signal generation.
// 90 daily bars: 28 for lookback + 62 for z-score baseline.
func (s *CryptoTSMStrategy) WarmupBars() int { return 90 }

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

type CryptoTSMConfig struct {
	// Signal parameters
	LookbackDays    int     // momentum lookback window (default 28)
	DecayTauDivisor float64 // tau = LookbackDays / DecayTauDivisor (default 3.0)
	ZScoreWindow    int     // trailing window for z-scoring the raw signal (default 90)
	EntryZThreshold float64 // minimum z-score to enter (default 0.5)
	ExitZThreshold  float64 // z-score below which to exit (default 0.0)

	// Volume regime filter
	VolRegimeFast int // fast volume SMA period (default 20)
	VolRegimeSlow int // slow volume SMA period (default 50)

	// Volatility filters
	RealizedVolCapPct  float64 // max 20d annualized vol to allow entry (default 120)
	CrashVolExitPct    float64 // 5d vol spike that triggers immediate exit (default 150)
	VolAnnualizeFactor float64 // sqrt(trading days/year); crypto = sqrt(365) ≈ 19.1 (default 19.105)

	// Risk management
	TrailingStopATRMult float64 // trailing stop distance in ATR multiples (default 2.5)
	ATRPeriod           int     // ATR period (default 14)
	MaxHoldDays         int     // maximum holding period in days (default 14)
	HardStopPct         float64 // hard stop loss percentage (default 0.05 = 5%)
	RiskPerTradeBPS     int     // risk budget per trade in basis points (default 200)
	MaxGrossExposurePct float64 // max portfolio allocation to crypto (default 80)
	MaxPositions        int     // max concurrent positions (default 3)

	// Execution
	CooldownDays int // minimum days between trades on same symbol (default 2)
}

func parseCryptoTSMConfig(params map[string]any) CryptoTSMConfig {
	return CryptoTSMConfig{
		LookbackDays:        getInt(params, "lookback_days", 28),
		DecayTauDivisor:     getFloat64(params, "decay_tau_divisor", 3.0),
		ZScoreWindow:        getInt(params, "zscore_window", 90),
		EntryZThreshold:     getFloat64(params, "entry_z_threshold", 0.5),
		ExitZThreshold:      getFloat64(params, "exit_z_threshold", 0.0),
		VolRegimeFast:       getInt(params, "vol_regime_fast", 20),
		VolRegimeSlow:       getInt(params, "vol_regime_slow", 50),
		RealizedVolCapPct:   getFloat64(params, "realized_vol_cap_pct", 120.0),
		CrashVolExitPct:     getFloat64(params, "crash_vol_exit_pct", 150.0),
		VolAnnualizeFactor:  getFloat64(params, "vol_annualize_factor", 19.105), // sqrt(365)
		TrailingStopATRMult: getFloat64(params, "trailing_stop_atr_mult", 2.5),
		ATRPeriod:           getInt(params, "atr_period", 14),
		MaxHoldDays:         getInt(params, "max_hold_days", 14),
		HardStopPct:         getFloat64(params, "hard_stop_pct", 0.05),
		RiskPerTradeBPS:     getInt(params, "risk_per_trade_bps", 200),
		MaxGrossExposurePct: getFloat64(params, "max_gross_exposure_pct", 80.0),
		MaxPositions:        getInt(params, "max_positions", 3),
		CooldownDays:        getInt(params, "cooldown_days", 2),
	}
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

type CryptoTSMState struct {
	Symbol    string          `json:"symbol"`
	Config    CryptoTSMConfig `json:"config"`
	CalcBars  int             `json:"calcBars"` // total daily bars processed
	Indicators start.IndicatorData `json:"-"`

	// Rolling daily bar history (close + volume + log returns)
	Closes  []float64 `json:"closes"`  // recent daily closes
	Volumes []float64 `json:"volumes"` // recent daily volumes
	LogRets []float64 `json:"logRets"` // recent daily log returns

	// Raw VWTSM signal history for z-scoring
	RawSignals []float64 `json:"rawSignals"`

	// ATR tracking (true range history for daily ATR)
	TrueRanges []float64 `json:"trueRanges"`
	PrevClose  float64   `json:"prevClose"`

	// Position state
	PositionSide   start.Side `json:"positionSide,omitempty"`
	PendingEntry   start.Side `json:"pendingEntry,omitempty"`
	PendingEntryAt time.Time  `json:"pendingEntryAt,omitzero"`
	EntryPrice     float64    `json:"entryPrice,omitempty"`
	EntryTime      time.Time  `json:"entryTime,omitzero"`
	HighSinceEntry float64    `json:"highSinceEntry,omitempty"` // for trailing stop

	// Trade management
	CooldownUntil time.Time `json:"cooldownUntil,omitzero"`
	TradesToday   int       `json:"tradesToday"`
	LastTradeDate string    `json:"lastTradeDate,omitempty"`
}

func (st *CryptoTSMState) SetIndicators(ind start.IndicatorData) {
	st.Indicators = ind
}

func (st *CryptoTSMState) Marshal() ([]byte, error)   { return json.Marshal(st) }
func (st *CryptoTSMState) Unmarshal(data []byte) error { return json.Unmarshal(data, st) }

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func (s *CryptoTSMStrategy) Init(_ start.Context, symbol string, params map[string]any, prior start.State) (start.State, error) {
	cfg := parseCryptoTSMConfig(params)
	st := &CryptoTSMState{
		Symbol: symbol,
		Config: cfg,
	}
	if prior != nil {
		if tsmPrior, ok := prior.(*CryptoTSMState); ok {
			st = tsmPrior
			st.Config = cfg
		}
	}
	return st, nil
}

// ---------------------------------------------------------------------------
// ReplayOnBar — warmup without signals
// ---------------------------------------------------------------------------

func (s *CryptoTSMStrategy) ReplayOnBar(_ start.Context, _ string, bar start.Bar, st start.State, indicators start.IndicatorData) (start.State, error) {
	tsmSt, ok := st.(*CryptoTSMState)
	if !ok {
		return st, fmt.Errorf("CryptoTSMStrategy.ReplayOnBar: expected *CryptoTSMState, got %T", st)
	}
	tsmSt.Indicators = indicators
	tsmSt.updateDailyData(bar)
	return tsmSt, nil
}

// ---------------------------------------------------------------------------
// OnBar — main decision loop (daily bars)
// ---------------------------------------------------------------------------

func (s *CryptoTSMStrategy) OnBar(ctx start.Context, symbol string, bar start.Bar, st start.State) (start.State, []start.Signal, error) {
	tsmSt, ok := st.(*CryptoTSMState)
	if !ok {
		return st, nil, fmt.Errorf("CryptoTSMStrategy.OnBar: expected *CryptoTSMState, got %T", st)
	}
	cfg := tsmSt.Config
	ind := tsmSt.Indicators

	now := bar.Time
	if ctx != nil {
		now = ctx.Now()
	}

	// Daily trade counter reset.
	todayStr := now.Format("2006-01-02")
	if tsmSt.LastTradeDate != todayStr {
		tsmSt.TradesToday = 0
		tsmSt.LastTradeDate = todayStr
	}

	// Accumulate daily data.
	tsmSt.updateDailyData(bar)

	// Need enough data to compute signal.
	minBars := cfg.LookbackDays + 1 // +1 because log returns need n+1 closes
	if len(tsmSt.LogRets) < minBars {
		return tsmSt, nil, nil
	}

	// Compute VWTSM signal and z-score.
	rawSignal := tsmSt.computeVWTSM()
	tsmSt.RawSignals = appendCapped(tsmSt.RawSignals, rawSignal, cfg.ZScoreWindow)
	zScore := zScoreOf(tsmSt.RawSignals)

	// Compute realized volatility (20d and 5d).
	realizedVol20 := tsmSt.realizedVol(20)
	realizedVol5 := tsmSt.realizedVol(5)

	// Volume regime: fast SMA > slow SMA = expanding.
	volExpanding := tsmSt.volumeExpanding()

	// ATR for trailing stop.
	atr := tsmSt.dailyATR()
	if atr == 0 && ind.HTF != nil {
		if htf1d, ok := ind.HTF["1d"]; ok && htf1d.DailyATR > 0 {
			atr = htf1d.DailyATR
		}
	}

	var signals []start.Signal

	// -----------------------------------------------------------------------
	// EXIT LOGIC (evaluate first)
	// -----------------------------------------------------------------------
	if tsmSt.PositionSide == start.SideBuy {
		// Update trailing stop tracker.
		if bar.High > tsmSt.HighSinceEntry {
			tsmSt.HighSinceEntry = bar.High
		}

		exitReason := ""

		// 1. Signal reversal: z-score drops below exit threshold.
		if zScore < cfg.ExitZThreshold {
			exitReason = "signal_reversal"
		}

		// 2. Trailing stop: price drops 2.5 ATR from peak.
		if atr > 0 && exitReason == "" {
			trailingStop := tsmSt.HighSinceEntry - cfg.TrailingStopATRMult*atr
			if bar.Close <= trailingStop {
				exitReason = "trailing_stop"
			}
		}

		// 3. Time stop: max holding period exceeded.
		if exitReason == "" && !tsmSt.EntryTime.IsZero() {
			holdDays := int(now.Sub(tsmSt.EntryTime).Hours() / 24)
			if holdDays >= cfg.MaxHoldDays {
				exitReason = "time_stop"
			}
		}

		// 4. Volatility stop: 5d vol spike above crash threshold.
		if exitReason == "" && realizedVol5 > cfg.CrashVolExitPct {
			exitReason = "vol_spike"
		}

		// 5. Hard stop: price dropped more than HardStopPct from entry.
		if exitReason == "" && tsmSt.EntryPrice > 0 {
			pctLoss := (tsmSt.EntryPrice - bar.Close) / tsmSt.EntryPrice
			if pctLoss >= cfg.HardStopPct {
				exitReason = "hard_stop"
			}
		}

		if exitReason != "" {
			instanceID, _ := start.NewInstanceID(symbol)
			sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideSell, 1.0, map[string]string{
				"reason":   exitReason,
				"z_score":  fmt.Sprintf("%.3f", zScore),
				"vol5d":    fmt.Sprintf("%.1f", realizedVol5),
				"strategy": "crypto_tsm",
			})
			if err == nil {
				signals = append(signals, sig)
			}
			tsmSt.PositionSide = ""
			tsmSt.EntryPrice = 0
			tsmSt.EntryTime = time.Time{}
			tsmSt.HighSinceEntry = 0
			tsmSt.CooldownUntil = now.Add(time.Duration(cfg.CooldownDays) * 24 * time.Hour)
			return tsmSt, signals, nil
		}
	}

	// -----------------------------------------------------------------------
	// ENTRY LOGIC
	// -----------------------------------------------------------------------
	if tsmSt.PositionSide != "" || tsmSt.PendingEntry != "" {
		return tsmSt, nil, nil // already in position or pending
	}

	// Cooldown check.
	if now.Before(tsmSt.CooldownUntil) {
		return tsmSt, nil, nil
	}

	// Gate: z-score must be above entry threshold.
	if zScore < cfg.EntryZThreshold {
		return tsmSt, nil, nil
	}

	// Gate: volume regime must be expanding.
	if !volExpanding {
		return tsmSt, nil, nil
	}

	// Gate: realized volatility must be below cap.
	if realizedVol20 > cfg.RealizedVolCapPct {
		return tsmSt, nil, nil
	}

	// Compute signal strength: clamp z-score to [0.3, 1.0].
	strength := clampF(zScore/2.0, 0.3, 1.0)

	instanceID, _ := start.NewInstanceID(symbol)
	sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideBuy, strength, map[string]string{
		"reason":       "vwtsm_entry",
		"z_score":      fmt.Sprintf("%.3f", zScore),
		"raw_signal":   fmt.Sprintf("%.6f", rawSignal),
		"vol20d":       fmt.Sprintf("%.1f", realizedVol20),
		"vol_expanding": fmt.Sprintf("%v", volExpanding),
		"atr":          fmt.Sprintf("%.2f", atr),
		"strategy":     "crypto_tsm",
	})
	if err != nil {
		return tsmSt, nil, err
	}
	signals = append(signals, sig)

	tsmSt.PendingEntry = start.SideBuy
	tsmSt.PendingEntryAt = now
	return tsmSt, signals, nil
}

// ---------------------------------------------------------------------------
// OnEvent — handle fills and rejections
// ---------------------------------------------------------------------------

func (s *CryptoTSMStrategy) OnEvent(_ start.Context, _ string, evt any, st start.State) (start.State, []start.Signal, error) {
	tsmSt, ok := st.(*CryptoTSMState)
	if !ok {
		return st, nil, nil
	}

	switch e := evt.(type) {
	case start.FillConfirmation:
		if tsmSt.PendingEntry == start.SideBuy && e.Side == start.SideBuy {
			tsmSt.PendingEntry = ""
			tsmSt.PositionSide = start.SideBuy
			tsmSt.EntryPrice = e.Price
			tsmSt.EntryTime = tsmSt.PendingEntryAt
			tsmSt.HighSinceEntry = e.Price
			tsmSt.TradesToday++
		} else if e.Side == start.SideSell {
			// Exit fill confirmed — state already cleared in OnBar.
			tsmSt.PendingEntry = ""
		}
	case start.EntryRejection:
		tsmSt.PendingEntry = ""
	}

	return tsmSt, nil, nil
}

// ---------------------------------------------------------------------------
// Internal: daily data accumulation
// ---------------------------------------------------------------------------

// updateDailyData appends the bar's close/volume and computes log return.
func (st *CryptoTSMState) updateDailyData(bar start.Bar) {
	st.CalcBars++

	// True range for ATR.
	if st.PrevClose > 0 {
		tr := math.Max(bar.High-bar.Low,
			math.Max(math.Abs(bar.High-st.PrevClose), math.Abs(bar.Low-st.PrevClose)))
		maxTR := st.Config.ATRPeriod + st.Config.LookbackDays
		st.TrueRanges = appendCapped(st.TrueRanges, tr, maxTR)
	}

	// Log return.
	if st.PrevClose > 0 && bar.Close > 0 {
		lr := math.Log(bar.Close / st.PrevClose)
		maxRets := st.Config.ZScoreWindow + st.Config.LookbackDays
		st.LogRets = appendCapped(st.LogRets, lr, maxRets)
	}

	maxHist := st.Config.ZScoreWindow + st.Config.LookbackDays
	st.Closes = appendCapped(st.Closes, bar.Close, maxHist)
	st.Volumes = appendCapped(st.Volumes, bar.Volume, maxHist)
	st.PrevClose = bar.Close
}

// ---------------------------------------------------------------------------
// Internal: VWTSM signal computation
// ---------------------------------------------------------------------------

// computeVWTSM computes the volume-weighted time-series momentum signal.
//
//	VWTSM = Σ_{k=1}^{L} exp(-k/τ) * r(t-k) * V(t-k) / V̄
//
// where L = lookback, τ = L/DecayTauDivisor, r = log return, V = volume.
func (st *CryptoTSMState) computeVWTSM() float64 {
	cfg := st.Config
	lookback := cfg.LookbackDays
	tau := float64(lookback) / cfg.DecayTauDivisor

	n := len(st.LogRets)
	if n < lookback {
		return 0
	}

	// Mean volume over the lookback window.
	var volSum float64
	for k := 1; k <= lookback; k++ {
		volSum += st.Volumes[len(st.Volumes)-k]
	}
	volMean := volSum / float64(lookback)
	if volMean == 0 {
		return 0
	}

	// Weighted sum of volume-adjusted returns.
	var signal float64
	for k := 1; k <= lookback; k++ {
		w := math.Exp(-float64(k) / tau)
		ret := st.LogRets[n-k]
		vol := st.Volumes[len(st.Volumes)-k]
		signal += w * ret * vol / volMean
	}

	return signal
}

// ---------------------------------------------------------------------------
// Internal: volatility computation
// ---------------------------------------------------------------------------

// realizedVol computes annualized realized volatility over the last n days
// using log returns. Returns percentage (e.g., 80.0 = 80%).
func (st *CryptoTSMState) realizedVol(n int) float64 {
	if len(st.LogRets) < n || n < 2 {
		return 0
	}
	rets := st.LogRets[len(st.LogRets)-n:]

	// Mean.
	var sum float64
	for _, r := range rets {
		sum += r
	}
	mean := sum / float64(n)

	// Variance.
	var ss float64
	for _, r := range rets {
		d := r - mean
		ss += d * d
	}
	variance := ss / float64(n-1)

	// Annualize: σ_daily * sqrt(365) * 100.
	return math.Sqrt(variance) * st.Config.VolAnnualizeFactor * 100.0
}

// volumeExpanding returns true if the fast volume SMA exceeds the slow SMA.
func (st *CryptoTSMState) volumeExpanding() bool {
	cfg := st.Config
	n := len(st.Volumes)
	if n < cfg.VolRegimeSlow {
		return true // insufficient data, allow entry
	}

	fastAvg := smaLast(st.Volumes, cfg.VolRegimeFast)
	slowAvg := smaLast(st.Volumes, cfg.VolRegimeSlow)
	return fastAvg > slowAvg
}

// dailyATR computes simple ATR from stored true ranges.
func (st *CryptoTSMState) dailyATR() float64 {
	period := st.Config.ATRPeriod
	if len(st.TrueRanges) < period {
		return 0
	}
	return smaLast(st.TrueRanges, period)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// zScoreOf computes z-score of the last element in a slice against the full slice.
func zScoreOf(vals []float64) float64 {
	n := len(vals)
	if n < 3 {
		return 0
	}
	last := vals[n-1]

	var sum float64
	for _, v := range vals {
		sum += v
	}
	mean := sum / float64(n)

	var ss float64
	for _, v := range vals {
		d := v - mean
		ss += d * d
	}
	sd := math.Sqrt(ss / float64(n-1))
	if sd == 0 {
		return 0
	}
	return (last - mean) / sd
}

// smaLast computes the simple moving average of the last n elements.
func smaLast(vals []float64, n int) float64 {
	if len(vals) < n || n == 0 {
		return 0
	}
	var sum float64
	for i := len(vals) - n; i < len(vals); i++ {
		sum += vals[i]
	}
	return sum / float64(n)
}

// appendCapped appends v to s and trims from the front if len exceeds cap.
func appendCapped(s []float64, v float64, cap int) []float64 {
	s = append(s, v)
	if len(s) > cap {
		s = s[len(s)-cap:]
	}
	return s
}

// clampF clamps v to [lo, hi].
func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
