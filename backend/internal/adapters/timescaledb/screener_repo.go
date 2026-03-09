package timescaledb

import (
	"context"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/domain/screener"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

const (
	queryUpsertScreenerResult = `INSERT INTO screener_results (
        tenant_id, env_mode, run_id, as_of, symbol,
        prev_close, premarket_price, premarket_volume,
        avg_hist_volume, gap_pct, rvol,
        gap_score, rvol_score, news_score, total_score,
        status, price_source, error_msg, created_at
    ) VALUES (
        $1,$2,$3,$4,$5,
        $6,$7,$8,
        $9,$10,$11,
        $12,$13,$14,$15,
        $16,$17,$18,$19
    )
    ON CONFLICT (tenant_id, env_mode, run_id, symbol) DO UPDATE SET
        as_of = EXCLUDED.as_of,
        prev_close = EXCLUDED.prev_close,
        premarket_price = EXCLUDED.premarket_price,
        premarket_volume = EXCLUDED.premarket_volume,
        avg_hist_volume = EXCLUDED.avg_hist_volume,
        gap_pct = EXCLUDED.gap_pct,
        rvol = EXCLUDED.rvol,
        gap_score = EXCLUDED.gap_score,
        rvol_score = EXCLUDED.rvol_score,
        news_score = EXCLUDED.news_score,
        total_score = EXCLUDED.total_score,
        status = EXCLUDED.status,
        price_source = EXCLUDED.price_source,
        error_msg = EXCLUDED.error_msg,
        created_at = EXCLUDED.created_at`
)

type ScreenerRepo struct {
	db  DBTX
	log zerolog.Logger
}

func NewScreenerRepo(db DBTX, log zerolog.Logger) *ScreenerRepo {
	return &ScreenerRepo{db: db, log: log}
}

var _ ports.ScreenerRepoPort = (*ScreenerRepo)(nil)

func (r *ScreenerRepo) SaveResults(ctx context.Context, results []screener.ScreenerResult) error {
	for _, res := range results {
		if err := res.Validate(); err != nil {
			return fmt.Errorf("timescaledb: invalid screener result: %w", err)
		}

		createdAt := res.CreatedAt
		if createdAt.IsZero() {
			createdAt = res.AsOf
		}

		var newsScore any
		if res.Score.NewsScore != nil {
			newsScore = *res.Score.NewsScore
		} else {
			newsScore = nil
		}

		var priceSource any
		if res.PriceSource != nil {
			priceSource = string(*res.PriceSource)
		} else {
			priceSource = nil
		}

		var errMsg any
		if res.ErrorMsg != nil {
			errMsg = *res.ErrorMsg
		} else {
			errMsg = nil
		}

		_, err := r.db.ExecContext(ctx, queryUpsertScreenerResult,
			res.TenantID,
			res.EnvMode,
			res.RunID,
			res.AsOf,
			res.Symbol,
			nf64(res.PrevClose),
			nf64(res.PreMarketPrice),
			ni64(res.PreMarketVolume),
			ni64(res.AvgHistVolume),
			nf64(res.GapPct),
			nf64(res.RVOL),
			res.Score.GapScore,
			res.Score.RVOLScore,
			newsScore,
			res.Score.Total,
			string(res.Status),
			priceSource,
			errMsg,
			createdAt,
		)
		if err != nil {
			r.log.Error().Err(err).Str("symbol", res.Symbol).Msg("failed to save screener result")
			return fmt.Errorf("timescaledb: save screener result: %w", err)
		}
	}

	return nil
}

func nf64(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func ni64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
