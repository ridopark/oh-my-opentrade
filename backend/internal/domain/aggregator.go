package domain

import (
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

// Silent Push rejections caused a production stall (2026-04-21 11:50 CDT)
// where the general 5m aggregator stopped emitting across all symbols while
// the ORB aggregator kept working. The rejection paths below returned
// ok=false with no logging, so the failure was invisible until a watchdog
// detected zero evaluations minutes later. These counters + WARN logs let
// the next occurrence self-diagnose.
var (
	aggRejectedSessionOpen    atomic.Int64
	aggRejectedBadBar         atomic.Int64
	aggRejectedBucketEnd      atomic.Int64
	aggRejectedStartNewFailed atomic.Int64
)

// AggregatorRejectionCounts returns cumulative Push rejections by reason.
// A sustained rise in any counter during live trading indicates an
// aggregator silently dropping bars.
func AggregatorRejectionCounts() (sessionOpen, badBar, bucketEnd, startNew int64) {
	return aggRejectedSessionOpen.Load(), aggRejectedBadBar.Load(),
		aggRejectedBucketEnd.Load(), aggRejectedStartNewFailed.Load()
}

type BarAggregator struct {
	symbol      Symbol
	tf          Timeframe
	sessionOpen time.Time
	cur         MarketBar
	hasCur      bool
	curEnd      time.Time
}

func NewBarAggregator(symbol Symbol, targetTF Timeframe, sessionOpen time.Time) (*BarAggregator, error) {
	if sessionOpen.IsZero() {
		return nil, errors.New("sessionOpen is required")
	}
	switch targetTF {
	case "5m", "15m", "30m", "1h", "1d":
	default:
		return nil, fmt.Errorf("invalid target timeframe: %q", targetTF)
	}
	return &BarAggregator{
		symbol:      symbol,
		tf:          targetTF,
		sessionOpen: sessionOpen,
		
		curEnd:      time.Time{},
	}, nil
}

// NewClockAlignedAggregator creates an aggregator using UTC clock-aligned buckets.
// For 5m: buckets are 00:00, 00:05, 00:10, ... aligned to UTC.
// This is appropriate for 24/7 markets (crypto) that have no session open concept.
// It works by anchoring to the Unix epoch (1970-01-01 00:00:00 UTC), which naturally
// aligns all bucket boundaries with clock minutes.
func NewClockAlignedAggregator(symbol Symbol, targetTF Timeframe) (*BarAggregator, error) {
	switch targetTF {
	case "5m", "15m", "30m", "1h", "1d":
	default:
		return nil, fmt.Errorf("invalid target timeframe: %q", targetTF)
	}
	epoch := time.Unix(0, 0).UTC()
	return &BarAggregator{
		symbol:      symbol,
		tf:          targetTF,
		sessionOpen: epoch,
		
		curEnd:      time.Time{},
	}, nil
}

func (a *BarAggregator) Push(bar MarketBar) (closed MarketBar, ok bool) {
	if bar.Symbol != a.symbol {
		return MarketBar{}, false
	}
	if bar.Timeframe != "1m" {
		return MarketBar{}, false
	}
	if bar.Time.Before(a.sessionOpen) {
		// Legitimate during warmup replay (yesterday's bars pushed to today's
		// session-aligned aggregator). Counter lets us spot unexpected spikes
		// without logging per bar — 52k WARN writes during warmup saturated I/O.
		aggRejectedSessionOpen.Add(1)
		return MarketBar{}, false
	}
	if bar.High < bar.Low || bar.Volume <= 0 {
		aggRejectedBadBar.Add(1)
		slog.Warn("aggregator: invalid bar shape — dropped",
			"symbol", a.symbol, "tf", a.tf, "bar_time", bar.Time,
			"high", bar.High, "low", bar.Low, "volume", bar.Volume)
		return MarketBar{}, false
	}

	dur := timeframeDuration(a.tf)
	end, ok := sessionAlignedBucketEnd(bar.Time, a.sessionOpen, dur)
	if !ok {
		aggRejectedBucketEnd.Add(1)
		slog.Warn("aggregator: sessionAlignedBucketEnd failed — dropped",
			"symbol", a.symbol, "tf", a.tf,
			"bar_time", bar.Time, "session_open", a.sessionOpen)
		return MarketBar{}, false
	}

	switch {
	case !a.hasCur:
		a.startNew(end, dur, bar)
	case end.After(a.curEnd):
		out := a.cur
		a.startNew(end, dur, bar)
		return out, true
	default:
		a.apply(bar)
	}

	if !bar.Time.Add(time.Minute).Before(a.curEnd) {
		out := a.cur
		a.hasCur = false
		a.curEnd = time.Time{}
		return out, true
	}

	return MarketBar{}, false
}

func (a *BarAggregator) Reset(sessionOpen time.Time) {
	a.sessionOpen = sessionOpen
	a.hasCur = false
	a.curEnd = time.Time{}
}

func timeframeDuration(tf Timeframe) time.Duration {
	switch tf {
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "30m":
		return 30 * time.Minute
	case "1h":
		return 60 * time.Minute
	case "1d":
		return 24 * time.Hour
	default:
		return 0
	}
}

func sessionAlignedBucketEnd(barTime, sessionOpen time.Time, tfDur time.Duration) (time.Time, bool) {
	if tfDur <= 0 {
		return time.Time{}, false
	}
	delta := barTime.Sub(sessionOpen)
	if delta < 0 {
		return time.Time{}, false
	}
	k := int(delta/tfDur) + 1
	return sessionOpen.Add(time.Duration(k) * tfDur), true
}

func (a *BarAggregator) startNew(end time.Time, dur time.Duration, bar MarketBar) {
	start := end.Add(-dur)
	a.curEnd = end

	agg, err := NewMarketBar(start, a.symbol, a.tf, bar.Open, bar.High, bar.Low, bar.Close, bar.Volume)
	if err != nil {
		aggRejectedStartNewFailed.Add(1)
		slog.Warn("aggregator: startNew NewMarketBar failed — state cleared, subsequent bars will retry",
			"symbol", a.symbol, "tf", a.tf, "bar_time", bar.Time,
			"bucket_start", start, "error", err)
		a.hasCur = false
		a.curEnd = time.Time{}
		return
	}
	agg.Suspect = bar.Suspect
	a.cur = agg; a.hasCur = true
}

func (a *BarAggregator) apply(bar MarketBar) {
	if !a.hasCur {
		return
	}
	if bar.High > a.cur.High {
		a.cur.High = bar.High
	}
	if bar.Low < a.cur.Low {
		a.cur.Low = bar.Low
	}
	a.cur.Close = bar.Close
	a.cur.Volume += bar.Volume
	a.cur.Suspect = a.cur.Suspect || bar.Suspect
}
