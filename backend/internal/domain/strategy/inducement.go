package strategy

import (
	"math"
	"time"
)

// InducementSwingSide classifies which side of the swing a SwingLevel tracks.
// Kept distinct from CandidateAnchorType to keep the inducement tracker
// decoupled from anchor promotion logic.
type InducementSwingSide int

const (
	InducementSwingHigh InducementSwingSide = iota
	InducementSwingLow
)

// SwingLevel is a confirmed swing high/low that is a candidate for future
// liquidity sweeps. BarAge is in bars since the swing's center bar; the
// consumer increments it each bar and prunes when it exceeds the configured
// cap.
type SwingLevel struct {
	Time   time.Time
	Price  float64
	Side   InducementSwingSide
	BarAge int
}

// PendingInducement is an in-progress multi-bar reversal candidate. Created
// when price breaches a swing but does not close back inside the same bar;
// BarsRemaining counts down each bar until the close returns inside (fires
// a moderate signal) or the countdown hits zero (expires silently).
type PendingInducement struct {
	Swing         SwingLevel
	BreachBar     time.Time
	BreachBPS     float64
	VolumeRatio   float64
	BarsRemaining int
}

// InducementStrength classifies the quality of a fired inducement signal.
type InducementStrength string

const (
	InducementStrong   InducementStrength = "strong"
	InducementModerate InducementStrength = "moderate"
	InducementWeak     InducementStrength = "weak"
)

// InducementSignal is the per-bar detector output consumed by the AVWAP
// confluence scorer. Direction is SideBuy when a swing LOW was swept (longs
// favored) and SideSell when a swing HIGH was swept (shorts favored).
type InducementSignal struct {
	Strength    InducementStrength
	Score       int
	Direction   Side
	Tag         string
	Swing       SwingLevel
	BreachBPS   float64
	VolumeRatio float64
}

// InducementConfig groups the detector tuning knobs. Populated from the
// strategy's AVWAPConfig; kept as its own struct so the domain detect
// function does not depend on the strategy config shape.
type InducementConfig struct {
	BreachMinBPS   float64
	BreachMaxBPS   float64
	ReversalBars   int
	VolumeMinRatio float64
	MaxAgeBars     int
}

// DetectInducement inspects the newest bar against the supplied swing ring
// buffers and any in-flight PendingInducement, returning the best qualifying
// signal (or nil) and the updated pending state.
//
// Contract:
//   - Pure: no global state, no clock. Callers feed bars in order.
//   - pending may be nil; the returned pending pointer replaces the caller's.
//   - Same-bar reversal beats multi-bar; higher BreachBPS breaks ties.
//   - If both a swing HIGH and a swing LOW are swept on this bar, returns
//     (nil, nil) — ambiguous volatility event, not a directional inducement.
//   - When a new sweep fires on the same bar as an existing pending, the new
//     sweep replaces the pending (pending returned is nil or the fresh one
//     for multi-bar cases).
func DetectInducement(
	bar Bar,
	recentHighs []SwingLevel,
	recentLows []SwingLevel,
	pending *PendingInducement,
	cfg InducementConfig,
	volumeSMA float64,
) (*InducementSignal, *PendingInducement) {
	volRatio := 0.0
	if volumeSMA > 0 {
		volRatio = bar.Volume / volumeSMA
	}
	volumeConfirmed := cfg.VolumeMinRatio <= 0 || volRatio >= cfg.VolumeMinRatio

	sweptHigh := false
	sweptLow := false
	for _, sw := range recentHighs {
		if sw.BarAge > cfg.MaxAgeBars {
			continue
		}
		if bar.High > sw.Price && withinBreach(bar.High, sw.Price, cfg) {
			sweptHigh = true
			break
		}
	}
	for _, sw := range recentLows {
		if sw.BarAge > cfg.MaxAgeBars {
			continue
		}
		if bar.Low < sw.Price && withinBreach(sw.Price, bar.Low, cfg) {
			sweptLow = true
			break
		}
	}
	if sweptHigh && sweptLow {
		// Ambiguous bar — drop any pending too, since the market is
		// signaling a two-sided volatility event rather than a clean sweep.
		return nil, nil
	}

	// Resolve pending first: if a reversal completes this bar, fire moderate.
	// Pending is kept only when the current bar does not itself generate a
	// new sweep that would replace it.
	var nextPending *PendingInducement
	var pendingSignal *InducementSignal
	if pending != nil {
		ps, stillPending := resolvePending(bar, pending, cfg, volRatio)
		pendingSignal = ps
		nextPending = stillPending
	}

	// Look for a fresh sweep on this bar. Track best by breach size.
	var best *InducementSignal
	var bestPending *PendingInducement
	for _, sw := range recentHighs {
		if sw.BarAge > cfg.MaxAgeBars {
			continue
		}
		sig, pend := evaluateSweep(bar, sw, cfg, volRatio, volumeConfirmed)
		best, bestPending = pickBetter(best, bestPending, sig, pend)
	}
	for _, sw := range recentLows {
		if sw.BarAge > cfg.MaxAgeBars {
			continue
		}
		sig, pend := evaluateSweep(bar, sw, cfg, volRatio, volumeConfirmed)
		best, bestPending = pickBetter(best, bestPending, sig, pend)
	}

	// A fresh sweep on this bar replaces any in-flight pending (spec:
	// "Pending inducement + new sweep: new replaces pending").
	if best != nil || bestPending != nil {
		return best, bestPending
	}
	return pendingSignal, nextPending
}

