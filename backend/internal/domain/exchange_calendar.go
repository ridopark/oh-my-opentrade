package domain

import "time"

// NYSE market holidays and early close days.
// Source: NYSE Group official announcements
// https://www.nyse.com/markets/hours-calendars
// Data covers 2025-2028. Update annually when NYSE publishes new calendars.

type dateKey struct {
	year  int
	month time.Month
	day   int
}

func toDateKey(t time.Time) dateKey {
	return dateKey{year: t.Year(), month: t.Month(), day: t.Day()}
}

var nyseHolidays = map[dateKey]bool{
	{year: 2025, month: time.January, day: 1}:   true,
	{year: 2025, month: time.January, day: 20}:  true,
	{year: 2025, month: time.February, day: 17}: true,
	{year: 2025, month: time.April, day: 18}:    true,
	{year: 2025, month: time.May, day: 26}:      true,
	{year: 2025, month: time.June, day: 19}:     true,
	{year: 2025, month: time.July, day: 4}:      true,
	{year: 2025, month: time.September, day: 1}: true,
	{year: 2025, month: time.November, day: 27}: true,
	{year: 2025, month: time.December, day: 25}: true,

	{year: 2026, month: time.January, day: 1}:   true,
	{year: 2026, month: time.January, day: 19}:  true,
	{year: 2026, month: time.February, day: 16}: true,
	{year: 2026, month: time.April, day: 3}:     true,
	{year: 2026, month: time.May, day: 25}:      true,
	{year: 2026, month: time.June, day: 19}:     true,
	{year: 2026, month: time.July, day: 3}:      true,
	{year: 2026, month: time.September, day: 7}: true,
	{year: 2026, month: time.November, day: 26}: true,
	{year: 2026, month: time.December, day: 25}: true,

	{year: 2027, month: time.January, day: 1}:   true,
	{year: 2027, month: time.January, day: 18}:  true,
	{year: 2027, month: time.February, day: 15}: true,
	{year: 2027, month: time.March, day: 26}:    true,
	{year: 2027, month: time.May, day: 31}:      true,
	{year: 2027, month: time.June, day: 18}:     true,
	{year: 2027, month: time.July, day: 5}:      true,
	{year: 2027, month: time.September, day: 6}: true,
	{year: 2027, month: time.November, day: 25}: true,
	{year: 2027, month: time.December, day: 24}: true,

	{year: 2028, month: time.January, day: 17}:  true,
	{year: 2028, month: time.February, day: 21}: true,
	{year: 2028, month: time.April, day: 14}:    true,
	{year: 2028, month: time.May, day: 29}:      true,
	{year: 2028, month: time.June, day: 19}:     true,
	{year: 2028, month: time.July, day: 4}:      true,
	{year: 2028, month: time.September, day: 4}: true,
	{year: 2028, month: time.November, day: 23}: true,
	{year: 2028, month: time.December, day: 25}: true,
}

var nyseEarlyCloses = map[dateKey]bool{
	{year: 2025, month: time.July, day: 3}:      true,
	{year: 2025, month: time.November, day: 28}: true,
	{year: 2025, month: time.December, day: 24}: true,

	{year: 2026, month: time.November, day: 27}: true,
	{year: 2026, month: time.December, day: 24}: true,

	{year: 2027, month: time.November, day: 26}: true,

	{year: 2028, month: time.July, day: 3}:      true,
	{year: 2028, month: time.November, day: 24}: true,
}

// IsNYSEHoliday returns true if the given date (year, month, day only) is a full NYSE market closure.
func IsNYSEHoliday(t time.Time) bool {
	return nyseHolidays[toDateKey(t)]
}

// IsNYSEEarlyClose returns true if the given date is an NYSE early close day (1:00 PM ET).
func IsNYSEEarlyClose(t time.Time) bool {
	return nyseEarlyCloses[toDateKey(t)]
}

// NYSECloseTime returns the close time for a given date: 13:00 for early close, 16:00 for normal.
// Returns 0,0 if the market is closed (holiday/weekend).
func NYSECloseTime(t time.Time) (hour, minute int) {
	if !isNYSETradingDay(t) {
		return 0, 0
	}
	if IsNYSEEarlyClose(t) {
		return 13, 0
	}
	return 16, 0
}

// isNYSETradingDay returns true if the market is open (weekday + not a holiday)
func isNYSETradingDay(t time.Time) bool {
	wd := t.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	if IsNYSEHoliday(t) {
		return false
	}
	return true
}

func PreviousRTHSession(now time.Time) (start, end time.Time) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.Local
	}

	nowET := now.In(loc)
	day := time.Date(nowET.Year(), nowET.Month(), nowET.Day(), 0, 0, 0, 0, loc)

	for !isNYSETradingDay(day) {
		day = day.AddDate(0, 0, -1)
	}

	todayClose := time.Date(day.Year(), day.Month(), day.Day(), 16, 0, 0, 0, loc)
	useDay := day

	if nowET.Before(todayClose) {
		useDay = day.AddDate(0, 0, -1)
		for !isNYSETradingDay(useDay) {
			useDay = useDay.AddDate(0, 0, -1)
		}
	}

	start = time.Date(useDay.Year(), useDay.Month(), useDay.Day(), 9, 30, 0, 0, loc)
	h, m := NYSECloseTime(useDay)
	end = time.Date(useDay.Year(), useDay.Month(), useDay.Day(), h, m, 0, 0, loc)
	return start, end
}
