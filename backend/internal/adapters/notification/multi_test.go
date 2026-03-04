package notification_test

import (
	"context"
	"errors"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/adapters/notification"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// stubNotifier is a test double implementing ports.NotifierPort.
type stubNotifier struct {
	called   bool
	tenantID string
	message  string
	err      error
}

func (s *stubNotifier) Notify(_ context.Context, tenantID, message string) error {
	s.called = true
	s.tenantID = tenantID
	s.message = message
	return s.err
}

var _ ports.NotifierPort = (*stubNotifier)(nil)

func TestMultiNotifier_CallsAllNotifiers(t *testing.T) {
	a := &stubNotifier{}
	b := &stubNotifier{}
	m := notification.NewMultiNotifier(a, b)

	err := m.Notify(context.Background(), "tenant-X", "msg")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !a.called {
		t.Error("expected notifier A to be called")
	}
	if !b.called {
		t.Error("expected notifier B to be called")
	}
}

func TestMultiNotifier_ForwardsMessageAndTenantID(t *testing.T) {
	a := &stubNotifier{}
	m := notification.NewMultiNotifier(a)

	_ = m.Notify(context.Background(), "tenant-Y", "hello world")

	if a.tenantID != "tenant-Y" {
		t.Errorf("expected tenantID=tenant-Y, got: %s", a.tenantID)
	}
	if a.message != "hello world" {
		t.Errorf("expected message=hello world, got: %s", a.message)
	}
}

func TestMultiNotifier_ReturnsErrorIfOneFails(t *testing.T) {
	errA := errors.New("notifier A failed")
	a := &stubNotifier{err: errA}
	b := &stubNotifier{}
	m := notification.NewMultiNotifier(a, b)

	err := m.Notify(context.Background(), "t", "m")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errA) {
		t.Errorf("expected error to wrap errA, got: %v", err)
	}
	// B must still be called even though A failed
	if !b.called {
		t.Error("expected notifier B to be called even though A failed")
	}
}

func TestMultiNotifier_ReturnsJoinedErrorIfBothFail(t *testing.T) {
	errA := errors.New("A failed")
	errB := errors.New("B failed")
	a := &stubNotifier{err: errA}
	b := &stubNotifier{err: errB}
	m := notification.NewMultiNotifier(a, b)

	err := m.Notify(context.Background(), "t", "m")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errA) {
		t.Errorf("expected joined error to contain errA, got: %v", err)
	}
	if !errors.Is(err, errB) {
		t.Errorf("expected joined error to contain errB, got: %v", err)
	}
}

func TestMultiNotifier_NoNotifiers_ReturnsNil(t *testing.T) {
	m := notification.NewMultiNotifier()
	err := m.Notify(context.Background(), "t", "m")
	if err != nil {
		t.Errorf("expected nil for empty notifier list, got: %v", err)
	}
}

func TestMultiNotifier_ImplementsNotifierPort(t *testing.T) {
	var _ ports.NotifierPort = notification.NewMultiNotifier()
}
