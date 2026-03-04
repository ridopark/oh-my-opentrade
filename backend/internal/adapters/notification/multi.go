package notification

import (
	"context"
	"errors"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/ports"
)

// MultiNotifier fans out Notify calls to multiple NotifierPort implementations.
// All notifiers are called regardless of individual failures; any errors are joined.
type MultiNotifier struct {
	notifiers []ports.NotifierPort
}

// NewMultiNotifier creates a new MultiNotifier wrapping the given notifiers.
func NewMultiNotifier(notifiers ...ports.NotifierPort) *MultiNotifier {
	return &MultiNotifier{
		notifiers: notifiers,
	}
}

// Notify calls all underlying notifiers, collecting any errors.
// Returns a joined error if one or more notifiers fail; nil if all succeed.
func (m *MultiNotifier) Notify(ctx context.Context, tenantID, message string) error {
	var errs []error
	for i, n := range m.notifiers {
		if err := n.Notify(ctx, tenantID, message); err != nil {
			errs = append(errs, fmt.Errorf("notifier[%d]: %w", i, err))
		}
	}
	return errors.Join(errs...)
}
