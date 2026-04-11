package timescaledb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// BacktestHistoryRepo is the Postgres-backed implementation of
// ports.BacktestHistoryPort. It takes a raw *sql.DB (not DBTX) because the
// Save path wraps the parent + child inserts in a transaction for atomicity.
type BacktestHistoryRepo struct {
	db  *sql.DB
	log zerolog.Logger
}

// NewBacktestHistoryRepo constructs a new repository backed by the given DB.
func NewBacktestHistoryRepo(db *sql.DB, log zerolog.Logger) *BacktestHistoryRepo {
	return &BacktestHistoryRepo{db: db, log: log}
}

var _ ports.BacktestHistoryPort = (*BacktestHistoryRepo)(nil)

const insertBacktestRun = `INSERT INTO backtest_runs (
	id, ran_at,
	strategies, symbols, period_start, period_end,
	initial_equity, slippage_bps, no_ai,
	pf, win_rate, expectancy, max_drawdown, sharpe,
	trade_count, win_count, loss_count,
	net_pnl, total_return, final_equity,
	equity_curve, dna_snapshot, tags
) VALUES (
	$1, $2,
	$3, $4, $5, $6,
	$7, $8, $9,
	$10, $11, $12, $13, $14,
	$15, $16, $17,
	$18, $19, $20,
	$21, $22, $23
)`

