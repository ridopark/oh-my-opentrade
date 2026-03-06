package llm

import (
	"context"
	"errors"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// ErrAIDisabled is returned by NoOpAdvisor when AI debate is not enabled.
var ErrAIDisabled = errors.New("ai advisor: disabled")

// NoOpAdvisor implements ports.AIAdvisorPort but always returns ErrAIDisabled.
// Use it as a safe placeholder when AI is not configured, so downstream
// consumers (e.g. SignalDebateEnricher) can fall through to their error/timeout
// path and still emit enriched signals with fallback confidence.
type NoOpAdvisor struct{}

// NewNoOpAdvisor creates a no-op advisor that always returns ErrAIDisabled.
func NewNoOpAdvisor() *NoOpAdvisor {
	return &NoOpAdvisor{}
}

// RequestDebate always returns ErrAIDisabled, causing callers to use their
// fallback/error path.
func (n *NoOpAdvisor) RequestDebate(
	_ context.Context,
	_ domain.Symbol,
	_ domain.MarketRegime,
	_ domain.IndicatorSnapshot,
	_ ...ports.DebateOption,
) (*domain.AdvisoryDecision, error) {
	return nil, ErrAIDisabled
}
