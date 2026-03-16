package ibkr

import (
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/scmhub/ibsync"
)

type barAggregator struct {
	symbol    domain.Symbol
	timeframe domain.Timeframe
	period    time.Duration

	open      float64
	high      float64
	low       float64
	close     float64
	volume    float64
	vwapNumer float64
	vwapDenom float64
	barStart  time.Time
	hasData   bool
}

func newBarAggregator(symbol domain.Symbol, tf domain.Timeframe) *barAggregator {
	return &barAggregator{
		symbol:    symbol,
		timeframe: tf,
		period:    timeframePeriod(tf),
	}
}

func (a *barAggregator) Feed(rtb ibsync.RealTimeBar) *domain.MarketBar {
	return a.add(rtb)
}

func (a *barAggregator) add(rtb ibsync.RealTimeBar) *domain.MarketBar {
	t := time.Unix(rtb.Time, 0).UTC()
	barStart := t.Truncate(a.period)

	if !a.hasData {
		a.reset(barStart, rtb)
		return nil
	}

	if barStart == a.barStart {
		if rtb.High > a.high {
			a.high = rtb.High
		}
		if rtb.Low < a.low {
			a.low = rtb.Low
		}
		a.close = rtb.Close
		vol := rtb.Volume.Float()
		wap := rtb.Wap.Float()
		a.volume += vol
		a.vwapNumer += wap * vol
		a.vwapDenom += vol
		return nil
	}

	completed := &domain.MarketBar{
		Symbol:    a.symbol,
		Timeframe: a.timeframe,
		Time:      a.barStart,
		Open:      a.open,
		High:      a.high,
		Low:       a.low,
		Close:     a.close,
		Volume:    a.volume,
	}

	a.reset(barStart, rtb)
	return completed
}

func (a *barAggregator) reset(barStart time.Time, rtb ibsync.RealTimeBar) {
	a.barStart = barStart
	a.open = rtb.Open
	a.high = rtb.High
	a.low = rtb.Low
	a.close = rtb.Close
	vol := rtb.Volume.Float()
	wap := rtb.Wap.Float()
	a.volume = vol
	a.vwapNumer = wap * vol
	a.vwapDenom = vol
	a.hasData = true
}

func timeframePeriod(tf domain.Timeframe) time.Duration {
	switch string(tf) {
	case "1m":
		return time.Minute
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "1h":
		return time.Hour
	case "1d":
		return 24 * time.Hour
	default:
		return time.Minute
	}
}
