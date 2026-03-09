package hooks_yaegi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrCircuitOpen   = errors.New("yaegi hook executor: circuit open")
	ErrHookTimeout   = errors.New("yaegi hook executor: timeout")
	ErrBlockedImport = errors.New("yaegi sandbox: blocked import")
)

type HookExecutor struct {
	fn HookFunc

	timeout     time.Duration
	maxFailures int
	cooldown    time.Duration

	mu           sync.Mutex
	consecutive  int
	circuitUntil time.Time

	totalCalls   uint64
	failures     uint64
	timeouts     uint64
	circuitTrips uint64
}

type ExecutorOption func(*HookExecutor)

func WithTimeout(d time.Duration) ExecutorOption {
	return func(e *HookExecutor) {
		if d > 0 {
			e.timeout = d
		}
	}
}

func WithMaxFailures(n int) ExecutorOption {
	return func(e *HookExecutor) {
		if n > 0 {
			e.maxFailures = n
		}
	}
}

func WithCooldown(d time.Duration) ExecutorOption {
	return func(e *HookExecutor) {
		if d > 0 {
			e.cooldown = d
		}
	}
}

func NewHookExecutor(fn HookFunc, opts ...ExecutorOption) *HookExecutor {
	e := &HookExecutor{
		fn:          fn,
		timeout:     100 * time.Millisecond,
		maxFailures: 3,
		cooldown:    30 * time.Second,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	return e
}

func (e *HookExecutor) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.consecutive = 0
	e.circuitUntil = time.Time{}
}

func (e *HookExecutor) Execute(ctx context.Context, params map[string]any, bar map[string]any) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	atomic.AddUint64(&e.totalCalls, 1)

	if err := e.checkCircuit(); err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	type result struct {
		out map[string]any
		err error
	}
	resCh := make(chan result, 1)

	go func() {
		out, err := e.fn(params, bar)
		resCh <- result{out: out, err: err}
	}()

	select {
	case <-callCtx.Done():
		atomic.AddUint64(&e.timeouts, 1)
		e.recordFailure()
		return nil, fmt.Errorf("%w: %w", ErrHookTimeout, callCtx.Err())
	case r := <-resCh:
		if r.err != nil {
			atomic.AddUint64(&e.failures, 1)
			e.recordFailure()
			return nil, r.err
		}
		e.recordSuccess()
		return r.out, nil
	}
}

func (e *HookExecutor) checkCircuit() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.circuitUntil.IsZero() {
		return nil
	}
	if time.Now().After(e.circuitUntil) {
		e.consecutive = 0
		e.circuitUntil = time.Time{}
		return nil
	}
	return ErrCircuitOpen
}

func (e *HookExecutor) recordSuccess() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.consecutive = 0
}

func (e *HookExecutor) recordFailure() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.consecutive++
	if e.consecutive < e.maxFailures {
		return
	}
	if e.circuitUntil.IsZero() {
		atomic.AddUint64(&e.circuitTrips, 1)
	}
	e.circuitUntil = time.Now().Add(e.cooldown)
}

func (e *HookExecutor) TotalCalls() uint64   { return atomic.LoadUint64(&e.totalCalls) }
func (e *HookExecutor) Failures() uint64     { return atomic.LoadUint64(&e.failures) }
func (e *HookExecutor) Timeouts() uint64     { return atomic.LoadUint64(&e.timeouts) }
func (e *HookExecutor) CircuitTrips() uint64 { return atomic.LoadUint64(&e.circuitTrips) }
