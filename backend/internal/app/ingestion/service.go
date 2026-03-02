package ingestion

import (
	"context"
	"fmt"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"sync"
)

// Service is the ingestion application service.
type Service struct {
	eventBus   ports.EventBusPort
	repository ports.RepositoryPort
	filter     *ZScoreFilter
	mu         sync.Mutex
}

// NewService creates a new ingestion Service.
func NewService(eventBus ports.EventBusPort, repo ports.RepositoryPort, filter *ZScoreFilter) *Service {
	return &Service{
		eventBus:   eventBus,
		repository: repo,
		filter:     filter,
	}
}

// Start subscribes the service to incoming market data events.
func (s *Service) Start(ctx context.Context) error {
	err := s.eventBus.Subscribe(ctx, domain.EventMarketBarReceived, s.HandleMarketBar)
	if err != nil {
		return fmt.Errorf("ingestion: failed to subscribe to MarketBarReceived: %w", err)
	}
	return nil
}

// HandleMarketBar processes a single market bar event.
// It verifies the event payload, passes it through the Z-score filter,
// and then either persists the bar and emits a sanitized event or
// emits a rejected event without saving.
func (s *Service) HandleMarketBar(ctx context.Context, event domain.Event) error {
	bar, ok := event.Payload.(domain.MarketBar)
	if !ok {
		return fmt.Errorf("ingestion: payload is not a MarketBar, got %T", event.Payload)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	suspect := s.filter.Check(bar)
	if suspect {
		bar.Suspect = true

		emittedEvent, err := domain.NewEvent(
			domain.EventMarketBarRejected,
			event.TenantID,
			event.EnvMode,
			event.IdempotencyKey+"-rejected",
			bar,
		)
		if err != nil {
			return fmt.Errorf("ingestion: failed to create rejected event: %w", err)
		}

		if err := s.eventBus.Publish(ctx, *emittedEvent); err != nil {
			return fmt.Errorf("ingestion: failed to publish rejected event: %w", err)
		}

		return nil
	}

	if err := s.repository.SaveMarketBar(ctx, bar); err != nil {
		return fmt.Errorf("ingestion: failed to save market bar: %w", err)
	}

	emittedEvent, err := domain.NewEvent(
		domain.EventMarketBarSanitized,
		event.TenantID,
		event.EnvMode,
		event.IdempotencyKey+"-sanitized",
		bar,
	)
	if err != nil {
		return fmt.Errorf("ingestion: failed to create sanitized event: %w", err)
	}

	if err := s.eventBus.Publish(ctx, *emittedEvent); err != nil {
		return fmt.Errorf("ingestion: failed to publish sanitized event: %w", err)
	}

	return nil
}
