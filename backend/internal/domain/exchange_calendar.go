package domain

import (
	"sync"
	"time"
)

var (
	nyOnce sync.Once
	nyLoc  *time.Location
)

// NYLocation returns a cached *time.Location for America/New_York.
func NYLocation() *time.Location {
	nyOnce.Do(func() {
		var err error
		nyLoc, err = time.LoadLocation("America/New_York")
		if err != nil {
			nyLoc = time.FixedZone("EST", -5*3600)
		}
	})
	return nyLoc
}

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
	loc := NYLocation()

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

// TradingCalendar defines market session queries for an asset class.
type TradingCalendar interface {
	IsOpen(t time.Time) bool
	SessionOpen(t time.Time) time.Time
	SessionClose(t time.Time) time.Time
	PreviousSession(now time.Time) (start, end time.Time)
}

// NYSECalendar implements TradingCalendar for US equity markets.
type NYSECalendar struct{}

func (c NYSECalendar) IsOpen(t time.Time) bool {
	loc := NYLocation()
	et := t.In(loc)
	if !isNYSETradingDay(et) {
		return false
	}
	open := time.Date(et.Year(), et.Month(), et.Day(), 9, 30, 0, 0, loc)
	h, m := NYSECloseTime(et)
	close := time.Date(et.Year(), et.Month(), et.Day(), h, m, 0, 0, loc)
	return !et.Before(open) && et.Before(close)
}

func (c NYSECalendar) SessionOpen(t time.Time) time.Time {
	loc := NYLocation()
	et := t.In(loc)
	return time.Date(et.Year(), et.Month(), et.Day(), 9, 30, 0, 0, loc)
}

func (c NYSECalendar) SessionClose(t time.Time) time.Time {
	loc := NYLocation()
	et := t.In(loc)
	h, m := NYSECloseTime(et)
	if h == 0 && m == 0 {
		return time.Date(et.Year(), et.Month(), et.Day(), 16, 0, 0, 0, loc)
	}
	return time.Date(et.Year(), et.Month(), et.Day(), h, m, 0, 0, loc)
}

func (c NYSECalendar) PreviousSession(now time.Time) (start, end time.Time) {
	return PreviousRTHSession(now)
}

// Crypto24x7Calendar implements TradingCalendar for 24/7 crypto markets.
type Crypto24x7Calendar struct{}

func (c Crypto24x7Calendar) IsOpen(_ time.Time) bool { return true }

func (c Crypto24x7Calendar) SessionOpen(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func (c Crypto24x7Calendar) SessionClose(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
}

func (c Crypto24x7Calendar) PreviousSession(now time.Time) (start, end time.Time) {
	yesterday := now.UTC().AddDate(0, 0, -1)
	start = time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, time.UTC)
	end = time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 23, 59, 59, 0, time.UTC)
	return start, end
}

// TradingDaysBetween counts the number of trading days (weekdays excluding NYSE holidays)
// between two times. Both from and to are date-truncated before counting.
// For crypto (24/7), falls back to calendar days.
func TradingDaysBetween(from, to time.Time) int {
	loc := NYLocation()
	fromDate := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	toDate := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc)

	if !toDate.After(fromDate) {
		return 0
	}

	count := 0
	for d := fromDate; d.Before(toDate); d = d.AddDate(0, 0, 1) {
		if isNYSETradingDay(d) {
			count++
		}
	}
	return count
}

// ExpectedBarTimestamps returns the canonical sequence of bar-close timestamps
// expected for sym at tf within [from, to). Equity excludes weekends, NYSE
// holidays and non-RTH minutes; early-close days end at 13:00 ET. Crypto is
// 24/7. For tf="1d", returns one timestamp per NYSE trading day (equity)
// anchored to 16:00 ET, or per calendar day (crypto) anchored to 00:00 UTC.
// Returns nil for unsupported tf or when from is not strictly before to.
func ExpectedBarTimestamps(sym Symbol, tf Timeframe, from, to time.Time) []time.Time {
	if !from.Before(to) {
		return nil
	}
	stepSec, ok := timeframeStepSeconds(tf)
	if !ok && tf != "1d" {
		return nil
	}

	if sym.IsCryptoSymbol() {
		return cryptoExpectedBars(tf, stepSec, from, to)
	}
	return equityExpectedBars(tf, stepSec, from, to)
}

