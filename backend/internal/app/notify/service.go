package notify

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

var etLoc = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic("notify: failed to load America/New_York timezone: " + err.Error())
	}
	return loc
}()

const (
	jobQueueSize  = 100
	workerCount   = 3
	chartCacheTTL = 5 * time.Minute
)

type notifyJob struct {
	event     domain.Event
	eventType string
	message   string
	withChart bool
}

type chartCacheEntry struct {
	data      []byte
	createdAt time.Time
}

type Service struct {
	eventBus ports.EventBusPort
	notifier ports.NotifierPort
	chartGen ports.ChartGeneratorPort
	repo     ports.RepositoryPort
	log      zerolog.Logger
	jobs     chan notifyJob
	wg       sync.WaitGroup

	cacheMu    sync.RWMutex
	chartCache map[string]chartCacheEntry
}

type Option func(*Service)

func WithChartGenerator(cg ports.ChartGeneratorPort) Option {
	return func(s *Service) { s.chartGen = cg }
}

func WithRepository(repo ports.RepositoryPort) Option {
	return func(s *Service) { s.repo = repo }
}

func NewService(eventBus ports.EventBusPort, notifier ports.NotifierPort, log zerolog.Logger, opts ...Option) *Service {
	s := &Service{
		eventBus:   eventBus,
		notifier:   notifier,
		log:        log,
		jobs:       make(chan notifyJob, jobQueueSize),
		chartCache: make(map[string]chartCacheEntry),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *Service) Start(ctx context.Context) error {
	for i := 0; i < workerCount; i++ {
		s.wg.Add(1)
		go s.worker(ctx, i)
	}

	events := []struct {
		eventType string
		formatter func(domain.Event) string
		chart     bool
	}{
		{domain.EventOrderSubmitted, s.fmtOrderSubmitted, true},
		{domain.EventOrderAccepted, s.fmtOrderAccepted, false},
		{domain.EventOrderRejected, s.fmtOrderRejected, false},
		{domain.EventOrderIntentRejected, s.fmtOrderIntentRejected, false},
		{domain.EventFillReceived, s.fmtFillReceived, false},
		{domain.EventTradeRealized, s.fmtTradeRealized, false},
		{domain.EventKillSwitchEngaged, s.fmtKillSwitch, false},
		{domain.EventCircuitBreakerTripped, s.fmtCircuitBreaker, false},
		{domain.EventDebateCompleted, s.fmtDebateCompleted, false},
		{domain.EventSignalEnriched, s.fmtSignalEnriched, false},
		{domain.EventRiskRevaluated, s.fmtRiskRevaluated, false},
	}

	for _, e := range events {
		formatter := e.formatter
		eventType := e.eventType
		withChart := e.chart
		handler := func(ctx context.Context, ev domain.Event) error {
			msg := fmt.Sprintf("[%s] %s", ev.OccurredAt.In(etLoc).Format("15:04:05"), formatter(ev))
			job := notifyJob{
				event:     ev,
				eventType: eventType,
				message:   msg,
				withChart: withChart,
			}
			select {
			case s.jobs <- job:
			default:
				s.log.Warn().Str("event", eventType).Msg("notification queue full, dropping")
			}
			return nil
		}
		if err := s.eventBus.Subscribe(ctx, eventType, handler); err != nil {
			return fmt.Errorf("notify: failed to subscribe to %s: %w", eventType, err)
		}
	}
	s.log.Info().Int("event_types", len(events)).Int("workers", workerCount).Msg("notification service started (async)")
	return nil
}

func (s *Service) Stop() {
	close(s.jobs)
	s.wg.Wait()
}

func (s *Service) worker(ctx context.Context, id int) {
	defer s.wg.Done()
	for job := range s.jobs {
		s.processJob(ctx, id, job)
	}
}

func (s *Service) processJob(ctx context.Context, workerID int, job notifyJob) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error().Int("worker", workerID).Interface("panic", r).Str("event", job.eventType).Msg("worker panic recovered")
		}
	}()

	jobCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if !job.withChart || s.chartGen == nil || s.repo == nil {
		if err := s.notifier.Notify(jobCtx, job.event.TenantID, job.message); err != nil {
			s.log.Warn().Err(err).Str("event", job.eventType).Msg("notification failed")
		}
		return
	}

	symbol := s.extractSymbol(job.event)
	if symbol == "" {
		if err := s.notifier.Notify(jobCtx, job.event.TenantID, job.message); err != nil {
			s.log.Warn().Err(err).Str("event", job.eventType).Msg("notification failed")
		}
		return
	}

	imgNotifier, supportsImage := s.notifier.(ports.ImageNotifierPort)
	if !supportsImage {
		if err := s.notifier.Notify(jobCtx, job.event.TenantID, job.message); err != nil {
			s.log.Warn().Err(err).Str("event", job.eventType).Msg("notification failed")
		}
		return
	}

	chartPNG, err := s.getOrGenerateChart(jobCtx, symbol)
	if err != nil {
		s.log.Warn().Err(err).Str("symbol", symbol).Msg("chart generation failed, sending text-only")
		if err := s.notifier.Notify(jobCtx, job.event.TenantID, job.message); err != nil {
			s.log.Warn().Err(err).Str("event", job.eventType).Msg("notification failed")
		}
		return
	}

	attachment := ports.Attachment{
		Data:     chartPNG,
		Filename: fmt.Sprintf("%s_chart.png", symbol),
	}
	if err := imgNotifier.NotifyWithImage(jobCtx, job.event.TenantID, job.message, attachment); err != nil {
		s.log.Warn().Err(err).Str("event", job.eventType).Msg("image notification failed")
	}
}

