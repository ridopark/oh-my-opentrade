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

type Service struct {
	eventBus   ports.EventBusPort
	repository ports.RepositoryPort
	filter     *AdaptiveFilter
	barWriter  *AsyncBarWriter // optional: when set, DB writes are async
	mu         sync.Mutex
	log        zerolog.Logger
	metrics    *metrics.Metrics
}

func NewService(eventBus ports.EventBusPort, repo ports.RepositoryPort, filter *AdaptiveFilter, log zerolog.Logger) *Service {
	return &Service{
		eventBus:   eventBus,
		repository: repo,
		filter:     filter,
		log:        log,
	}
}

func (s *Service) SetMetrics(m *metrics.Metrics) { s.metrics = m }

func (s *Service) SetBarWriter(w *AsyncBarWriter) { s.barWriter = w }

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
//
// IMPORTANT: The mutex only protects the adaptive filter's internal state
// (rolling averages, z-scores). DB writes and downstream event publishing
// run WITHOUT the mutex to avoid blocking the entire bar pipeline during
// potentially slow operations (LLM calls, broker API, DB writes).
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

	// Hold the mutex ONLY for the adaptive filter — it has internal state
	// (running averages, z-scores) that requires serialized access.
	// Everything after this block (DB writes, event publishing, downstream
	// pipeline) runs concurrently.
	s.mu.Lock()
	if s.metrics != nil {
		s.metrics.Bars.ReceivedTotal.WithLabelValues("ws", string(bar.Symbol), string(bar.Timeframe)).Inc()
	}
	result := s.filter.Process(bar)
	s.mu.Unlock()

	switch result.Status {
	case FilterRejected:
		result.Bar.Suspect = true
		l.Warn().
			Float64("open", result.Bar.Open).
			Float64("high", result.Bar.High).
			Float64("low", result.Bar.Low).
			Float64("close", result.Bar.Close).
			Float64("volume", result.Bar.Volume).
			Str("gate", string(result.Gate)).
			Msg("bar rejected by adaptive filter")

		if s.metrics != nil {
			s.metrics.Bars.DroppedTotal.WithLabelValues("ws", string(result.Gate)).Inc()
		}

		emittedEvent, err := domain.NewEvent(
			domain.EventMarketBarRejected,
			event.TenantID,
			event.EnvMode,
			event.IdempotencyKey+"-rejected",
			result.Bar,
		)
		if err != nil {
			return fmt.Errorf("ingestion: failed to create rejected event: %w", err)
		}
		if err := s.eventBus.Publish(ctx, *emittedEvent); err != nil {
			return fmt.Errorf("ingestion: failed to publish rejected event: %w", err)
		}
		return nil

	case FilterRepaired:
		l.Info().
			Float64("original_high", result.Bar.OriginalHigh).
			Float64("original_low", result.Bar.OriginalLow).
			Float64("repaired_high", result.Bar.High).
			Float64("repaired_low", result.Bar.Low).
			Float64("close", result.Bar.Close).
			Uint64("trade_count", result.Bar.TradeCount).
			Str("gate", string(result.Gate)).
			Msg("bar repaired by adaptive filter")

		if s.metrics != nil {
			s.metrics.Bars.RepairedTotal.WithLabelValues("ws", string(result.Gate)).Inc()
		}
	}

	if s.barWriter != nil {
		s.barWriter.Enqueue(result.Bar)
		l.Debug().Float64("close", result.Bar.Close).Bool("repaired", result.Bar.Repaired).Msg("market bar enqueued for async save")
	} else {
		if err := s.repository.SaveMarketBar(ctx, result.Bar); err != nil {
			l.Error().Err(err).Msg("failed to save market bar to repository")
			return fmt.Errorf("ingestion: failed to save market bar: %w", err)
		}
		l.Debug().Float64("close", result.Bar.Close).Bool("repaired", result.Bar.Repaired).Msg("market bar saved")
	}

	emittedEvent, err := domain.NewEvent(
		domain.EventMarketBarSanitized,
		event.TenantID,
		event.EnvMode,
		event.IdempotencyKey+"-sanitized",
		result.Bar,
	)
	if err != nil {
		return fmt.Errorf("ingestion: failed to create sanitized event: %w", err)
	}
	if err := s.eventBus.Publish(ctx, *emittedEvent); err != nil {
		return fmt.Errorf("ingestion: failed to publish sanitized event: %w", err)
	}

	if s.metrics != nil {
		s.metrics.Bars.ProcLatency.WithLabelValues("ws", string(bar.Timeframe)).Observe(time.Since(start).Seconds())
	}

	return nil
}
