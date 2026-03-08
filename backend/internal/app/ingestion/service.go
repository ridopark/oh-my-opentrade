package ingestion

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// Service is the ingestion application service.
type Service struct {
	eventBus   ports.EventBusPort
	repository ports.RepositoryPort
	filter     *ZScoreFilter
	mu         sync.Mutex
	log        zerolog.Logger
	metrics    *metrics.Metrics
}

// NewService creates a new ingestion Service.
func NewService(eventBus ports.EventBusPort, repo ports.RepositoryPort, filter *ZScoreFilter, log zerolog.Logger) *Service {
	return &Service{
		eventBus:   eventBus,
		repository: repo,
		filter:     filter,
		log:        log,
	}
}

// SetMetrics injects Prometheus collectors. Safe to leave nil (no-op).
func (s *Service) SetMetrics(m *metrics.Metrics) { s.metrics = m }

// Start subscribes the service to incoming market data events.
func (s *Service) Start(ctx context.Context) error {
	err := s.eventBus.Subscribe(ctx, domain.EventMarketBarReceived, s.HandleMarketBar)
	if err != nil {
		return fmt.Errorf("ingestion: failed to subscribe to MarketBarReceived: %w", err)
	}
	s.log.Info().Msg("subscribed to MarketBarReceived events")
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

	start := time.Now()

	l := s.log.With().
		Str("symbol", string(bar.Symbol)).
		Str("timeframe", string(bar.Timeframe)).
		Time("bar_time", bar.Time).
		Logger()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Record bar received.
	if s.metrics != nil {
		s.metrics.Bars.ReceivedTotal.WithLabelValues("ws", string(bar.Symbol), string(bar.Timeframe)).Inc()
	}

	suspect := s.filter.Check(bar)
	if suspect {
		bar.Suspect = true
		l.Warn().
			Float64("open", bar.Open).
			Float64("high", bar.High).
			Float64("low", bar.Low).
			Float64("close", bar.Close).
			Float64("volume", bar.Volume).
			Msg("bar flagged as suspect by Z-score filter \u2014 rejecting")

		if s.metrics != nil {
			s.metrics.Bars.DroppedTotal.WithLabelValues("ws", "zscore").Inc()
		}

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
		l.Error().Err(err).Msg("failed to save market bar to repository")
		return fmt.Errorf("ingestion: failed to save market bar: %w", err)
	}

	l.Debug().Float64("close", bar.Close).Msg("market bar saved")

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

	// Record processing latency for successfully processed bars.
	if s.metrics != nil {
		s.metrics.Bars.ProcLatency.WithLabelValues("ws", string(bar.Timeframe)).Observe(time.Since(start).Seconds())
	}

	return nil
}
