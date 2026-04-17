package positionmonitor

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestKForDTE(t *testing.T) {
	cases := []struct {
		name string
		dte  int
		want float64
	}{
		{"14 DTE: far bucket", 14, 0.25},
		{"13 DTE: near bucket boundary", 13, 0.35},
		{"5 DTE: near bucket lower edge", 5, 0.35},
		{"4 DTE: short bucket", 4, 0.45},
		{"0 DTE: 0DTE", 0, 0.45},
		{"30 DTE: far bucket", 30, 0.25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, kForDTE(tc.dte))
		})
	}
}

func TestBuildExitLimitPrice(t *testing.T) {
	now := time.Date(2026, 4, 16, 15, 0, 0, 0, time.UTC)

	t.Run("healthy quote DTE=10 uses k=0.35", func(t *testing.T) {
		// bid=1.70 ask=1.80 mid=1.75 spread=0.10
		// target = 1.75 - min(0.35*0.10, 0.05*1.75) = 1.75 - 0.035 = 1.715
		// floor  = max(1.70 + 0.01, 1.715) = 1.715
		q := domain.OptionQuote{Bid: 1.70, Ask: 1.80, BidSize: 10, AskSize: 10, Timestamp: now}
		p, ok := buildExitLimitPrice(q, now, 10, false)
		assert.True(t, ok)
		assert.InDelta(t, 1.715, p, 1e-9)
	})

	t.Run("blown spread rejects quote", func(t *testing.T) {
		// spread/mid = 0.50/1.75 = 0.286 > 0.25
		q := domain.OptionQuote{Bid: 1.50, Ask: 2.00, BidSize: 10, AskSize: 10, Timestamp: now}
		_, ok := buildExitLimitPrice(q, now, 10, false)
		assert.False(t, ok)
	})

	t.Run("stale quote rejects", func(t *testing.T) {
		stale := now.Add(-10 * time.Second)
		q := domain.OptionQuote{Bid: 1.70, Ask: 1.80, BidSize: 10, AskSize: 10, Timestamp: stale}
		_, ok := buildExitLimitPrice(q, now, 10, false)
		assert.False(t, ok)
	})

	t.Run("zero bid size rejects", func(t *testing.T) {
		q := domain.OptionQuote{Bid: 1.70, Ask: 1.80, BidSize: 0, AskSize: 10, Timestamp: now}
		_, ok := buildExitLimitPrice(q, now, 10, false)
		assert.False(t, ok)
	})

	t.Run("zero bid price rejects", func(t *testing.T) {
		q := domain.OptionQuote{Bid: 0, Ask: 1.80, BidSize: 10, AskSize: 10, Timestamp: now}
		_, ok := buildExitLimitPrice(q, now, 10, false)
		assert.False(t, ok)
	})

	t.Run("zero ask price rejects", func(t *testing.T) {
		q := domain.OptionQuote{Bid: 1.70, Ask: 0, BidSize: 10, AskSize: 10, Timestamp: now}
		_, ok := buildExitLimitPrice(q, now, 10, false)
		assert.False(t, ok)
	})

	t.Run("DTE=20 uses k=0.25", func(t *testing.T) {
		// bid=1.70 ask=1.80 mid=1.75 spread=0.10
		// target = 1.75 - min(0.25*0.10, 0.05*1.75) = 1.75 - 0.025 = 1.725
		q := domain.OptionQuote{Bid: 1.70, Ask: 1.80, BidSize: 10, AskSize: 10, Timestamp: now}
		p, ok := buildExitLimitPrice(q, now, 20, false)
		assert.True(t, ok)
		assert.InDelta(t, 1.725, p, 1e-9)
	})

	t.Run("DTE=2 uses k=0.45", func(t *testing.T) {
		// bid=1.70 ask=1.80 mid=1.75 spread=0.10
		// target = 1.75 - min(0.45*0.10, 0.05*1.75) = 1.75 - 0.045 = 1.705
		// floor  = max(1.71, 1.705) = 1.71 (the floor wins)
		q := domain.OptionQuote{Bid: 1.70, Ask: 1.80, BidSize: 10, AskSize: 10, Timestamp: now}
		p, ok := buildExitLimitPrice(q, now, 2, false)
		assert.True(t, ok)
		assert.InDelta(t, 1.71, p, 1e-9)
	})

	t.Run("tight spread: bid+tick floor wins", func(t *testing.T) {
		// bid=1.70 ask=1.72 mid=1.71 spread=0.02 DTE=20 k=0.25
		// target = 1.71 - min(0.25*0.02, 0.05*1.71) = 1.71 - 0.005 = 1.705
		// floor  = max(1.70 + 0.01, 1.705) = 1.71
		q := domain.OptionQuote{Bid: 1.70, Ask: 1.72, BidSize: 10, AskSize: 10, Timestamp: now}
		p, ok := buildExitLimitPrice(q, now, 20, false)
		assert.True(t, ok)
		assert.InDelta(t, 1.71, p, 1e-9)
	})

	t.Run("bps cap engages when k*spread exceeds 5pct of mid", func(t *testing.T) {
		// bid=1.00 ask=1.50 mid=1.25 spread=0.50 spread/mid=0.40 -> blown
		// use bid=1.10 ask=1.40 mid=1.25 spread=0.30 spread/mid=0.24 OK
		// DTE=2 k=0.45 -> k*spread = 0.135, bps cap = 0.0625
		// target = 1.25 - 0.0625 = 1.1875, floor = max(1.11, 1.1875) = 1.1875
		q := domain.OptionQuote{Bid: 1.10, Ask: 1.40, BidSize: 10, AskSize: 10, Timestamp: now}
		p, ok := buildExitLimitPrice(q, now, 2, false)
		assert.True(t, ok)
		assert.InDelta(t, 1.1875, p, 1e-9)
	})

	t.Run("short side: target above mid", func(t *testing.T) {
		// bid=1.70 ask=1.80 mid=1.75 DTE=10 k=0.35 spread=0.10
		// target = 1.75 + 0.035 = 1.785, ceil = 1.80 - 0.01 = 1.79 -> 1.785
		q := domain.OptionQuote{Bid: 1.70, Ask: 1.80, BidSize: 10, AskSize: 10, Timestamp: now}
		p, ok := buildExitLimitPrice(q, now, 10, true)
		assert.True(t, ok)
		assert.InDelta(t, 1.785, p, 1e-9)
	})
}

func TestDteFromExpiry(t *testing.T) {
	now := time.Date(2026, 4, 16, 15, 0, 0, 0, time.UTC)
	t.Run("future expiry", func(t *testing.T) {
		exp := now.Add(7 * 24 * time.Hour)
		assert.Equal(t, 7, dteFromExpiry(exp, now))
	})
	t.Run("zero expiry", func(t *testing.T) {
		assert.Equal(t, 0, dteFromExpiry(time.Time{}, now))
	})
	t.Run("past expiry clamps to 0", func(t *testing.T) {
		exp := now.Add(-24 * time.Hour)
		assert.Equal(t, 0, dteFromExpiry(exp, now))
	})
}
