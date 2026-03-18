package timescaledb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/rs/zerolog"
)

const (
	queryUpsertAnchor = `INSERT INTO anchor_points (id, symbol, anchor_time, price, anchor_type, timeframe, strength, source, touch_count, volume_context, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (id) DO UPDATE SET strength = EXCLUDED.strength, touch_count = EXCLUDED.touch_count`

	queryLoadActiveAnchors = `SELECT id, symbol, anchor_time, price, anchor_type, timeframe, strength, source, touch_count, volume_context, ai_rank, ai_confidence, ai_reason
		FROM anchor_points WHERE symbol = $1 AND expired_at IS NULL ORDER BY strength DESC`

	queryExpireAnchor = `UPDATE anchor_points SET expired_at = $2, expired_reason = $3 WHERE id = $1`

	queryUpdateSelection = `UPDATE anchor_points SET ai_rank = $2, ai_confidence = $3, ai_reason = $4 WHERE id = $1`
)

type AnchorStore struct {
	db  DBTX
	log zerolog.Logger
}

func NewAnchorStore(db DBTX, log zerolog.Logger) *AnchorStore {
	return &AnchorStore{db: db, log: log}
}

func (s *AnchorStore) Save(ctx context.Context, anchors []strategy.CandidateAnchor) error {
	for _, a := range anchors {
		var vcJSON []byte
		if a.VolumeContext != nil {
			var err error
			vcJSON, err = json.Marshal(a.VolumeContext)
			if err != nil {
				return fmt.Errorf("anchor_store: marshal volume context: %w", err)
			}
		}

		_, err := s.db.ExecContext(ctx, queryUpsertAnchor,
			a.ID, "", a.Time, a.Price, string(a.Type), a.Timeframe,
			a.Strength, a.Source, a.TouchCount, vcJSON, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("anchor_store: upsert %s: %w", a.ID, err)
		}
	}
	return nil
}

func (s *AnchorStore) LoadActive(ctx context.Context, symbol string) ([]strategy.CandidateAnchor, error) {
	rows, err := s.db.QueryContext(ctx, queryLoadActiveAnchors, symbol)
	if err != nil {
		return nil, fmt.Errorf("anchor_store: load active: %w", err)
	}
	defer rows.Close()

	var result []strategy.CandidateAnchor
	for rows.Next() {
		var (
			ca         strategy.CandidateAnchor
			anchorType string
			vcJSON     []byte
			aiRank     *int
			aiConf     *float64
			aiReason   *string
		)

		if err := rows.Scan(
			&ca.ID, &ca.Source, &ca.Time, &ca.Price,
			&anchorType, &ca.Timeframe, &ca.Strength, &ca.Source,
			&ca.TouchCount, &vcJSON, &aiRank, &aiConf, &aiReason,
		); err != nil {
			return nil, fmt.Errorf("anchor_store: scan row: %w", err)
		}

		ca.Type = strategy.CandidateAnchorType(anchorType)

		if len(vcJSON) > 0 {
			var vc strategy.VolumeRotationContext
			if err := json.Unmarshal(vcJSON, &vc); err == nil {
				ca.VolumeContext = &vc
			}
		}

		result = append(result, ca)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("anchor_store: rows iteration: %w", err)
	}

	return result, nil
}

func (s *AnchorStore) Expire(ctx context.Context, anchorID string, reason string) error {
	_, err := s.db.ExecContext(ctx, queryExpireAnchor, anchorID, time.Now().UTC(), reason)
	if err != nil {
		return fmt.Errorf("anchor_store: expire %s: %w", anchorID, err)
	}
	return nil
}

func (s *AnchorStore) SaveSelection(ctx context.Context, symbol string, sel strategy.AnchorSelection) error {
	for _, sa := range sel.SelectedAnchors {
		_, err := s.db.ExecContext(ctx, queryUpdateSelection,
			sa.CandidateID, sa.Rank, sa.Confidence, sa.Reason)
		if err != nil {
			return fmt.Errorf("anchor_store: save selection for %s: %w", sa.CandidateID, err)
		}
	}
	return nil
}
