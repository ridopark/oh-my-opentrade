package copytradereplay

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

func writeJSONL(t *testing.T, path string, msgs []historyMessage) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			t.Fatal(err)
		}
	}
}

func newTestService(t *testing.T) (*Service, *memory.Bus, *[]domain.Event) {
	t.Helper()
	bus := memory.NewSyncBus()
	var captured []domain.Event
	if err := bus.Subscribe(context.Background(), domain.EventCopytradeSignalReceived, func(_ context.Context, ev domain.Event) error {
		captured = append(captured, ev)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc := New(bus, "default", domain.EnvModePaper, zerolog.Nop())
	return svc, bus, &captured
}

func TestService_LoadAndDrain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	writeJSONL(t, path, []historyMessage{
		{ID: "m1", Author: "a", TS: "2026-02-01T15:20:00Z", Text: "BTO AMZN 2/02 245c @ 2.18"},
		{ID: "m2", Author: "a", TS: "2026-02-01T15:30:00Z", Text: "stop @ entry"},
		{ID: "m3", Author: "a", TS: "2026-02-01T15:45:00Z", Text: "STC AMZN 2/02 245c @ 2.50 partial"},
	})

	svc, _, captured := newTestService(t)
	stats, err := svc.Load(path, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.SignalsLoaded != 2 || stats.MessagesDropped != 1 || stats.MessagesRead != 3 {
		t.Fatalf("stats: %+v", stats)
	}
	if svc.Pending() != 2 {
		t.Fatalf("pending=%d", svc.Pending())
	}

	n, err := svc.AdvanceTo(context.Background(), time.Date(2026, 2, 1, 15, 20, 0, 0, time.UTC))
	if err != nil || n != 1 {
		t.Fatalf("first advance n=%d err=%v", n, err)
	}
	if svc.Pending() != 1 {
		t.Fatalf("pending after first advance=%d", svc.Pending())
	}

	n, err = svc.AdvanceTo(context.Background(), time.Date(2026, 2, 1, 16, 0, 0, 0, time.UTC))
	if err != nil || n != 1 {
		t.Fatalf("second advance n=%d err=%v", n, err)
	}
	if svc.Pending() != 0 {
		t.Fatalf("pending after second advance=%d", svc.Pending())
	}

	if len(*captured) != 2 {
		t.Fatalf("published=%d", len(*captured))
	}
	got := (*captured)[0].Payload.(domain.CopytradeSignalPayload)
	if got.Action != domain.CopytradeActionBTO || got.Ticker != "AMZN" {
		t.Errorf("first event payload = %+v", got)
	}
	if (*captured)[0].OccurredAt != time.Date(2026, 2, 1, 15, 20, 0, 0, time.UTC) {
		t.Errorf("OccurredAt = %v, want tick time", (*captured)[0].OccurredAt)
	}
}

func TestService_SameMinuteBTOBeforeSTC(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	// Intentionally write STC before BTO in file order, same PostedAt.
	writeJSONL(t, path, []historyMessage{
		{ID: "m1", Author: "a", TS: "2026-02-01T15:20:00Z", Text: "STC AMZN 2/02 245c @ 2.50"},
		{ID: "m2", Author: "a", TS: "2026-02-01T15:20:00Z", Text: "BTO AMZN 2/02 245c @ 2.18"},
	})

	svc, _, captured := newTestService(t)
	if _, err := svc.Load(path, time.Time{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AdvanceTo(context.Background(), time.Date(2026, 2, 1, 15, 21, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if len(*captured) != 2 {
		t.Fatalf("published=%d", len(*captured))
	}
	first := (*captured)[0].Payload.(domain.CopytradeSignalPayload)
	second := (*captured)[1].Payload.(domain.CopytradeSignalPayload)
	if first.Action != domain.CopytradeActionBTO {
		t.Errorf("first action = %s, want BTO", first.Action)
	}
	if second.Action != domain.CopytradeActionSTC {
		t.Errorf("second action = %s, want STC", second.Action)
	}
}

func TestService_FromToFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	writeJSONL(t, path, []historyMessage{
		{ID: "m1", Author: "a", TS: "2026-01-15T15:20:00Z", Text: "BTO AMZN 2/02 245c @ 2.18"},
		{ID: "m2", Author: "a", TS: "2026-02-15T15:20:00Z", Text: "BTO AMZN 3/02 245c @ 2.18"},
		{ID: "m3", Author: "a", TS: "2026-03-15T15:20:00Z", Text: "BTO AMZN 4/02 245c @ 2.18"},
	})

	svc, _, _ := newTestService(t)
	from := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	stats, err := svc.Load(path, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if stats.SignalsLoaded != 1 {
		t.Errorf("signals_loaded = %d, want 1", stats.SignalsLoaded)
	}
}

func TestService_AdvanceToEmptyQueue(t *testing.T) {
	svc, _, _ := newTestService(t)
	n, err := svc.AdvanceTo(context.Background(), time.Now())
	if err != nil || n != 0 {
		t.Errorf("n=%d err=%v", n, err)
	}
}

func TestService_IdempotentAdvanceTo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	writeJSONL(t, path, []historyMessage{
		{ID: "m1", Author: "a", TS: "2026-02-01T15:20:00Z", Text: "BTO AMZN 2/02 245c @ 2.18"},
	})
	svc, _, captured := newTestService(t)
	if _, err := svc.Load(path, time.Time{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	tick := time.Date(2026, 2, 1, 16, 0, 0, 0, time.UTC)
	if _, err := svc.AdvanceTo(context.Background(), tick); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AdvanceTo(context.Background(), tick); err != nil {
		t.Fatal(err)
	}
	if len(*captured) != 1 {
		t.Errorf("published %d events, want 1 (idempotent)", len(*captured))
	}
}
