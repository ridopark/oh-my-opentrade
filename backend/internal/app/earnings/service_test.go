package earnings

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNextRunTime(t *testing.T) {
	et, _ := time.LoadLocation("America/New_York")
	svc := &Service{
		cfg: Config{RunAtHourET: 5, RunAtMinuteET: 30},
		et:  et,
	}

	t.Run("schedules for today if before target time", func(t *testing.T) {
		// 3 AM ET Monday
		now := time.Date(2026, 4, 6, 3, 0, 0, 0, et)
		next := svc.nextRunTime(now)
		assert.Equal(t, 5, next.Hour())
		assert.Equal(t, 30, next.Minute())
		assert.Equal(t, 6, next.Day())
	})

	t.Run("schedules for next day if after target time", func(t *testing.T) {
		// 7 AM ET Monday
		now := time.Date(2026, 4, 6, 7, 0, 0, 0, et)
		next := svc.nextRunTime(now)
		assert.Equal(t, 7, next.Day())
	})

	t.Run("skips weekends", func(t *testing.T) {
		// Friday 7 AM ET — should schedule for Monday
		now := time.Date(2026, 4, 10, 7, 0, 0, 0, et) // Friday
		next := svc.nextRunTime(now)
		assert.Equal(t, time.Monday, next.Weekday())
		assert.Equal(t, 13, next.Day())
	})
}