func (s *Service) extractSymbol(ev domain.Event) string {
	if p, ok := ev.Payload.(domain.OrderIntentEventPayload); ok {
		return p.Symbol
	}
	return ""
}

func (s *Service) getOrGenerateChart(ctx context.Context, symbol string) ([]byte, error) {
	s.cacheMu.RLock()
	if entry, ok := s.chartCache[symbol]; ok && time.Since(entry.createdAt) < chartCacheTTL {
		s.cacheMu.RUnlock()
		return entry.data, nil
	}
	s.cacheMu.RUnlock()

	now := time.Now()
	loc, _ := time.LoadLocation("America/New_York")
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 4, 0, 0, 0, loc)

	bars, err := s.repo.GetMarketBars(ctx, domain.Symbol(symbol), "1Min", dayStart, now)
	if err != nil {
		return nil, fmt.Errorf("fetch bars for %s: %w", symbol, err)
	}
	if len(bars) < 2 {
		return nil, fmt.Errorf("insufficient bars for %s: got %d", symbol, len(bars))
	}

	title := fmt.Sprintf("%s — %s", symbol, now.In(loc).Format("Jan 02"))
	chartPNG, err := s.chartGen.GenerateCandlestickChart(ctx, bars, title)
	if err != nil {
		return nil, fmt.Errorf("render chart for %s: %w", symbol, err)
	}

	s.cacheMu.Lock()
	s.chartCache[symbol] = chartCacheEntry{data: chartPNG, createdAt: now}
	s.cacheMu.Unlock()

	return chartPNG, nil
}

func (s *Service) fmtOrderSubmitted(ev domain.Event) string {
	if p, ok := ev.Payload.(domain.OrderIntentEventPayload); ok {
		emoji := "📤"
		action := "Order Submitted"
		if p.Direction == string(domain.DirectionCloseLong) {
			emoji = "📕"
			action = "Exit Submitted"
		}
		msg := fmt.Sprintf("%s %s: %s %s @ $%.2f (qty: %.2f)",
			emoji, action, p.Direction, p.Symbol, p.LimitPrice, p.Quantity)
		msg += fmt.Sprintf("\n📊 Strategy: %s | Confidence: %.0f%%", p.Strategy, p.Confidence*100)
		if p.Rationale != "" {
			msg += fmt.Sprintf("\n💡 Rationale: %s", p.Rationale)
		}
		return msg
	}
	return "📤 Order Submitted"
}

func (s *Service) fmtSignalEnriched(ev domain.Event) string {
	if e, ok := ev.Payload.(domain.SignalEnrichment); ok {
		emoji := "🧠"
		msg := fmt.Sprintf("%s Signal Enriched: %s %s %s [%s] (Confidence: %.0f%%)",
			emoji, e.Signal.SignalType, e.Signal.Side, e.Signal.Symbol,
			string(e.Status), e.Confidence*100)
		if e.HasPnL {
			pnlEmoji := "📈"
			if e.UnrealizedPnLPct < 0 {
				pnlEmoji = "📉"
			}
			sign := "+"
			absUSD := e.UnrealizedPnLUSD
			if absUSD < 0 {
				sign = "-"
				absUSD = -absUSD
			}
			msg += fmt.Sprintf("\n%s Est. P&L: %s$%.2f (%+.2f%%) | Entry: $%.2f", pnlEmoji, sign, absUSD, e.UnrealizedPnLPct*100, e.EntryPrice)
		}
		if e.BullArgument != "" {
			msg += fmt.Sprintf("\n🟢 Bull: %s", e.BullArgument)
		}
		if e.BearArgument != "" {
			msg += fmt.Sprintf("\n🔴 Bear: %s", e.BearArgument)
		}
		if e.JudgeReasoning != "" {
			msg += fmt.Sprintf("\n⚖️ Judge: %s", e.JudgeReasoning)
		}
		return msg
	}
	return "🧠 Signal Enriched"
}

func (s *Service) fmtOrderAccepted(ev domain.Event) string {
	return "✅ Order Accepted by broker"
}

func (s *Service) fmtOrderRejected(ev domain.Event) string {
	if reason, ok := ev.Payload.(string); ok {
		return fmt.Sprintf("❌ Order Rejected: %s", reason)
	}
	return "❌ Order Rejected"
}

