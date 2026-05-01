package optionsimport

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/options"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAlpacaClient struct {
	mu               sync.Mutex
	contractsByDate  map[string][]domain.OptionContract     // key: YYYY-MM-DD
	barsByOCCAndDate map[string]map[string]*domain.MarketBar // key1: OCC, key2: YYYY-MM-DD
	contractsErr     error
	barErr           error
	barCalls         int
}

func (f *fakeAlpacaClient) ListOptionContractsAsOf(_ context.Context, _ domain.Symbol, asOf time.Time, _ int) ([]domain.OptionContract, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.contractsErr != nil {
		return nil, f.contractsErr
	}
	return f.contractsByDate[asOf.Format("2006-01-02")], nil
}

func (f *fakeAlpacaClient) GetOptionDayBar(_ context.Context, _ string, occ domain.Symbol, date time.Time) (*domain.MarketBar, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.barCalls++
	if f.barErr != nil {
		return nil, f.barErr
	}
	if m, ok := f.barsByOCCAndDate[string(occ)]; ok {
		return m[date.Format("2006-01-02")], nil
	}
	return nil, nil
}

func dayBar(occ string, t time.Time, close float64) *domain.MarketBar {
	bar, err := domain.NewMarketBar(t, domain.Symbol(occ), domain.Timeframe("1d"), close, close, close, close, 100)
	if err != nil {
		panic(err)
	}
	return &bar
}

func contractFor(occ, underlying string, expiry time.Time, strike float64, right domain.OptionRight) domain.OptionContract {
	return domain.OptionContract{
		ContractSymbol: domain.Symbol(occ),
		Underlying:     domain.Symbol(underlying),
		Expiry:         expiry,
		Strike:         strike,
		Right:          right,
		Style:          domain.OptionStyleAmerican,
		Multiplier:     100,
	}
}

func staticSpot(price float64) SpotLookup {
	return func(context.Context, domain.Symbol, time.Time) (float64, error) {
		return price, nil
	}
}

// computeFairCallPrice runs forward BSM to derive a price the inverter
// can recover. Used to seed test bars whose IV will round-trip.
func computeFairCallPrice(t *testing.T, s, k, dteYears, r, sigma float64) float64 {
	t.Helper()
	price, _, _, _ := options.BSMPrice(s, k, dteYears, r, sigma, true)
	return price
}

func newAlpacaSvc(t *testing.T, client *fakeAlpacaClient, repo *fakeHistRepo, spot SpotLookup) *AlpacaService {
	t.Helper()
	return NewAlpacaService(client, "http://data.test", repo, spot, AlpacaConfig{
		DTERangeDays:     60,
		MaxConcurrency:   1,
		DefaultSpreadPct: 0.05,
		RiskFreeRate:     0.045,
	}, zerolog.Nop())
}

func TestCaptureDate_HappyPath_BSMRoundTripsAndRowPersists(t *testing.T) {
	day := time.Date(2024, 6, 14, 0, 0, 0, 0, time.UTC) // Friday
	expiry := time.Date(2024, 7, 19, 0, 0, 0, 0, time.UTC)
	strike := 100.0
	spot := 100.0
	dteYears := float64(int(expiry.Sub(day).Hours()/24)) / 365.0
	fair := computeFairCallPrice(t, spot, strike, dteYears, 0.045, 0.30)

	occ := "AAPL240719C00100000"
	client := &fakeAlpacaClient{
		contractsByDate:  map[string][]domain.OptionContract{day.Format("2006-01-02"): {contractFor(occ, "AAPL", expiry, strike, domain.OptionRightCall)}},
		barsByOCCAndDate: map[string]map[string]*domain.MarketBar{occ: {day.Format("2006-01-02"): dayBar(occ, day, fair)}},
	}
	repo := &fakeHistRepo{}
	svc := newAlpacaSvc(t, client, repo, staticSpot(spot))

	got, err := svc.CaptureDate(context.Background(), "AAPL", day)
	require.NoError(t, err)
	assert.Equal(t, AlpacaCaptureSaved, got)
	require.Len(t, repo.batches, 1)
	require.Len(t, repo.batches[0], 1)
	row := repo.batches[0][0]
	assert.InDelta(t, 0.30, row.IV, 1e-3, "IV round-trips to seeded sigma within tolerance")
	assert.Greater(t, row.Bid, 0.0)
	assert.Greater(t, row.Ask, row.Bid)
	assert.Equal(t, day, row.Date)
	assert.Equal(t, domain.Symbol("AAPL"), row.Symbol)
}

func TestCaptureDate_HasDataTrue_SkipsWithoutCalls(t *testing.T) {
	day := time.Date(2024, 6, 14, 0, 0, 0, 0, time.UTC)
	repo := &fakeHistRepo{hasData: map[string]bool{"AAPL|2024-06-14": true}}
	client := &fakeAlpacaClient{}
	svc := newAlpacaSvc(t, client, repo, staticSpot(100))

	got, err := svc.CaptureDate(context.Background(), "AAPL", day)
	require.NoError(t, err)
	assert.Equal(t, AlpacaCaptureSkipped, got)
	assert.Equal(t, 0, client.barCalls, "no day-bar fetches when HasData skips the date")
	assert.Empty(t, repo.batches)
}

