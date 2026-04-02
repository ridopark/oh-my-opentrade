package backfill

import "time"

// GapThreshold is the minimum gap duration to consider filling.
// Gaps shorter than this are normal overnight/weekend gaps.
const GapThreshold = 4 * time.Hour

// IsRTHGap returns true if a gap falls during Regular Trading Hours
// (9:30-16:00 ET, weekdays only) and thus represents missing data.
func IsRTHGap(gapStart, gapEnd time.Time, loc *time.Location) bool {
	// Clamp end to now — we cannot fetch future data.
	now := time.Now()
	if gapEnd.After(now) {
		gapEnd = now
	}
	if !gapEnd.After(gapStart) {
		return false
	}

	startET := gapStart.In(loc)
	endET := gapEnd.In(loc)

	// Single-day gap: both timestamps on the same calendar day.
	if startET.Year() == endET.Year() && startET.YearDay() == endET.YearDay() {
		if startET.Weekday() == time.Saturday || startET.Weekday() == time.Sunday {
			return false
		}
		rthOpen := time.Date(startET.Year(), startET.Month(), startET.Day(), 9, 30, 0, 0, loc)
		rthClose := time.Date(startET.Year(), startET.Month(), startET.Day(), 16, 0, 0, 0, loc)
		return startET.After(rthOpen) && endET.Before(rthClose)
	}

	// Multi-day gap: return true if at least one weekday exists in the range.
	for d := startET; !d.After(endET); d = d.AddDate(0, 0, 1) {
		wd := d.Weekday()
		if wd != time.Saturday && wd != time.Sunday {
			return true
		}
	}
	return false
}
