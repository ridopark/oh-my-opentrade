package activation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/domain/screener"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type HistoricalDataProvider interface {
	GetHistoricalBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
}

type SymbolSubscriber interface {
	SubscribeSymbols(ctx context.Context, symbols []domain.Symbol) error
}

type SpikeFilterConfig interface {
	SetMaxDeviation(symbol domain.Symbol, maxDev float64)
	Seed(sym domain.Symbol, bars []domain.MarketBar) int
}

// StrategyActivator registers new symbols with the strategy V2 pipeline.
// Nil-safe: if nil, V2 activation is skipped.
type StrategyActivator interface {
	ActivateSymbol(symbol string, bars1m, barsHTF []domain.MarketBar, sessionOpen time.Time)
}

type Service struct {
	log           zerolog.Logger
	bus           ports.EventBusPort
	monitor       *monitor.Service
	data          HistoricalDataProvider
	subscriber    SymbolSubscriber
	spikeFilter   SpikeFilterConfig
	strategy      StrategyActivator
	mu            sync.Mutex
	warmedSymbols map[string]struct{}
	baseTimeframe domain.Timeframe
}

func NewService(
	log zerolog.Logger,
	bus ports.EventBusPort,
	mon *monitor.Service,
	data HistoricalDataProvider,
	subscriber SymbolSubscriber,
	spikeFilter SpikeFilterConfig,
	strategy StrategyActivator,
	baseTimeframe domain.Timeframe,
) *Service {
	return &Service{
		log:           log,
		bus:           bus,
		monitor:       mon,
		data:          data,
		subscriber:    subscriber,
		spikeFilter:   spikeFilter,
		strategy:      strategy,
		warmedSymbols: make(map[string]struct{}),
		baseTimeframe: baseTimeframe,
	}
}

func (s *Service) Start(ctx context.Context) error {
	if err := s.bus.Subscribe(ctx, domain.EventEffectiveSymbolsUpdated, s.handleEffectiveSymbolsUpdated); err != nil {
		return fmt.Errorf("activation: subscribe to EffectiveSymbolsUpdated: %w", err)
	}
	s.log.Info().Msg("activation service started")
	return nil
}

func (s *Service) MarkWarmed(symbols ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sym := range symbols {
		s.warmedSymbols[sym] = struct{}{}
	}
}

func (s *Service) handleEffectiveSymbolsUpdated(ctx context.Context, evt domain.Event) error {
	payload, ok := evt.Payload.(screener.EffectiveSymbolsUpdatedPayload)
	if !ok {
		return fmt.Errorf("activation: payload is not EffectiveSymbolsUpdatedPayload, got %T", evt.Payload)
	}

	newSymbols := s.diffNewSymbols(payload.Symbols)
	if len(newSymbols) == 0 {
		s.log.Info().
			Int("effective", len(payload.Symbols)).
			Msg("no new symbols to activate")
		return nil
	}

	s.log.Info().
		Strs("new_symbols", newSymbols).
		Int("effective_total", len(payload.Symbols)).
		Str("source", payload.Source).
		Msg("activating new symbols")

	start := time.Now()
	activated := s.activateSymbols(ctx, newSymbols)

	s.log.Info().
		Strs("activated", activated).
		Dur("duration", time.Since(start)).
		Msg("symbol activation complete")

	if len(activated) > 0 {
		outEvt, err := domain.NewEvent(
			domain.EventSymbolsActivated,
			evt.TenantID,
			evt.EnvMode,
			fmt.Sprintf("activated-%d", time.Now().UnixNano()),
			domain.SymbolsActivatedPayload{
				Symbols: activated,
				Source:  payload.Source,
			},
		)
		if err == nil {
			_ = s.bus.Publish(ctx, *outEvt)
		}
	}

	return nil
}

func (s *Service) diffNewSymbols(effective []string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var newSyms []string
	for _, sym := range effective {
		if _, ok := s.warmedSymbols[sym]; !ok {
			newSyms = append(newSyms, sym)
		}
	}
	return newSyms
}

