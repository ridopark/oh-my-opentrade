package ports

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

type AnchorStorePort interface {
	Save(ctx context.Context, anchors []strategy.CandidateAnchor) error
	LoadActive(ctx context.Context, symbol string) ([]strategy.CandidateAnchor, error)
	Expire(ctx context.Context, anchorID string, reason string) error
	SaveSelection(ctx context.Context, symbol string, sel strategy.AnchorSelection) error
}