// withinBreach reports whether the breach (high/low vs swing price) falls
// inside the configured [BreachMin, BreachMax] band in basis points.
func withinBreach(extreme, swingPrice float64, cfg InducementConfig) bool {
	if swingPrice <= 0 {
		return false
	}
	bps := math.Abs(extreme-swingPrice) / swingPrice * 10000.0
	return bps >= cfg.BreachMinBPS && bps <= cfg.BreachMaxBPS
}

// evaluateSweep inspects a single swing against the current bar and returns
// either a fresh signal (same-bar reversal), a new pending (breach without
// same-bar reversal), or (nil, nil) when the swing is not swept.
func evaluateSweep(
	bar Bar,
	sw SwingLevel,
	cfg InducementConfig,
	volRatio float64,
	volumeConfirmed bool,
) (*InducementSignal, *PendingInducement) {
	if sw.Price <= 0 {
		return nil, nil
	}
	switch sw.Side {
	case InducementSwingHigh:
		if !(bar.High > sw.Price) {
			return nil, nil
		}
		breachBPS := (bar.High - sw.Price) / sw.Price * 10000.0
		if breachBPS < cfg.BreachMinBPS || breachBPS > cfg.BreachMaxBPS {
			return nil, nil
		}
		if bar.Close < sw.Price {
			// Same-bar reversal of a swing high => bearish signal (short).
			strength, score, tag := sameBarScore(volumeConfirmed, false)
			return &InducementSignal{
				Strength:    strength,
				Score:       score,
				Direction:   SideSell,
				Tag:         tag,
				Swing:       sw,
				BreachBPS:   breachBPS,
				VolumeRatio: volRatio,
			}, nil
		}
		// Breach without same-bar reversal: start multi-bar pending.
		return nil, &PendingInducement{
			Swing:         sw,
			BreachBar:     bar.Time,
			BreachBPS:     breachBPS,
			VolumeRatio:   volRatio,
			BarsRemaining: cfg.ReversalBars,
		}
	case InducementSwingLow:
		if !(bar.Low < sw.Price) {
			return nil, nil
		}
		breachBPS := (sw.Price - bar.Low) / sw.Price * 10000.0
		if breachBPS < cfg.BreachMinBPS || breachBPS > cfg.BreachMaxBPS {
			return nil, nil
		}
		if bar.Close > sw.Price {
			strength, score, tag := sameBarScore(volumeConfirmed, true)
			return &InducementSignal{
				Strength:    strength,
				Score:       score,
				Direction:   SideBuy,
				Tag:         tag,
				Swing:       sw,
				BreachBPS:   breachBPS,
				VolumeRatio: volRatio,
			}, nil
		}
		return nil, &PendingInducement{
			Swing:         sw,
			BreachBar:     bar.Time,
			BreachBPS:     breachBPS,
			VolumeRatio:   volRatio,
			BarsRemaining: cfg.ReversalBars,
		}
	}
	return nil, nil
}

