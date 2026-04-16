package timescaledb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// FundingRepo persists and queries funding rate data in the funding_rates
// hypertable (migration 040).
type FundingRepo struct {
	db  DBTX
	log zerolog.Logger
}

// NewFundingRepo creates a FundingRepo with a structured logger.
func NewFundingRepo(db DBTX, log zerolog.Logger) *FundingRepo {
	return &FundingRepo{
		db:  db,
		log: log.With().Str("component", "funding_repo").Logger(),
	}
}

// Insert stores a batch of funding rate records, ignoring conflicts on
// the (venue, symbol, timestamp) primary key.
func (r *FundingRepo) Insert(ctx context.Context, rates []ports.FundingRate) error {
	if len(rates) == 0 {
		return nil
	}

	const maxBatchSize = 5000
	if len(rates) > maxBatchSize {
		for i := 0; i < len(rates); i += maxBatchSize {
			end := i + maxBatchSize
			if end > len(rates) {
				end = len(rates)
			}
			if err := r.Insert(ctx, rates[i:end]); err != nil {
				return err
			}
		}
		return nil
	}

	const cols = 6
	var b strings.Builder
	b.WriteString("INSERT INTO funding_rates (venue, symbol, timestamp, rate, interval_hours, mark_price) VALUES ")

	args := make([]any, 0, len(rates)*cols)
	for i, fr := range rates {
		if i > 0 {
			b.WriteString(", ")
		}
		base := i*cols + 1
		fmt.Fprintf(&b, "($%d, $%d, $%d, $%d, $%d, $%d)",
			base, base+1, base+2, base+3, base+4, base+5)
		args = append(args,
			string(fr.Venue),
			string(fr.Symbol),
			fr.Timestamp,
			fr.Rate,
			fr.IntervalHours,
			fr.MarkPrice,
		)
	}

	b.WriteString(" ON CONFLICT (venue, symbol, timestamp) DO NOTHING")

	_, err := r.db.ExecContext(ctx, b.String(), args...)
	if err != nil {
		r.log.Error().Err(err).Int("batch_size", len(rates)).Msg("failed to insert funding rates batch")
		return fmt.Errorf("timescaledb: insert funding rates: %w", err)
	}
	return nil
}

// Query returns funding rates for a venue+symbol in [from, to) ordered by
// timestamp ascending.
func (r *FundingRepo) Query(ctx context.Context, venue domain.Venue, symbol domain.Symbol, from, to time.Time) ([]ports.FundingRate, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT venue, symbol, timestamp, rate, interval_hours, COALESCE(mark_price, 0)
		FROM funding_rates
		WHERE venue = $1 AND symbol = $2 AND timestamp >= $3 AND timestamp < $4
		ORDER BY timestamp ASC`,
		string(venue), string(symbol), from, to)
	if err != nil {
		return nil, fmt.Errorf("timescaledb: query funding rates: %w", err)
	}
	defer rows.Close()

	var result []ports.FundingRate
	for rows.Next() {
		var fr ports.FundingRate
		var v, s string
		if err := rows.Scan(&v, &s, &fr.Timestamp, &fr.Rate, &fr.IntervalHours, &fr.MarkPrice); err != nil {
			return nil, fmt.Errorf("timescaledb: scan funding rate: %w", err)
		}
		fr.Venue = domain.Venue(v)
		fr.Symbol = domain.Symbol(s)
		result = append(result, fr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate funding rates: %w", err)
	}
	return result, nil
}

// Latest returns the most recent funding rate for a venue+symbol.
// Returns sql.ErrNoRows (wrapped) when no record exists.
func (r *FundingRepo) Latest(ctx context.Context, venue domain.Venue, symbol domain.Symbol) (ports.FundingRate, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT venue, symbol, timestamp, rate, interval_hours, COALESCE(mark_price, 0)
		FROM funding_rates
		WHERE venue = $1 AND symbol = $2
		ORDER BY timestamp DESC
		LIMIT 1`,
		string(venue), string(symbol))

	var fr ports.FundingRate
	var v, s string
	if err := row.Scan(&v, &s, &fr.Timestamp, &fr.Rate, &fr.IntervalHours, &fr.MarkPrice); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ports.FundingRate{}, fmt.Errorf("timescaledb: latest funding rate: %w", err)
		}
		return ports.FundingRate{}, fmt.Errorf("timescaledb: latest funding rate: %w", err)
	}
	fr.Venue = domain.Venue(v)
	fr.Symbol = domain.Symbol(s)
	return fr, nil
}
