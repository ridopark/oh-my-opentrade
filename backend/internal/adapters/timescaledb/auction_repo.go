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
