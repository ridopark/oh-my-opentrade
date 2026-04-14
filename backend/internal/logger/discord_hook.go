// Package logger: Discord hook — routes ErrorLevel (and Fatal/Panic) log
// events to a NotifierPort so operators see every error as a Discord alert.
//
// The hook is attached to the root logger at construction time. The notifier
// is wired in post-construction via SetNotifier because the Discord sink is
// built later in service startup (after config + multi-notifier assembly).
// Before the notifier is wired, the hook silently no-ops — earliest-boot
// errors still hit stderr via zerolog's normal path.
package logger

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// DiscordHook forwards ErrorLevel and above to a NotifierPort asynchronously.
type DiscordHook struct {
	notifier atomic.Pointer[notifierHolder]
	tenantID string
}

type notifierHolder struct{ n ports.NotifierPort }

// NewDiscordHook constructs a hook with no notifier wired. Call SetNotifier
// once the Discord sink is available.
func NewDiscordHook(tenantID string) *DiscordHook {
	return &DiscordHook{tenantID: tenantID}
}

// SetNotifier wires the Discord sink post-construction. Safe to call from
// any goroutine.
func (h *DiscordHook) SetNotifier(n ports.NotifierPort) {
	h.notifier.Store(&notifierHolder{n: n})
}

// Run satisfies zerolog.Hook. Fires only for ErrorLevel and above, sends
// asynchronously so the logging goroutine never blocks on a webhook call.
// DiscordNotifier has its own cooldown/retry — no extra dedup needed here.
func (h *DiscordHook) Run(_ *zerolog.Event, level zerolog.Level, msg string) {
	if level < zerolog.ErrorLevel {
		return
	}
	holder := h.notifier.Load()
	if holder == nil || holder.n == nil {
		return
	}
	n := holder.n
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = n.Notify(ctx, h.tenantID, msg)
	}()
}
