package optionsimport

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeOptionsMarket struct {
	mu        sync.Mutex
	chainsBy  map[domain.OptionRight][]domain.OptionContractSnapshot
	errByRight map[domain.OptionRight]error
	calls     int
}

func (f *fakeOptionsMarket) GetOptionChain(
	_ context.Context,
	_ domain.Symbol,
	_ time.Time,
	right domain.OptionRight,
	_ int,
	_ int,
) ([]domain.OptionContractSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err, ok := f.errByRight[right]; ok {
		return nil, err
	}
	return f.chainsBy[right], nil
}

type fakeHistRepo struct {
	mu       sync.Mutex
	hasData  map[string]bool // key: symbol|YYYY-MM-DD
	hasErr   error
	saveErr  error
	batches  [][]domain.HistoricalOptionChainRow
}

func (f *fakeHistRepo) GetHistoricalChain(
	context.Context,
	domain.Symbol,
	time.Time,
	domain.OptionRight,
	int,
	int,
) ([]domain.HistoricalOptionChainRow, error) {
	return nil, nil
}

func (f *fakeHistRepo) GetHistoricalChainBulk(
	context.Context,
	[]domain.Symbol,
	time.Time,
	time.Time,
) ([]domain.HistoricalOptionChainRow, error) {
	return nil, nil
}

func (f *fakeHistRepo) GetHistoricalContract(
	context.Context,
	domain.Symbol,
	time.Time,
	float64,
	time.Time,
	domain.OptionRight,
) (*domain.HistoricalOptionChainRow, error) {
	return nil, nil
}

func (f *fakeHistRepo) HasData(_ context.Context, sym domain.Symbol, date time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hasErr != nil {
		return false, f.hasErr
	}
	return f.hasData[string(sym)+"|"+date.Format("2006-01-02")], nil
}

func (f *fakeHistRepo) SaveBatch(_ context.Context, rows []domain.HistoricalOptionChainRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.batches = append(f.batches, rows)
	return nil
}

func contractSnap(strike float64, right domain.OptionRight, expiry time.Time, bid, ask, iv, delta float64) domain.OptionContractSnapshot {
	contract, _ := domain.NewOptionContract("AAPL", expiry, strike, right, domain.OptionStyleAmerican)
	return domain.OptionContractSnapshot{
		OptionContract: contract,
		OptionQuote: domain.OptionQuote{
			Bid:       bid,
			Ask:       ask,
			Timestamp: time.Now(),
		},
		Greeks: domain.Greeks{
			Delta: delta,
			IV:    iv,
		},
	}
}

func newService(t *testing.T, optionsData *fakeOptionsMarket, repo *fakeHistRepo) *ForwardCaptureService {
	t.Helper()
	return NewForwardCaptureService(
		ForwardCaptureConfig{Symbols: []string{"AAPL"}, Concurrency: 1},
		optionsData,
		repo,
		zerolog.Nop(),
	)
}

func TestCaptureDay_HappyPath_PersistsCallsAndPuts(t *testing.T) {
	expiry := time.Now().AddDate(0, 0, 14)
	chains := map[domain.OptionRight][]domain.OptionContractSnapshot{
		domain.OptionRightCall: {
			contractSnap(150, domain.OptionRightCall, expiry, 1.20, 1.25, 0.30, 0.45),
			contractSnap(155, domain.OptionRightCall, expiry, 0.55, 0.60, 0.32, 0.30),
		},
		domain.OptionRightPut: {
			contractSnap(150, domain.OptionRightPut, expiry, 1.10, 1.15, 0.31, -0.45),
		},
	}
	market := &fakeOptionsMarket{chainsBy: chains}
	repo := &fakeHistRepo{}
	svc := newService(t, market, repo)

	day := time.Date(2026, 5, 1, 16, 30, 0, 0, time.UTC)
	got, err := svc.CaptureDay(context.Background(), "AAPL", day)
	require.NoError(t, err)
	assert.Equal(t, CaptureWroteRows, got)
	require.Len(t, repo.batches, 1)
	assert.Len(t, repo.batches[0], 3, "all three snapshots persisted (2 calls + 1 put)")
	for _, row := range repo.batches[0] {
		assert.Equal(t, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), row.Date,
			"row date is the day midnight UTC, not the wall-clock fetch time")
		assert.Equal(t, domain.Symbol("AAPL"), row.Symbol)
	}
}

