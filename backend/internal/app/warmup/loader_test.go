package warmup

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

type stubRepo struct {
	bars []domain.MarketBar
}

func (s *stubRepo) GetMarketBars(_ context.Context, _ domain.Symbol, _ domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	out := make([]domain.MarketBar, 0, len(s.bars))
	for _, b := range s.bars {
		if !b.Time.Before(from) && !b.Time.After(to) {
			out = append(out, b)
		}
	}
	return out, nil
}

func TestEquitySpecRequiredCounts(t *testing.T) {
	s := EquitySpec()
	if !s.RTHFilter {
		t.Fatal("EquitySpec must have RTHFilter=true")
	}
	want := map[domain.Timeframe]int{
		"1m": 800,
		"5m": 800,
		"1h": 200,
		"1d": 800,
	}
	for tf, exp := range want {
		if got := s.Required[tf]; got != exp {
			t.Errorf("Required[%q] = %d, want %d", tf, got, exp)
		}
	}
}

func TestCryptoSpecHasNoRTHFilter(t *testing.T) {
	if CryptoSpec().RTHFilter {
		t.Fatal("CryptoSpec must have RTHFilter=false")
	}
}

func TestIsRTH_RegularWednesday(t *testing.T) {
	loc := domain.NYLocation()
	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"09:29 ET pre-open", time.Date(2026, 4, 22, 9, 29, 0, 0, loc), false},
		{"09:30 ET open", time.Date(2026, 4, 22, 9, 30, 0, 0, loc), true},
		{"12:00 ET midday", time.Date(2026, 4, 22, 12, 0, 0, 0, loc), true},
		{"15:59 ET pre-close", time.Date(2026, 4, 22, 15, 59, 0, 0, loc), true},
		{"16:00 ET close", time.Date(2026, 4, 22, 16, 0, 0, 0, loc), false},
		{"08:00 ET pre-market", time.Date(2026, 4, 22, 8, 0, 0, 0, loc), false},
		{"18:00 ET post-market", time.Date(2026, 4, 22, 18, 0, 0, 0, loc), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRTH(c.t); got != c.want {
				t.Errorf("isRTH(%s) = %v, want %v", c.t, got, c.want)
			}
		})
	}
}

func TestIsRTH_Weekend(t *testing.T) {
	loc := domain.NYLocation()
	sat := time.Date(2026, 4, 25, 12, 0, 0, 0, loc)
	sun := time.Date(2026, 4, 26, 12, 0, 0, 0, loc)
	if isRTH(sat) || isRTH(sun) {
		t.Fatal("weekend midday must not count as RTH")
	}
}

func TestFilterRTH_DropsExtendedHours(t *testing.T) {
	loc := domain.NYLocation()
	mkBar := func(h, m int) domain.MarketBar {
		return domain.MarketBar{
			Symbol:    "SPY",
			Timeframe: "1m",
			Time:      time.Date(2026, 4, 22, h, m, 0, 0, loc),
		}
	}
	in := []domain.MarketBar{
		mkBar(7, 0),   // pre
		mkBar(9, 30),  // RTH
		mkBar(12, 0),  // RTH
		mkBar(15, 59), // RTH
		mkBar(18, 0),  // post
	}
	out := filterRTH(in)
	if len(out) != 3 {
		t.Fatalf("got %d RTH bars, want 3", len(out))
	}
	for _, b := range out {
		if !isRTH(b.Time) {
			t.Errorf("filterRTH let %s through", b.Time)
		}
	}
}

