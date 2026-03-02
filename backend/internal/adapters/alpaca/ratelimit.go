package alpaca

import (
	"context"
	"time"

	"golang.org/x/time/rate"
)

// RateLimiter wraps a golang rate.Limiter for Alpaca API requests.
type RateLimiter struct {
	limiter *rate.Limiter
}

// NewRateLimiter creates a new RateLimiter allowing maxPerMinute requests.
func NewRateLimiter(maxPerMinute int) *RateLimiter {
	return &RateLimiter{
		limiter: rate.NewLimiter(rate.Every(time.Minute/time.Duration(maxPerMinute)), maxPerMinute),
	}
}

// Wait blocks until the rate limiter permits an event to happen or ctx is canceled.
func (r *RateLimiter) Wait(ctx context.Context) error {
	return r.limiter.Wait(ctx)
}
