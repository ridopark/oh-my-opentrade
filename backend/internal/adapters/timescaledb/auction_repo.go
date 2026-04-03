package timescaledb

import (
	"context"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

const (
	queryInsertAuctionImbalance = `INSERT INTO auction_imbalances (time, symbol, volume, price, imbalance)
		VALUES ($1, $2, $3, $4, $5)`

	querySelectAuctionImbalances = `SELECT time, symbol, volume, price, imbalance
		FROM auction_imbalances
		WHERE symbol = $1 AND time >= $2 AND time < $3
		ORDER BY time`
)

// AuctionImbalanceRepo persists NYSE closing auction imbalance snapshots.
type AuctionImbalanceRepo struct {
	db  DBTX
	log zerolog.Logger
}

// NewAuctionImbalanceRepo creates a new auction imbalance repository.
func NewAuctionImbalanceRepo(db DBTX, log zerolog.Logger) *AuctionImbalanceRepo {
	return &AuctionImbalanceRepo{db: db, log: log}
}

// SaveAuctionImbalance inserts a single auction imbalance snapshot.
func (r *AuctionImbalanceRepo) SaveAuctionImbalance(ctx context.Context, snap domain.AuctionImbalanceSnapshot) error {
	_, err := r.db.ExecContext(ctx, queryInsertAuctionImbalance,
		snap.Time, string(snap.Symbol), snap.Volume, snap.Price, snap.Imbalance,
	)
	if err != nil {
		r.log.Error().Err(err).
			Str("symbol", string(snap.Symbol)).
			Time("time", snap.Time).
			Msg("failed to save auction imbalance")
		return fmt.Errorf("auction_repo: save: %w", err)
	}
	return nil
}

// SaveAuctionImbalanceBatch inserts multiple auction imbalance snapshots using
// a single multi-row INSERT with ON CONFLICT DO NOTHING to skip duplicates.
func (r *AuctionImbalanceRepo) SaveAuctionImbalanceBatch(ctx context.Context, snaps []domain.AuctionImbalanceSnapshot) (int64, error) {
	if len(snaps) == 0 {
		return 0, nil
	}

	// Build multi-row VALUES clause.
	const cols = 5 // time, symbol, volume, price, imbalance
	query := "INSERT INTO auction_imbalances (time, symbol, volume, price, imbalance) VALUES "
	args := make([]any, 0, len(snaps)*cols)
	for i, s := range snaps {
		if i > 0 {
			query += ","
		}
		base := i * cols
		query += fmt.Sprintf("($%d,$%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4, base+5)
		args = append(args, s.Time, string(s.Symbol), s.Volume, s.Price, s.Imbalance)
	}
	query += " ON CONFLICT DO NOTHING"

	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		r.log.Error().Err(err).Int("batch_size", len(snaps)).Msg("failed to batch save auction imbalances")
		return 0, fmt.Errorf("auction_repo: batch save: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// GetAuctionImbalances returns auction imbalance snapshots for a symbol in [from, to).
func (r *AuctionImbalanceRepo) GetAuctionImbalances(ctx context.Context, symbol domain.Symbol, from, to time.Time) ([]domain.AuctionImbalanceSnapshot, error) {
	rows, err := r.db.QueryContext(ctx, querySelectAuctionImbalances, string(symbol), from, to)
	if err != nil {
		return nil, fmt.Errorf("auction_repo: query: %w", err)
	}
	defer rows.Close()

	var results []domain.AuctionImbalanceSnapshot
	for rows.Next() {
		var snap domain.AuctionImbalanceSnapshot
		var sym string
		if err := rows.Scan(&snap.Time, &sym, &snap.Volume, &snap.Price, &snap.Imbalance); err != nil {
			return nil, fmt.Errorf("auction_repo: scan: %w", err)
		}
		snap.Symbol = domain.Symbol(sym)
		results = append(results, snap)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auction_repo: rows: %w", err)
	}
	return results, nil
}
