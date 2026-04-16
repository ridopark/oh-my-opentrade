package timescaledb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// CorporateActionsRepo implements ports.CorporateActionsPort over the
// corporate_actions table (migration 038).
type CorporateActionsRepo struct {
	db  DBTX
	log zerolog.Logger
}

// NewCorporateActionsRepo constructs a repo over the given DBTX. The
// logger is decorated with a component tag for structured logs.
func NewCorporateActionsRepo(db DBTX, log zerolog.Logger) *CorporateActionsRepo {
	return &CorporateActionsRepo{
		db:  db,
		log: log.With().Str("component", "corporate_actions_repo").Logger(),
	}
}

var _ ports.CorporateActionsPort = (*CorporateActionsRepo)(nil)

const (
	queryUpsertCorporateAction = `
		INSERT INTO corporate_actions
			(symbol, action_type, effective_date, ratio_numerator, ratio_denominator, cash_component, source, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (symbol, effective_date, action_type) DO UPDATE SET
			ratio_numerator   = EXCLUDED.ratio_numerator,
			ratio_denominator = EXCLUDED.ratio_denominator,
			cash_component    = EXCLUDED.cash_component,
			source            = EXCLUDED.source,
			note              = EXCLUDED.note`

	querySelectCorporateActionsBetween = `
		SELECT symbol, action_type, effective_date, ratio_numerator, ratio_denominator,
		       COALESCE(cash_component, 0), source
		FROM corporate_actions
		WHERE symbol = $1
		  AND effective_date >= $2
		  AND effective_date <= $3
		ORDER BY effective_date ASC`

	querySelectDelisted = `
		SELECT 1
		FROM corporate_actions
		WHERE symbol = $1
		  AND action_type = 'delisting'
		  AND effective_date <= $2
		LIMIT 1`
)

// Upsert inserts a corporate action, replacing any existing row that shares
// (symbol, effective_date, action_type). The cash_component column is
// populated from ca.CashComponent; note is left NULL because the port type
// does not yet carry free-form text.
func (r *CorporateActionsRepo) Upsert(ctx context.Context, ca ports.CorporateAction) error {
	if ca.Symbol == "" {
		return fmt.Errorf("corporate_actions_repo: upsert: symbol required")
	}
	if ca.ActionType == "" {
		return fmt.Errorf("corporate_actions_repo: upsert: action_type required")
	}
	if ca.Source == "" {
		return fmt.Errorf("corporate_actions_repo: upsert: source required")
	}
	_, err := r.db.ExecContext(ctx, queryUpsertCorporateAction,
		ca.Symbol,
		ca.ActionType,
		ca.EffectiveDate,
		ca.RatioNumerator,
		ca.RatioDenominator,
		ca.CashComponent,
		ca.Source,
		sql.NullString{}, // note reserved
	)
	if err != nil {
		return fmt.Errorf("corporate_actions_repo: upsert %s/%s: %w", ca.Symbol, ca.ActionType, err)
	}
	return nil
}

// Between returns every corporate action for symbol in [from, to]. The
// returned slice is never nil (an empty slice is preferred) so callers can
// range without a nil check.
func (r *CorporateActionsRepo) Between(ctx context.Context, symbol string, from, to time.Time) ([]ports.CorporateAction, error) {
	rows, err := r.db.QueryContext(ctx, querySelectCorporateActionsBetween, symbol, from, to)
	if err != nil {
		return nil, fmt.Errorf("corporate_actions_repo: between %s: %w", symbol, err)
	}
	defer rows.Close()

	out := make([]ports.CorporateAction, 0)
	for rows.Next() {
		var ca ports.CorporateAction
		if scanErr := rows.Scan(
			&ca.Symbol,
			&ca.ActionType,
			&ca.EffectiveDate,
			&ca.RatioNumerator,
			&ca.RatioDenominator,
			&ca.CashComponent,
			&ca.Source,
		); scanErr != nil {
			return nil, fmt.Errorf("corporate_actions_repo: scan %s: %w", symbol, scanErr)
		}
		out = append(out, ca)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("corporate_actions_repo: rows err %s: %w", symbol, err)
	}
	return out, nil
}

// Delisted reports whether the symbol has a delisting action on or before
// asOf. A missing row is not an error — delisting is rare and the absence
// of a record means "still listed".
func (r *CorporateActionsRepo) Delisted(ctx context.Context, symbol string, asOf time.Time) (bool, error) {
	row := r.db.QueryRowContext(ctx, querySelectDelisted, symbol, asOf)
	var one int
	if err := row.Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("corporate_actions_repo: delisted %s: %w", symbol, err)
	}
	return true, nil
}
