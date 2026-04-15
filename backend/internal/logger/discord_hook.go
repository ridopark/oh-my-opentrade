// Package logger: Discord sink — routes ErrorLevel (and Fatal/Panic) log
// events to a NotifierPort so operators see every error as a Discord alert.
//
// The sink is attached as a zerolog writer (not a zerolog.Hook) because the
// Hook interface only exposes the Msg() string — structured fields such as
// Err(err) are discarded, which leaves Discord messages like "ibkr:
// reconnect failed" with no actual error text. Wiring as a Writer gives us
// the full JSON event and lets us render every field.
//
// The notifier is wired in post-construction via SetNotifier because the
// Discord sink is built later in service startup (after config +
// multi-notifier assembly). Before the notifier is wired, the writer
// silently no-ops — earliest-boot errors still hit stderr via zerolog's
// normal path.
package logger

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// DiscordHook forwards ErrorLevel and above to a NotifierPort asynchronously.
// It implements io.Writer so zerolog will hand it every serialized event.
type DiscordHook struct {
	notifier atomic.Pointer[notifierHolder]
	tenantID string
}

type notifierHolder struct{ n ports.NotifierPort }

// NewDiscordHook constructs a sink with no notifier wired. Call SetNotifier
// once the Discord notifier is available.
func NewDiscordHook(tenantID string) *DiscordHook {
	return &DiscordHook{tenantID: tenantID}
}

// SetNotifier wires the Discord notifier post-construction. Safe to call
// from any goroutine.
func (h *DiscordHook) SetNotifier(n ports.NotifierPort) {
	h.notifier.Store(&notifierHolder{n: n})
}

// Write implements io.Writer. Zerolog invokes it with the serialized JSON
// event for every log line. We filter to ErrorLevel and above, format all
// structured fields (including `error`) into a readable message, and send
// asynchronously so the logging goroutine never blocks on a webhook call.
// DiscordNotifier has its own cooldown/retry — no extra dedup needed here.
func (h *DiscordHook) Write(p []byte) (int, error) {
	n := len(p)
	holder := h.notifier.Load()
	if holder == nil || holder.n == nil {
		return n, nil
	}
	var evt map[string]any
	if err := json.Unmarshal(p, &evt); err != nil {
		return n, nil
	}
	levelStr, _ := evt["level"].(string)
	lvl, err := zerolog.ParseLevel(levelStr)
	if err != nil || lvl < zerolog.ErrorLevel {
		return n, nil
	}
	msg := formatEvent(evt)
	notifier := holder.n
	tenant := h.tenantID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = notifier.Notify(ctx, tenant, msg)
	}()
	return n, nil
}

// formatEvent renders a zerolog JSON event map as a single Discord-friendly
// string. Message comes first, then `error` (if present, the field zerolog
// uses for Err()), then remaining structured fields sorted alphabetically.
// Reserved metadata (level, time, message) are omitted.
func formatEvent(evt map[string]any) string {
	var sb strings.Builder
	if level, ok := evt["level"].(string); ok && level != "" {
		sb.WriteString("[")
		sb.WriteString(strings.ToUpper(level))
		sb.WriteString("] ")
	}
	if msg, ok := evt["message"].(string); ok && msg != "" {
		sb.WriteString(msg)
	}
	if errStr, ok := evt["error"].(string); ok && errStr != "" {
		sb.WriteString(" | error=")
		sb.WriteString(errStr)
	}
	keys := make([]string, 0, len(evt))
	for k := range evt {
		switch k {
		case "level", "time", "message", "error":
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&sb, " %s=%v", k, evt[k])
	}
	return sb.String()
}
