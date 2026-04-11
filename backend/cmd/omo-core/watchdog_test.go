package main

import (
	"testing"
	"time"
)

func TestShouldSkipHeartbeat(t *testing.T) {
	// Fixed reference "now" — keeps the arithmetic readable.
	now := time.Date(2026, 4, 11, 14, 30, 0, 0, time.UTC)

	cases := []struct {
		name     string
		lastBar  time.Time
		wantSkip bool
	}{
		{
			name:    "zero_lastBar_never_skips_during_warmup",
			lastBar: time.Time{},
			// Pipeline hasn't reported any bar yet. We must not starve systemd
			// during warmup / before first bar.
			wantSkip: false,
		},
		{
			name:     "fresh_feed_one_second_ago",
			lastBar:  now.Add(-1 * time.Second),
			wantSkip: false,
		},
		{
			name:     "boundary_exactly_max_age",
			lastBar:  now.Add(-watchdogFeedMaxAge),
			wantSkip: false, // strict > in the predicate, 90s exact is healthy
		},
		{
			name:     "wedge_just_over_threshold",
			lastBar:  now.Add(-(watchdogFeedMaxAge + time.Second)),
			wantSkip: true, // 91s old during an active session → skip
		},
		{
			name:     "wedge_deep_inside_detection_window",
			lastBar:  now.Add(-3 * time.Minute),
			wantSkip: true,
		},
		{
			name:     "boundary_exactly_stale_cutoff",
			lastBar:  now.Add(-watchdogFeedStaleCutoff),
			wantSkip: false, // strict < in the predicate, 5m exact is off-hours
		},
		{
			name:     "off_hours_overnight_feed_ten_minutes_old",
			lastBar:  now.Add(-10 * time.Minute),
			// Past the 5-min cutoff → presumed off-hours, don't starve the
			// watchdog or we'd exhaust StartLimitBurst overnight.
			wantSkip: false,
		},
		{
			name:     "off_hours_weekend_feed_days_old",
			lastBar:  now.Add(-48 * time.Hour),
			wantSkip: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skip, _ := shouldSkipHeartbeat(tc.lastBar, now)
			if skip != tc.wantSkip {
				t.Fatalf("shouldSkipHeartbeat(lastBar=%v) = %v, want %v", tc.lastBar, skip, tc.wantSkip)
			}
		})
	}
}