func (s *Service) activateSymbols(ctx context.Context, symbols []string) []string {
	type result struct {
		symbol string
		ok     bool
	}

	results := make(chan result, len(symbols))
	var wg sync.WaitGroup

	for _, sym := range symbols {
		wg.Add(1)
		go func(symbol string) {
			defer wg.Done()
			if err := s.activateOne(ctx, symbol); err != nil {
				s.log.Error().Err(err).Str("symbol", symbol).Msg("symbol activation failed")
				results <- result{symbol: symbol, ok: false}
				return
			}
			results <- result{symbol: symbol, ok: true}
		}(sym)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var activated []string
	for r := range results {
		if r.ok {
			activated = append(activated, r.symbol)
		}
	}

	if s.subscriber != nil && len(activated) > 0 {
		domSyms := make([]domain.Symbol, len(activated))
		for i, sym := range activated {
			domSyms[i] = domain.Symbol(sym)
		}
		if err := s.subscriber.SubscribeSymbols(ctx, domSyms); err != nil {
			s.log.Error().Err(err).Strs("symbols", activated).Msg("WebSocket subscription failed")
		}
	}

	return activated
}

const (
	hourlyBarsNeeded = 50
	dailyBarsNeeded  = 200
)

func (s *Service) activateOne(ctx context.Context, symbol string) error {
	sym := domain.Symbol(symbol)
	l := s.log.With().Str("symbol", symbol).Logger()

	var hourlyTo, dailyTo, warmupTo time.Time
	if sym.IsCryptoSymbol() {
		now := time.Now().UTC()
		hourlyTo = now
		dailyTo = now
		warmupTo = now
	} else {
		_, prevEnd := domain.PreviousRTHSession(time.Now())
		hourlyTo = prevEnd
		dailyTo = prevEnd
		warmupTo = prevEnd
	}

	hourlyFrom := hourlyTo.Add(-time.Duration(float64(hourlyBarsNeeded)*1.3) * time.Hour)
	bars1h, err := s.data.GetHistoricalBars(ctx, sym, "1h", hourlyFrom, hourlyTo)
	if err != nil {
		l.Warn().Err(err).Msg("1H warmup fetch failed")
	} else if len(bars1h) > 0 {
		n := s.monitor.WarmUpHTF(bars1h)
		l.Info().Int("bars", n).Msg("1H EMA50 warmup complete")
	}

	dailyFrom := dailyTo.Add(-time.Duration(float64(dailyBarsNeeded)*1.5) * 24 * time.Hour)
	bars1d, err := s.data.GetHistoricalBars(ctx, sym, "1d", dailyFrom, dailyTo)
	if err != nil {
		return fmt.Errorf("1D warmup fetch failed for %s: %w", symbol, err)
	}
	if len(bars1d) < dailyBarsNeeded {
		l.Warn().Int("bars", len(bars1d)).Int("needed", dailyBarsNeeded).Msg("insufficient daily bars for EMA200")
	}

	closes := make([]float64, len(bars1d))
	for i, b := range bars1d {
		closes[i] = b.Close
	}
	ema200 := monitor.ComputeStaticEMA(closes, dailyBarsNeeded)

	if ema200 > 0 {
		lastClose := bars1d[len(bars1d)-1].Close
		bias := "NEUTRAL"
		if lastClose > ema200*1.005 {
			bias = "BULLISH"
		} else if lastClose < ema200*0.995 {
			bias = "BEARISH"
		}
		s.monitor.SetStaticHTFData(symbol, "1d", domain.HTFData{
			EMA200: ema200,
			Bias:   bias,
		})
		l.Info().Float64("ema200", ema200).Str("bias", bias).Msg("1D EMA200 warmup complete")
	}

	warmupFrom := warmupTo.Add(-120 * time.Minute)
	bars1m, err := s.data.GetHistoricalBars(ctx, sym, s.baseTimeframe, warmupFrom, warmupTo)
	if err != nil {
		l.Warn().Err(err).Msg("1m indicator warmup fetch failed")
	} else if len(bars1m) > 0 {
		n := s.monitor.WarmUp(bars1m)
		s.monitor.ResetSessionIndicators(symbol)
		l.Info().Int("bars", n).Msg("1m indicator warmup complete")

		if s.spikeFilter != nil {
			if sym.IsCryptoSymbol() {
				s.spikeFilter.SetMaxDeviation(sym, 0.03)
			} else {
				s.spikeFilter.SetMaxDeviation(sym, 0.10)
			}
			s.spikeFilter.Seed(sym, bars1m)
		}
	}

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("EST", -5*3600)
	}
	nowET := time.Now().In(loc)
	todayOpen := time.Date(nowET.Year(), nowET.Month(), nowET.Day(), 9, 30, 0, 0, loc)
	s.monitor.InitAggregators([]domain.Symbol{sym}, todayOpen)

	if s.strategy != nil {
		s.strategy.ActivateSymbol(symbol, bars1m, bars1h, todayOpen)
		l.Info().Msg("strategy V2 activation complete")
	}

	isWeekday := nowET.Weekday() != time.Saturday && nowET.Weekday() != time.Sunday
	isOpen := !domain.IsNYSEHoliday(nowET) && isWeekday
	if isOpen && nowET.After(todayOpen) {
		orbBars, err := s.data.GetHistoricalBars(ctx, sym, s.baseTimeframe, todayOpen.UTC(), time.Now())
		if err != nil {
			l.Warn().Err(err).Msg("ORB replay fetch failed")
		} else if len(orbBars) > 0 {
			s.monitor.WarmUpORB(orbBars)
			l.Info().Int("bars", len(orbBars)).Msg("ORB replay complete")
		}
	}

	s.mu.Lock()
	s.warmedSymbols[symbol] = struct{}{}
	s.mu.Unlock()

	s.monitor.MarkReady(symbol)
	return nil
}