func TestCaptureDate_SpotZero_SkipsWithoutError(t *testing.T) {
	day := time.Date(2024, 6, 14, 0, 0, 0, 0, time.UTC)
	repo := &fakeHistRepo{}
	client := &fakeAlpacaClient{}
	svc := newAlpacaSvc(t, client, repo, staticSpot(0))

	got, err := svc.CaptureDate(context.Background(), "AAPL", day)
	require.NoError(t, err, "spot=0 must surface as a quiet skip, not an error")
	assert.Equal(t, AlpacaCaptureSkipped, got)
	assert.Empty(t, repo.batches)
}

func TestCaptureDate_DropsContractsWithBarAtPriceFloor(t *testing.T) {
	day := time.Date(2024, 6, 14, 0, 0, 0, 0, time.UTC)
	expiry := time.Date(2024, 7, 19, 0, 0, 0, 0, time.UTC)
	occLow := "AAPL240719C00200000" // way OTM, bar will be at 0.01 floor
	occGood := "AAPL240719C00100000"
	dteYears := float64(int(expiry.Sub(day).Hours()/24)) / 365.0
	fair := computeFairCallPrice(t, 100, 100, dteYears, 0.045, 0.30)

	client := &fakeAlpacaClient{
		contractsByDate: map[string][]domain.OptionContract{day.Format("2006-01-02"): {
			contractFor(occLow, "AAPL", expiry, 200, domain.OptionRightCall),
			contractFor(occGood, "AAPL", expiry, 100, domain.OptionRightCall),
		}},
		barsByOCCAndDate: map[string]map[string]*domain.MarketBar{
			occLow:  {day.Format("2006-01-02"): dayBar(occLow, day, 0.01)},
			occGood: {day.Format("2006-01-02"): dayBar(occGood, day, fair)},
		},
	}
	repo := &fakeHistRepo{}
	svc := newAlpacaSvc(t, client, repo, staticSpot(100))

	got, err := svc.CaptureDate(context.Background(), "AAPL", day)
	require.NoError(t, err)
	assert.Equal(t, AlpacaCaptureSaved, got)
	require.Len(t, repo.batches, 1)
	assert.Len(t, repo.batches[0], 1, "0.01-floor bar dropped, only the priced strike persists")
}

func TestCaptureDate_DropsContractsWithMissingBar(t *testing.T) {
	day := time.Date(2024, 6, 14, 0, 0, 0, 0, time.UTC)
	expiry := time.Date(2024, 7, 19, 0, 0, 0, 0, time.UTC)
	occMissing := "AAPL240719C00150000"
	occGood := "AAPL240719C00100000"
	dteYears := float64(int(expiry.Sub(day).Hours()/24)) / 365.0
	fair := computeFairCallPrice(t, 100, 100, dteYears, 0.045, 0.30)

	client := &fakeAlpacaClient{
		contractsByDate: map[string][]domain.OptionContract{day.Format("2006-01-02"): {
			contractFor(occMissing, "AAPL", expiry, 150, domain.OptionRightCall),
			contractFor(occGood, "AAPL", expiry, 100, domain.OptionRightCall),
		}},
		// occMissing has no bar entry → GetOptionDayBar returns nil
		barsByOCCAndDate: map[string]map[string]*domain.MarketBar{
			occGood: {day.Format("2006-01-02"): dayBar(occGood, day, fair)},
		},
	}
	repo := &fakeHistRepo{}
	svc := newAlpacaSvc(t, client, repo, staticSpot(100))

	got, err := svc.CaptureDate(context.Background(), "AAPL", day)
	require.NoError(t, err)
	assert.Equal(t, AlpacaCaptureSaved, got)
	require.Len(t, repo.batches, 1)
	assert.Len(t, repo.batches[0], 1)
}

func TestCaptureDate_ExpiredContractDropped(t *testing.T) {
	day := time.Date(2024, 6, 14, 0, 0, 0, 0, time.UTC)
	expiredYesterday := time.Date(2024, 6, 13, 0, 0, 0, 0, time.UTC)
	occ := "AAPL240613C00100000"
	client := &fakeAlpacaClient{
		contractsByDate: map[string][]domain.OptionContract{day.Format("2006-01-02"): {
			contractFor(occ, "AAPL", expiredYesterday, 100, domain.OptionRightCall),
		}},
		barsByOCCAndDate: map[string]map[string]*domain.MarketBar{occ: {day.Format("2006-01-02"): dayBar(occ, day, 1.50)}},
	}
	repo := &fakeHistRepo{}
	svc := newAlpacaSvc(t, client, repo, staticSpot(100))

	got, err := svc.CaptureDate(context.Background(), "AAPL", day)
	require.NoError(t, err)
	assert.Equal(t, AlpacaCaptureSkipped, got, "all contracts dropped (DTE<=0) -> no rows -> skip")
	assert.Empty(t, repo.batches)
}

