package backtest

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubUniverse is a minimal in-package UniverseHistoryPort so the
// backtest filter can be exercised without pulling in the DB or
// cross-package test helpers.
type stubUniverse struct {
	windows map[domain.Symbol][]ports.UniverseWindow
}

func (s *stubUniverse) WasTradable(_ context.Context, sym domain.Symbol, at time.Time) (bool, error) {
	for _, w := range s.windows[sym] {
		if at.Before(w.FromDate) {
			continue
		}
		if w.ToDate != nil && !at.Before(*w.ToDate) {
			continue
		}
		return true, nil
	}
	return false, nil
}
func (s *stubUniverse) WindowsFor(_ context.Context, sym domain.Symbol) ([]ports.UniverseWindow, error) {
	return s.windows[sym], nil
}
func (s *stubUniverse) Upsert(_ context.Context, w ports.UniverseWindow) error {
	s.windows[w.Symbol] = append(s.windows[w.Symbol], w)
	return nil
}
func (s *stubUniverse) ActiveSymbols(_ context.Context, _ time.Time) ([]domain.Symbol, error) {
	return nil, nil
}

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestSessionResolver_CheckUniverse(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	// HOOD IPO 2021-07-29; delisted hypothetically 2023-06-01; relisted 2024-01-01.
	delist := date(2023, 6, 1)
	store := &stubUniverse{windows: map[domain.Symbol][]ports.UniverseWindow{
		"HOOD": {
			{Symbol: "HOOD", FromDate: date(2021, 7, 29), ToDate: &delist, Source: "seed"},
			{Symbol: "HOOD", FromDate: date(2024, 1, 1), Source: "seed"},
		},
		"AAPL": {
			{Symbol: "AAPL", FromDate: date(2020, 1, 1), Source: "seed"},
		},
	}}

	tests := []struct {
		name     string
		enforce  bool
		port     ports.UniverseHistoryPort
		sym      domain.Symbol
		from, to time.Time
		want     bool
	}{
		{
			name: "filter disabled - always tradable",
			sym:  "UNKNOWN", from: date(1990, 1, 1), to: date(1991, 1, 1),
			want: true,
		},
		{
			name: "enforce but no port - fail open",
			enforce: true, port: nil, sym: "HOOD",
			from: date(2019, 1, 1), to: date(2020, 1, 1),
			want: true,
		},
		{
			name:    "range entirely before first window",
			enforce: true, port: store, sym: "HOOD",
			from: date(2020, 1, 1), to: date(2021, 1, 1),
			want: false,
		},
		{
			name:    "range inside first tradable window",
			enforce: true, port: store, sym: "HOOD",
			from: date(2022, 1, 1), to: date(2023, 1, 1),
			want: true,
		},
		{
			name:    "range lands in delist gap",
			enforce: true, port: store, sym: "HOOD",
			from: date(2023, 7, 1), to: date(2023, 12, 1),
			want: false,
		},
		{
			name:    "range straddles delist - partial overlap",
			enforce: true, port: store, sym: "HOOD",
			from: date(2023, 5, 1), to: date(2023, 8, 1),
			want: true,
		},
		{
			name:    "range inside relisted window",
			enforce: true, port: store, sym: "HOOD",
			from: date(2024, 6, 1), to: date(2024, 12, 1),
			want: true,
		},
		{
			name:    "always-tradable symbol",
			enforce: true, port: store, sym: "AAPL",
			from: date(2024, 1, 1), to: date(2025, 1, 1),
			want: true,
		},
		{
			name:    "symbol absent from universe",
			enforce: true, port: store, sym: "NOPE",
			from: date(2024, 1, 1), to: date(2025, 1, 1),
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewSessionResolver(loc)
			if tc.enforce || tc.port != nil {
				r.SetUniverseHistory(tc.port, tc.enforce)
			}
			got, err := r.CheckUniverse(context.Background(), tc.sym, tc.from, tc.to)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSessionResolver_TradableAt(t *testing.T) {
	toDate := date(2023, 6, 1)
	windows := []ports.UniverseWindow{
		{Symbol: "X", FromDate: date(2020, 1, 1), ToDate: &toDate},
		{Symbol: "X", FromDate: date(2024, 1, 1)},
	}
	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"before any window", date(2019, 1, 1), false},
		{"inside first window", date(2021, 6, 15), true},
		{"on upper bound (exclusive)", date(2023, 6, 1), false},
		{"gap", date(2023, 9, 1), false},
		{"inside second window", date(2024, 6, 1), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tradableAt(windows, tc.at))
		})
	}
}

