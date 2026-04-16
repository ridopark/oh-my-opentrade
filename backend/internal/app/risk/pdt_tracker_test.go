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
