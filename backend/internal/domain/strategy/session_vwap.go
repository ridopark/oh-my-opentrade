package strategy

import (
	"math"
	"time"
)

// SessionVWAP computes a rolling volume-weighted average price anchored to a
// UTC session boundary, and a sigma band around it for mean-reversion
// z-score gating. Used by MFT crypto revert strategies.
//
// Because crypto trades 24/7, "session" is defined as the UTC day starting at
// SessionResetUTCHour (default 0 = UTC-midnight). VWAP state resets at that
// boundary each day. Sigma computation offers three interchangeable methods
// so we can A/B them in backtest: session-cumulative, fixed-N rolling, and
// EWMA. The doc's default is "session" but the quant analyst flagged it as
// a likely source of noisy first-backtest reads (low N early in the day),
// so this implementation defaults to "rolling".
//
// The indicator is purely in-memory and deterministic. It is NOT thread-safe;
// the caller (single shard runner) guarantees serial access.
type SessionVWAP struct {
	cfg SessionVWAPConfig

	// Session anchor — we treat these as the start-of-session sentinel.
	sessionYear  int
	sessionMonth time.Month
	sessionDay   int
	hasSession   bool

	// Cumulative session state for VWAP + session-sigma.
	cumPV       float64 // sum(price * volume)
	cumVolume   float64
	cumDevSq    float64 // sum((close - vwap_at_step)^2) — approx session variance
	cumCount    int

	// Rolling window of (close - vwap) deviations for "rolling" sigma.
	devWindow []float64

	// EWMA state for "ewma" sigma.
	ewmaMean float64
	ewmaVar  float64
	ewmaInit bool
}

// SessionVWAPConfig controls session anchoring and sigma method.
type SessionVWAPConfig struct {
	// SessionResetUTCHour is the UTC hour at which the session rolls over.
	// Default 0 = UTC-midnight.
	SessionResetUTCHour int

	// SigmaMethod selects the deviation estimator: "session", "rolling",
	// or "ewma". Default (empty) -> "rolling".
	SigmaMethod string

	// SigmaLookbackBars is the window length for "rolling" sigma and the
	// effective span for "ewma" sigma (alpha = 2/(N+1)). Default 96
	// (8 hours at 5m bars).
	SigmaLookbackBars int
}

const (
	SigmaMethodSession = "session"
	SigmaMethodRolling = "rolling"
	SigmaMethodEWMA    = "ewma"
)

// DefaultSessionVWAPConfig returns the defaults described in the doc — with
// "rolling" as the chosen sigma method pending A/B validation.
func DefaultSessionVWAPConfig() SessionVWAPConfig {
	return SessionVWAPConfig{
		SessionResetUTCHour: 0,
		SigmaMethod:         SigmaMethodRolling,
		SigmaLookbackBars:   96,
	}
}

// NewSessionVWAP constructs a SessionVWAP with the given config. Zero config
// is replaced with defaults; unknown sigma method falls back to "rolling".
func NewSessionVWAP(cfg SessionVWAPConfig) *SessionVWAP {
	if cfg.SigmaLookbackBars == 0 && cfg.SigmaMethod == "" {
		cfg = DefaultSessionVWAPConfig()
	}
	if cfg.SigmaMethod == "" {
		cfg.SigmaMethod = SigmaMethodRolling
	}
	if cfg.SigmaLookbackBars <= 0 {
		cfg.SigmaLookbackBars = 96
	}
	return &SessionVWAP{cfg: cfg}
}

// Update ingests a new bar and returns the current VWAP, sigma estimate, and
// deviation z-score (close - vwap)/sigma. ok=false until at least one bar has
// been ingested in the current session AND sigma is well-defined for the
// chosen method (rolling/ewma need 2+ samples; session needs 2+).
func (s *SessionVWAP) Update(bar Bar) (vwap, sigma, devZ float64, ok bool) {
	s.rolloverIfNewSession(bar.Time)

	if bar.Volume > 0 {
		s.cumPV += bar.Close * bar.Volume
		s.cumVolume += bar.Volume
	}

	if s.cumVolume <= 0 {
		return 0, 0, 0, false
	}
	vwap = s.cumPV / s.cumVolume
	dev := bar.Close - vwap

	// Session-cumulative variance estimator (online, biased).
	s.cumDevSq += dev * dev
	s.cumCount++

	// Rolling window.
	s.devWindow = append(s.devWindow, dev)
	if len(s.devWindow) > s.cfg.SigmaLookbackBars {
		s.devWindow = s.devWindow[len(s.devWindow)-s.cfg.SigmaLookbackBars:]
	}

	// EWMA of mean + variance of deviation. alpha from effective span.
	alpha := 2.0 / (float64(s.cfg.SigmaLookbackBars) + 1.0)
	if !s.ewmaInit {
		s.ewmaMean = dev
		s.ewmaVar = 0
		s.ewmaInit = true
	} else {
		diff := dev - s.ewmaMean
		s.ewmaMean += alpha * diff
		s.ewmaVar = (1-alpha)*(s.ewmaVar + alpha*diff*diff)
	}

	sigma, okSigma := s.computeSigma()
	if !okSigma {
		return vwap, 0, 0, false
	}
	if sigma == 0 {
		return vwap, 0, 0, false
	}
	return vwap, sigma, dev / sigma, true
}

func (s *SessionVWAP) computeSigma() (float64, bool) {
	switch s.cfg.SigmaMethod {
	case SigmaMethodSession:
		if s.cumCount < 2 {
			return 0, false
		}
		return math.Sqrt(s.cumDevSq / float64(s.cumCount)), true
	case SigmaMethodEWMA:
		if !s.ewmaInit || s.ewmaVar <= 0 {
			return 0, false
		}
		return math.Sqrt(s.ewmaVar), true
	default: // rolling
		n := len(s.devWindow)
		if n < 2 {
			return 0, false
		}
		mean := 0.0
		for _, v := range s.devWindow {
			mean += v
		}
		mean /= float64(n)
		variance := 0.0
		for _, v := range s.devWindow {
			d := v - mean
			variance += d * d
		}
		variance /= float64(n)
		return math.Sqrt(variance), true
	}
}

// rolloverIfNewSession resets cumulative session state when the bar crosses
// the configured UTC session boundary. Rolling-window and EWMA state are
// preserved across sessions — they are intra-bar noise estimators, not
// session-local concepts.
func (s *SessionVWAP) rolloverIfNewSession(ts time.Time) {
	y, m, d := sessionDateUTC(ts, s.cfg.SessionResetUTCHour)
	if !s.hasSession {
		s.sessionYear, s.sessionMonth, s.sessionDay = y, m, d
		s.hasSession = true
		return
	}
	if y != s.sessionYear || m != s.sessionMonth || d != s.sessionDay {
		s.cumPV = 0
		s.cumVolume = 0
		s.cumDevSq = 0
		s.cumCount = 0
		s.sessionYear, s.sessionMonth, s.sessionDay = y, m, d
	}
}

// sessionDateUTC returns the (year, month, day) of the session that contains
// ts given that sessions start at resetHour UTC. For resetHour=0 this is
// simply ts.UTC().Date().
func sessionDateUTC(ts time.Time, resetHour int) (int, time.Month, int) {
	u := ts.UTC()
	if resetHour > 0 {
		u = u.Add(-time.Duration(resetHour) * time.Hour)
	}
	return u.Date()
}