func TestSessionResolver_SetUniverseHistory_NilPortDisablesEnforce(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	r := NewSessionResolver(loc)
	// enforce=true but port=nil must still behave as disabled
	// (fail-open semantics documented on BacktestConfig.EnforceUniverseHistory).
	r.SetUniverseHistory(nil, true)
	ok, err := r.CheckUniverse(context.Background(), "ZZZZ", date(2020, 1, 1), date(2021, 1, 1))
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestSessionResolver_GetBarsSince_CryptoCap24H verifies the crypto-vs-equity
// end-of-day cap. Equity caps at 16:00 ET; crypto (symbol contains "/") caps
// at 24:00 ET. Covers the Load24H-adjacent crypto session path that previously
// had no assertions.
func TestSessionResolver_GetBarsSince_CryptoCap24H(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	r := NewSessionResolver(loc)

	day := time.Date(2026, 4, 15, 0, 0, 0, 0, loc)
	at := func(hour, min int) time.Time {
		return time.Date(2026, 4, 15, hour, min, 0, 0, loc)
	}
	bars := []domain.MarketBar{
		{Time: at(10, 0), Close: 100},
		{Time: at(15, 30), Close: 101},
		{Time: at(18, 0), Close: 102}, // post-RTH for equities; in-session for crypto
		{Time: at(22, 0), Close: 103},
	}
	r.PopulateBarCache("AAPL", bars)
	r.PopulateBarCache("BTC/USD", bars)

	// Equity cap 16:00: only the 10:00 and 15:30 bars are in-session.
	equityBars := r.GetBarsSince(context.Background(), nil, "AAPL", day.Add(9*time.Hour))
	assert.Len(t, equityBars, 2, "equity GetBarsSince should cap at 16:00 ET")

	// Crypto cap 24:00: all four bars are in-session.
	cryptoBars := r.GetBarsSince(context.Background(), nil, "BTC/USD", day.Add(9*time.Hour))
	assert.Len(t, cryptoBars, 4, "crypto GetBarsSince should include full 24h")
}

// TestSessionResolver_Stats_TracksUnknownSymbolHits - seeding gap visibility.
// CheckUniverse on a symbol with no windows must increment unknownSymbolHits
// so the end-of-run summary can surface "you backtested with an incomplete
// universe seed" instead of silently marking all those symbols non-tradable.
func TestSessionResolver_Stats_TracksUnknownSymbolHits(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	store := &stubUniverse{windows: map[domain.Symbol][]ports.UniverseWindow{
		"AAPL": {{Symbol: "AAPL", FromDate: date(2020, 1, 1), Source: "seed"}},
	}}
	r := NewSessionResolver(loc)
	r.SetUniverseHistory(store, true)

	// Seeded symbol - no counter increment.
	_, err = r.CheckUniverse(context.Background(), "AAPL", date(2024, 1, 1), date(2025, 1, 1))
	require.NoError(t, err)
	// Unseeded symbols - each must count.
	for _, sym := range []domain.Symbol{"NOPE", "ALSO_NOPE"} {
		_, err = r.CheckUniverse(context.Background(), sym, date(2024, 1, 1), date(2025, 1, 1))
		require.NoError(t, err)
	}

	scanErrs, unknownSyms := r.Stats()
	assert.Equal(t, 0, scanErrs)
	assert.Equal(t, 2, unknownSyms)
}
