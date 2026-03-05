package timescaledb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain/dnaapproval"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

const (
	queryInsertDNAVersion = `INSERT INTO dna_versions (id, strategy_key, content_toml, content_hash, detected_at)
		VALUES ($1, $2, $3, $4, $5)`
	querySelectDNAVersion       = `SELECT id, strategy_key, content_toml, content_hash, detected_at FROM dna_versions WHERE id = $1`
	querySelectDNAVersionByHash = `SELECT id, strategy_key, content_toml, content_hash, detected_at FROM dna_versions WHERE strategy_key = $1 AND content_hash = $2`

	queryInsertDNAApproval = `INSERT INTO dna_approvals (id, version_id, status, decided_by, decided_at, comment, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
	queryUpdateDNAApproval    = `UPDATE dna_approvals SET status = $2, decided_by = $3, decided_at = $4, comment = $5 WHERE id = $1`
	querySelectDNAApproval    = `SELECT id, version_id, status, decided_by, decided_at, comment, created_at FROM dna_approvals WHERE id = $1`
	queryListPendingApprovals = `SELECT id, version_id, status, decided_by, decided_at, comment, created_at
		FROM dna_approvals
		WHERE status = 'pending'
		ORDER BY created_at DESC`

	querySelectActiveDNAVersion = `SELECT v.id, v.strategy_key, v.content_toml, v.content_hash, v.detected_at
		FROM dna_versions v
		JOIN dna_approvals a ON a.version_id = v.id
		WHERE v.strategy_key = $1 AND a.status = 'approved'
		ORDER BY v.detected_at DESC
		LIMIT 1`
)

type DNAApprovalRepo struct {
	db  DBTX
	log zerolog.Logger
}

func NewDNAApprovalRepo(db DBTX, log zerolog.Logger) *DNAApprovalRepo {
	return &DNAApprovalRepo{db: db, log: log}
}

var _ ports.DNAApprovalRepoPort = (*DNAApprovalRepo)(nil)

func (r *DNAApprovalRepo) SaveDNAVersion(ctx context.Context, v dnaapproval.DNAVersion) error {
	_, err := r.db.ExecContext(ctx, queryInsertDNAVersion, v.ID, v.StrategyKey, v.ContentTOML, v.ContentHash, v.DetectedAt)
	if err != nil {
		r.log.Error().Err(err).Str("strategy_key", v.StrategyKey).Msg("failed to save dna version")
		return fmt.Errorf("timescaledb: save dna version: %w", err)
	}
	return nil
}

func (r *DNAApprovalRepo) GetDNAVersion(ctx context.Context, id string) (*dnaapproval.DNAVersion, error) {
	row := r.db.QueryRowContext(ctx, querySelectDNAVersion, id)
	var v dnaapproval.DNAVersion
	if err := row.Scan(&v.ID, &v.StrategyKey, &v.ContentTOML, &v.ContentHash, &v.DetectedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("timescaledb: get dna version: %w", err)
	}
	return &v, nil
}

func (r *DNAApprovalRepo) GetDNAVersionByHash(ctx context.Context, strategyKey, contentHash string) (*dnaapproval.DNAVersion, error) {
	row := r.db.QueryRowContext(ctx, querySelectDNAVersionByHash, strategyKey, contentHash)
	var v dnaapproval.DNAVersion
	if err := row.Scan(&v.ID, &v.StrategyKey, &v.ContentTOML, &v.ContentHash, &v.DetectedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("timescaledb: get dna version by hash: %w", err)
	}
	return &v, nil
}

func (r *DNAApprovalRepo) SaveDNAApproval(ctx context.Context, a dnaapproval.DNAApproval) error {
	_, err := r.db.ExecContext(ctx, queryInsertDNAApproval,
		a.ID, a.VersionID, string(a.Status), a.DecidedBy, a.DecidedAt, a.Comment, a.CreatedAt,
	)
	if err != nil {
		r.log.Error().Err(err).Str("approval_id", a.ID).Msg("failed to save dna approval")
		return fmt.Errorf("timescaledb: save dna approval: %w", err)
	}
	return nil
}

func (r *DNAApprovalRepo) UpdateDNAApproval(ctx context.Context, id string, status dnaapproval.DNAStatus, decidedBy string, comment string) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, queryUpdateDNAApproval, id, string(status), decidedBy, now, comment)
	if err != nil {
		r.log.Error().Err(err).Str("approval_id", id).Msg("failed to update dna approval")
		return fmt.Errorf("timescaledb: update dna approval: %w", err)
	}
	return nil
}

func (r *DNAApprovalRepo) GetDNAApproval(ctx context.Context, id string) (*dnaapproval.DNAApproval, error) {
	row := r.db.QueryRowContext(ctx, querySelectDNAApproval, id)
	var a dnaapproval.DNAApproval
	var status string
	if err := row.Scan(&a.ID, &a.VersionID, &status, &a.DecidedBy, &a.DecidedAt, &a.Comment, &a.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("timescaledb: get dna approval: %w", err)
	}
	a.Status = dnaapproval.DNAStatus(status)
	return &a, nil
}

func (r *DNAApprovalRepo) ListPendingApprovals(ctx context.Context) ([]dnaapproval.DNAApproval, error) {
	rows, err := r.db.QueryContext(ctx, queryListPendingApprovals)
	if err != nil {
		r.log.Error().Err(err).Msg("failed to list pending dna approvals")
		return nil, fmt.Errorf("timescaledb: list pending dna approvals: %w", err)
	}
	defer rows.Close()

	var out []dnaapproval.DNAApproval
	for rows.Next() {
		var a dnaapproval.DNAApproval
		var status string
		if err := rows.Scan(&a.ID, &a.VersionID, &status, &a.DecidedBy, &a.DecidedAt, &a.Comment, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("timescaledb: scan dna approval: %w", err)
		}
		a.Status = dnaapproval.DNAStatus(status)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate dna approvals: %w", err)
	}
	if out == nil {
		out = []dnaapproval.DNAApproval{}
	}
	return out, nil
}

func (r *DNAApprovalRepo) GetActiveDNAVersion(ctx context.Context, strategyKey string) (*dnaapproval.DNAVersion, error) {
	row := r.db.QueryRowContext(ctx, querySelectActiveDNAVersion, strategyKey)
	var v dnaapproval.DNAVersion
	if err := row.Scan(&v.ID, &v.StrategyKey, &v.ContentTOML, &v.ContentHash, &v.DetectedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("timescaledb: get active dna version: %w", err)
	}
	return &v, nil
}