// Save writes the parent row and all trade rows in a single transaction.
func (r *BacktestHistoryRepo) Save(ctx context.Context, row ports.BacktestRunRow, trades []ports.BacktestTradeRow) error {
	equityJSON, err := json.Marshal(row.EquityCurve)
	if err != nil {
		return fmt.Errorf("backtest_history: marshal equity: %w", err)
	}
	dnaJSON, err := json.Marshal(row.DNASnapshot)
	if err != nil {
		return fmt.Errorf("backtest_history: marshal dna: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("backtest_history: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if row.RanAt.IsZero() {
		row.RanAt = time.Now().UTC()
	}
	if row.Tags == nil {
		row.Tags = []string{}
	}

	if _, err := tx.ExecContext(ctx, insertBacktestRun,
		row.ID, row.RanAt,
		stringArray(row.Strategies), stringArray(row.Symbols), row.PeriodStart, row.PeriodEnd,
		row.InitialEquity, row.SlippageBPS, row.NoAI,
		row.PF, row.WinRate, row.Expectancy, row.MaxDrawdown, row.Sharpe,
		row.TradeCount, row.WinCount, row.LossCount,
		row.NetPnL, row.TotalReturn, row.FinalEquity,
		equityJSON, dnaJSON, stringArray(row.Tags),
	); err != nil {
		return fmt.Errorf("backtest_history: insert run: %w", err)
	}

	if err := insertTradesBatched(ctx, tx, row.ID, trades); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("backtest_history: commit: %w", err)
	}
	committed = true
	return nil
}

// insertTradesBatched bulk-inserts trade rows using multi-row INSERT. Batched
// at 1000 rows to stay well under Postgres's 65535 parameter limit
// (1000 * 13 cols = 13000 params).
func insertTradesBatched(ctx context.Context, tx *sql.Tx, runID string, trades []ports.BacktestTradeRow) error {
	if len(trades) == 0 {
		return nil
	}
	const batchSize = 1000
	const colsPerRow = 13

	for start := 0; start < len(trades); start += batchSize {
		end := min(start+batchSize, len(trades))
		batch := trades[start:end]

		var (
			sb   strings.Builder
			args = make([]any, 0, len(batch)*colsPerRow+1)
		)
		sb.WriteString(`INSERT INTO backtest_run_trades (
			run_id, seq, symbol, side, direction, quantity, price, filled_at,
			pnl, strategy_id, rationale, regime, vix_bucket, market_context
		) VALUES `)

		args = append(args, runID)
		for i, t := range batch {
			if i > 0 {
				sb.WriteString(", ")
			}
			base := 2 + i*colsPerRow
			fmt.Fprintf(&sb, "($1, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
				base, base+1, base+2, base+3, base+4, base+5, base+6,
				base+7, base+8, base+9, base+10, base+11, base+12,
			)
			args = append(args,
				t.Seq, t.Symbol, t.Side, nullableString(t.Direction),
				t.Quantity, t.Price, t.FilledAt, t.PnL,
				nullableString(t.StrategyID), nullableString(t.Rationale),
				nullableString(t.Regime), nullableString(t.VIXBucket),
				nullableString(t.MarketContext),
			)
		}

		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("backtest_history: insert trades batch %d: %w", start, err)
		}
	}
	return nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// List returns (summaries, totalMatchingCount, error).
func (r *BacktestHistoryRepo) List(ctx context.Context, f ports.BacktestListFilter) ([]ports.BacktestRunSummary, int, error) {
	where, args := buildHistoryWhere(f)

	countQ := "SELECT COUNT(*) FROM backtest_runs " + where
	var total int
	if err := r.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("backtest_history: count: %w", err)
	}

	orderBy := sanitizeOrderBy(f.OrderBy)
	orderDir := "DESC"
	if strings.EqualFold(f.OrderDir, "asc") {
		orderDir = "ASC"
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	listQ := fmt.Sprintf(`SELECT
		id, ran_at, strategies, symbols, period_start, period_end,
		pf, win_rate, expectancy, max_drawdown, sharpe,
		trade_count, net_pnl, total_return, equity_curve, tags, pinned
	FROM backtest_runs
	%s
	ORDER BY %s %s
	LIMIT $%d OFFSET $%d`, where, orderBy, orderDir, len(args)+1, len(args)+2)

	args = append(args, limit, f.Offset)

	rows, err := r.db.QueryContext(ctx, listQ, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("backtest_history: list: %w", err)
	}
	defer rows.Close()

	out := make([]ports.BacktestRunSummary, 0, limit)
	for rows.Next() {
		var (
			s           ports.BacktestRunSummary
			equityJSON  []byte
			stratArr    stringArray
			symArr      stringArray
			tagArr      stringArray
		)
		if err := rows.Scan(
			&s.ID, &s.RanAt, &stratArr, &symArr, &s.PeriodStart, &s.PeriodEnd,
			&s.PF, &s.WinRate, &s.Expectancy, &s.MaxDrawdown, &s.Sharpe,
			&s.TradeCount, &s.NetPnL, &s.TotalReturn, &equityJSON, &tagArr, &s.Pinned,
		); err != nil {
			return nil, 0, fmt.Errorf("backtest_history: scan: %w", err)
		}
		s.Strategies = []string(stratArr)
		s.Symbols = []string(symArr)
		s.Tags = []string(tagArr)
		if len(equityJSON) > 0 {
			_ = json.Unmarshal(equityJSON, &s.EquityCurve)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("backtest_history: rows: %w", err)
	}
	return out, total, nil
}

// Get returns the full detail payload for a single run.
func (r *BacktestHistoryRepo) Get(ctx context.Context, id string) (*ports.BacktestRunDetail, error) {
	const runQ = `SELECT
		id, ran_at, strategies, symbols, period_start, period_end,
		initial_equity, slippage_bps, no_ai,
		pf, win_rate, expectancy, max_drawdown, sharpe,
		trade_count, win_count, loss_count,
		net_pnl, total_return, final_equity,
		equity_curve, dna_snapshot, tags, pinned, COALESCE(notes, '')
	FROM backtest_runs WHERE id = $1`

	var (
		d          ports.BacktestRunDetail
		equityJSON []byte
		dnaJSON    []byte
		stratArr   stringArray
		symArr     stringArray
		tagArr     stringArray
	)
	err := r.db.QueryRowContext(ctx, runQ, id).Scan(
		&d.Summary.ID, &d.Summary.RanAt, &stratArr, &symArr, &d.Summary.PeriodStart, &d.Summary.PeriodEnd,
		&d.InitialEquity, &d.SlippageBPS, &d.NoAI,
		&d.Summary.PF, &d.Summary.WinRate, &d.Summary.Expectancy, &d.Summary.MaxDrawdown, &d.Summary.Sharpe,
		&d.Summary.TradeCount, &d.WinCount, &d.LossCount,
		&d.Summary.NetPnL, &d.Summary.TotalReturn, &d.FinalEquity,
		&equityJSON, &dnaJSON, &tagArr, &d.Summary.Pinned, &d.Notes,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backtest_history: get run: %w", err)
	}
	d.Summary.Strategies = []string(stratArr)
	d.Summary.Symbols = []string(symArr)
	d.Summary.Tags = []string(tagArr)
	if len(equityJSON) > 0 {
		_ = json.Unmarshal(equityJSON, &d.Summary.EquityCurve)
	}
	if len(dnaJSON) > 0 {
		_ = json.Unmarshal(dnaJSON, &d.DNASnapshot)
	}

	const tradesQ = `SELECT
		seq, symbol, side, COALESCE(direction, ''), quantity, price, filled_at,
		pnl, COALESCE(strategy_id, ''), COALESCE(rationale, ''),
		COALESCE(regime, ''), COALESCE(vix_bucket, ''), COALESCE(market_context, '')
	FROM backtest_run_trades
	WHERE run_id = $1
	ORDER BY seq ASC`

	rows, err := r.db.QueryContext(ctx, tradesQ, id)
	if err != nil {
		return nil, fmt.Errorf("backtest_history: get trades: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var t ports.BacktestTradeRow
		if err := rows.Scan(
			&t.Seq, &t.Symbol, &t.Side, &t.Direction,
			&t.Quantity, &t.Price, &t.FilledAt, &t.PnL,
			&t.StrategyID, &t.Rationale, &t.Regime, &t.VIXBucket, &t.MarketContext,
		); err != nil {
			return nil, fmt.Errorf("backtest_history: scan trade: %w", err)
		}
		d.Trades = append(d.Trades, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backtest_history: trade rows: %w", err)
	}
	return &d, nil
}

// SetTags replaces the tags array for a run.
func (r *BacktestHistoryRepo) SetTags(ctx context.Context, id string, tags []string) error {
	if tags == nil {
		tags = []string{}
	}
	_, err := r.db.ExecContext(ctx, `UPDATE backtest_runs SET tags = $2 WHERE id = $1`, id, stringArray(tags))
	if err != nil {
		return fmt.Errorf("backtest_history: set tags: %w", err)
	}
	return nil
}

// SetPinned toggles the pinned flag.
func (r *BacktestHistoryRepo) SetPinned(ctx context.Context, id string, pinned bool) error {
	_, err := r.db.ExecContext(ctx, `UPDATE backtest_runs SET pinned = $2 WHERE id = $1`, id, pinned)
	if err != nil {
		return fmt.Errorf("backtest_history: set pinned: %w", err)
	}
	return nil
}

// buildHistoryWhere assembles a WHERE clause + positional args from the
// filter. Returns an empty string (no WHERE) when no filters are set.
func buildHistoryWhere(f ports.BacktestListFilter) (string, []any) {
	var (
		clauses []string
		args    []any
	)
	add := func(clause string, vals ...any) {
		// Rewrite "$?" placeholders to match current arg count.
		var b strings.Builder
		for _, ch := range clause {
			if ch == '?' {
				fmt.Fprintf(&b, "$%d", len(args)+1)
				args = append(args, vals[0])
				vals = vals[1:]
			} else {
				b.WriteRune(ch)
			}
		}
		clauses = append(clauses, b.String())
	}

	if len(f.Strategies) > 0 {
		add("strategies && ?", stringArray(f.Strategies))
	}
	if len(f.Symbols) > 0 {
		add("symbols && ?", stringArray(f.Symbols))
	}
	if !f.From.IsZero() {
		add("ran_at >= ?", f.From)
	}
	if !f.To.IsZero() {
		add("ran_at <= ?", f.To)
	}
	if f.MinPF > 0 {
		add("pf >= ?", f.MinPF)
	}
	if len(f.Tags) > 0 {
		add("tags && ?", stringArray(f.Tags))
	}
	if f.PinnedOnly {
		clauses = append(clauses, "pinned = true")
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		pat := "%" + strings.ToLower(s) + "%"
		add("(id::text ILIKE ? OR array_to_string(symbols, ',') ILIKE ? OR array_to_string(strategies, ',') ILIKE ? OR array_to_string(tags, ',') ILIKE ?)", pat, pat, pat, pat)
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func sanitizeOrderBy(s string) string {
	switch s {
	case "pf", "win_rate", "expectancy", "max_drawdown", "sharpe",
		"trade_count", "net_pnl", "total_return", "ran_at":
		return s
	default:
		return "ran_at"
	}
}
