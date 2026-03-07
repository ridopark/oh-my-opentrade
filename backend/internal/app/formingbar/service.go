package formingbar

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

const (
	throttleInterval = 200 * time.Millisecond
	bucketDuration   = time.Minute
)

type bucket struct {
	start   time.Time
	bar     domain.FormingBar
	dirty   bool
	lastPub time.Time
}

// Service aggregates trade ticks into forming (partial) OHLCV candles
// and publishes them via the event bus for real-time chart display.
type Service struct {
	eventBus ports.EventBusPort
	log      zerolog.Logger
	mu       sync.Mutex
	buckets  map[domain.Symbol]*bucket
}

func NewService(eventBus ports.EventBusPort, log zerolog.Logger) *Service {
	return &Service{
		eventBus: eventBus,
		log:      log,
		buckets:  make(map[domain.Symbol]*bucket),
	}
}

func (s *Service) Start(ctx context.Context) error {
	if err := s.eventBus.Subscribe(ctx, domain.EventTradeReceived, s.handleTrade); err != nil {
		return fmt.Errorf("formingbar: subscribe TradeReceived: %w", err)
	}
	s.log.Info().Msg("subscribed to TradeReceived events")
	return nil
}

func (s *Service) handleTrade(ctx context.Context, evt domain.Event) error {
	trade, ok := evt.Payload.(domain.MarketTrade)
	if !ok {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bucketStart := trade.Time.Truncate(bucketDuration)
	b, exists := s.buckets[trade.Symbol]

	if !exists || bucketStart.After(b.start) {
		b = &bucket{
			start: bucketStart,
			bar: domain.FormingBar{
				Time:      bucketStart,
				Symbol:    trade.Symbol,
				Timeframe: "1m",
				Open:      trade.Price,
				High:      trade.Price,
				Low:       trade.Price,
				Close:     trade.Price,
				Volume:    trade.Size,
			},
			dirty: true,
		}
		s.buckets[trade.Symbol] = b
	} else {
		if trade.Price > b.bar.High {
			b.bar.High = trade.Price
		}
		if trade.Price < b.bar.Low {
			b.bar.Low = trade.Price
		}
		b.bar.Close = trade.Price
		b.bar.Volume += trade.Size
		b.dirty = true
	}

	if time.Since(b.lastPub) < throttleInterval {
		return nil
	}

	return s.publish(ctx, b)
}

func (s *Service) publish(ctx context.Context, b *bucket) error {
	if !b.dirty {
		return nil
	}

	evt, err := domain.NewEvent(
		domain.EventFormingBar,
		"system",
		domain.EnvModePaper,
		fmt.Sprintf("forming-%s-%d", b.bar.Symbol, b.start.Unix()),
		b.bar,
	)
	if err != nil {
		return err
	}

	b.dirty = false
	b.lastPub = time.Now()

	return s.eventBus.Publish(ctx, *evt)
}
