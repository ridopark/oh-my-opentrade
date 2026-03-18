package strategy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeeklyAnchor_EquityMondayOpen(t *testing.T) {
	det := NewWeeklyAnchorDetector(false, "5m")
	et, _ := time.LoadLocation("America/New_York")
	monday := time.Date(2026, 3, 16, 9, 30, 0, 0, et)

	bar := Bar{Time: monday, Open: 185.0, High: 186.0, Low: 184.0, Close: 185.5, Volume: 1000}
	result := det.Push(bar)
	require.NotNil(t, result)
	assert.Equal(t, AnchorWeeklyOpen, result.Type)
	assert.Equal(t, 185.0, result.Price)
	assert.Equal(t, "5m", result.Timeframe)
}

func TestWeeklyAnchor_EquityFridayNoTrigger(t *testing.T) {
	det := NewWeeklyAnchorDetector(false, "5m")
	et, _ := time.LoadLocation("America/New_York")

	monday := time.Date(2026, 3, 16, 9, 30, 0, 0, et)
	det.Push(Bar{Time: monday, Open: 185.0, High: 186.0, Low: 184.0, Close: 185.5, Volume: 1000})

	friday := time.Date(2026, 3, 20, 10, 0, 0, 0, et)
	result := det.Push(Bar{Time: friday, Open: 190.0, High: 191.0, Low: 189.0, Close: 190.5, Volume: 800})
	assert.Nil(t, result)
}

func TestWeeklyAnchor_CryptoMondayUTC(t *testing.T) {
	det := NewWeeklyAnchorDetector(true, "1m")
	monday := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)

	bar := Bar{Time: monday, Open: 85000.0, High: 85500.0, Low: 84500.0, Close: 85200.0, Volume: 500}
	result := det.Push(bar)
	require.NotNil(t, result)
	assert.Equal(t, AnchorWeeklyOpen, result.Type)
	assert.Equal(t, 85000.0, result.Price)
}

func TestWeeklyAnchor_OnlyOnePerWeek(t *testing.T) {
	det := NewWeeklyAnchorDetector(false, "5m")
	et, _ := time.LoadLocation("America/New_York")
	monday := time.Date(2026, 3, 16, 9, 30, 0, 0, et)

	result1 := det.Push(Bar{Time: monday, Open: 185.0, High: 186.0, Low: 184.0, Close: 185.5, Volume: 1000})
	require.NotNil(t, result1)

	mondayNext := monday.Add(5 * time.Minute)
	result2 := det.Push(Bar{Time: mondayNext, Open: 186.0, High: 187.0, Low: 185.0, Close: 186.5, Volume: 900})
	assert.Nil(t, result2)
}

func TestWeeklyAnchor_NewWeekTriggersAgain(t *testing.T) {
	det := NewWeeklyAnchorDetector(false, "5m")
	et, _ := time.LoadLocation("America/New_York")

	week1Monday := time.Date(2026, 3, 16, 9, 30, 0, 0, et)
	result1 := det.Push(Bar{Time: week1Monday, Open: 185.0, High: 186.0, Low: 184.0, Close: 185.5, Volume: 1000})
	require.NotNil(t, result1)

	week2Monday := time.Date(2026, 3, 23, 9, 30, 0, 0, et)
	result2 := det.Push(Bar{Time: week2Monday, Open: 190.0, High: 191.0, Low: 189.0, Close: 190.5, Volume: 1100})
	require.NotNil(t, result2)
	assert.Equal(t, 190.0, result2.Price)
}

func TestWeeklyAnchor_EquityBeforeMondayOpen(t *testing.T) {
	det := NewWeeklyAnchorDetector(false, "5m")
	et, _ := time.LoadLocation("America/New_York")

	mondayPremarket := time.Date(2026, 3, 16, 9, 0, 0, 0, et)
	result := det.Push(Bar{Time: mondayPremarket, Open: 185.0, High: 186.0, Low: 184.0, Close: 185.5, Volume: 500})
	assert.Nil(t, result, "before 09:30 ET should not trigger")
}

func TestWeeklyAnchor_FirstBarMidweekEquity(t *testing.T) {
	det := NewWeeklyAnchorDetector(false, "5m")
	et, _ := time.LoadLocation("America/New_York")

	wednesday := time.Date(2026, 3, 18, 10, 0, 0, 0, et)
	result := det.Push(Bar{Time: wednesday, Open: 185.0, High: 186.0, Low: 184.0, Close: 185.5, Volume: 1000})
	assert.Nil(t, result, "first bar midweek on equity should not trigger — only Monday")
}

func TestWeeklyAnchor_CryptoWeekRollover(t *testing.T) {
	det := NewWeeklyAnchorDetector(true, "1m")

	sunday := time.Date(2026, 3, 15, 23, 59, 0, 0, time.UTC)
	det.Push(Bar{Time: sunday, Open: 85000.0, High: 85500.0, Low: 84500.0, Close: 85200.0, Volume: 100})

	monday := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	result := det.Push(Bar{Time: monday, Open: 85100.0, High: 85600.0, Low: 84600.0, Close: 85300.0, Volume: 200})
	require.NotNil(t, result)
	assert.Equal(t, 85100.0, result.Price)
}
