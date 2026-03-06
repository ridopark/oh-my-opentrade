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

func TestPreviousRTHSession_TuesdayAfterMLKDay2026(t *testing.T) {
	loc := mustLoadNY(t)
	now := time.Date(2026, time.January, 20, 7, 0, 0, 0, loc)
	if now.Weekday() != time.Tuesday {
		t.Fatalf("expected Tuesday, got %s", now.Weekday())
	}

	start, end := PreviousRTHSession(now)

	expStart := time.Date(2026, time.January, 16, 9, 30, 0, 0, loc)
	expEnd := time.Date(2026, time.January, 16, 16, 0, 0, 0, loc)
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

func TestPreviousRTHSession_MondayAfterGoodFriday2026(t *testing.T) {
	loc := mustLoadNY(t)
	now := time.Date(2026, time.April, 6, 7, 0, 0, 0, loc)
	if now.Weekday() != time.Monday {
		t.Fatalf("expected Monday, got %s", now.Weekday())
	}

	start, end := PreviousRTHSession(now)

	expStart := time.Date(2026, time.April, 2, 9, 30, 0, 0, loc)
	expEnd := time.Date(2026, time.April, 2, 16, 0, 0, 0, loc)
	if expStart.Weekday() != time.Thursday {
		t.Fatalf("expected Thursday, got %s", expStart.Weekday())
	}

	if !start.Equal(expStart) {
		t.Fatalf("start: expected %v, got %v", expStart, start)
	}
	if !end.Equal(expEnd) {
		t.Fatalf("end: expected %v, got %v", expEnd, end)
	}
}

func TestPreviousRTHSession_MondayAfterThanksgivingWeek2026(t *testing.T) {
	loc := mustLoadNY(t)
	now := time.Date(2026, time.November, 30, 7, 0, 0, 0, loc)
	if now.Weekday() != time.Monday {
		t.Fatalf("expected Monday, got %s", now.Weekday())
	}

	start, end := PreviousRTHSession(now)

	expStart := time.Date(2026, time.November, 27, 9, 30, 0, 0, loc)
	expEnd := time.Date(2026, time.November, 27, 13, 0, 0, 0, loc)
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

func TestPreviousRTHSession_AfterBlackFriday2026(t *testing.T) {
	loc := mustLoadNY(t)
	now := time.Date(2026, time.November, 28, 12, 0, 0, 0, loc)
	if now.Weekday() != time.Saturday {
		t.Fatalf("expected Saturday, got %s", now.Weekday())
	}

	start, end := PreviousRTHSession(now)

	expStart := time.Date(2026, time.November, 27, 9, 30, 0, 0, loc)
	expEnd := time.Date(2026, time.November, 27, 13, 0, 0, 0, loc)
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

func TestPreviousRTHSession_AfterChristmasEve2025(t *testing.T) {
	loc := mustLoadNY(t)
	now := time.Date(2025, time.December, 25, 12, 0, 0, 0, loc)
	if now.Weekday() != time.Thursday {
		t.Fatalf("expected Thursday, got %s", now.Weekday())
	}

	start, end := PreviousRTHSession(now)

	expStart := time.Date(2025, time.December, 24, 9, 30, 0, 0, loc)
	expEnd := time.Date(2025, time.December, 24, 13, 0, 0, 0, loc)
	if expStart.Weekday() != time.Wednesday {
		t.Fatalf("expected Wednesday, got %s", expStart.Weekday())
	}

	if !start.Equal(expStart) {
		t.Fatalf("start: expected %v, got %v", expStart, start)
	}
	if !end.Equal(expEnd) {
		t.Fatalf("end: expected %v, got %v", expEnd, end)
	}
}

func TestPreviousRTHSession_AfterJuly4Weekend2026(t *testing.T) {
	loc := mustLoadNY(t)
	now := time.Date(2026, time.July, 6, 7, 0, 0, 0, loc)
	if now.Weekday() != time.Monday {
		t.Fatalf("expected Monday, got %s", now.Weekday())
	}

	start, end := PreviousRTHSession(now)

	expStart := time.Date(2026, time.July, 2, 9, 30, 0, 0, loc)
	expEnd := time.Date(2026, time.July, 2, 16, 0, 0, 0, loc)
	if expStart.Weekday() != time.Thursday {
		t.Fatalf("expected Thursday, got %s", expStart.Weekday())
	}

	if !start.Equal(expStart) {
		t.Fatalf("start: expected %v, got %v", expStart, start)
	}
	if !end.Equal(expEnd) {
		t.Fatalf("end: expected %v, got %v", expEnd, end)
	}
}

func TestPreviousRTHSession_NormalWednesday(t *testing.T) {
	loc := mustLoadNY(t)
	now := time.Date(2026, time.March, 4, 7, 0, 0, 0, loc)
	if now.Weekday() != time.Wednesday {
		t.Fatalf("expected Wednesday, got %s", now.Weekday())
	}

	start, end := PreviousRTHSession(now)

	expStart := time.Date(2026, time.March, 3, 9, 30, 0, 0, loc)
	expEnd := time.Date(2026, time.March, 3, 16, 0, 0, 0, loc)
	if expStart.Weekday() != time.Tuesday {
		t.Fatalf("expected Tuesday, got %s", expStart.Weekday())
	}

	if !start.Equal(expStart) {
		t.Fatalf("start: expected %v, got %v", expStart, start)
	}
	if !end.Equal(expEnd) {
		t.Fatalf("end: expected %v, got %v", expEnd, end)
	}
}

func TestNYSECalendar_IsOpen(t *testing.T) {
	loc := mustLoadNY(t)
	cal := NYSECalendar{}
	
	// Weekday 10AM ET = open
	open := time.Date(2026, time.March, 4, 10, 0, 0, 0, loc) // Wednesday
	if !cal.IsOpen(open) {
		t.Fatal("expected NYSE open on Wednesday 10AM ET")
	}
	
	// Saturday = closed
	sat := time.Date(2026, time.March, 7, 10, 0, 0, 0, loc)
	if cal.IsOpen(sat) {
		t.Fatal("expected NYSE closed on Saturday")
	}
	
	// Holiday (MLK Day 2026 = Jan 19) = closed
	holiday := time.Date(2026, time.January, 19, 10, 0, 0, 0, loc)
	if cal.IsOpen(holiday) {
		t.Fatal("expected NYSE closed on MLK Day")
	}
	
	// After hours 5PM = closed
	afterHours := time.Date(2026, time.March, 4, 17, 0, 0, 0, loc)
	if cal.IsOpen(afterHours) {
		t.Fatal("expected NYSE closed at 5PM ET")
	}
}

func TestCrypto24x7Calendar_IsOpen(t *testing.T) {
	cal := Crypto24x7Calendar{}
	loc := mustLoadNY(t)
	
	// Always true
	times := []time.Time{
		time.Date(2026, time.March, 7, 3, 0, 0, 0, loc),    // Saturday 3AM
		time.Date(2026, time.March, 8, 12, 0, 0, 0, loc),   // Sunday noon
		time.Date(2026, time.January, 19, 10, 0, 0, 0, loc), // MLK Day
		time.Date(2026, time.March, 4, 10, 0, 0, 0, loc),    // Normal Wednesday
	}
	for _, tt := range times {
		if !cal.IsOpen(tt) {
			t.Fatalf("expected Crypto24x7 open at %v", tt)
		}
	}
}

func TestCalendarFor(t *testing.T) {
	nyseCal := CalendarFor(AssetClassEquity)
	if _, ok := nyseCal.(NYSECalendar); !ok {
		t.Fatal("expected NYSECalendar for Equity")
	}
	
	cryptoCal := CalendarFor(AssetClassCrypto)
	if _, ok := cryptoCal.(Crypto24x7Calendar); !ok {
		t.Fatal("expected Crypto24x7Calendar for Crypto")
	}
}
