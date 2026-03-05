package alpaca

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateLimiter_AllowsBurst(t *testing.T) {
	// Arrange
	limiter := NewRateLimiter(5)
	ctx := context.Background()

	// Act & Assert
	for i := 0; i < 5; i++ {
		err := limiter.Wait(ctx)
		require.NoError(t, err)
	}
}

func TestRateLimiter_WaitsWhenExhausted(t *testing.T) {
	// Arrange
	limiter := NewRateLimiter(60) // 1 per second
	ctx := context.Background()

	// Act
	// First 60 tokens should be available immediately due to burst
	for i := 0; i < 60; i++ {
		err := limiter.Wait(ctx)
		require.NoError(t, err)
	}

	start := time.Now()
	// Next token should block for ~1 second
	err := limiter.Wait(ctx)
	require.NoError(t, err)

	// Assert
	duration := time.Since(start)
	assert.GreaterOrEqual(t, duration.Milliseconds(), int64(900), "should wait when exhausted")
}

func TestRateLimiter_ContextCancellation(t *testing.T) {
	// Arrange
	limiter := NewRateLimiter(60) // 1 per second
	ctxBg := context.Background()

	// Consume all 60 burst tokens
	for i := 0; i < 60; i++ {
		err := limiter.Wait(ctxBg)
		require.NoError(t, err)
	}
	// Create a context that is already canceled
	ctx, cancel := context.WithCancel(ctxBg)
	cancel()

	// Act
	err := limiter.Wait(ctx)

	// Assert
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRateLimiter_ConcurrentSafe(t *testing.T) {
	// Arrange
	limiter := NewRateLimiter(1000)
	ctx := context.Background()
	var wg sync.WaitGroup

	// Act & Assert
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := limiter.Wait(ctx)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
}

func TestRateLimiter_WaitWithPriority_TradingUsesGlobalOnly(t *testing.T) {
	limiter := NewPriorityRateLimiter(60, 1)
	ctx := context.Background()

	for i := 0; i < 60; i++ {
		require.NoError(t, limiter.WaitWithPriority(ctx, PriorityTrading))
	}

	start := time.Now()
	require.NoError(t, limiter.WaitWithPriority(ctx, PriorityTrading))
	d := time.Since(start)
	assert.GreaterOrEqual(t, d.Milliseconds(), int64(900))
}

func TestRateLimiter_WaitWithPriority_BackgroundUsesBoth(t *testing.T) {
	limiter := NewPriorityRateLimiter(1000, 60)
	ctx := context.Background()

	for i := 0; i < 60; i++ {
		require.NoError(t, limiter.WaitWithPriority(ctx, PriorityBackground))
	}

	start := time.Now()
	require.NoError(t, limiter.WaitWithPriority(ctx, PriorityBackground))
	d := time.Since(start)
	assert.GreaterOrEqual(t, d.Milliseconds(), int64(900))
}

func TestRateLimiter_BackgroundThrottledMoreThanTrading(t *testing.T) {
	limiter := NewPriorityRateLimiter(1000, 60)
	ctx := context.Background()

	for i := 0; i < 60; i++ {
		require.NoError(t, limiter.WaitWithPriority(ctx, PriorityBackground))
	}

	startTrading := time.Now()
	require.NoError(t, limiter.WaitWithPriority(ctx, PriorityTrading))
	tradingDur := time.Since(startTrading)

	startBg := time.Now()
	require.NoError(t, limiter.WaitWithPriority(ctx, PriorityBackground))
	bgDur := time.Since(startBg)

	assert.Less(t, tradingDur.Milliseconds(), int64(200))
	assert.GreaterOrEqual(t, bgDur.Milliseconds(), int64(900))
}
