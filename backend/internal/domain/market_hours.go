package domain

import "time"

// IsEquityMarketOpen reports whether t falls inside the US equity regular
// trading session: Monday-Friday, 09:30 through 15:59:59 America/New_York.
//
// The check is intentionally calendar-date-only. NYSE holidays and early-close
// days (Good Friday, Thanksgiving half-day, etc.) are NOT modeled — a future
// holiday-calendar helper can layer on top when precision matters. For the
// dashboard's "market closed" dot this weekday-plus-RTH check already covers
// the common case (weekends and after-hours) that makes the feed look broken.
//
// When the Go timezone database is unavailable (e.g. distroless images without
// tzdata), the function falls back to a fixed-offset Eastern zone that honors
// US DST rules so the gate still works inside stripped-down containers.
func IsEquityMarketOpen(t time.Time) bool {
	et, err := time.LoadLocation("America/New_York")
	if err != nil {
		et = easternFallbackZone(t.UTC())
	}
	local := t.In(et)
	switch local.Weekday() {
	case time.Saturday, time.Sunday:
		return false
	}
	h, m, _ := local.Clock()
	minutes := h*60 + m
	return minutes >= 9*60+30 && minutes < 16*60
}

// easternFallbackZone returns a time.Location approximating America/New_York
// when tzdata isn't on disk. It picks EST (UTC-5) or EDT (UTC-4) based on
// whether t is inside the US DST window.
func easternFallbackZone(utc time.Time) *time.Location {
	if isEasternDST(utc) {
		return time.FixedZone("EDT", -4*60*60)
	}
	return time.FixedZone("EST", -5*60*60)
}

// isEasternDST reports whether utc falls within US Daylight Saving Time.
// DST starts: 2nd Sunday of March, 02:00 EST (07:00 UTC).
// DST ends:   1st Sunday of November, 02:00 EDT (06:00 UTC).
func isEasternDST(utc time.Time) bool {
	year := utc.Year()
	start := nthWeekdayOfMonth(year, time.March, time.Sunday, 2).Add(7 * time.Hour) // 07:00 UTC
	end := nthWeekdayOfMonth(year, time.November, time.Sunday, 1).Add(6 * time.Hour) // 06:00 UTC
	return !utc.Before(start) && utc.Before(end)
}

func nthWeekdayOfMonth(year int, month time.Month, weekday time.Weekday, n int) time.Time {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	offset := (int(weekday) - int(first.Weekday()) + 7) % 7
	return first.AddDate(0, 0, offset+(n-1)*7)
}
