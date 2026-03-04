package timescaledb

import (
	"context"
	"database/sql"
)

// SqlDB wraps *sql.DB to satisfy the DBTX interface.
// *sql.DB.QueryContext returns (*sql.Rows, error) but DBTX.QueryContext returns (Rows, error),
// so a thin wrapper is needed to bridge the type mismatch.
type SqlDB struct {
	db *sql.DB
}

// NewSqlDB wraps a *sql.DB as a DBTX-compatible adapter.
func NewSqlDB(db *sql.DB) *SqlDB {
	return &SqlDB{db: db}
}

func (s *SqlDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, query, args...)
}

func (s *SqlDB) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	return s.db.QueryContext(ctx, query, args...)
}

func (s *SqlDB) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	return s.db.QueryRowContext(ctx, query, args...)
}
