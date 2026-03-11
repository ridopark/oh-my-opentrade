package timescaledb

import (
	"context"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain/screener"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

const (
	queryInsertAIResult = `INSERT INTO ai_screener_results (
		tenant_id, env_mode, run_id, as_of, strategy_key,
		symbol, anon_id, score, rationale, model,
		latency_ms, prompt_hash, created_at
	) VALUES (
		$1,$2,$3,$4,$5,
		$6,$7,$8,$9,$10,
		$11,$12,$13
	)
	ON CONFLICT (tenant_id, env_mode, run_id, strategy_key, symbol, as_of) DO UPDATE SET
		anon_id = EXCLUDED.anon_id,
		score = EXCLUDED.score,
		rationale = EXCLUDED.rationale,
		model = EXCLUDED.model,
		latency_ms = EXCLUDED.latency_ms,
		prompt_hash = EXCLUDED.prompt_hash,
		created_at = EXCLUDED.created_at`

	querySelectLatestAIResults = `SELECT
		tenant_id, env_mode, run_id, as_of, strategy_key,
		symbol, anon_id, score, rationale, model,
		latency_ms, prompt_hash, created_at
	FROM ai_screener_results
	WHERE tenant_id = $1 AND env_mode = $2 AND strategy_key = $3
		AND as_of = (
			SELECT MAX(as_of) FROM ai_screener_results
			WHERE tenant_id = $1 AND env_mode = $2 AND strategy_key = $3
		)
	ORDER BY score DESC`
)

type AIScreenerRepo struct {
	db  DBTX
	log zerolog.Logger
}

func NewAIScreenerRepo(db DBTX, log zerolog.Logger) *AIScreenerRepo {
	return &AIScreenerRepo{db: db, log: log}
}

var _ ports.AIScreenerRepoPort = (*AIScreenerRepo)(nil)

func (r *AIScreenerRepo) SaveAIResults(ctx context.Context, results []screener.AIScreenerResult) error {
	for _, res := range results {
		createdAt := res.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now()
		}

		_, err := r.db.ExecContext(ctx, queryInsertAIResult,
			res.TenantID,
			res.EnvMode,
			res.RunID,
			res.AsOf,
			res.StrategyKey,
			res.Symbol,
			res.AnonID,
			res.Score,
			res.Rationale,
			res.Model,
			res.LatencyMS,
			res.PromptHash,
			createdAt,
		)
		if err != nil {
			r.log.Error().Err(err).Str("symbol", res.Symbol).Msg("failed to save AI screener result")
			return fmt.Errorf("timescaledb: save ai screener result: %w", err)
		}
	}
	return nil
}

func (r *AIScreenerRepo) GetLatestAIResults(ctx context.Context, tenantID, envMode, strategyKey string) ([]screener.AIScreenerResult, error) {
	rows, err := r.db.QueryContext(ctx, querySelectLatestAIResults, tenantID, envMode, strategyKey)
	if err != nil {
		r.log.Error().Err(err).
			Str("tenant_id", tenantID).
			Str("strategy_key", strategyKey).
			Msg("failed to query latest AI screener results")
		return nil, fmt.Errorf("timescaledb: get latest ai screener results: %w", err)
	}
	defer rows.Close()

	var results []screener.AIScreenerResult
	for rows.Next() {
		var res screener.AIScreenerResult
		if err := rows.Scan(
			&res.TenantID,
			&res.EnvMode,
			&res.RunID,
			&res.AsOf,
			&res.StrategyKey,
			&res.Symbol,
			&res.AnonID,
			&res.Score,
			&res.Rationale,
			&res.Model,
			&res.LatencyMS,
			&res.PromptHash,
			&res.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("timescaledb: scan ai screener result: %w", err)
		}
		results = append(results, res)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate ai screener results: %w", err)
	}
	return results, nil
}
