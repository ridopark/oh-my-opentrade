package timescaledb_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/domain/dnaapproval"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDNAApprovalRepo_ImplementsPort(t *testing.T) {
	var _ ports.DNAApprovalRepoPort = (*timescaledb.DNAApprovalRepo)(nil)
}

func TestDNAApprovalRepo_SaveDNAVersion_Success(t *testing.T) {
	now := time.Now().UTC()
	v := dnaapproval.DNAVersion{ID: "v1", StrategyKey: "orb", ContentTOML: "x", ContentHash: strings.Repeat("a", 64), DetectedAt: now}

	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			assert.True(t, strings.Contains(query, "INSERT INTO dna_versions"))
			assert.Equal(t, v.ID, args[0])
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	require.NoError(t, repo.SaveDNAVersion(context.Background(), v))
}

func TestDNAApprovalRepo_GetDNAVersion_NotFound(t *testing.T) {
	db := &mockDB{
		queryRowFunc: func(ctx context.Context, query string, args ...any) timescaledb.Row {
			return &mockRow{scanFunc: func(dest ...any) error { return sql.ErrNoRows }}
		},
	}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	v, err := repo.GetDNAVersion(context.Background(), "missing")
	require.NoError(t, err)
	require.Nil(t, v)
}

func TestDNAApprovalRepo_GetDNAVersion_DBError(t *testing.T) {
	dbErr := errors.New("db")
	db := &mockDB{
		queryRowFunc: func(ctx context.Context, query string, args ...any) timescaledb.Row {
			return &mockRow{scanFunc: func(dest ...any) error { return dbErr }}
		},
	}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	_, err := repo.GetDNAVersion(context.Background(), "id")
	require.ErrorIs(t, err, dbErr)
}

func TestDNAApprovalRepo_SaveDNAApproval_Success(t *testing.T) {
	now := time.Now().UTC()
	a := dnaapproval.DNAApproval{ID: "a1", VersionID: "v1", Status: dnaapproval.DNAStatusPending, CreatedAt: now}
	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			assert.True(t, strings.Contains(query, "INSERT INTO dna_approvals"))
			assert.Equal(t, a.ID, args[0])
			assert.Equal(t, a.VersionID, args[1])
			assert.Equal(t, string(a.Status), args[2])
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	require.NoError(t, repo.SaveDNAApproval(context.Background(), a))
}

func TestDNAApprovalRepo_UpdateDNAApproval_DBError(t *testing.T) {
	dbErr := errors.New("db")
	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) { return nil, dbErr },
	}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	err := repo.UpdateDNAApproval(context.Background(), "a1", dnaapproval.DNAStatusApproved, "bob", "ok")
	require.ErrorIs(t, err, dbErr)
}

func TestDNAApprovalRepo_ListPendingApprovals_Success(t *testing.T) {
	now := time.Now().UTC()
	db := &mockDB{
		queryFunc: func(ctx context.Context, query string, args ...any) (timescaledb.Rows, error) {
			assert.True(t, strings.Contains(query, "FROM dna_approvals"))
			rows := &mockRows{data: [][]any{{"a1", "v1", "pending", nil, nil, nil, now}}}
			return rows, nil
		},
	}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	items, err := repo.ListPendingApprovals(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, dnaapproval.DNAStatusPending, items[0].Status)
}

func TestDNAApprovalRepo_GetActiveDNAVersion_NotFound(t *testing.T) {
	db := &mockDB{
		queryRowFunc: func(ctx context.Context, query string, args ...any) timescaledb.Row {
			return &mockRow{scanFunc: func(dest ...any) error { return sql.ErrNoRows }}
		},
	}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	v, err := repo.GetActiveDNAVersion(context.Background(), "orb")
	require.NoError(t, err)
	require.Nil(t, v)
}

