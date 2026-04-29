package strategywatchdog

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type fakeNotifier struct {
	mu   sync.Mutex
	msgs []string
}

func (f *fakeNotifier) Notify(_ context.Context, _ string, m string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, m)
	return nil
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.msgs)
}

// rthNow returns a time that domain.CalendarFor(Equity).IsOpen reports as open.
// Tuesday 2026-04-21 10:00:00 ET — active RTH, post-open.
func rthNow() time.Time {
	loc := domain.NYLocation()
	return time.Date(2026, 4, 21, 10, 0, 0, 0, loc)
}

func TestTick_SkipOutsideRTH(t *testing.T) {
	loc := domain.NYLocation()
	saturday := time.Date(2026, 4, 25, 10, 0, 0, 0, loc) // Saturday
	nf := &fakeNotifier{}
	svc := New(Deps{
		ListStrategies: func() []WatchedStrategy {
			t.Fatal("should not be called outside RTH")
			return nil
		},
		LivenessFor: func(string) []domain.SymbolLiveness { return nil },
		Notifier:    nf,
		Log:         zerolog.Nop(),
	}, Config{})
	svc.Tick(context.Background(), saturday)
	require.Zero(t, nf.count())
}

func TestTick_FreshEval_NoAlert(t *testing.T) {
	now := rthNow()
	nf := &fakeNotifier{}
	svc := New(Deps{
		ListStrategies: func() []WatchedStrategy {
			return []WatchedStrategy{{ID: "avwap_v4", Active: true, Symbols: []string{"IWM"}}}
		},
		LivenessFor: func(string) []domain.SymbolLiveness {
			return []domain.SymbolLiveness{{Symbol: "IWM", LastEvalAt: now.Add(-30 * time.Second)}}
		},
		Notifier: nf,
		Log:      zerolog.Nop(),
	}, Config{})
	svc.Tick(context.Background(), now)
	require.Zero(t, nf.count(), "no alert expected for 30s stale")
}

func TestTick_WarnThreshold_NoNotify(t *testing.T) {
	now := rthNow()
	nf := &fakeNotifier{}
	svc := New(Deps{
		ListStrategies: func() []WatchedStrategy {
			return []WatchedStrategy{{ID: "avwap_v4", Active: true, Symbols: []string{"IWM"}}}
		},
		LivenessFor: func(string) []domain.SymbolLiveness {
			return []domain.SymbolLiveness{{Symbol: "IWM", LastEvalAt: now.Add(-7 * time.Minute)}}
		},
		Notifier: nf,
		Log:      zerolog.Nop(),
	}, Config{})
	svc.Tick(context.Background(), now)
	require.Zero(t, nf.count(), "WARN log only — no notification")
}

func TestTick_AlertThreshold_Notifies(t *testing.T) {
	now := rthNow()
	startAt := now.Add(-1 * time.Hour) // service was running before LastEvalAt
	nf := &fakeNotifier{}
	svc := New(Deps{
		ListStrategies: func() []WatchedStrategy {
			return []WatchedStrategy{{ID: "avwap_v4", Active: true, Symbols: []string{"IWM"}}}
		},
		LivenessFor: func(string) []domain.SymbolLiveness {
			return []domain.SymbolLiveness{{Symbol: "IWM", LastEvalAt: now.Add(-10 * time.Minute)}}
		},
		Notifier: nf,
		Log:      zerolog.Nop(),
		Now:      func() time.Time { return startAt },
	}, Config{})
	svc.Tick(context.Background(), now)
	require.Equal(t, 1, nf.count(), "alert should notify once")
	require.Contains(t, nf.msgs[0], "avwap_v4")
	require.Contains(t, nf.msgs[0], "IWM")
}

func TestTick_AlertDedupe(t *testing.T) {
	now := rthNow()
	startAt := now.Add(-1 * time.Hour)
	nf := &fakeNotifier{}
	staleAt := now.Add(-10 * time.Minute)
	svc := New(Deps{
		ListStrategies: func() []WatchedStrategy {
			return []WatchedStrategy{{ID: "avwap_v4", Active: true, Symbols: []string{"IWM"}}}
		},
		LivenessFor: func(string) []domain.SymbolLiveness {
			return []domain.SymbolLiveness{{Symbol: "IWM", LastEvalAt: staleAt}}
		},
		Notifier: nf,
		Log:      zerolog.Nop(),
		Now:      func() time.Time { return startAt },
	}, Config{DedupeWindow: 10 * time.Minute})
	svc.Tick(context.Background(), now)
	svc.Tick(context.Background(), now.Add(30*time.Second))
	svc.Tick(context.Background(), now.Add(60*time.Second))
	require.Equal(t, 1, nf.count(), "dedupe should suppress repeat alerts within window")

	svc.Tick(context.Background(), now.Add(11*time.Minute))
	require.Equal(t, 2, nf.count(), "alert fires again after dedupe window")
}

func TestTick_InactiveStrategy_Skipped(t *testing.T) {
	now := rthNow()
	nf := &fakeNotifier{}
	svc := New(Deps{
		ListStrategies: func() []WatchedStrategy {
			return []WatchedStrategy{{ID: "avwap_v4", Active: false, Symbols: []string{"IWM"}}}
		},
		LivenessFor: func(string) []domain.SymbolLiveness {
			return []domain.SymbolLiveness{{Symbol: "IWM", LastEvalAt: now.Add(-10 * time.Minute)}}
		},
		Notifier: nf,
		Log:      zerolog.Nop(),
	}, Config{})
	svc.Tick(context.Background(), now)
	require.Zero(t, nf.count(), "inactive strategies should be skipped")
}

func TestTick_LastEvalBeforeStart_NotAlerted(t *testing.T) {
	// LastEvalAt stamped by warmup replay with historical bar time (days ago)
	// must not trigger alerts — no live eval has occurred yet post-start.
	now := rthNow()
	startAt := now.Add(-2 * time.Minute) // service just started
	historicalWarmupBar := now.Add(-3 * 24 * time.Hour)
	nf := &fakeNotifier{}
	svc := New(Deps{
		ListStrategies: func() []WatchedStrategy {
			return []WatchedStrategy{{ID: "avwap_v4", Active: true, Symbols: []string{"IWM"}}}
		},
		LivenessFor: func(string) []domain.SymbolLiveness {
			return []domain.SymbolLiveness{{Symbol: "IWM", LastEvalAt: historicalWarmupBar}}
		},
		Notifier: nf,
		Log:      zerolog.Nop(),
		Now:      func() time.Time { return startAt },
	}, Config{})
	svc.Tick(context.Background(), now)
	require.Zero(t, nf.count(), "warmup-replayed LastEvalAt must not trigger alerts")
}

func TestTick_ZeroLastEval_NotAlerted(t *testing.T) {
	now := rthNow()
	nf := &fakeNotifier{}
	svc := New(Deps{
		ListStrategies: func() []WatchedStrategy {
			return []WatchedStrategy{{ID: "avwap_v4", Active: true, Symbols: []string{"IWM"}}}
		},
		LivenessFor: func(string) []domain.SymbolLiveness {
			return []domain.SymbolLiveness{{Symbol: "IWM"}} // LastEvalAt zero — never evaluated yet
		},
		Notifier: nf,
		Log:      zerolog.Nop(),
	}, Config{})
	svc.Tick(context.Background(), now)
	require.Zero(t, nf.count(), "zero LastEvalAt means warmup not complete — not an alert condition")
}