func TestLoad_TruncatesToRequired(t *testing.T) {
	loc := domain.NYLocation()
	now := time.Date(2026, 4, 22, 16, 0, 0, 0, loc)
	bars := make([]domain.MarketBar, 0, 1000)
	for i := range 1000 {
		bars = append(bars, domain.MarketBar{
			Symbol:    "SPY",
			Timeframe: "1m",
			Time:      now.Add(time.Duration(-1000+i) * time.Minute),
		})
	}
	repo := &stubRepo{bars: bars}
	spec := Spec{
		Required:  map[domain.Timeframe]int{"1m": 100},
		RTHFilter: false,
	}
	out, err := Load(context.Background(), repo, spec, "SPY", "1m", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 100 {
		t.Errorf("got %d bars, want 100 (truncation broken)", len(out))
	}
	if !out[len(out)-1].Time.Equal(bars[len(bars)-1].Time) {
		t.Error("Load did not keep the most recent bars")
	}
}

func TestLoad_RTHFilterDropsPreMarket(t *testing.T) {
	loc := domain.NYLocation()
	// 100 sequential 1m bars across pre-market and RTH on a Wednesday.
	day := time.Date(2026, 4, 22, 9, 0, 0, 0, loc)
	bars := make([]domain.MarketBar, 0, 100)
	for i := range 100 {
		bars = append(bars, domain.MarketBar{
			Symbol:    "SPY",
			Timeframe: "1m",
			Time:      day.Add(time.Duration(i) * time.Minute),
		})
	}
	repo := &stubRepo{bars: bars}
	spec := Spec{
		Required:  map[domain.Timeframe]int{"1m": 1000},
		RTHFilter: true,
	}
	out, err := Load(context.Background(), repo, spec, "SPY", "1m", day.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range out {
		if !isRTH(b.Time) {
			t.Errorf("RTH-filtered Load returned non-RTH bar at %s", b.Time)
		}
	}
}

func TestTrimWithBoot1_AppendsPreMarketBar(t *testing.T) {
	loc := domain.NYLocation()
	rthTue := time.Date(2026, 4, 21, 9, 30, 0, 0, loc)
	preMarketWed := time.Date(2026, 4, 22, 8, 30, 0, 0, loc)
	warmupEnd := time.Date(2026, 4, 22, 8, 31, 0, 0, loc)

	raw := []domain.MarketBar{
		{Symbol: "SPY", Timeframe: "1m", Time: rthTue},
		{Symbol: "SPY", Timeframe: "1m", Time: preMarketWed},
	}
	out := TrimWithBoot1(EquitySpec(), "1m", raw, warmupEnd)
	if len(out) != 2 {
		t.Fatalf("got %d bars, want 2 (RTH + boot1)", len(out))
	}
	if !out[0].Time.Equal(rthTue) {
		t.Errorf("first bar should be RTH, got %s", out[0].Time)
	}
	if !out[1].Time.Equal(preMarketWed) {
		t.Errorf("second bar should be the pre-market boot+1, got %s", out[1].Time)
	}
}

// Regression: filterRTH mutates the input slice's backing array (bars[:0]
// + append). If the boot+1 lookup runs after Trim, it walks a corrupted
// backing array and may pick up the wrong bar. Capture the candidate
// before Trim runs.
func TestTrimWithBoot1_HonorsBackingArrayMutation(t *testing.T) {
	loc := domain.NYLocation()
	rthMon := time.Date(2026, 4, 20, 9, 30, 0, 0, loc)
	rthTue := time.Date(2026, 4, 21, 9, 30, 0, 0, loc)
	preMarketWed := time.Date(2026, 4, 22, 8, 30, 0, 0, loc)
	warmupEnd := time.Date(2026, 4, 22, 8, 31, 0, 0, loc)

	raw := []domain.MarketBar{
		{Symbol: "SPY", Timeframe: "1m", Time: rthMon, Close: 100},
		{Symbol: "SPY", Timeframe: "1m", Time: rthTue, Close: 101},
		{Symbol: "SPY", Timeframe: "1m", Time: preMarketWed, Close: 102},
	}
	out := TrimWithBoot1(EquitySpec(), "1m", raw, warmupEnd)
	if len(out) != 3 {
		t.Fatalf("got %d bars, want 3 (2 RTH + 1 boot1)", len(out))
	}
	if out[2].Close != 102 {
		t.Errorf("boot+1 bar should be the pre-market 102 close, got %v (backing array was corrupted by filterRTH?)", out[2].Close)
	}
}

func TestTrimWithBoot1_NoDuplicateWhenAlreadyKept(t *testing.T) {
	loc := domain.NYLocation()
	rthBar := time.Date(2026, 4, 22, 9, 30, 0, 0, loc)
	warmupEnd := time.Date(2026, 4, 22, 9, 31, 0, 0, loc)

	raw := []domain.MarketBar{{Symbol: "SPY", Timeframe: "1m", Time: rthBar}}
	out := TrimWithBoot1(EquitySpec(), "1m", raw, warmupEnd)
	if len(out) != 1 {
		t.Fatalf("RTH bar already kept by Trim should not be re-appended: got %d", len(out))
	}
}

func TestLoad_UnknownTimeframeErrors(t *testing.T) {
	repo := &stubRepo{}
	_, err := Load(context.Background(), repo, EquitySpec(), "SPY", "15m", time.Now())
	if err == nil {
		t.Fatal("expected error for unknown timeframe")
	}
}