// sameBarScore maps the volume-confirm flag to the spec's strength/score
// table (strong=5, weak=2). bullish is only used to distinguish tag strings.
func sameBarScore(volumeConfirmed, bullish bool) (InducementStrength, int, string) {
	if volumeConfirmed {
		if bullish {
			return InducementStrong, 5, "inducement_strong_long"
		}
		return InducementStrong, 5, "inducement_strong_short"
	}
	if bullish {
		return InducementWeak, 2, "inducement_weak_long"
	}
	return InducementWeak, 2, "inducement_weak_short"
}

// resolvePending advances the pending countdown and fires a moderate signal
// when the close returns inside the swept level within the window. Returns
// the signal (or nil) and the remaining pending (nil once resolved/expired).
func resolvePending(
	bar Bar,
	pending *PendingInducement,
	cfg InducementConfig,
	volRatio float64,
) (*InducementSignal, *PendingInducement) {
	if pending.BarsRemaining <= 0 {
		return nil, nil
	}
	next := *pending
	next.BarsRemaining--

	switch pending.Swing.Side {
	case InducementSwingHigh:
		if bar.Close < pending.Swing.Price {
			volumeConfirmed := cfg.VolumeMinRatio <= 0 || pending.VolumeRatio >= cfg.VolumeMinRatio
			if !volumeConfirmed {
				return nil, nil
			}
			return &InducementSignal{
				Strength:    InducementModerate,
				Score:       3,
				Direction:   SideSell,
				Tag:         "inducement_moderate_short",
				Swing:       pending.Swing,
				BreachBPS:   pending.BreachBPS,
				VolumeRatio: pending.VolumeRatio,
			}, nil
		}
	case InducementSwingLow:
		if bar.Close > pending.Swing.Price {
			volumeConfirmed := cfg.VolumeMinRatio <= 0 || pending.VolumeRatio >= cfg.VolumeMinRatio
			if !volumeConfirmed {
				return nil, nil
			}
			return &InducementSignal{
				Strength:    InducementModerate,
				Score:       3,
				Direction:   SideBuy,
				Tag:         "inducement_moderate_long",
				Swing:       pending.Swing,
				BreachBPS:   pending.BreachBPS,
				VolumeRatio: pending.VolumeRatio,
			}, nil
		}
	}
	if next.BarsRemaining <= 0 {
		return nil, nil
	}
	_ = volRatio // volRatio unused for pending resolution; reserved for future use
	return nil, &next
}

// pickBetter returns whichever of the two candidates has the higher-priority
// outcome. Priority order: a fired signal beats a pending; among fired
// signals, higher BreachBPS wins; among pendings, higher BreachBPS wins.
func pickBetter(
	curSig *InducementSignal, curPend *PendingInducement,
	newSig *InducementSignal, newPend *PendingInducement,
) (*InducementSignal, *PendingInducement) {
	switch {
	case newSig != nil && curSig != nil:
		if newSig.BreachBPS > curSig.BreachBPS {
			return newSig, nil
		}
		return curSig, nil
	case newSig != nil:
		return newSig, nil
	case curSig != nil:
		return curSig, curPend
	case newPend != nil && curPend != nil:
		if newPend.BreachBPS > curPend.BreachBPS {
			return nil, newPend
		}
		return nil, curPend
	case newPend != nil:
		return nil, newPend
	}
	return curSig, curPend
}
