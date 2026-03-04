package domain

import (
	"testing"
	"time"
)

func mustLoadNY(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("failed to load NY location: %v", err)
	}
	return loc
}

func TestPreviousRTHSession_PreMarketMonday_ReturnsFriday(t *testing.T) {
	loc := mustLoadNY(t)
	now := time.Date(2026, time.March, 2, 7, 0, 0, 0, loc)
	if now.Weekday() != time.Monday {
		t.Fatalf("expected Monday, got %s", now.Weekday())
	}

	start, end := PreviousRTHSession(now)

	expStart := time.Date(2026, time.February, 27, 9, 30, 0, 0, loc)
	expEnd := time.Date(2026, time.February, 27, 16, 0, 0, 0, loc)
	if expStart.Weekday() != time.Friday {
		t.Fatalf("expected Friday, got %s", expStart.Weekday())
	}

	if !start.Equal(expStart) {
		t.Fatalf("start: expected %v, got %v", expStart, start)
	}
	if !end.Equal(expEnd) {
		t.Fatalf("end: expected %v, got %v", expEnd, end)
	}
}

func TestPreviousRTHSession_PreMarketTuesday_ReturnsMonday(t *testing.T) {
	loc := mustLoadNY(t)
	now := time.Date(2026, time.March, 3, 7, 0, 0, 0, loc)
	if now.Weekday() != time.Tuesday {
		t.Fatalf("expected Tuesday, got %s", now.Weekday())
	}

	start, end := PreviousRTHSession(now)

	expStart := time.Date(2026, time.March, 2, 9, 30, 0, 0, loc)
	expEnd := time.Date(2026, time.March, 2, 16, 0, 0, 0, loc)
	if !start.Equal(expStart) {
		t.Fatalf("start: expected %v, got %v", expStart, start)
	}
	if !end.Equal(expEnd) {
		t.Fatalf("end: expected %v, got %v", expEnd, end)
	}
}

func TestPreviousRTHSession_DuringRTHWednesday11am_ReturnsTuesday(t *testing.T) {
	loc := mustLoadNY(t)
	now := time.Date(2026, time.March, 4, 11, 0, 0, 0, loc)
	if now.Weekday() != time.Wednesday {
		t.Fatalf("expected Wednesday, got %s", now.Weekday())
	}

	start, end := PreviousRTHSession(now)

	expStart := time.Date(2026, time.March, 3, 9, 30, 0, 0, loc)
	expEnd := time.Date(2026, time.March, 3, 16, 0, 0, 0, loc)
	if !start.Equal(expStart) {
		t.Fatalf("start: expected %v, got %v", expStart, start)
	}
	if !end.Equal(expEnd) {
		t.Fatalf("end: expected %v, got %v", expEnd, end)
	}
}

func TestPreviousRTHSession_AfterHoursWednesday6pm_ReturnsWednesday(t *testing.T) {
	loc := mustLoadNY(t)
	now := time.Date(2026, time.March, 4, 18, 0, 0, 0, loc)
	if now.Weekday() != time.Wednesday {
		t.Fatalf("expected Wednesday, got %s", now.Weekday())
	}

	start, end := PreviousRTHSession(now)

	expStart := time.Date(2026, time.March, 4, 9, 30, 0, 0, loc)
	expEnd := time.Date(2026, time.March, 4, 16, 0, 0, 0, loc)
	if !start.Equal(expStart) {
		t.Fatalf("start: expected %v, got %v", expStart, start)
	}
	if !end.Equal(expEnd) {
		t.Fatalf("end: expected %v, got %v", expEnd, end)
	}
}

func TestPreviousRTHSession_Saturday_ReturnsFriday(t *testing.T) {
	loc := mustLoadNY(t)
	now := time.Date(2026, time.March, 7, 12, 0, 0, 0, loc)
	if now.Weekday() != time.Saturday {
		t.Fatalf("expected Saturday, got %s", now.Weekday())
	}

	start, end := PreviousRTHSession(now)

	expStart := time.Date(2026, time.March, 6, 9, 30, 0, 0, loc)
	expEnd := time.Date(2026, time.March, 6, 16, 0, 0, 0, loc)
	if !start.Equal(expStart) {
		t.Fatalf("start: expected %v, got %v", expStart, start)
	}
	if !end.Equal(expEnd) {
		t.Fatalf("end: expected %v, got %v", expEnd, end)
	}
}

func TestPreviousRTHSession_Sunday_ReturnsFriday(t *testing.T) {
	loc := mustLoadNY(t)
	now := time.Date(2026, time.March, 8, 12, 0, 0, 0, loc)
	if now.Weekday() != time.Sunday {
		t.Fatalf("expected Sunday, got %s", now.Weekday())
	}

	start, end := PreviousRTHSession(now)

	expStart := time.Date(2026, time.March, 6, 9, 30, 0, 0, loc)
	expEnd := time.Date(2026, time.March, 6, 16, 0, 0, 0, loc)
	if !start.Equal(expStart) {
		t.Fatalf("start: expected %v, got %v", expStart, start)
	}
	if !end.Equal(expEnd) {
		t.Fatalf("end: expected %v, got %v", expEnd, end)
	}
}
