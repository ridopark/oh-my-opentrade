package timescaledb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

const (
	queryInsertOrderIntent = `INSERT INTO order_intents (
		id, idempotency_key, tenant_id, env_mode, symbol, direction, asset_class,
		order_type, time_in_force, quantity, limit_price, stop_loss, max_slippage_bps,
		strategy, confidence, max_loss_usd, instrument_kind, instrument_json,
		status, created_at, meta, venue
	) VALUES (
		$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
		$14, $15, $16, $17, $18, $19, $20, $21, $22
	)`

	queryMarkIntentSubmitted = `UPDATE order_intents
		SET status = 'submitted', broker_order_id = $2, submitted_at = $3
		WHERE id = $1 AND status = 'pending_submit'`

	queryMarkIntentSubmitFailed = `UPDATE order_intents
		SET status = 'rejected', submit_error = $2, terminal_at = $3
		WHERE id = $1 AND status = 'pending_submit'`

	queryMarkIntentTerminal = `UPDATE order_intents
		SET status = $2, filled_qty = $3, filled_avg_price = $4, terminal_at = $5
		WHERE broker_order_id = $1 AND status NOT IN ('filled', 'canceled', 'rejected', 'expired', 'lost')`

	queryMarkIntentLost = `UPDATE order_intents
		SET status = 'lost', terminal_at = $2
		WHERE id = $1 AND status = 'submitted'`

	querySelectOpenIntents = `SELECT
		id, idempotency_key, tenant_id, env_mode, symbol, direction, asset_class,
		order_type, time_in_force, quantity,
		COALESCE(limit_price, 0), COALESCE(stop_loss, 0), COALESCE(max_slippage_bps, 0),
		COALESCE(strategy, ''), COALESCE(confidence, 0), COALESCE(max_loss_usd, 0),
		COALESCE(instrument_kind, ''), instrument_json,
		status, COALESCE(broker_order_id, ''), COALESCE(submit_error, ''),
		filled_qty, filled_avg_price,
		created_at, submitted_at, terminal_at, meta, COALESCE(venue, '')
	FROM order_intents
	WHERE tenant_id = $1 AND env_mode = $2
	  AND status NOT IN ('filled', 'canceled', 'rejected', 'expired', 'lost')
	  AND created_at >= $3
	ORDER BY created_at ASC`
)

// OrderIntentRepo is the TimescaleDB-backed implementation of the
// order-intent write-ahead journal introduced in Sprint 2.
type OrderIntentRepo struct {
	db  DBTX
	log zerolog.Logger
}

// NewOrderIntentRepo returns a new journal repo backed by the given DBTX.
func NewOrderIntentRepo(db DBTX, log zerolog.Logger) *OrderIntentRepo {
	return &OrderIntentRepo{db: db, log: log}
}

// Compile-time interface assertion.
var _ ports.OrderIntentJournal = (*OrderIntentRepo)(nil)

// SaveOrderIntent persists a new intent row with status=pending_submit.
// Returns ports.ErrDuplicateIntent when the unique idempotency key is violated.
func (r *OrderIntentRepo) SaveOrderIntent(ctx context.Context, intent domain.OrderIntent) error {
	var limitPrice, stopLoss, confidence, maxLoss any
	if intent.LimitPrice > 0 {
		limitPrice = intent.LimitPrice
	}
	if intent.StopLoss > 0 {
		stopLoss = intent.StopLoss
	}
	if intent.Confidence > 0 {
		confidence = intent.Confidence
	}
	if intent.MaxLossUSD > 0 {
		maxLoss = intent.MaxLossUSD
	}

	var instrumentKind any
	var instrumentJSON any
	if intent.Instrument != nil {
		instrumentKind = string(intent.Instrument.Type)
		if raw, err := json.Marshal(intent.Instrument); err == nil {
			instrumentJSON = raw
		}
	}

	var metaJSON any
	if len(intent.Meta) > 0 {
		if raw, err := json.Marshal(intent.Meta); err == nil {
			metaJSON = raw
		}
	}

	orderType := intent.OrderType
	if orderType == "" {
		orderType = "limit"
	}
	tif := intent.TimeInForce
	if tif == "" {
		tif = "gtc"
	}

	// Venue: empty (NULL) means "implicit default per asset class" for
	// backward compat; only non-empty venues get persisted so legacy rows
	// keep their NULL and callers fall through DefaultVenue at read time.
	var venue any
	if !intent.Venue.IsUnspecified() {
		venue = string(intent.Venue)
	}

	_, err := r.db.ExecContext(ctx, queryInsertOrderIntent,
		intent.ID,
		intent.IdempotencyKey,
		intent.TenantID,
		string(intent.EnvMode),
		string(intent.Symbol),
		string(intent.Direction),
		string(intent.AssetClass),
		orderType,
		tif,
		intent.Quantity,
		limitPrice,
		stopLoss,
		intent.MaxSlippageBPS,
		intent.Strategy,
		confidence,
		maxLoss,
		instrumentKind,
		instrumentJSON,
		domain.OrderIntentJournalPendingSubmit,
		time.Now().UTC(),
		metaJSON,
		venue,
	)
	if err != nil {
		if isDuplicateKeyErr(err) {
			return ports.ErrDuplicateIntent
		}
		r.log.Error().Err(err).Str("intent_id", intent.ID.String()).Msg("failed to save order intent journal row")
		return fmt.Errorf("timescaledb: save order intent: %w", err)
	}
	return nil
}

