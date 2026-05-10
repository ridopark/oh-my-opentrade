package risk

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// AuthorMirrorBucket gates entries from a configured set of "author-mirror"
// strategies (today: copytrade_v1 + tradingthetrend_v1) so that their
// combined exposure on a single underlying does not exceed
// CapMult * MaxRiskPct * AccountEquity, and so they do not collectively
// fire more than MaxFires entries within FireWindow.
//
// Disabled when CapMult <= 0 or len(Members) == 0; Check returns nil.
//
// Restart behavior: state is in-process. A process restart resets the
// fire-rate counter; this is acceptable because (a) restart is rare and
// supervised, and (b) other gates (portfolio_heat, exposure_guard,
// daily_loss) also apply.
type AuthorMirrorBucket struct {
	cfg          AuthorMirrorConfig
	posSource    PositionSource
	equitySource EquitySource
	clock        func() time.Time
	log          zerolog.Logger

	mu    sync.Mutex
	fires []time.Time
}

// AuthorMirrorConfig is the bucket's static configuration. Disabled when
// CapMult <= 0 or len(Members) == 0.
type AuthorMirrorConfig struct {
	Members    []string
	CapMult    float64
	FireWindow time.Duration
	MaxFires   int
	MaxRiskPct float64
}

// NewAuthorMirrorBucket constructs the gate. Pass clock=nil for time.Now.
func NewAuthorMirrorBucket(
	cfg AuthorMirrorConfig,
	posSource PositionSource,
	equitySource EquitySource,
	clock func() time.Time,
	log zerolog.Logger,
) *AuthorMirrorBucket {
	if clock == nil {
		clock = time.Now
	}
	return &AuthorMirrorBucket{
		cfg:          cfg,
		posSource:    posSource,
		equitySource: equitySource,
		clock:        clock,
		log:          log.With().Str("component", "author_mirror_bucket").Logger(),
	}
}

// Check enforces (a) per-underlying notional cap and (b) rolling-window
// fire-rate cap. Increments the fire counter on pass.
func (b *AuthorMirrorBucket) Check(_ context.Context, intent domain.OrderIntent) error {
	if !b.enabled() {
		return nil
	}
	if !b.isMember(intent.Strategy) {
		return nil
	}
	if intent.Direction.IsExit() {
		return nil
	}

	underlying := bucketKey(intent)
	if underlying == "" {
		return fmt.Errorf("author_mirror: cannot derive underlying from intent %s", intent.Symbol)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.clock()
	b.pruneFiresLocked(now)
	if len(b.fires) >= b.cfg.MaxFires {
		return fmt.Errorf("author_mirror: fire-rate cap %d/%s exceeded", b.cfg.MaxFires, b.cfg.FireWindow)
	}

	intentNotional := intent.Quantity * intent.LimitPrice
	if intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeOption && intent.MaxLossUSD > 0 {
		intentNotional = intent.MaxLossUSD
	}

	currentNotional := b.currentBucketNotionalLocked(underlying)
	cap := b.bucketCap()

	if cap > 0 && currentNotional+intentNotional > cap {
		return fmt.Errorf(
			"author_mirror: %s notional $%.0f + intent $%.0f would exceed cap $%.0f",
			underlying, currentNotional, intentNotional, cap,
		)
	}

	b.fires = append(b.fires, now)
	b.log.Debug().
		Str("underlying", underlying).
		Float64("current_notional", currentNotional).
		Float64("intent_notional", intentNotional).
		Float64("cap", cap).
		Int("fires_in_window", len(b.fires)).
		Msg("author_mirror: pass")
	return nil
}

func (b *AuthorMirrorBucket) enabled() bool {
	return b.cfg.CapMult > 0 && len(b.cfg.Members) > 0 && b.cfg.MaxFires > 0
}

func (b *AuthorMirrorBucket) isMember(strategy string) bool {
	for _, m := range b.cfg.Members {
		if m == strategy {
			return true
		}
	}
	return false
}

func (b *AuthorMirrorBucket) bucketCap() float64 {
	equity := b.equitySource.AccountEquity()
	return b.cfg.CapMult * b.cfg.MaxRiskPct * equity
}

func (b *AuthorMirrorBucket) pruneFiresLocked(now time.Time) {
	cutoff := now.Add(-b.cfg.FireWindow)
	keep := b.fires[:0]
	for _, t := range b.fires {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	b.fires = keep
}

func (b *AuthorMirrorBucket) currentBucketNotionalLocked(underlying string) float64 {
	var total float64
	for _, p := range b.posSource.ListPositions() {
		if p.Strategy == "" || !b.isMember(p.Strategy) {
			continue
		}
		if positionUnderlying(p) != underlying {
			continue
		}
		total += math.Abs(p.Quantity * p.EntryPrice)
	}
	return total
}

// bucketKey returns the underlying ticker as the aggregation key.
// For option intents we use Instrument.UnderlyingSymbol; for equities
// we use the intent symbol directly.
func bucketKey(intent domain.OrderIntent) string {
	if intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeOption {
		if intent.Instrument.UnderlyingSymbol != "" {
			return string(intent.Instrument.UnderlyingSymbol)
		}
		return ""
	}
	return string(intent.Symbol)
}

func positionUnderlying(p domain.MonitoredPosition) string {
	if p.InstrumentType == domain.InstrumentTypeOption {
		return string(domain.UnderlyingFromOCC(p.Symbol))
	}
	return string(p.Symbol)
}
