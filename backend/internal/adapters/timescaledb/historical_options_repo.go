package timescaledb

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// HistoricalOptionsRepository implements ports.HistoricalOptionsPort using TimescaleDB.
type HistoricalOptionsRepository struct {
	db  DBTX
	log zerolog.Logger
}

// NewHistoricalOptionsRepository creates a new repository.
func NewHistoricalOptionsRepository(db DBTX, log zerolog.Logger) *HistoricalOptionsRepository {
	return &HistoricalOptionsRepository{
		db:  db,
		log: log.With().Str("component", "hist_options_repo").Logger(),
	}
}

// GetHistoricalChain returns option contracts for a symbol on a date, filtered by right and DTE range.
func (r *HistoricalOptionsRepository) GetHistoricalChain(
	ctx context.Context,
	symbol domain.Symbol,
	date time.Time,
	right domain.OptionRight,
	minDTE, maxDTE int,
) ([]domain.HistoricalOptionChainRow, error) {
	callPut := "Call"
	if right == domain.OptionRightPut {
		callPut = "Put"
	}
	minExpiry := date.AddDate(0, 0, minDTE)
	maxExpiry := date.AddDate(0, 0, maxDTE)

	const q = `SELECT date, symbol, expiration, strike, call_put, bid, ask, iv, delta, gamma, theta, vega, rho
		FROM historical_option_chain
		WHERE symbol = $1 AND date = $2 AND call_put = $3
		  AND expiration >= $4 AND expiration <= $5
		ORDER BY expiration, strike`

	rows, err := r.db.QueryContext(ctx, q, string(symbol), date.Format("2006-01-02"),
		callPut, minExpiry.Format("2006-01-02"), maxExpiry.Format("2006-01-02"))
	if err != nil {
		return nil, fmt.Errorf("GetHistoricalChain: %w", err)
	}
	defer rows.Close()

	var results []domain.HistoricalOptionChainRow
	for rows.Next() {
		row, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	return results, rows.Err()
}

// GetHistoricalContract returns the contract closest to the given strike/expiry/right.
func (r *HistoricalOptionsRepository) GetHistoricalContract(
	ctx context.Context,
	symbol domain.Symbol,
	date time.Time,
	strike float64,
	expiry time.Time,
	right domain.OptionRight,
) (*domain.HistoricalOptionChainRow, error) {
	callPut := "Call"
	if right == domain.OptionRightPut {
		callPut = "Put"
	}

	// Find closest strike within ±$2 of target, closest expiry within ±7 days.
	const q = `SELECT date, symbol, expiration, strike, call_put, bid, ask, iv, delta, gamma, theta, vega, rho
		FROM historical_option_chain
		WHERE symbol = $1 AND date = $2 AND call_put = $3
		  AND ABS(strike - $4) <= 2.0
		  AND ABS(expiration - $5::date) <= 7
		ORDER BY ABS(strike - $4), ABS(expiration - $5::date)
		LIMIT 1`

	row := r.db.QueryRowContext(ctx, q, string(symbol), date.Format("2006-01-02"),
		callPut, strike, expiry.Format("2006-01-02"))

	result, err := scanSingleRow(row)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// HasData reports whether any data exists for the symbol on the given date.
func (r *HistoricalOptionsRepository) HasData(
	ctx context.Context,
	symbol domain.Symbol,
	date time.Time,
) (bool, error) {
	const q = `SELECT EXISTS(SELECT 1 FROM historical_option_chain WHERE symbol = $1 AND date = $2)`
	row := r.db.QueryRowContext(ctx, q, string(symbol), date.Format("2006-01-02"))
	var exists bool
	if err := row.Scan(&exists); err != nil {
		return false, fmt.Errorf("HasData: %w", err)
	}
	return exists, nil
}

// SaveBatch inserts a batch of rows with ON CONFLICT DO NOTHING.
func (r *HistoricalOptionsRepository) SaveBatch(
	ctx context.Context,
	rows []domain.HistoricalOptionChainRow,
) error {
	if len(rows) == 0 {
		return nil
	}

	// Build multi-row INSERT.
	const cols = "(date, symbol, expiration, strike, call_put, bid, ask, iv, delta, gamma, theta, vega, rho)"
	var sb strings.Builder
	sb.WriteString("INSERT INTO historical_option_chain " + cols + " VALUES ")

	args := make([]any, 0, len(rows)*13)
	for i, row := range rows {
		if i > 0 {
			sb.WriteString(",")
		}
		base := i * 13
		sb.WriteString(fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7,
			base+8, base+9, base+10, base+11, base+12, base+13))

		callPut := "Call"
		if row.Right == domain.OptionRightPut {
			callPut = "Put"
		}
		args = append(args,
			row.Date.Format("2006-01-02"),
			string(row.Symbol),
			row.Expiration.Format("2006-01-02"),
			row.Strike,
			callPut,
			nilIfZero(row.Bid),
			nilIfZero(row.Ask),
			nilIfZero(row.IV),
			nilIfZero(row.Delta),
			nilIfZero(row.Gamma),
			nilIfZero(row.Theta),
			nilIfZero(row.Vega),
			nilIfZero(row.Rho),
		)
	}
	sb.WriteString(" ON CONFLICT (date, symbol, expiration, strike, call_put) DO NOTHING")

	_, err := r.db.ExecContext(ctx, sb.String(), args...)
	if err != nil {
		return fmt.Errorf("SaveBatch: %w", err)
	}
	return nil
}

func scanRow(rows interface{ Scan(dest ...any) error }) (domain.HistoricalOptionChainRow, error) {
	var (
		dateStr, symbol, expiryStr, callPut string
		strike                              float64
		bid, ask, iv, delta, gamma          *float64
		theta, vega, rho                    *float64
	)
	if err := rows.Scan(&dateStr, &symbol, &expiryStr, &strike, &callPut,
		&bid, &ask, &iv, &delta, &gamma, &theta, &vega, &rho); err != nil {
		return domain.HistoricalOptionChainRow{}, fmt.Errorf("scan row: %w", err)
	}

	date, _ := time.Parse("2006-01-02", dateStr)
	expiry, _ := time.Parse("2006-01-02", expiryStr)

	right := domain.OptionRightCall
	if callPut == "Put" {
		right = domain.OptionRightPut
	}

	return domain.HistoricalOptionChainRow{
		Date:       date,
		Symbol:     domain.Symbol(symbol),
		Expiration: expiry,
		Strike:     strike,
		Right:      right,
		Bid:        deref(bid),
		Ask:        deref(ask),
		IV:         deref(iv),
		Delta:      deref(delta),
		Gamma:      deref(gamma),
		Theta:      deref(theta),
		Vega:       deref(vega),
		Rho:        deref(rho),
	}, nil
}

func scanSingleRow(row Row) (*domain.HistoricalOptionChainRow, error) {
	r, err := scanRow(row)
	if err != nil {
		return nil, err
	}
	if r.Strike == 0 && math.Abs(r.Bid) < 1e-9 && math.Abs(r.Ask) < 1e-9 {
		return nil, fmt.Errorf("no matching contract found")
	}
	return &r, nil
}

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func nilIfZero(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}