// timeframeStepSeconds returns the intra-session step size for an intraday tf.
// 1d is handled separately because daily bars step by trading days, not seconds.
func timeframeStepSeconds(tf Timeframe) (int, bool) {
	switch tf {
	case "1m":
		return 60, true
	case "5m":
		return 300, true
	case "15m":
		return 900, true
	case "1h":
		return 3600, true
	}
	return 0, false
}

func cryptoExpectedBars(tf Timeframe, stepSec int, from, to time.Time) []time.Time {
	if tf == "1d" {
		fromUTC := from.UTC()
		start := time.Date(fromUTC.Year(), fromUTC.Month(), fromUTC.Day(), 0, 0, 0, 0, time.UTC)
		if start.Before(fromUTC) {
			start = start.AddDate(0, 0, 1)
		}
		days := int(to.UTC().Sub(start)/(24*time.Hour)) + 2
		if days < 0 {
			days = 0
		}
		out := make([]time.Time, 0, days)
		for t := start; t.Before(to); t = t.AddDate(0, 0, 1) {
			out = append(out, t)
		}
		return out
	}
	step := time.Duration(stepSec) * time.Second
	fromUTC := from.UTC()
	startUnix := ((fromUTC.Unix() + int64(stepSec) - 1) / int64(stepSec)) * int64(stepSec)
	start := time.Unix(startUnix, 0).UTC()
	count := int(to.Sub(start)/step) + 1
	if count < 0 {
		count = 0
	}
	out := make([]time.Time, 0, count)
	for t := start; t.Before(to); t = t.Add(step) {
		out = append(out, t)
	}
	return out
}

func equityExpectedBars(tf Timeframe, stepSec int, from, to time.Time) []time.Time {
	loc := NYLocation()
	fromET := from.In(loc)
	toET := to.In(loc)

	day := time.Date(fromET.Year(), fromET.Month(), fromET.Day(), 0, 0, 0, 0, loc)
	endDay := time.Date(toET.Year(), toET.Month(), toET.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)

	approxDays := int(endDay.Sub(day)/(24*time.Hour)) + 1
	var perDay int
	switch tf {
	case "1m":
		perDay = 390
	case "5m":
		perDay = 78
	case "15m":
		perDay = 26
	case "1h":
		perDay = 7
	case "1d":
		perDay = 1
	}
	out := make([]time.Time, 0, approxDays*perDay)

	for d := day; d.Before(endDay); d = d.AddDate(0, 0, 1) {
		if !isNYSETradingDay(d) {
			continue
		}
		closeH, closeM := NYSECloseTime(d)
		if tf == "1d" {
			ts := time.Date(d.Year(), d.Month(), d.Day(), closeH, closeM, 0, 0, loc)
			if !ts.Before(from) && ts.Before(to) {
				out = append(out, ts)
			}
			continue
		}
		open := time.Date(d.Year(), d.Month(), d.Day(), 9, 30, 0, 0, loc)
		closeT := time.Date(d.Year(), d.Month(), d.Day(), closeH, closeM, 0, 0, loc)
		step := time.Duration(stepSec) * time.Second
		// Bars are timestamped at their open. The last bar of the session
		// opens at closeT-step (its close coincides with the session close).
		last := closeT.Add(-step)
		for t := open; !t.After(last); t = t.Add(step) {
			if t.Before(from) || !t.Before(to) {
				continue
			}
			out = append(out, t)
		}
	}
	return out
}

// CalendarFor returns the appropriate TradingCalendar for the given asset class.
func CalendarFor(ac AssetClass) TradingCalendar {
	switch ac {
	case AssetClassCrypto:
		return Crypto24x7Calendar{}
	default:
		return NYSECalendar{}
	}
}
