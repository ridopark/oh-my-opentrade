package risk_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingSink captures transitions for assertion and is safe for concurrent use.
type recordingSink struct {
	mu          sync.Mutex
	transitions []risk.KillSwitchTransition
}

func (s *recordingSink) RecordTransition(_ context.Context, t risk.KillSwitchTransition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transitions = append(s.transitions, t)
	return nil
}

func (s *recordingSink) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.transitions)
}

func newBreaker(t *testing.T) *risk.DailyLossBreaker {
	t.Helper()
	src := &stubPnLSource{pnl: 0}
	return risk.NewDailyLossBreaker(0, 0, src, time.Now, zerolog.Nop())
}

func TestKillSwitchState_String(t *testing.T) {
	assert.Equal(t, "ACTIVE", risk.KillSwitchActive.String())
	assert.Equal(t, "REDUCING", risk.KillSwitchReducing.String())
	assert.Equal(t, "HALTED", risk.KillSwitchHalted.String())
	assert.Equal(t, "UNKNOWN", risk.KillSwitchState(99).String())
}

func TestParseKillSwitchState(t *testing.T) {
	cases := []struct {
		in   string
		want risk.KillSwitchState
		err  bool
	}{
		{"ACTIVE", risk.KillSwitchActive, false},
		{"reducing", risk.KillSwitchReducing, false},
		{"Halted", risk.KillSwitchHalted, false},
		{"", risk.KillSwitchActive, true},
		{"nope", risk.KillSwitchActive, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := risk.ParseKillSwitchState(c.in)
			if c.err {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestDailyLossBreaker_InitialStateIsActive(t *testing.T) {
	b := newBreaker(t)
	assert.Equal(t, risk.KillSwitchActive, b.State())
}

func TestDailyLossBreaker_SetStateTransitions(t *testing.T) {
	b := newBreaker(t)
	sink := &recordingSink{}
	b.SetSink(sink)

	prev := b.SetState(risk.KillSwitchReducing, "operator", "alice")
	assert.Equal(t, risk.KillSwitchActive, prev)
	assert.Equal(t, risk.KillSwitchReducing, b.State())
	assert.Equal(t, 1, sink.Len())

	prev = b.SetState(risk.KillSwitchHalted, "escalate", "alice")
	assert.Equal(t, risk.KillSwitchReducing, prev)
	assert.Equal(t, risk.KillSwitchHalted, b.State())
	assert.Equal(t, 2, sink.Len())
}

func TestDailyLossBreaker_SetStateNoOpWhenUnchanged(t *testing.T) {
	b := newBreaker(t)
	sink := &recordingSink{}
	b.SetSink(sink)

	b.SetState(risk.KillSwitchReducing, "first", "op")
	assert.Equal(t, 1, sink.Len())

	prev := b.SetState(risk.KillSwitchReducing, "second", "op")
	assert.Equal(t, risk.KillSwitchReducing, prev)
	assert.Equal(t, 1, sink.Len(), "repeated SetState to same value must not emit")
}

func TestDailyLossBreaker_SetStateConcurrent(t *testing.T) {
	b := newBreaker(t)
	sink := &recordingSink{}
	b.SetSink(sink)

	const goroutines = 16
	const perGoroutine = 50
	var wg sync.WaitGroup
	var observed atomic.Int32

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				var next risk.KillSwitchState
				switch (i + j) % 3 {
				case 0:
					next = risk.KillSwitchActive
				case 1:
					next = risk.KillSwitchReducing
				case 2:
					next = risk.KillSwitchHalted
				}
				b.SetState(next, "concurrent", "test")
				_ = b.State()
				observed.Add(1)
			}
		}(i)
	}
	wg.Wait()

	// No crashes, no data races (run with -race). Final state is one of the
	// valid enum values.
	final := b.State()
	assert.Contains(t,
		[]risk.KillSwitchState{risk.KillSwitchActive, risk.KillSwitchReducing, risk.KillSwitchHalted},
		final,
	)
	// Every recorded transition had distinct old/new values.
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, tr := range sink.transitions {
		assert.NotEqual(t, tr.OldState, tr.NewState, "sink should never record no-op transitions")
	}
}

func TestDailyLossBreaker_SetStateSinkErrorNonFatal(t *testing.T) {
	// A sink that returns an error must not prevent the state transition.
	b := newBreaker(t)
	b.SetSink(errSink{})
	b.SetState(risk.KillSwitchHalted, "err-sink", "system")
	assert.Equal(t, risk.KillSwitchHalted, b.State())
}

type errSink struct{}

func (errSink) RecordTransition(_ context.Context, _ risk.KillSwitchTransition) error {
	return assert.AnError
}

func TestDailyLossBreaker_ResetRestoresActive(t *testing.T) {
	b := newBreaker(t)
	b.SetState(risk.KillSwitchHalted, "trip", "system")
	assert.Equal(t, risk.KillSwitchHalted, b.State())

	b.Reset()
	assert.Equal(t, risk.KillSwitchActive, b.State())
}