func TestDNAApprovalRepo_GetActiveDNAVersion_Success(t *testing.T) {
	now := time.Now().UTC()
	db := &mockDB{
		queryRowFunc: func(ctx context.Context, query string, args ...any) timescaledb.Row {
			assert.True(t, strings.Contains(query, "JOIN dna_approvals"))
			return &mockRow{scanFunc: func(dest ...any) error {
				*dest[0].(*string) = "v1"
				*dest[1].(*string) = "orb"
				*dest[2].(*string) = "toml"
				*dest[3].(*string) = strings.Repeat("a", 64)
				*dest[4].(*time.Time) = now
				return nil
			}}
		},
	}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	v, err := repo.GetActiveDNAVersion(context.Background(), "orb")
	require.NoError(t, err)
	require.NotNil(t, v)
}

func TestDNAApprovalRepo_GetDNAApproval_SetsStatus(t *testing.T) {
	now := time.Now().UTC()
	decidedBy := "bob"
	comment := "ok"
	db := &mockDB{
		queryRowFunc: func(ctx context.Context, query string, args ...any) timescaledb.Row {
			return &mockRow{scanFunc: func(dest ...any) error {
				*dest[0].(*string) = "a1"
				*dest[1].(*string) = "v1"
				*dest[2].(*string) = "approved"
				*dest[3].(**string) = &decidedBy
				*dest[4].(**time.Time) = &now
				*dest[5].(**string) = &comment
				*dest[6].(*time.Time) = now
				return nil
			}}
		},
	}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	a, err := repo.GetDNAApproval(context.Background(), "a1")
	require.NoError(t, err)
	require.NotNil(t, a)
	require.Equal(t, dnaapproval.DNAStatus("approved"), a.Status)
}

func TestDNAApprovalRepo_SaveDNAVersion_DBError(t *testing.T) {
	dbErr := errors.New("db")
	now := time.Now().UTC()
	v := dnaapproval.DNAVersion{ID: "v1", StrategyKey: "orb", ContentTOML: "x", ContentHash: strings.Repeat("a", 64), DetectedAt: now}
	db := &mockDB{execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) { return nil, dbErr }}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	err := repo.SaveDNAVersion(context.Background(), v)
	require.ErrorIs(t, err, dbErr)
}

func TestDNAApprovalRepo_ListPendingApprovals_DBError(t *testing.T) {
	dbErr := errors.New("db")
	db := &mockDB{queryFunc: func(ctx context.Context, query string, args ...any) (timescaledb.Rows, error) { return nil, dbErr }}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	_, err := repo.ListPendingApprovals(context.Background())
	require.ErrorIs(t, err, dbErr)
}

func TestDNAApprovalRepo_GetDNAVersionByHash_NotFound(t *testing.T) {
	db := &mockDB{
		queryRowFunc: func(ctx context.Context, query string, args ...any) timescaledb.Row {
			return &mockRow{scanFunc: func(dest ...any) error { return sql.ErrNoRows }}
		},
	}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	v, err := repo.GetDNAVersionByHash(context.Background(), "orb", strings.Repeat("a", 64))
	require.NoError(t, err)
	require.Nil(t, v)
}

func TestDNAApprovalRepo_UpdateDNAApproval_SetsTimeArg(t *testing.T) {
	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			assert.Equal(t, "a1", args[0])
			assert.Equal(t, "approved", args[1])
			assert.Equal(t, "bob", args[2])
			_, ok := args[3].(time.Time)
			assert.True(t, ok)
			assert.Equal(t, "ok", args[4])
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	err := repo.UpdateDNAApproval(context.Background(), "a1", dnaapproval.DNAStatusApproved, "bob", "ok")
	require.NoError(t, err)
}

func TestDNAApprovalRepo_GetDNAApproval_NotFound(t *testing.T) {
	db := &mockDB{queryRowFunc: func(ctx context.Context, query string, args ...any) timescaledb.Row {
		return &mockRow{scanFunc: func(dest ...any) error { return sql.ErrNoRows }}
	}}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	a, err := repo.GetDNAApproval(context.Background(), "missing")
	require.NoError(t, err)
	require.Nil(t, a)
}

