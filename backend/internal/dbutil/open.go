package dbutil

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Open parses dsn, opens a *sql.DB with standard pool limits, and pings.
func Open(dsn string) (*sql.DB, error) {
	pgCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("dbutil: parse config: %w", err)
	}
	db := stdlib.OpenDB(*pgCfg)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("dbutil: ping: %w", err)
	}
	return db, nil
}
