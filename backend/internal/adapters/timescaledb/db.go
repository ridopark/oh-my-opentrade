package timescaledb

import (
	"context"
	"database/sql"
)

// Row represents a single database row.
type Row interface {
	Scan(dest ...any) error
}

// Rows represents a set of database rows.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close() error
	Err() error
}

// DBTX abstracts database operations for testability.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) Row
}

// Repository implements ports.RepositoryPort using TimescaleDB for persistent storage of market data, trades, and strategy configurations.
type Repository struct {
	db DBTX
}

// NewRepository creates a new TimescaleDB repository.
func NewRepository(db DBTX) *Repository {
	return &Repository{
		db: db,
	}
}