func TestDNAApprovalRepo_GetDNAApproval_DBError(t *testing.T) {
	dbErr := errors.New("db")
	db := &mockDB{queryRowFunc: func(ctx context.Context, query string, args ...any) timescaledb.Row {
		return &mockRow{scanFunc: func(dest ...any) error { return dbErr }}
	}}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	_, err := repo.GetDNAApproval(context.Background(), "a1")
	require.ErrorIs(t, err, dbErr)
}

func TestDNAApprovalRepo_GetActiveDNAVersion_DBError(t *testing.T) {
	dbErr := errors.New("db")
	db := &mockDB{queryRowFunc: func(ctx context.Context, query string, args ...any) timescaledb.Row {
		return &mockRow{scanFunc: func(dest ...any) error { return dbErr }}
	}}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	_, err := repo.GetActiveDNAVersion(context.Background(), "orb")
	require.ErrorIs(t, err, dbErr)
}

func TestDNAApprovalRepo_SaveDNAApproval_DBError(t *testing.T) {
	dbErr := errors.New("db")
	now := time.Now().UTC()
	a := dnaapproval.DNAApproval{ID: "a1", VersionID: "v1", Status: dnaapproval.DNAStatusPending, CreatedAt: now}
	db := &mockDB{execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) { return nil, dbErr }}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	err := repo.SaveDNAApproval(context.Background(), a)
	require.ErrorIs(t, err, dbErr)
}

func TestDNAApprovalRepo_ListPendingApprovals_ScanError(t *testing.T) {
	now := time.Now().UTC()
	db := &mockDB{
		queryFunc: func(ctx context.Context, query string, args ...any) (timescaledb.Rows, error) {
			return &mockRows{data: [][]any{{"a1", "v1", "pending", nil, nil, nil, now}}, scanErr: errors.New("scan")}, nil
		},
	}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	_, err := repo.ListPendingApprovals(context.Background())
	require.Error(t, err)
}

func TestDNAApprovalRepo_ListPendingApprovals_EmptyReturnsSlice(t *testing.T) {
	db := &mockDB{
		queryFunc: func(ctx context.Context, query string, args ...any) (timescaledb.Rows, error) {
			return &mockRows{data: [][]any{}}, nil
		},
	}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	items, err := repo.ListPendingApprovals(context.Background())
	require.NoError(t, err)
	require.NotNil(t, items)
}

func TestDNAApprovalRepo_UpdateDNAApproval_StatusIsPassed(t *testing.T) {
	called := false
	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			called = true
			assert.Equal(t, "rejected", args[1])
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	require.NoError(t, repo.UpdateDNAApproval(context.Background(), "a1", dnaapproval.DNAStatusRejected, "bob", "no"))
	require.True(t, called)
}

func TestDNAApprovalRepo_GetDNAVersionByHash_DBError(t *testing.T) {
	dbErr := errors.New("db")
	db := &mockDB{queryRowFunc: func(ctx context.Context, query string, args ...any) timescaledb.Row {
		return &mockRow{scanFunc: func(dest ...any) error { return dbErr }}
	}}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	_, err := repo.GetDNAVersionByHash(context.Background(), "orb", strings.Repeat("a", 64))
	require.ErrorIs(t, err, dbErr)
}

func TestDNAApprovalRepo_ListPendingApprovals_QueryCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := &mockDB{queryFunc: func(ctx context.Context, query string, args ...any) (timescaledb.Rows, error) {
		return nil, context.Canceled
	}}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	_, err := repo.ListPendingApprovals(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestDNAApprovalRepo_UpdateDNAApproval_WrapsError(t *testing.T) {
	dbErr := errors.New("db")
	db := &mockDB{execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) { return nil, dbErr }}
	repo := timescaledb.NewDNAApprovalRepo(db, zerolog.Nop())
	err := repo.UpdateDNAApproval(context.Background(), "a1", dnaapproval.DNAStatusApproved, "bob", "ok")
	require.Error(t, err)
	require.True(t, errors.Is(err, dbErr))
}
