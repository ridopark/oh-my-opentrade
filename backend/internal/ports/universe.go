package ports

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

type Asset struct {
	Symbol     string
	Name       string
	AssetClass domain.AssetClass
	Exchange   string
	Tradeable  bool
}

type UniverseProviderPort interface {
	ListTradeable(ctx context.Context, assetClass domain.AssetClass) ([]Asset, error)
}
