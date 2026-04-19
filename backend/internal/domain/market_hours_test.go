package domain_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

func parseET(t *testing.T, iso string) time.Time {
	t.Helper()
	et, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load ET: %v", err)
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04", iso, et)
	if err != nil {
		t.Fatalf("parse %q: %v", iso, err)
	}
	return parsed
}

func TestIsEquityMarketOpen_WeekdayRTH(t *testing.T) {
	// Wednesday 2026-04-15 at 10:30 ET — mid-session.
	got := domain.IsEquityMarketOpen(parseET(t, "2026-04-15 10:30"))
	if !got {
		t.Errorf("expected market open mid-session Wed 10:30 ET, got closed")
	}
}

func TestIsEquityMarketOpen_Weekend(t *testing.T) {
	// Saturday 2026-04-18 at 12:00 ET.
	got := domain.IsEquityMarketOpen(parseET(t, "2026-04-18 12:00"))
	if got {
		t.Errorf("expected market closed Saturday, got open")
	}
}

func TestIsEquityMarketOpen_BeforeOpen(t *testing.T) {
	// Monday 09:29 ET — one minute before the bell.
	got := domain.IsEquityMarketOpen(parseET(t, "2026-04-13 09:29"))
	if got {
		t.Errorf("expected market closed 09:29 Mon, got open")
	}
}

func TestIsEquityMarketOpen_AtOpen(t *testing.T) {
	// Monday 09:30 ET — the bell itself is inside the session.
	got := domain.IsEquityMarketOpen(parseET(t, "2026-04-13 09:30"))
	if !got {
		t.Errorf("expected market open at 09:30 sharp, got closed")
	}
}

func TestIsEquityMarketOpen_AtClose(t *testing.T) {
	// Monday 16:00 ET — the closing bell itself is OUTSIDE the window
	// (boundary is [09:30, 16:00)).
	got := domain.IsEquityMarketOpen(parseET(t, "2026-04-13 16:00"))
	if got {
		t.Errorf("expected market closed at 16:00 sharp, got open")
	}
}

func TestIsEquityMarketOpen_AfterHours(t *testing.T) {
	// Monday 17:00 ET.
	got := domain.IsEquityMarketOpen(parseET(t, "2026-04-13 17:00"))
	if got {
		t.Errorf("expected market closed 17:00 Mon, got open")
	}
}

func TestIsEquityMarketOpen_UTCInputIsConvertedToET(t *testing.T) {
	// 14:30 UTC on a weekday in April (EDT, UTC-4) = 10:30 ET — session open.
	utc := time.Date(2026, 4, 15, 14, 30, 0, 0, time.UTC)
	if !domain.IsEquityMarketOpen(utc) {
		t.Errorf("expected market open for 14:30 UTC (= 10:30 ET), got closed")
	}
}