func TestCaptureDay_HasDataTrue_SkipsFetchAndSave(t *testing.T) {
	market := &fakeOptionsMarket{}
	repo := &fakeHistRepo{hasData: map[string]bool{"AAPL|2026-05-01": true}}
	svc := newService(t, market, repo)

	got, err := svc.CaptureDay(context.Background(), "AAPL", time.Date(2026, 5, 1, 16, 30, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, CaptureSkippedExisting, got)
	assert.Equal(t, 0, market.calls, "GetOptionChain must not be called when HasData is true")
	assert.Empty(t, repo.batches)
}

func TestCaptureDay_DropsSnapshotsWithNoQuote(t *testing.T) {
	expiry := time.Now().AddDate(0, 0, 14)
	chains := map[domain.OptionRight][]domain.OptionContractSnapshot{
		domain.OptionRightCall: {
			contractSnap(150, domain.OptionRightCall, expiry, 0.0, 0.0, 0.30, 0.45),
			contractSnap(155, domain.OptionRightCall, expiry, 0.55, 0.60, 0.32, 0.30),
		},
	}
	market := &fakeOptionsMarket{chainsBy: chains}
	repo := &fakeHistRepo{}
	svc := newService(t, market, repo)

	got, err := svc.CaptureDay(context.Background(), "AAPL", time.Date(2026, 5, 1, 16, 30, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, CaptureWroteRows, got)
	require.Len(t, repo.batches, 1)
	assert.Len(t, repo.batches[0], 1, "the bid=ask=0 snapshot is filtered out")
}

func TestCaptureDay_NoChainsAvailable_ReturnsNoSnapshots(t *testing.T) {
	market := &fakeOptionsMarket{chainsBy: map[domain.OptionRight][]domain.OptionContractSnapshot{}}
	repo := &fakeHistRepo{}
	svc := newService(t, market, repo)

	got, err := svc.CaptureDay(context.Background(), "AAPL", time.Date(2026, 5, 1, 16, 30, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, CaptureNoSnapshots, got)
	assert.Empty(t, repo.batches)
}

func TestCaptureDay_OneRightFails_OtherRightStillPersisted(t *testing.T) {
	expiry := time.Now().AddDate(0, 0, 14)
	market := &fakeOptionsMarket{
		chainsBy: map[domain.OptionRight][]domain.OptionContractSnapshot{
			domain.OptionRightPut: {contractSnap(150, domain.OptionRightPut, expiry, 1.10, 1.15, 0.31, -0.45)},
		},
		errByRight: map[domain.OptionRight]error{domain.OptionRightCall: errors.New("upstream 503")},
	}
	repo := &fakeHistRepo{}
	svc := newService(t, market, repo)

	got, err := svc.CaptureDay(context.Background(), "AAPL", time.Date(2026, 5, 1, 16, 30, 0, 0, time.UTC))
	require.NoError(t, err, "per-right fetch errors are logged, not bubbled")
	assert.Equal(t, CaptureWroteRows, got)
	require.Len(t, repo.batches, 1)
	assert.Len(t, repo.batches[0], 1)
}

func TestCaptureDay_SaveBatchError_BubblesUp(t *testing.T) {
	expiry := time.Now().AddDate(0, 0, 14)
	chains := map[domain.OptionRight][]domain.OptionContractSnapshot{
		domain.OptionRightCall: {contractSnap(150, domain.OptionRightCall, expiry, 1.20, 1.25, 0.30, 0.45)},
	}
	market := &fakeOptionsMarket{chainsBy: chains}
	repo := &fakeHistRepo{saveErr: errors.New("disk full")}
	svc := newService(t, market, repo)

	_, err := svc.CaptureDay(context.Background(), "AAPL", time.Date(2026, 5, 1, 16, 30, 0, 0, time.UTC))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SaveBatch")
}

func TestCaptureDay_HasDataError_BubblesUp(t *testing.T) {
	repo := &fakeHistRepo{hasErr: errors.New("db unreachable")}
	svc := newService(t, &fakeOptionsMarket{}, repo)

	_, err := svc.CaptureDay(context.Background(), "AAPL", time.Date(2026, 5, 1, 16, 30, 0, 0, time.UTC))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HasData")
}

func TestNextRunTime_PastTodaysSlot_RollsToTomorrowAndSkipsWeekend(t *testing.T) {
	svc := NewForwardCaptureService(
		ForwardCaptureConfig{Symbols: []string{"AAPL"}, RunAtHourET: 16, RunAtMinuteET: 15},
		nil, nil, zerolog.Nop(),
	)
	et, _ := time.LoadLocation("America/New_York")
	// Friday 2026-05-01 17:00 ET — past today's 16:15 slot, so next slot is
	// Monday 2026-05-04 16:15 ET (skipping the weekend).
	friday := time.Date(2026, 5, 1, 17, 0, 0, 0, et)
	got := svc.nextRunTime(friday)
	assert.Equal(t, time.Monday, got.Weekday())
	assert.Equal(t, 16, got.Hour())
	assert.Equal(t, 15, got.Minute())
}
