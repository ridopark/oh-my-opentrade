package ibkr

import (
	"fmt"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/scmhub/ibsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeRTB(unixTs int64, open, high, low, close, vol, wap float64) ibsync.RealTimeBar {
	rtb := ibsync.NewRealTimeBar()
	rtb.Time = unixTs
	rtb.Open = open
	rtb.High = high
	rtb.Low = low
	rtb.Close = close
	rtb.Volume = ibsync.StringToDecimal(fmt.Sprintf("%.6f", vol))
	rtb.Wap = ibsync.StringToDecimal(fmt.Sprintf("%.6f", wap))
	return rtb
}

var (
	min0 = time.Date(2025, 1, 2, 9, 30, 0, 0, time.UTC).Unix()
	min1 = time.Date(2025, 1, 2, 9, 31, 0, 0, time.UTC).Unix()
)

func TestBarAggregator_1Min_12BarsYieldOneBar(t *testing.T) {
	agg := newBarAggregator("AAPL", "1m")
	for i := 0; i < 12; i++ {
		mb := agg.Feed(makeRTB(min0+int64(i*5), 100, 101, 99, 100, 100, 100))
		assert.Nil(t, mb, "bar %d: should not emit until minute boundary", i)
	}
	mb := agg.Feed(makeRTB(min1, 102, 103, 101, 102, 50, 102))
	require.NotNil(t, mb, "crossing minute boundary must emit bar")
}

func TestBarAggregator_OHLCV_Correct(t *testing.T) {
	agg := newBarAggregator("AAPL", "1m")
	agg.Feed(makeRTB(min0+0, 100, 105, 98, 102, 500, 101))
	agg.Feed(makeRTB(min0+5, 102, 103, 99, 101, 300, 102))
	agg.Feed(makeRTB(min0+10, 101, 102, 97, 100, 200, 100))

	mb := agg.Feed(makeRTB(min1, 99, 99, 99, 99, 10, 99))
	require.NotNil(t, mb)
	assert.Equal(t, float64(100), mb.Open)
	assert.Equal(t, float64(105), mb.High)
	assert.Equal(t, float64(97), mb.Low)
	assert.Equal(t, float64(100), mb.Close)
	assert.Equal(t, float64(1000), mb.Volume)
}

func TestBarAggregator_Symbol_Timeframe_Propagated(t *testing.T) {
	agg := newBarAggregator("MSFT", "5m")
	agg.Feed(makeRTB(min0, 200, 201, 199, 200, 100, 200))
	mb := agg.Feed(makeRTB(min0+int64(5*60), 201, 202, 200, 201, 50, 201))
	require.NotNil(t, mb)
	assert.Equal(t, domain.Symbol("MSFT"), mb.Symbol)
	assert.Equal(t, domain.Timeframe("5m"), mb.Timeframe)
}

func TestBarAggregator_FirstBar_ReturnsNil(t *testing.T) {
	agg := newBarAggregator("AAPL", "1m")
	mb := agg.Feed(makeRTB(min0, 100, 101, 99, 100, 100, 100))
	assert.Nil(t, mb)
}