// MarkIntentSubmitted transitions pending_submit -> submitted and stamps the broker_order_id.
func (r *OrderIntentRepo) MarkIntentSubmitted(ctx context.Context, intentID uuid.UUID, brokerOrderID string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, queryMarkIntentSubmitted, intentID, brokerOrderID, at.UTC())
	if err != nil {
		r.log.Error().Err(err).Str("intent_id", intentID.String()).Msg("failed to mark intent submitted")
		return fmt.Errorf("timescaledb: mark intent submitted: %w", err)
	}
	return nil
}

// MarkIntentSubmitFailed transitions pending_submit -> rejected and stores the broker error message.
func (r *OrderIntentRepo) MarkIntentSubmitFailed(ctx context.Context, intentID uuid.UUID, errMsg string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, queryMarkIntentSubmitFailed, intentID, errMsg, at.UTC())
	if err != nil {
		r.log.Error().Err(err).Str("intent_id", intentID.String()).Msg("failed to mark intent submit failed")
		return fmt.Errorf("timescaledb: mark intent submit failed: %w", err)
	}
	return nil
}

// MarkIntentTerminal transitions the row matching brokerOrderID to a terminal state.
func (r *OrderIntentRepo) MarkIntentTerminal(ctx context.Context, brokerOrderID string, status string, filledQty, filledAvgPrice float64, at time.Time) error {
	if brokerOrderID == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, queryMarkIntentTerminal, brokerOrderID, status, filledQty, filledAvgPrice, at.UTC())
	if err != nil {
		r.log.Error().Err(err).Str("broker_order_id", brokerOrderID).Msg("failed to mark intent terminal")
		return fmt.Errorf("timescaledb: mark intent terminal: %w", err)
	}
	return nil
}

// MarkIntentLost transitions submitted -> lost for startup reconciliation.
func (r *OrderIntentRepo) MarkIntentLost(ctx context.Context, intentID uuid.UUID, at time.Time) error {
	_, err := r.db.ExecContext(ctx, queryMarkIntentLost, intentID, at.UTC())
	if err != nil {
		r.log.Error().Err(err).Str("intent_id", intentID.String()).Msg("failed to mark intent lost")
		return fmt.Errorf("timescaledb: mark intent lost: %w", err)
	}
	return nil
}

// OpenIntents returns all non-terminal journal rows created within the lookback window.
func (r *OrderIntentRepo) OpenIntents(ctx context.Context, tenantID string, envMode domain.EnvMode, lookback time.Duration) ([]domain.OrderIntentJournalRow, error) {
	cutoff := time.Now().UTC().Add(-lookback)
	rows, err := r.db.QueryContext(ctx, querySelectOpenIntents, tenantID, string(envMode), cutoff)
	if err != nil {
		return nil, fmt.Errorf("timescaledb: query open intents: %w", err)
	}
	defer rows.Close()

	out := make([]domain.OrderIntentJournalRow, 0)
	for rows.Next() {
		var row domain.OrderIntentJournalRow
		var envModeStr, symbolStr, directionStr, assetClassStr, venueStr string
		var instrumentJSON sql.NullString
		var submittedAt, terminalAt sql.NullTime
		var metaJSON sql.NullString
		if err := rows.Scan(
			&row.ID,
			&row.IdempotencyKey,
			&row.TenantID,
			&envModeStr,
			&symbolStr,
			&directionStr,
			&assetClassStr,
			&row.OrderType,
			&row.TimeInForce,
			&row.Quantity,
			&row.LimitPrice,
			&row.StopLoss,
			&row.MaxSlippageBPS,
			&row.Strategy,
			&row.Confidence,
			&row.MaxLossUSD,
			&row.InstrumentKind,
			&instrumentJSON,
			&row.Status,
			&row.BrokerOrderID,
			&row.SubmitError,
			&row.FilledQty,
			&row.FilledAvgPrice,
			&row.CreatedAt,
			&submittedAt,
			&terminalAt,
			&metaJSON,
			&venueStr,
		); err != nil {
			return nil, fmt.Errorf("timescaledb: scan open intent: %w", err)
		}
		row.EnvMode = domain.EnvMode(envModeStr)
		row.Symbol = domain.Symbol(symbolStr)
		row.Direction = domain.Direction(directionStr)
		row.AssetClass = domain.AssetClass(assetClassStr)
		row.Venue = domain.Venue(venueStr)
		if instrumentJSON.Valid {
			row.InstrumentJSON = []byte(instrumentJSON.String)
		}
		if submittedAt.Valid {
			t := submittedAt.Time
			row.SubmittedAt = &t
		}
		if terminalAt.Valid {
			t := terminalAt.Time
			row.TerminalAt = &t
		}
		if metaJSON.Valid {
			row.Meta = []byte(metaJSON.String)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate open intents: %w", err)
	}
	return out, nil
}

// isDuplicateKeyErr inspects a database error for the Postgres unique-violation
// signature (SQLSTATE 23505). We don't import lib/pq or pgx directly from the
// repo package, so we fall back to a case-insensitive substring match on the
// error message, which covers both drivers and the test mocks.
func isDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ports.ErrDuplicateIntent) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "unique constraint")
}
