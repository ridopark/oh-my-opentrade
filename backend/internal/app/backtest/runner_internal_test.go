package backtest

import (
	"testing"
	"time"
)

func TestIsRTHGap(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		start    time.Time
		end      time.Time
		expected bool
	}{
		{
			name:     "same-day intraday gap during RTH",
			start:    time.Date(2026, 2, 10, 10, 0, 0, 0, loc), // Mon 10:00 ET
			end:      time.Date(2026, 2, 10, 14, 0, 0, 0, loc), // Mon 14:00 ET
			expected: true,
		},
		{
			name:     "same-day gap before market open",
			start:    time.Date(2026, 2, 10, 8, 0, 0, 0, loc),
			end:      time.Date(2026, 2, 10, 9, 0, 0, 0, loc),
			expected: false,
		},
		{
			name:     "pure weekend gap",
			start:    time.Date(2026, 2, 14, 0, 0, 0, 0, loc), // Saturday
			end:      time.Date(2026, 2, 15, 23, 59, 0, 0, loc), // Sunday
			expected: false,
		},
		{
			name:     "multi-day gap with weekdays (Feb 10 to Feb 17 bug)",
			start:    time.Date(2026, 2, 10, 16, 0, 0, 0, loc), // Mon close
			end:      time.Date(2026, 2, 17, 9, 30, 0, 0, loc),  // Next Tue open
			expected: true,
		},
		{
			name:     "multi-day trailing gap (Feb 24 to Mar 30)",
			start:    time.Date(2026, 2, 24, 16, 0, 0, 0, loc),
			end:      time.Date(2026, 3, 30, 16, 0, 0, 0, loc),
			expected: true,
		},
		{
			name:     "Friday close to Monday open",
			start:    time.Date(2026, 2, 13, 16, 0, 0, 0, loc), // Friday
			end:      time.Date(2026, 2, 16, 9, 30, 0, 0, loc),  // Monday
			expected: true,
		},
		{
			name:     "end before start returns false",
			start:    time.Date(2026, 2, 17, 12, 0, 0, 0, loc),
			end:      time.Date(2026, 2, 10, 12, 0, 0, 0, loc),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRTHGap(tt.start, tt.end, loc)
			if got != tt.expected {
				t.Errorf("isRTHGap(%v, %v) = %v, want %v", tt.start, tt.end, got, tt.expected)
			}
		})
	}
}
