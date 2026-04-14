package logger_test

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

type fakeNotifier struct {
	mu    sync.Mutex
	calls []string
	count atomic.Int32
}

func (f *fakeNotifier) Notify(_ context.Context, _, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, message)
	f.count.Add(1)
	return nil
}

func (f *fakeNotifier) wait(target int32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f.count.Load() >= target {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func newTestLogger(h *logger.DiscordHook) zerolog.Logger {
	return zerolog.New(io.Discard).Hook(h)
}

func TestDiscordHook_FiresOnError(t *testing.T) {
	hook := logger.NewDiscordHook("test")
	n := &fakeNotifier{}
	hook.SetNotifier(n)

	log := newTestLogger(hook)
	log.Error().Msg("boom")

	assert.True(t, n.wait(1, time.Second), "expected hook to deliver 1 message")
	n.mu.Lock()
	defer n.mu.Unlock()
	assert.Equal(t, []string{"boom"}, n.calls)
}

func TestDiscordHook_IgnoresBelowError(t *testing.T) {
	hook := logger.NewDiscordHook("test")
	n := &fakeNotifier{}
	hook.SetNotifier(n)

	log := newTestLogger(hook)
	log.Info().Msg("ignored")
	log.Warn().Msg("ignored")
	log.Debug().Msg("ignored")

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), n.count.Load(), "non-error levels must not trigger notifier")
}

func TestDiscordHook_NoopBeforeNotifierWired(t *testing.T) {
	// Earliest-boot logs must not panic when the notifier has not yet
	// been wired via SetNotifier.
	hook := logger.NewDiscordHook("test")
	log := newTestLogger(hook)
	log.Error().Msg("no notifier yet — must not panic")
}
