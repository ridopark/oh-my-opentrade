package timescaledb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// DarkPoolRepo handles persistence of dark pool bar data in TimescaleDB.
type DarkPoolRepo struct {
	db  DBTX
	log zerolog.Logger
}

// NewDarkPoolRepo creates a new DarkPoolRepo with a structured logger.
func NewDarkPoolRepo(db DBTX, log zerolog.Logger) *DarkPoolRepo {
	return &DarkPoolRepo{db: db, log: log}
}

// SaveDarkPoolBars upserts a batch of dark pool bars in a single INSERT statement.
// Returns the number of bars saved. Max batch size is 5000; larger slices are split automatically.
func (r *DarkPoolRepo) SaveDarkPoolBars(ctx context.Context, bars []domain.DarkPoolBar) (int, error) {
	if len(bars) == 0 {
		return 0, nil
	}

	const maxBatchSize = 5000
	if len(bars) > maxBatchSize {
		total := 0
		for i := 0; i < len(bars); i += maxBatchSize {
			end := i + maxBatchSize
			if end > len(bars) {
				end = len(bars)
			}
			n, err := r.SaveDarkPoolBars(ctx, bars[i:end])
			total += n
			if err != nil {
				return total, err
			}
		}
		return total, nil
	}

	const cols = 14
	var b strings.Builder
	b.WriteString("INSERT INTO darkpool_bars (time, symbol, timeframe, dp_volume, dp_trades, dp_vwap, lit_volume, total_volume, dp_ratio, buy_volume, sell_volume, large_print_volume, large_print_count, max_print_size) VALUES ")

	args := make([]any, 0, len(bars)*cols)
	for i, bar := range bars {
		if i > 0 {
			b.WriteString(", ")
		}
		base := i*cols + 1
		fmt.Fprintf(&b, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base, base+1, base+2, base+3, base+4, base+5, base+6,
			base+7, base+8, base+9, base+10, base+11, base+12, base+13)
		args = append(args,
			bar.Time, string(bar.Symbol), string(bar.Timeframe),
			bar.DPVolume, bar.DPTrades, bar.DPVWAP,
			bar.LitVolume, bar.TotalVolume, bar.DPRatio,
			bar.BuyVolume, bar.SellVolume,
			bar.LargePrintVolume, bar.LargePrintCount, bar.MaxPrintSize,
		)
	}

	b.WriteString(" ON CONFLICT (symbol, timeframe, time) DO UPDATE SET " +
		"dp_volume=EXCLUDED.dp_volume, dp_trades=EXCLUDED.dp_trades, dp_vwap=EXCLUDED.dp_vwap, " +
		"lit_volume=EXCLUDED.lit_volume, total_volume=EXCLUDED.total_volume, dp_ratio=EXCLUDED.dp_ratio, " +
		"buy_volume=EXCLUDED.buy_volume, sell_volume=EXCLUDED.sell_volume, " +
		"large_print_volume=EXCLUDED.large_print_volume, large_print_count=EXCLUDED.large_print_count, " +
		"max_print_size=EXCLUDED.max_print_size")

	_, err := r.db.ExecContext(ctx, b.String(), args...)
	if err != nil {
		r.log.Error().Err(err).Int("batch_size", len(bars)).Msg("failed to save dark pool bars batch")
		return 0, fmt.Errorf("timescaledb: save dark pool bars batch: %w", err)
	}
	return len(bars), nil
}

