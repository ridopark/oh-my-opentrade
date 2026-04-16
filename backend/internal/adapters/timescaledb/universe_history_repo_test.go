package timescaledb_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUniverseHistoryRepo_ImplementsPort(t *testing.T) {
	var _ ports.UniverseHistoryPort = (*timescaledb.UniverseHistoryRepo)(nil)
}

// fakeUniverseStore is an in-memory implementation of UniverseHistoryPort
// used to drive the session filter tests and to exercise the port
// semantics (multi-window, upsert idempotency) independently of SQL.
type fakeUniverseStore struct {
	windows map[domain.Symbol][]ports.UniverseWindow
}

func newFakeUniverseStore() *fakeUniverseStore {
	return &fakeUniverseStore{windows: make(map[domain.Symbol][]ports.UniverseWindow)}
}

func (f *fakeUniverseStore) WasTradable(_ context.Context, sym domain.Symbol, at time.Time) (bool, error) {
	for _, w := range f.windows[sym] {
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

func (f *fakeUniverseStore) WindowsFor(_ context.Context, sym domain.Symbol) ([]ports.UniverseWindow, error) {
	return append([]ports.UniverseWindow(nil), f.windows[sym]...), nil
}

func (f *fakeUniverseStore) Upsert(_ context.Context, w ports.UniverseWindow) error {
	list := f.windows[w.Symbol]
	for i, existing := range list {
		if existing.FromDate.Equal(w.FromDate) {
			list[i] = w
			f.windows[w.Symbol] = list
			return nil
		}
	}
	f.windows[w.Symbol] = append(list, w)
	return nil
}

func (f *fakeUniverseStore) ActiveSymbols(_ context.Context, at time.Time) ([]domain.Symbol, error) {
	var out []domain.Symbol
	for sym, list := range f.windows {
		for _, w := range list {
			if at.Before(w.FromDate) {
				continue
			}
			if w.ToDate != nil && !at.Before(*w.ToDate) {
				continue
			}
			out = append(out, sym)
			break
		}
	}
	return out, nil
}

func TestUniverseHistoryRepo_Upsert_WritesColumns(t *testing.T) {
	seen := make(map[string]any)
	db := &mockDB{
		execFunc: func(_ context.Context, query string, args ...any) (sql.Result, error) {
			assert.True(t, strings.Contains(query, "INSERT INTO universe_history"))
			seen["symbol"] = args[0]
			seen["from_date"] = args[1]
			seen["to_date"] = args[2]
			seen["source"] = args[3]
			seen["note"] = args[4]
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewUniverseHistoryRepo(db, zerolog.Nop())
	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, repo.Upsert(context.Background(), ports.UniverseWindow{
		Symbol:   domain.Symbol("AAPL"),
		FromDate: from,
		Source:   "seed",
		Note:     "initial",
	}))
	assert.Equal(t, "AAPL", seen["symbol"])
	assert.Equal(t, from, seen["from_date"])
	assert.Equal(t, sql.NullTime{}, seen["to_date"], "nil ToDate must persist as SQL NULL")
	assert.Equal(t, "seed", seen["source"])
	assert.Equal(t, sql.NullString{String: "initial", Valid: true}, seen["note"])
}

func TestUniverseHistoryRepo_Upsert_Validates(t *testing.T) {
	repo := timescaledb.NewUniverseHistoryRepo(&mockDB{}, zerolog.Nop())
	// empty symbol must be rejected before hitting the DB so we can call
	// Upsert without wiring execFunc.
	require.Error(t, repo.Upsert(context.Background(), ports.UniverseWindow{
		FromDate: time.Now().UTC(), Source: "seed",
	}))
	require.Error(t, repo.Upsert(context.Background(), ports.UniverseWindow{
		Symbol: domain.Symbol("AAPL"), FromDate: time.Now().UTC(),
	}))
}

// TestFakeUniverseStore_WasTradable exercises the port contract itself,
// including the delist-then-relist scenario (two non-overlapping windows
// for the same ticker).
func TestFakeUniverseStore_WasTradable(t *testing.T) {
	date := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	t1 := date(2022, 1, 1)
	t2 := date(2023, 6, 1)
	t3 := date(2024, 1, 1)
	// DLST relisted on 2024-01-01 after being delisted 2023-06-01.
	store := newFakeUniverseStore()
	require.NoError(t, store.Upsert(context.Background(), ports.UniverseWindow{
		Symbol: "DLST", FromDate: date(2020, 1, 1), ToDate: &t2, Source: "seed",
	}))
	require.NoError(t, store.Upsert(context.Background(), ports.UniverseWindow{
		Symbol: "DLST", FromDate: t3, Source: "seed",
	}))

	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"before first window", date(2019, 12, 31), false},
		{"inside first window", t1, true},
		{"first window exclusive upper bound", t2, false},
		{"gap between windows", date(2023, 9, 1), false},
		{"inside relisted window", date(2024, 6, 1), true},
		{"far future still tradable", date(2030, 1, 1), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.WasTradable(context.Background(), "DLST", tc.at)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFakeUniverseStore_UpsertIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newFakeUniverseStore()
	w := ports.UniverseWindow{
		Symbol:   "AAPL",
		FromDate: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		Source:   "seed",
	}
	require.NoError(t, store.Upsert(ctx, w))
	require.NoError(t, store.Upsert(ctx, w))
	// Re-upserting (sym, from_date) must overwrite, not duplicate.
	got, err := store.WindowsFor(ctx, "AAPL")
	require.NoError(t, err)
	assert.Len(t, got, 1)

	// Update source on second upsert — same primary key.
	w2 := w
	w2.Source = "ibkr"
	require.NoError(t, store.Upsert(ctx, w2))
	got, err = store.WindowsFor(ctx, "AAPL")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "ibkr", got[0].Source)
}
