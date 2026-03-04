package monitor

import (
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

func nyLocation() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.FixedZone("EST", -5*3600)
	}
	return loc
}

func SessionKeyET(t time.Time) string {
	et := t.In(nyLocation())
	return fmt.Sprintf("%04d-%02d-%02d", et.Year(), int(et.Month()), et.Day())
}

func RTHOpenUTC(t time.Time) time.Time {
	loc := nyLocation()
	et := t.In(loc)
	openET := time.Date(et.Year(), et.Month(), et.Day(), 9, 30, 0, 0, loc)
	return openET.UTC()
}

func RTHEndUTC(t time.Time) time.Time {
	loc := nyLocation()
	et := t.In(loc)
	h, m := domain.NYSECloseTime(et)
	closeET := time.Date(et.Year(), et.Month(), et.Day(), h, m, 0, 0, loc)
	return closeET.UTC()
}

func IsWithinORBWindow(barTime time.Time, windowMinutes int) bool {
	if windowMinutes <= 0 {
		return false
	}
	loc := nyLocation()
	et := barTime.In(loc)
	openET := time.Date(et.Year(), et.Month(), et.Day(), 9, 30, 0, 0, loc)
	endExclusive := openET.Add(time.Duration(windowMinutes) * time.Minute)
	return (et.Equal(openET) || et.After(openET)) && et.Before(endExclusive)
}
