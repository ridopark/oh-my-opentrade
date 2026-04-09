package timescaledb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// WhaleRepo handles persistence of 13F whale filings and accumulation scores.
type WhaleRepo struct {
	db  DBTX
	log zerolog.Logger
}

// NewWhaleRepo creates a new WhaleRepo with a structured logger.
func NewWhaleRepo(db DBTX, log zerolog.Logger) *WhaleRepo {
	return &WhaleRepo{db: db, log: log}
}

// SaveWhaleFilingsBatch upserts a batch of whale filings in a single INSERT statement.
// Returns the number of rows affected. Max batch size is 5000; larger slices are split automatically.
func (r *WhaleRepo) SaveWhaleFilingsBatch(ctx context.Context, filings []domain.WhaleFiling) (int64, error) {
	if len(filings) == 0 {
		return 0, nil
	}

	const maxBatchSize = 5000
	if len(filings) > maxBatchSize {
		var total int64
		for i := 0; i < len(filings); i += maxBatchSize {
			end := i + maxBatchSize
			if end > len(filings) {
				end = len(filings)
			}
			n, err := r.SaveWhaleFilingsBatch(ctx, filings[i:end])
			total += n
			if err != nil {
				return total, err
			}
		}
		return total, nil
	}

	const cols = 10
	var b strings.Builder
	b.WriteString("INSERT INTO whale_filings (filing_date, filer_cik, filer_name, cusip, ticker, issuer_name, share_count, market_value, put_call, filer_tier) VALUES ")

	args := make([]any, 0, len(filings)*cols)
	for i, f := range filings {
		if i > 0 {
			b.WriteString(", ")
		}
		base := i*cols + 1
		fmt.Fprintf(&b, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base, base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9)
		args = append(args,
			f.FilingDate, f.FilerCIK, f.FilerName, f.CUSIP, f.Ticker,
			f.IssuerName, f.ShareCount, f.MarketValue1000, f.PutCall, f.FilerTier,
		)
	}

	b.WriteString(" ON CONFLICT (filer_cik, filing_date, cusip, put_call) DO UPDATE SET " +
		"filer_name=EXCLUDED.filer_name, ticker=EXCLUDED.ticker, issuer_name=EXCLUDED.issuer_name, " +
		"share_count=EXCLUDED.share_count, market_value=EXCLUDED.market_value, filer_tier=EXCLUDED.filer_tier")

	res, err := r.db.ExecContext(ctx, b.String(), args...)
	if err != nil {
		r.log.Error().Err(err).Int("batch_size", len(filings)).Msg("whale_repo: failed to batch save filings")
		return 0, fmt.Errorf("whale_repo: save filings batch: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// GetWhaleFilingsByQuarter returns all whale filings for a given quarter-end date.
func (r *WhaleRepo) GetWhaleFilingsByQuarter(ctx context.Context, quarterEnd time.Time) ([]domain.WhaleFiling, error) {
	const query = `SELECT filing_date, filer_cik, filer_name, cusip, ticker, issuer_name, share_count, market_value, put_call, filer_tier
		FROM whale_filings WHERE filing_date = $1`

	rows, err := r.db.QueryContext(ctx, query, quarterEnd)
	if err != nil {
		return nil, fmt.Errorf("whale_repo: query filings: %w", err)
	}
	defer rows.Close()

	var results []domain.WhaleFiling
	for rows.Next() {
		var f domain.WhaleFiling
		if err := rows.Scan(
			&f.FilingDate, &f.FilerCIK, &f.FilerName, &f.CUSIP, &f.Ticker,
			&f.IssuerName, &f.ShareCount, &f.MarketValue1000, &f.PutCall, &f.FilerTier,
		); err != nil {
			return nil, fmt.Errorf("whale_repo: scan filing: %w", err)
		}
		results = append(results, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("whale_repo: rows filings: %w", err)
	}
	return results, nil
}

// SaveWhaleAccumulationBatch upserts a batch of accumulation scores in a single INSERT statement.
// Returns the number of rows affected. Max batch size is 5000; larger slices are split automatically.
func (r *WhaleRepo) SaveWhaleAccumulationBatch(ctx context.Context, scores []domain.WhaleAccumulation) (int64, error) {
	if len(scores) == 0 {
		return 0, nil
	}

	const maxBatchSize = 5000
	if len(scores) > maxBatchSize {
		var total int64
		for i := 0; i < len(scores); i += maxBatchSize {
			end := i + maxBatchSize
			if end > len(scores) {
				end = len(scores)
			}
			n, err := r.SaveWhaleAccumulationBatch(ctx, scores[i:end])
			total += n
			if err != nil {
				return total, err
			}
		}
		return total, nil
	}

	const cols = 8
	var b strings.Builder
	b.WriteString("INSERT INTO whale_accumulation (quarter_end, ticker, score, new_positions, additions_50, additions_25, reductions, total_filers, top_filer_json) VALUES ")

	args := make([]any, 0, len(scores)*(cols+1))
	for i, s := range scores {
		if i > 0 {
			b.WriteString(", ")
		}
		base := i*(cols+1) + 1
		fmt.Fprintf(&b, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base, base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8)
		args = append(args,
			s.QuarterEnd, s.Ticker, s.Score, s.NewPositions,
			s.Additions50Pct, s.Additions25Pct, s.Reductions, s.TotalFilers, s.TopFilerDetail,
		)
	}

	b.WriteString(" ON CONFLICT (ticker, quarter_end) DO UPDATE SET " +
		"score=EXCLUDED.score, new_positions=EXCLUDED.new_positions, " +
		"additions_50=EXCLUDED.additions_50, additions_25=EXCLUDED.additions_25, " +
		"reductions=EXCLUDED.reductions, total_filers=EXCLUDED.total_filers, " +
		"top_filer_json=EXCLUDED.top_filer_json")

	res, err := r.db.ExecContext(ctx, b.String(), args...)
	if err != nil {
		r.log.Error().Err(err).Int("batch_size", len(scores)).Msg("whale_repo: failed to batch save accumulation")
		return 0, fmt.Errorf("whale_repo: save accumulation batch: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// GetWhaleAccumulation returns the latest accumulation score for each requested ticker.
func (r *WhaleRepo) GetWhaleAccumulation(ctx context.Context, tickers []string) (map[string]domain.WhaleAccumulation, error) {
	if len(tickers) == 0 {
		return nil, nil
	}

	const query = `SELECT DISTINCT ON (ticker) quarter_end, ticker, score, new_positions, additions_50, additions_25, reductions, total_filers, top_filer_json
		FROM whale_accumulation
		WHERE ticker = ANY($1)
		ORDER BY ticker, quarter_end DESC`

	rows, err := r.db.QueryContext(ctx, query, tickers)
	if err != nil {
		return nil, fmt.Errorf("whale_repo: query accumulation: %w", err)
	}
	defer rows.Close()

	result := make(map[string]domain.WhaleAccumulation, len(tickers))
	for rows.Next() {
		var a domain.WhaleAccumulation
		if err := rows.Scan(
			&a.QuarterEnd, &a.Ticker, &a.Score, &a.NewPositions,
			&a.Additions50Pct, &a.Additions25Pct, &a.Reductions, &a.TotalFilers, &a.TopFilerDetail,
		); err != nil {
			return nil, fmt.Errorf("whale_repo: scan accumulation: %w", err)
		}
		result[a.Ticker] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("whale_repo: rows accumulation: %w", err)
	}
	return result, nil
}

// GetLatestFilingDate returns the most recent filing_date for a given CIK, or nil if none exist.
func (r *WhaleRepo) GetLatestFilingDate(ctx context.Context, cik string) (*time.Time, error) {
	const query = `SELECT MAX(filing_date) FROM whale_filings WHERE filer_cik = $1`

	row := r.db.QueryRowContext(ctx, query, cik)
	var t *time.Time
	if err := row.Scan(&t); err != nil {
		return nil, fmt.Errorf("whale_repo: get latest filing date: %w", err)
	}
	return t, nil
}
