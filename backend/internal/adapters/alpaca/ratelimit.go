package alpaca

import (
	"context"
	"time"

	"golang.org/x/time/rate"
)

type RequestPriority int

const (
	PriorityTrading RequestPriority = iota
	PriorityBackground
)

func (p RequestPriority) String() string {
	switch p {
	case PriorityTrading:
		return "trading"
	case PriorityBackground:
		return "background"
	default:
		return "unknown"
	}
}

// RateLimiter wraps a golang rate.Limiter for Alpaca API requests.
type RateLimiter struct {
	globalLimiter     *rate.Limiter
	backgroundLimiter *rate.Limiter
}

// NewRateLimiter creates a new RateLimiter allowing maxPerMinute requests.
func NewRateLimiter(maxPerMinute int) *RateLimiter {
	return &RateLimiter{
		globalLimiter: rate.NewLimiter(rate.Every(time.Minute/time.Duration(maxPerMinute)), maxPerMinute),
	}
}

// NewPriorityRateLimiter creates a priority-aware limiter.
func NewPriorityRateLimiter(globalMaxPerMinute, backgroundMaxPerMinute int) *RateLimiter {
	r := &RateLimiter{
		globalLimiter: rate.NewLimiter(rate.Every(time.Minute/time.Duration(globalMaxPerMinute)), globalMaxPerMinute),
	}
	if backgroundMaxPerMinute > 0 {
		r.backgroundLimiter = rate.NewLimiter(rate.Every(time.Minute/time.Duration(backgroundMaxPerMinute)), backgroundMaxPerMinute)
	}
	return r
}

// LimitPerMin returns the configured global cap as a per-minute integer,
// rounded. Useful for tests that need to verify a limiter override
// threaded through correctly.
func (r *RateLimiter) LimitPerMin() int {
	if r == nil || r.globalLimiter == nil {
		return 0
	}
	return int(float64(r.globalLimiter.Limit()) * 60.0)
}

// BackgroundLimitPerMin returns the configured background cap as a
// per-minute integer. Returns 0 when no background limiter is set
// (which means PriorityBackground calls only wait on the global cap).
func (r *RateLimiter) BackgroundLimitPerMin() int {
	if r == nil || r.backgroundLimiter == nil {
		return 0
	}
	return int(float64(r.backgroundLimiter.Limit()) * 60.0)
}

// Wait blocks until the rate limiter permits an event to happen or ctx is canceled.
func (r *RateLimiter) Wait(ctx context.Context) error {
	return r.WaitWithPriority(ctx, PriorityTrading)
}

func (r *RateLimiter) WaitWithPriority(ctx context.Context, priority RequestPriority) error {
	if r == nil {
		return nil
	}
	if r.globalLimiter != nil {
		if err := r.globalLimiter.Wait(ctx); err != nil {
			return err
		}
	}
	if priority == PriorityBackground && r.backgroundLimiter != nil {
		if err := r.backgroundLimiter.Wait(ctx); err != nil {
			return err
		}
	}
	return nil
}