func (s *Service) fmtOrderIntentRejected(ev domain.Event) string {
	if p, ok := ev.Payload.(domain.OrderIntentEventPayload); ok {
		if p.Reason != "" {
			return fmt.Sprintf("⚠️ Intent Rejected: %s %s — %s", p.Direction, p.Symbol, p.Reason)
		}
		return fmt.Sprintf("⚠️ Intent Rejected: %s %s — failed risk/slippage check",
			p.Direction, p.Symbol)
	}
	return "⚠️ Order Intent Rejected (risk/slippage check failed)"
}

func (s *Service) fmtFillReceived(ev domain.Event) string {
	if m, ok := ev.Payload.(map[string]any); ok {
		sym, _ := m["symbol"].(string)
		side, _ := m["side"].(string)
		qty, _ := m["quantity"].(float64)
		price, _ := m["price"].(float64)
		return fmt.Sprintf("💰 Fill: %s %s — %.2f shares @ $%.2f", side, sym, qty, price)
	}
	return "💰 Order Filled"
}

func (s *Service) fmtTradeRealized(ev domain.Event) string {
	p, ok := ev.Payload.(domain.TradeRealizedPayload)
	if !ok {
		return ""
	}

	pnlEmoji := "📈"
	if p.RealizedPnL < 0 {
		pnlEmoji = "📉"
	}

	sign := "+"
	if p.RealizedPnL < 0 {
		sign = "-"
	}
	absPnL := p.RealizedPnL
	if absPnL < 0 {
		absPnL = -absPnL
	}

	msg := fmt.Sprintf("%s P&L: %s$%.2f (%+.2f%%)", pnlEmoji, sign, absPnL, p.PnLPct)
	msg += fmt.Sprintf(" | Entry: $%.2f", p.EntryPrice)

	if p.HoldDuration > 0 {
		msg += fmt.Sprintf(" | Held: %s", fmtDuration(p.HoldDuration))
	}

	return msg
}

func fmtDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	totalMin := int(d.Minutes())
	if totalMin < 60 {
		return fmt.Sprintf("%dm", totalMin)
	}
	h := totalMin / 60
	m := totalMin % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

func (s *Service) fmtKillSwitch(ev domain.Event) string {
	return "🚨 KILL SWITCH ENGAGED — Trading halted for symbol"
}

func (s *Service) fmtCircuitBreaker(ev domain.Event) string {
	if reason, ok := ev.Payload.(string); ok {
		return fmt.Sprintf("🔴 CIRCUIT BREAKER TRIPPED: %s", reason)
	}
	return "🔴 CIRCUIT BREAKER TRIPPED — System-wide trading halt"
}

func (s *Service) fmtDebateCompleted(ev domain.Event) string {
	if d, ok := ev.Payload.(domain.AdvisoryDecision); ok {
		msg := fmt.Sprintf("🤖 AI Debate — %s (Confidence: %.0f%%)", d.Direction, d.Confidence*100)
		if d.BullArgument != "" {
			msg += fmt.Sprintf("\n🟢 Bull: %s", d.BullArgument)
		}
		if d.BearArgument != "" {
			msg += fmt.Sprintf("\n🔴 Bear: %s", d.BearArgument)
		}
		if d.JudgeReasoning != "" {
			msg += fmt.Sprintf("\n⚖️ Judge: %s", d.JudgeReasoning)
		}
		return msg
	}
	return "🤖 AI Debate completed"
}

func (s *Service) fmtRiskRevaluated(ev domain.Event) string {
	r, ok := ev.Payload.(domain.RiskRevaluationEvent)
	if !ok {
		return "🔍 Risk Re-evaluation completed"
	}

	thesisEmoji := "✅"
	switch r.ThesisStatus {
	case domain.ThesisDegrading:
		thesisEmoji = "⚠️"
	case domain.ThesisInvalidated:
		thesisEmoji = "🚨"
	}

	actionEmoji := "📊"
	switch r.Action {
	case domain.RiskActionTighten:
		actionEmoji = "🔒"
	case domain.RiskActionScaleOut:
		actionEmoji = "📉"
	case domain.RiskActionExit:
		actionEmoji = "🚪"
	}

	pnlEmoji := "📈"
	if r.UnrealizedPnL < 0 {
		pnlEmoji = "📉"
	}

	msg := fmt.Sprintf("🔍 Risk Re-evaluation: %s (%s)", r.Symbol, r.Strategy)
	msg += fmt.Sprintf("\n%s Position: Entry $%.2f → Now $%.2f (%+.2f%%)",
		pnlEmoji, r.EntryPrice, r.CurrentPrice, r.UnrealizedPnL*100)
	msg += fmt.Sprintf("\n⏱️ Held: %s", r.HoldDuration)
	msg += fmt.Sprintf("\n%s Thesis: %s", thesisEmoji, r.ThesisStatus)
	msg += fmt.Sprintf("\n%s Action: %s (Risk: %s, Confidence: %.0f%%)",
		actionEmoji, r.Action, r.UpdatedModifier, r.Confidence*100)

	if r.Reasoning != "" {
		msg += fmt.Sprintf("\n💡 %s", r.Reasoning)
	}

	return msg
}