// GetDarkPoolBars retrieves dark pool bars for a symbol/timeframe ordered by time ASC.
func (r *DarkPoolRepo) GetDarkPoolBars(ctx context.Context, sym domain.Symbol, tf domain.Timeframe, from, to time.Time) ([]domain.DarkPoolBar, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT time, symbol, timeframe, dp_volume, dp_trades, dp_vwap, lit_volume, total_volume, dp_ratio, buy_volume, sell_volume, large_print_volume, large_print_count, max_print_size
		FROM darkpool_bars
		WHERE symbol = $1 AND timeframe = $2 AND time >= $3 AND time < $4
		ORDER BY time ASC`,
		string(sym), string(tf), from, to)
	if err != nil {
		return nil, fmt.Errorf("timescaledb: get dark pool bars: %w", err)
	}
	defer rows.Close()

	var bars []domain.DarkPoolBar
	for rows.Next() {
		var bar domain.DarkPoolBar
		var symbol, timeframe string
		if err := rows.Scan(
			&bar.Time, &symbol, &timeframe,
			&bar.DPVolume, &bar.DPTrades, &bar.DPVWAP,
			&bar.LitVolume, &bar.TotalVolume, &bar.DPRatio,
			&bar.BuyVolume, &bar.SellVolume,
			&bar.LargePrintVolume, &bar.LargePrintCount, &bar.MaxPrintSize,
		); err != nil {
			return nil, fmt.Errorf("timescaledb: scan dark pool bar: %w", err)
		}
		bar.Symbol = domain.Symbol(symbol)
		bar.Timeframe = domain.Timeframe(timeframe)
		bars = append(bars, bar)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate dark pool bars: %w", err)
	}
	return bars, nil
}

// GetDarkPoolBarsMulti fetches dark pool bars for multiple symbols in a single query.
func (r *DarkPoolRepo) GetDarkPoolBarsMulti(ctx context.Context, syms []domain.Symbol, tf domain.Timeframe, from, to time.Time) (map[string][]domain.DarkPoolBar, error) {
	if len(syms) == 0 {
		return map[string][]domain.DarkPoolBar{}, nil
	}

	// Build IN clause
	placeholders := make([]string, len(syms))
	args := make([]any, 0, len(syms)+3)
	for i, s := range syms {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args = append(args, string(s))
	}
	args = append(args, string(tf), from, to)

	query := fmt.Sprintf(
		`SELECT time, symbol, timeframe, dp_volume, dp_trades, dp_vwap, lit_volume, total_volume, dp_ratio, buy_volume, sell_volume, large_print_volume, large_print_count, max_print_size
		FROM darkpool_bars
		WHERE symbol IN (%s) AND timeframe = $%d AND time >= $%d AND time < $%d
		ORDER BY time ASC`,
		strings.Join(placeholders, ", "),
		len(syms)+1, len(syms)+2, len(syms)+3,
	)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("timescaledb: get dark pool bars multi: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]domain.DarkPoolBar, len(syms))
	for rows.Next() {
		var bar domain.DarkPoolBar
		var symbol, timeframe string
		if err := rows.Scan(
			&bar.Time, &symbol, &timeframe,
			&bar.DPVolume, &bar.DPTrades, &bar.DPVWAP,
			&bar.LitVolume, &bar.TotalVolume, &bar.DPRatio,
			&bar.BuyVolume, &bar.SellVolume,
			&bar.LargePrintVolume, &bar.LargePrintCount, &bar.MaxPrintSize,
		); err != nil {
			return nil, fmt.Errorf("timescaledb: scan dark pool bar multi: %w", err)
		}
		bar.Symbol = domain.Symbol(symbol)
		bar.Timeframe = domain.Timeframe(timeframe)
		result[symbol] = append(result[symbol], bar)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate dark pool bars multi: %w", err)
	}
	return result, nil
}

// GetLatestDarkPoolBarTime returns the most recent dark pool bar time for a given symbol and timeframe.
// Returns (nil, nil) if no bars exist.
func (r *DarkPoolRepo) GetLatestDarkPoolBarTime(ctx context.Context, sym domain.Symbol, tf domain.Timeframe) (*time.Time, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT MAX(time) FROM darkpool_bars WHERE symbol = $1 AND timeframe = $2",
		string(sym), string(tf))

	var t *time.Time
	if err := row.Scan(&t); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("timescaledb: get latest dark pool bar time: %w", err)
	}
	return t, nil
}
