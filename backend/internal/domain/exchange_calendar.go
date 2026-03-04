package domain

import "time"

func PreviousRTHSession(now time.Time) (start, end time.Time) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.Local
	}

	nowET := now.In(loc)
	day := time.Date(nowET.Year(), nowET.Month(), nowET.Day(), 0, 0, 0, 0, loc)

	for day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		day = day.AddDate(0, 0, -1)
	}

	todayClose := time.Date(day.Year(), day.Month(), day.Day(), 16, 0, 0, 0, loc)
	useDay := day

	if nowET.Before(todayClose) {
		useDay = day.AddDate(0, 0, -1)
		for useDay.Weekday() == time.Saturday || useDay.Weekday() == time.Sunday {
			useDay = useDay.AddDate(0, 0, -1)
		}
	}

	start = time.Date(useDay.Year(), useDay.Month(), useDay.Day(), 9, 30, 0, 0, loc)
	end = time.Date(useDay.Year(), useDay.Month(), useDay.Day(), 16, 0, 0, 0, loc)
	return start, end
}
