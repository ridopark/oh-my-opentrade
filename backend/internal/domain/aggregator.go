package domain

import (
	"errors"
	"fmt"
	"time"
)

type BarAggregator struct {
	symbol      Symbol
	tf          Timeframe
	sessionOpen time.Time
	cur         *MarketBar
	curEnd      time.Time
}

func NewBarAggregator(symbol Symbol, targetTF Timeframe, sessionOpen time.Time) (*BarAggregator, error) {
	if sessionOpen.IsZero() {
		return nil, errors.New("sessionOpen is required")
	}
	switch targetTF {
	case "5m", "15m", "1h", "1d":
	default:
		return nil, fmt.Errorf("invalid target timeframe: %q", targetTF)
	}
	return &BarAggregator{
		symbol:      symbol,
		tf:          targetTF,
		sessionOpen: sessionOpen,
		cur:         nil,
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
	case "5m", "15m", "1h", "1d":
	default:
		return nil, fmt.Errorf("invalid target timeframe: %q", targetTF)
	}
	epoch := time.Unix(0, 0).UTC()
	return &BarAggregator{
		symbol:      symbol,
		tf:          targetTF,
		sessionOpen: epoch,
		cur:         nil,
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
		return MarketBar{}, false
	}
	if bar.High < bar.Low || bar.Volume <= 0 {
		return MarketBar{}, false
	}

	dur := timeframeDuration(a.tf)
	end, ok := sessionAlignedBucketEnd(bar.Time, a.sessionOpen, dur)
	if !ok {
		return MarketBar{}, false
	}

	switch {
	case a.cur == nil:
		a.startNew(end, dur, bar)
	case end.After(a.curEnd):
		out := *a.cur
		a.startNew(end, dur, bar)
		return out, true
	default:
		a.apply(bar)
	}

	if !bar.Time.Add(time.Minute).Before(a.curEnd) {
		out := *a.cur
		a.cur = nil
		a.curEnd = time.Time{}
		return out, true
	}

	return MarketBar{}, false
}

func (a *BarAggregator) Reset(sessionOpen time.Time) {
	a.sessionOpen = sessionOpen
	a.cur = nil
	a.curEnd = time.Time{}
}

func timeframeDuration(tf Timeframe) time.Duration {
	switch tf {
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
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
		a.cur = nil
		a.curEnd = time.Time{}
		return
	}
	agg.Suspect = bar.Suspect
	a.cur = &agg
}

func (a *BarAggregator) apply(bar MarketBar) {
	if a.cur == nil {
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
