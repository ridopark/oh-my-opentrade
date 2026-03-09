package timescaledb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
)

const (
	queryUpsertToken = `INSERT INTO oauth_tokens (provider, tenant_id, access_token, refresh_token, expires_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (provider, tenant_id) DO UPDATE
		SET access_token = EXCLUDED.access_token,
		    refresh_token = EXCLUDED.refresh_token,
		    expires_at = EXCLUDED.expires_at,
		    updated_at = EXCLUDED.updated_at`

	querySelectToken = `SELECT provider, tenant_id, access_token, refresh_token, expires_at, updated_at
		FROM oauth_tokens WHERE provider = $1 AND tenant_id = $2`

	queryDeleteToken = `DELETE FROM oauth_tokens WHERE provider = $1 AND tenant_id = $2`
)

// TokenStore implements ports.TokenStorePort using TimescaleDB.
type TokenStore struct {
	db DBTX
}

// NewTokenStore creates a new TimescaleDB-backed token store.
func NewTokenStore(db DBTX) *TokenStore {
	return &TokenStore{db: db}
}

// SaveToken upserts an OAuth token for the given provider and tenant.
func (s *TokenStore) SaveToken(ctx context.Context, token ports.OAuthToken) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, queryUpsertToken,
		token.Provider, token.TenantID,
		token.AccessToken, token.RefreshToken,
		token.ExpiresAt, now,
	)
	if err != nil {
		return fmt.Errorf("timescaledb: save token: %w", err)
	}
	return nil
}

// LoadToken retrieves an OAuth token for the given provider and tenant.
func (s *TokenStore) LoadToken(ctx context.Context, provider, tenantID string) (*ports.OAuthToken, error) {
	row := s.db.QueryRowContext(ctx, querySelectToken, provider, tenantID)

	var token ports.OAuthToken
	err := row.Scan(
		&token.Provider, &token.TenantID,
		&token.AccessToken, &token.RefreshToken,
		&token.ExpiresAt, &token.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("timescaledb: load token: %w", err)
	}
	return &token, nil
}

// DeleteToken removes an OAuth token for the given provider and tenant.
func (s *TokenStore) DeleteToken(ctx context.Context, provider, tenantID string) error {
	_, err := s.db.ExecContext(ctx, queryDeleteToken, provider, tenantID)
	if err != nil {
		return fmt.Errorf("timescaledb: delete token: %w", err)
	}
	return nil
}
