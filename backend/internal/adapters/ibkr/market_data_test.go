package ibkr

import (
	"testing"
	"time"
)

func TestDurationStr(t *testing.T) {
	tests := []struct {
		name     string
		from     time.Time
		to       time.Time
		expected string
	}{
		{"1 day", d(2026, 2, 10), d(2026, 2, 10), "1 D"},
		{"3 days", d(2026, 2, 10), d(2026, 2, 12), "3 D"},
		{"7 days", d(2026, 2, 10), d(2026, 2, 16), "7 D"},
		{"2 weeks", d(2026, 2, 10), d(2026, 2, 23), "2 W"},
		{"4 weeks", d(2026, 2, 1), d(2026, 2, 28), "4 W"},
		{"3 months", d(2026, 1, 1), d(2026, 3, 31), "3 M"},
		{"11 months", d(2025, 6, 20), d(2026, 4, 30), "11 M"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := durationStr(tt.from, tt.to)
			if got != tt.expected {
				t.Errorf("durationStr(%v, %v) = %q, want %q", tt.from, tt.to, got, tt.expected)
			}
		})
	}
}

func TestMaxChunkDays(t *testing.T) {
	tests := []struct {
		barSize string
		want    int
		exists  bool
	}{
		{"1 min", 7, true},
		{"5 mins", 14, true},
		{"15 mins", 0, false},
		{"1 hour", 0, false},
		{"1 day", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.barSize, func(t *testing.T) {
			got, ok := maxChunkDays[tt.barSize]
			if ok != tt.exists {
				t.Errorf("maxChunkDays[%q] exists = %v, want %v", tt.barSize, ok, tt.exists)
			}
			if ok && got != tt.want {
				t.Errorf("maxChunkDays[%q] = %d, want %d", tt.barSize, got, tt.want)
			}
		})
	}
}

func d(year, month, day int) time.Time {
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}
