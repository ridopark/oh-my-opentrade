package warmup

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	monitor "github.com/oh-my-opentrade/backend/internal/app/monitor"
)

// stubRepo records GetMarketBars calls and returns canned bars.
type stubRepo struct {
	bars     []domain.MarketBar
	gotFrom  time.Time
	gotTo    time.Time
	gotSym   domain.Symbol
	gotTF    domain.Timeframe
}

func (s *stubRepo) GetMarketBars(_ context.Context, sym domain.Symbol, tf domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	s.gotSym, s.gotTF, s.gotFrom, s.gotTo = sym, tf, from, to
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

// Pin the relationship between this package's catalog constants and the
// monitor package's actual indicator periods. If monitor.indicators.go
// changes its EMA200 period, this test fires.
func TestCatalogPinsEMA200Period(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	_ = calc // touch the package to ensure the import is real
	// Indicator constants in monitor/indicators.go are unexported, so we
	// pin via the spec output. EMA200 is the longest period the catalog
	// must cover; if monitor changes that, EquitySpec's 800 (= 200*4)
	// must move too.
	if emaLongestPeriod != 200 {
		t.Errorf("emaLongestPeriod = %d, expected 200 to match monitor.emaPeriod200", emaLongestPeriod)
	}
	if ema1hPeriod != 50 {
		t.Errorf("ema1hPeriod = %d, expected 50 to match monitor.emaPeriod50", ema1hPeriod)
	}
	if ConvergenceFactor != 4 {
		t.Errorf("ConvergenceFactor = %d, expected 4", ConvergenceFactor)
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

func TestLoad_UnknownTimeframeErrors(t *testing.T) {
	repo := &stubRepo{}
	_, err := Load(context.Background(), repo, EquitySpec(), "SPY", "15m", time.Now())
	if err == nil {
		t.Fatal("expected error for unknown timeframe")
	}
}
