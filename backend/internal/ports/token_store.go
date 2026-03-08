package ports

import (
	"context"
	"time"
)

// OAuthToken represents a stored OAuth2 token for a third-party provider.
type OAuthToken struct {
	Provider     string
	TenantID     string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	UpdatedAt    time.Time
}

// TokenStorePort defines the interface for persisting OAuth2 tokens.
type TokenStorePort interface {
	SaveToken(ctx context.Context, token OAuthToken) error
	LoadToken(ctx context.Context, provider, tenantID string) (*OAuthToken, error)
	DeleteToken(ctx context.Context, provider, tenantID string) error
}
