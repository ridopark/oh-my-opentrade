package pipeline

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/ports"
)

// NullNotifier is a no-op implementation of ports.NotifierPort and
// ports.ImageNotifierPort, used for ModeBacktest and ModeReplay where
// emitting Telegram/Discord/Kakao notifications would be the wrong
// behavior — operators don't want backtest sweeps lighting up their
// real-time alert channels. The codebase previously relied on the
// implicit policy "non-live paths don't call SetNotifier"; injecting
// NullNotifier makes the policy explicit at the wiring site.
type NullNotifier struct{}

func (NullNotifier) Notify(_ context.Context, _ string, _ string) error {
	return nil
}

func (NullNotifier) NotifyWithImage(_ context.Context, _, _ string, _ ports.Attachment) error {
	return nil
}
