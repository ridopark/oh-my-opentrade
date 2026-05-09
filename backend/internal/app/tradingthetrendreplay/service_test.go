package tradingthetrendreplay

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
	if err := bus.Subscribe(context.Background(), domain.EventTradingTheTrendSignalReceived, func(_ context.Context, ev domain.Event) error {
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
		// Two parseable signals on the same message.
		{ID: "m1", Author: "TradingTheTrend", TS: "2026-02-09T14:20:52Z", Text: "RKLB 90c > 88.00\nMSFT 425c > 423.00"},
		// Commentary-only message, no entry grammar matches.
		{ID: "m2", Author: "TradingTheTrend", TS: "2026-02-10T14:30:00Z", Text: "Good luck @everyone"},
		// One signal arriving later.
		{ID: "m3", Author: "TradingTheTrend", TS: "2026-02-11T14:00:00Z", Text: "TSLA 425p > 421.00"},
	})

	svc, _, captured := newTestService(t)
	stats, err := svc.Load(path, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.MessagesRead != 3 {
		t.Errorf("messages_read=%d want 3", stats.MessagesRead)
	}
	if stats.MessagesDropped != 1 {
		t.Errorf("messages_dropped=%d want 1", stats.MessagesDropped)
	}
	if stats.SignalsLoaded != 3 {
		t.Errorf("signals_loaded=%d want 3", stats.SignalsLoaded)
	}
	if svc.Pending() != 3 {
		t.Fatalf("pending=%d want 3", svc.Pending())
	}

	// Advance only past the first message.
	n, err := svc.AdvanceTo(context.Background(), time.Date(2026, 2, 9, 15, 0, 0, 0, time.UTC))
	if err != nil || n != 2 {
		t.Fatalf("first advance n=%d err=%v", n, err)
	}
	if svc.Pending() != 1 {
		t.Fatalf("pending after first advance=%d", svc.Pending())
	}

	// Advance past everything.
	n, err = svc.AdvanceTo(context.Background(), time.Date(2026, 2, 12, 0, 0, 0, 0, time.UTC))
	if err != nil || n != 1 {
		t.Fatalf("second advance n=%d err=%v", n, err)
	}
	if svc.Pending() != 0 {
		t.Fatalf("pending after second advance=%d", svc.Pending())
	}

	if len(*captured) != 3 {
		t.Fatalf("published=%d want 3", len(*captured))
	}
	first := (*captured)[0].Payload.(domain.TradingTheTrendSignalPayload)
	if first.Ticker != "RKLB" || first.Strike != 90 || first.Right != domain.OptionRightCall || first.Trigger != 88 {
		t.Errorf("first payload = %+v", first)
	}
	last := (*captured)[2].Payload.(domain.TradingTheTrendSignalPayload)
	if last.Ticker != "TSLA" || last.Right != domain.OptionRightPut {
		t.Errorf("last payload = %+v", last)
	}
}

func TestService_FromToFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	writeJSONL(t, path, []historyMessage{
		{ID: "early", Author: "a", TS: "2026-01-01T00:00:00Z", Text: "AAPL 200c > 198.00"},
		{ID: "mid", Author: "a", TS: "2026-02-15T00:00:00Z", Text: "AAPL 200c > 198.00"},
		{ID: "late", Author: "a", TS: "2026-03-01T00:00:00Z", Text: "AAPL 200c > 198.00"},
	})

	svc, _, _ := newTestService(t)
	from := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	stats, err := svc.Load(path, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if stats.SignalsLoaded != 1 {
		t.Errorf("expected only the mid-range signal, got SignalsLoaded=%d", stats.SignalsLoaded)
	}
}

func TestService_IdempotentAdvanceTo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	writeJSONL(t, path, []historyMessage{
		{ID: "m1", Author: "a", TS: "2026-02-09T14:20:52Z", Text: "RKLB 90c > 88.00"},
	})
	svc, _, captured := newTestService(t)
	if _, err := svc.Load(path, time.Time{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	tick := time.Date(2026, 2, 9, 15, 0, 0, 0, time.UTC)

	n, _ := svc.AdvanceTo(context.Background(), tick)
	if n != 1 {
		t.Fatalf("first n=%d", n)
	}
	n, _ = svc.AdvanceTo(context.Background(), tick.Add(time.Hour))
	if n != 0 {
		t.Errorf("second AdvanceTo must publish nothing once cursor drains, got n=%d", n)
	}
	if len(*captured) != 1 {
		t.Errorf("captured=%d want 1 (no double-fire)", len(*captured))
	}
}

func TestService_SortByPostedAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	// Out-of-order in file, must sort to TS ascending.
	writeJSONL(t, path, []historyMessage{
		{ID: "later", Author: "a", TS: "2026-02-10T14:00:00Z", Text: "MSFT 425c > 423.00"},
		{ID: "earlier", Author: "a", TS: "2026-02-09T14:00:00Z", Text: "RKLB 90c > 88.00"},
	})
	svc, _, captured := newTestService(t)
	if _, err := svc.Load(path, time.Time{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AdvanceTo(context.Background(), time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if len(*captured) != 2 {
		t.Fatalf("captured=%d", len(*captured))
	}
	first := (*captured)[0].Payload.(domain.TradingTheTrendSignalPayload)
	if first.Ticker != "RKLB" {
		t.Errorf("expected earlier RKLB first, got %s", first.Ticker)
	}
}