func TestCaptureDate_NoContracts_Skips(t *testing.T) {
	day := time.Date(2024, 6, 14, 0, 0, 0, 0, time.UTC)
	client := &fakeAlpacaClient{contractsByDate: map[string][]domain.OptionContract{}}
	repo := &fakeHistRepo{}
	svc := newAlpacaSvc(t, client, repo, staticSpot(100))

	got, err := svc.CaptureDate(context.Background(), "AAPL", day)
	require.NoError(t, err)
	assert.Equal(t, AlpacaCaptureSkipped, got)
	assert.Empty(t, repo.batches)
}

func TestCaptureDate_HasDataError_BubblesUp(t *testing.T) {
	repo := &fakeHistRepo{hasErr: errors.New("db down")}
	client := &fakeAlpacaClient{}
	svc := newAlpacaSvc(t, client, repo, staticSpot(100))

	_, err := svc.CaptureDate(context.Background(), "AAPL", time.Date(2024, 6, 14, 0, 0, 0, 0, time.UTC))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HasData")
}

func TestCaptureDate_ListContractsError_BubblesUp(t *testing.T) {
	client := &fakeAlpacaClient{contractsErr: errors.New("alpaca rate limit")}
	repo := &fakeHistRepo{}
	svc := newAlpacaSvc(t, client, repo, staticSpot(100))

	_, err := svc.CaptureDate(context.Background(), "AAPL", time.Date(2024, 6, 14, 0, 0, 0, 0, time.UTC))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list contracts")
}

func TestCaptureDate_SaveBatchError_BubblesUp(t *testing.T) {
	day := time.Date(2024, 6, 14, 0, 0, 0, 0, time.UTC)
	expiry := time.Date(2024, 7, 19, 0, 0, 0, 0, time.UTC)
	dteYears := float64(int(expiry.Sub(day).Hours()/24)) / 365.0
	fair := computeFairCallPrice(t, 100, 100, dteYears, 0.045, 0.30)
	occ := "AAPL240719C00100000"
	client := &fakeAlpacaClient{
		contractsByDate:  map[string][]domain.OptionContract{day.Format("2006-01-02"): {contractFor(occ, "AAPL", expiry, 100, domain.OptionRightCall)}},
		barsByOCCAndDate: map[string]map[string]*domain.MarketBar{occ: {day.Format("2006-01-02"): dayBar(occ, day, fair)}},
	}
	repo := &fakeHistRepo{saveErr: errors.New("constraint violation")}
	svc := newAlpacaSvc(t, client, repo, staticSpot(100))

	_, err := svc.CaptureDate(context.Background(), "AAPL", day)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SaveBatch")
}

func TestRun_RejectsEmptySymbols(t *testing.T) {
	svc := newAlpacaSvc(t, &fakeAlpacaClient{}, &fakeHistRepo{}, staticSpot(100))
	err := svc.Run(context.Background(), nil, time.Now(), time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one symbol")
}

func TestRun_RejectsBackwardsRange(t *testing.T) {
	svc := newAlpacaSvc(t, &fakeAlpacaClient{}, &fakeHistRepo{}, staticSpot(100))
	from := time.Date(2024, 6, 14, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, -5)
	err := svc.Run(context.Background(), []string{"AAPL"}, from, to)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "before")
}

func TestRun_SkipsWeekends(t *testing.T) {
	// Friday + Saturday + Sunday + Monday window. Only Friday and Monday
	// should hit the importer; Saturday/Sunday must be filtered.
	friday := time.Date(2024, 6, 14, 0, 0, 0, 0, time.UTC)
	monday := time.Date(2024, 6, 17, 0, 0, 0, 0, time.UTC)
	expiry := time.Date(2024, 7, 19, 0, 0, 0, 0, time.UTC)
	occ := "AAPL240719C00100000"
	dteYearsFri := float64(int(expiry.Sub(friday).Hours()/24)) / 365.0
	dteYearsMon := float64(int(expiry.Sub(monday).Hours()/24)) / 365.0

	client := &fakeAlpacaClient{
		contractsByDate: map[string][]domain.OptionContract{
			friday.Format("2006-01-02"): {contractFor(occ, "AAPL", expiry, 100, domain.OptionRightCall)},
			monday.Format("2006-01-02"): {contractFor(occ, "AAPL", expiry, 100, domain.OptionRightCall)},
		},
		barsByOCCAndDate: map[string]map[string]*domain.MarketBar{
			occ: {
				friday.Format("2006-01-02"): dayBar(occ, friday, computeFairCallPrice(t, 100, 100, dteYearsFri, 0.045, 0.30)),
				monday.Format("2006-01-02"): dayBar(occ, monday, computeFairCallPrice(t, 100, 100, dteYearsMon, 0.045, 0.30)),
			},
		},
	}
	repo := &fakeHistRepo{}
	svc := newAlpacaSvc(t, client, repo, staticSpot(100))

	err := svc.Run(context.Background(), []string{"AAPL"}, friday, monday)
	require.NoError(t, err)
	assert.Len(t, repo.batches, 2, "Sat/Sun filtered, Fri+Mon persist one batch each")
}
