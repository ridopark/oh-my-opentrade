package backfill

import "time"

// GapThreshold is the minimum gap duration to consider filling.
// Gaps shorter than this are normal intraday pauses (low-liquidity minutes).
const GapThreshold = 4 * time.Hour

// IsRTHGap returns true if a gap spans missing Regular Trading Hours data
// (9:30-16:00 ET, weekdays only). Normal overnight gaps (market close → next
// open) return false even though they exceed GapThreshold.
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

	// Multi-day gap: check if at least one FULL RTH session (9:30-16:00) on
	// a weekday falls entirely within the gap. A standard overnight gap
	// (e.g., Fri 18:00 → Mon 03:00) does NOT contain a full RTH session
	// and correctly returns false.
	for d := startET; !d.After(endET); d = d.AddDate(0, 0, 1) {
		wd := d.Weekday()
		if wd == time.Saturday || wd == time.Sunday {
			continue
		}
		dayOpen := time.Date(d.Year(), d.Month(), d.Day(), 9, 30, 0, 0, loc)
		dayClose := time.Date(d.Year(), d.Month(), d.Day(), 16, 0, 0, 0, loc)
		// A full RTH session is missing if both 9:30 and 16:00 fall within the gap.
		if !dayOpen.Before(gapStart) && !dayClose.After(gapEnd) {
			return true
		}
	}
	return false
}
