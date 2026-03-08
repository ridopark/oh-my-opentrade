package notification

import (
	"context"
	"errors"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/ports"
)

var _ ports.ImageNotifierPort = (*MultiNotifier)(nil)

type MultiNotifier struct {
	notifiers []ports.NotifierPort
}

func NewMultiNotifier(notifiers ...ports.NotifierPort) *MultiNotifier {
	return &MultiNotifier{
		notifiers: notifiers,
	}
}

func (m *MultiNotifier) Notify(ctx context.Context, tenantID, message string) error {
	var errs []error
	for i, n := range m.notifiers {
		if err := n.Notify(ctx, tenantID, message); err != nil {
			errs = append(errs, fmt.Errorf("notifier[%d]: %w", i, err))
		}
	}
	return errors.Join(errs...)
}

func (m *MultiNotifier) NotifyWithImage(ctx context.Context, tenantID, message string, image ports.Attachment) error {
	var errs []error
	for i, n := range m.notifiers {
		if imgN, ok := n.(ports.ImageNotifierPort); ok {
			if err := imgN.NotifyWithImage(ctx, tenantID, message, image); err != nil {
				errs = append(errs, fmt.Errorf("notifier[%d]: %w", i, err))
			}
		} else {
			if err := n.Notify(ctx, tenantID, message); err != nil {
				errs = append(errs, fmt.Errorf("notifier[%d]: %w", i, err))
			}
		}
	}
	return errors.Join(errs...)
}
