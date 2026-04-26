package strategy

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestNoopDPSource_AlwaysReturnsFalse(t *testing.T) {
	src := noopDPSource{}
	dp, ok := src.Lookup("AAPL", time.Now())
	assert.False(t, ok)
	assert.Equal(t, domain.DarkPoolBar{}, dp, "noop source returns the zero value")
	assert.False(t, src.HasData(), "noop source must report HasData=false so the runner skips DP overlay blocks")
}

func TestStaticDPSource_LookupHit(t *testing.T) {
	t0 := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	bar := domain.DarkPoolBar{
		Symbol:    "AAPL",
		Time:      t0,
		Timeframe: "5m",
		DPVolume:  1000,
		DPVWAP:    100.5,
		TotalVolume: 5000,
		DPRatio:   0.20,
	}
	src := staticDPSource{lookup: map[DPLookupKey]domain.DarkPoolBar{
		{Symbol: "AAPL", Time: t0}: bar,
	}}

	got, ok := src.Lookup("AAPL", t0)
	assert.True(t, ok)
	assert.Equal(t, bar, got, "static source must return the exact bar stored under the key")
	assert.True(t, src.HasData())
}

func TestStaticDPSource_LookupMiss(t *testing.T) {
	t0 := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	src := staticDPSource{lookup: map[DPLookupKey]domain.DarkPoolBar{
		{Symbol: "AAPL", Time: t0}: {Symbol: "AAPL", Time: t0, DPVolume: 100},
	}}

	// Wrong symbol.
	_, ok := src.Lookup("MSFT", t0)
	assert.False(t, ok)

	// Wrong time.
	_, ok = src.Lookup("AAPL", t0.Add(5*time.Minute))
	assert.False(t, ok)
}

func TestStaticDPSource_HasData_EmptyMap(t *testing.T) {
	src := staticDPSource{lookup: map[DPLookupKey]domain.DarkPoolBar{}}
	assert.False(t, src.HasData(), "empty map must report HasData=false so the runner skips overlay blocks identical to live pre-Phase-4")
}

func TestStaticDPSource_HasData_NilMap(t *testing.T) {
	// Defensive: a zero-value staticDPSource should not panic on HasData.
	src := staticDPSource{}
	assert.False(t, src.HasData())
	_, ok := src.Lookup("AAPL", time.Now())
	assert.False(t, ok, "nil map lookup must return ok=false, not panic")
}

func TestSetDarkPoolLookup_WrapsInStaticSource(t *testing.T) {
	r := &Runner{dpSource: noopDPSource{}}
	t0 := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	bar := domain.DarkPoolBar{Symbol: "AAPL", Time: t0, DPVolume: 500}

	r.SetDarkPoolLookup(map[DPLookupKey]domain.DarkPoolBar{
		{Symbol: "AAPL", Time: t0}: bar,
	})

	assert.True(t, r.dpSource.HasData())
	got, ok := r.dpSource.Lookup("AAPL", t0)
	assert.True(t, ok)
	assert.Equal(t, bar, got)
}

func TestSetDarkPoolSource_OverridesLookup(t *testing.T) {
	r := &Runner{dpSource: noopDPSource{}}
	t0 := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)

	// First install a static source, then override with a different one.
	r.SetDarkPoolLookup(map[DPLookupKey]domain.DarkPoolBar{
		{Symbol: "AAPL", Time: t0}: {Symbol: "AAPL", Time: t0, DPVolume: 1},
	})
	r.SetDarkPoolSource(staticDPSource{lookup: map[DPLookupKey]domain.DarkPoolBar{
		{Symbol: "AAPL", Time: t0}: {Symbol: "AAPL", Time: t0, DPVolume: 999},
	}})

	got, ok := r.dpSource.Lookup("AAPL", t0)
	assert.True(t, ok)
	assert.InDelta(t, 999.0, got.DPVolume, 0.01, "last write wins between SetDarkPoolLookup and SetDarkPoolSource")
}

func TestSetDarkPoolSource_NilFallsBackToNoop(t *testing.T) {
	r := &Runner{dpSource: noopDPSource{}}
	r.SetDarkPoolLookup(map[DPLookupKey]domain.DarkPoolBar{
		{Symbol: "AAPL", Time: time.Now()}: {DPVolume: 1},
	})
	r.SetDarkPoolSource(nil)

	assert.False(t, r.dpSource.HasData(), "nil source must be replaced with noopDPSource so the runner never panics on r.dpSource.HasData()")
}
