package risk

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

func TestPDTTracker_RoundTripIncrementsCount(t *testing.T) {
	tr := NewPDTTracker(nil, nil, zerolog.Nop())
	day := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)

	tr.RecordOpen("A1", "AAPL", 100, day)
	n := tr.RecordClose(context.Background(), "A1", "AAPL", 100, day.Add(2*time.Hour))
	assert.Equal(t, 1, n)
	assert.Equal(t, 1, tr.DayTradeCount("A1", day))
}

func TestPDTTracker_PriorDayOpenDoesNotCount(t *testing.T) {
	tr := NewPDTTracker(nil, nil, zerolog.Nop())
	d1 := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)

	tr.RecordOpen("A1", "AAPL", 100, d1)
	n := tr.RecordClose(context.Background(), "A1", "AAPL", 100, d2)
	assert.Equal(t, 0, n)
	assert.Equal(t, 0, tr.DayTradeCount("A1", d2))
}

func TestPDTTracker_HasSameDayOpen(t *testing.T) {
	tr := NewPDTTracker(nil, nil, zerolog.Nop())
	day := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)

	tr.RecordOpen("A1", "MSFT", 10, day)
	assert.True(t, tr.HasSameDayOpen("A1", "MSFT", day))
	assert.False(t, tr.HasSameDayOpen("A1", "MSFT", day.AddDate(0, 0, 1)))
	assert.False(t, tr.HasSameDayOpen("A1", "AAPL", day))
}

func TestPDTTracker_OpenOnlyDoesNotCount(t *testing.T) {
	tr := NewPDTTracker(nil, nil, zerolog.Nop())
	day := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)

	tr.RecordOpen("A1", "AAPL", 100, day)
	assert.Equal(t, 0, tr.DayTradeCount("A1", day))
}

func TestPDTTracker_RollingDayTradeCount(t *testing.T) {
	tr := NewPDTTracker(nil, nil, zerolog.Nop())
	// Mon Apr 13 through Thu Apr 16, 2026 — one day trade each day.
	mon := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)
	tue := mon.AddDate(0, 0, 1)
	wed := mon.AddDate(0, 0, 2)
	thu := mon.AddDate(0, 0, 3)

	for i, d := range []time.Time{mon, tue, wed} {
		sym := string(rune('A' + i))
		tr.RecordOpen("A1", sym, 10, d)
		tr.RecordClose(context.Background(), "A1", sym, 10, d.Add(time.Hour))
	}

	t.Run("3 biz days rolling on Wed", func(t *testing.T) {
		// Mon+Tue+Wed = 3 trades in a 3-day window
		assert.Equal(t, 3, tr.RollingDayTradeCount("A1", wed, 3))
	})

	t.Run("5 biz days rolling on Thu sees all 3", func(t *testing.T) {
		assert.Equal(t, 3, tr.RollingDayTradeCount("A1", thu, 5))
	})

	t.Run("1 biz day on Thu sees 0", func(t *testing.T) {
		assert.Equal(t, 0, tr.RollingDayTradeCount("A1", thu, 1))
	})

	t.Run("skips weekends", func(t *testing.T) {
		// If asOf is Monday Apr 20 and we look back 5 biz days,
		// that covers Mon 20, Fri 17, Thu 16, Wed 15, Tue 14 —
		// which misses Mon 13.
		nextMon := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
		assert.Equal(t, 2, tr.RollingDayTradeCount("A1", nextMon, 5))
		// 6 biz days would reach Mon 13
		assert.Equal(t, 3, tr.RollingDayTradeCount("A1", nextMon, 6))
	})
}

func TestPDTTracker_PartialCloses(t *testing.T) {
	tr := NewPDTTracker(nil, nil, zerolog.Nop())
	day := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)

	tr.RecordOpen("A1", "AAPL", 100, day)
	// Two partial closes on the same day are a single day trade (the
	// open leg is what gets matched; partial exits on a same-day lot
	// still each produce a "round-trip" record for the consumed qty).
	_ = tr.RecordClose(context.Background(), "A1", "AAPL", 40, day.Add(1*time.Hour))
	_ = tr.RecordClose(context.Background(), "A1", "AAPL", 60, day.Add(2*time.Hour))
	// Each close against a same-day open increments the count.
	assert.Equal(t, 2, tr.DayTradeCount("A1", day))
}
