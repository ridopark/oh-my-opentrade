package ibkr

import (
	"context"
	"errors"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

var errDeferred = errors.New("ibkr: not supported in this release (Phase 5 deferred)")

func (a *Adapter) GetOptionChain(_ context.Context, _ domain.Symbol, _ time.Time, _ domain.OptionRight) ([]domain.OptionContractSnapshot, error) {
	return nil, errDeferred
}

func (a *Adapter) ListTradeable(_ context.Context, _ domain.AssetClass) ([]ports.Asset, error) {
	return nil, errDeferred
}

func (a *Adapter) GetSnapshots(_ context.Context, _ []string, _ time.Time) (map[string]ports.Snapshot, error) {
	return nil, errDeferred
}
