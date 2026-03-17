package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

var etLoc *time.Location

const (
	jobQueueSize  = 100
	workerCount   = 3
	chartCacheTTL = 5 * time.Minute
	batchWindow   = 3 * time.Second
	maxBatchAge   = 10 * time.Second
)

type notifyJob struct {
	event     domain.Event
	eventType string
	message   string
	symbol    string
	withChart bool
	chartOpts ports.ChartOptions
}

type chartCacheEntry struct {
	data      []byte
	createdAt time.Time
}

type batchEntry struct {
	parts     []string
	symbol    string
	withChart bool
	lastEvent domain.Event
	timer     *time.Timer
	createdAt time.Time
	chartOpts ports.ChartOptions
	chartSet  bool
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

	batchMu    sync.Mutex
	batches    map[string]*batchEntry
	entryInfos map[string]entryInfo
}

type entryInfo struct {
	target    float64
	entryTime time.Time
}

type Option func(*Service)

func WithChartGenerator(cg ports.ChartGeneratorPort) Option {
	return func(s *Service) { s.chartGen = cg }
}

func WithRepository(repo ports.RepositoryPort) Option {
	return func(s *Service) { s.repo = repo }
}

func NewService(eventBus ports.EventBusPort, notifier ports.NotifierPort, log zerolog.Logger, opts ...Option) (*Service, error) {
	if etLoc == nil {
		loc, err := time.LoadLocation("America/New_York")
		if err != nil {
			return nil, fmt.Errorf("notify: failed to load America/New_York timezone: %w", err)
		}
		etLoc = loc
	}
	s := &Service{
		eventBus:   eventBus,
		notifier:   notifier,
		log:        log,
		jobs:       make(chan notifyJob, jobQueueSize),
		chartCache: make(map[string]chartCacheEntry),
		batches:    make(map[string]*batchEntry),
		entryInfos: make(map[string]entryInfo),
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
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
		{domain.EventTradeRealized, s.fmtTradeRealized, true},
		{domain.EventKillSwitchEngaged, s.fmtKillSwitch, false},
		{domain.EventCircuitBreakerTripped, s.fmtCircuitBreaker, false},
		{domain.EventDebateCompleted, s.fmtDebateCompleted, false},
		{domain.EventSignalEnriched, s.fmtSignalEnriched, false},
		{domain.EventSignalGated, s.fmtSignalGated, false},
		{domain.EventRiskRevaluated, s.fmtRiskRevaluated, false},
		{domain.EventFeedDegraded, s.fmtFeedDegraded, false},
		{domain.EventWSCircuitBreakerTripped, s.fmtWSCircuitBreaker, false},
		{domain.EventFillPollTimeout, s.fmtFillPollTimeout, false},
		{domain.EventStaleOrderCancelled, s.fmtStaleOrderCancelled, false},
		{domain.EventExitCircuitBroken, s.fmtExitCircuitBroken, false},
		{domain.EventSystemStarted, s.fmtSystemStarted, false},
	}

	for _, e := range events {
		formatter := e.formatter
		eventType := e.eventType
		withChart := e.chart
		handler := func(ctx context.Context, ev domain.Event) error {
			formatted := formatter(ev)
			if formatted == "" {
				return nil
			}
			msg := fmt.Sprintf("[%s] %s", ev.OccurredAt.In(etLoc).Format("15:04:05"), formatted)
			symbol := s.symbolFromEvent(ev)
			if symbol == "" {
				// No symbol — send immediately with separator.
				s.enqueueJob(notifyJob{
					event:     ev,
					eventType: eventType,
					message:   msg + "\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━",
					withChart: withChart,
				})
				return nil
			}
			s.addToBatch(symbol, msg, withChart, ev, eventType)
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
	s.flushAllBatches()
	close(s.jobs)
	s.wg.Wait()
}

func (s *Service) enqueueJob(job notifyJob) {
	select {
	case s.jobs <- job:
	default:
		s.log.Warn().Str("event", job.eventType).Msg("notification queue full, dropping")
	}
}

func (s *Service) symbolFromEvent(ev domain.Event) string {
	switch p := ev.Payload.(type) {
	case domain.OrderIntentEventPayload:
		return p.Symbol
	case domain.SignalEnrichment:
		return p.Signal.Symbol
	case domain.SignalGatedPayload:
		return p.Symbol
	case domain.TradeRealizedPayload:
		return string(p.Symbol)
	case domain.RiskRevaluationEvent:
		return string(p.Symbol)
	case domain.FillPollTimeoutPayload:
		return string(p.Symbol)
	case domain.StaleOrderCancelledPayload:
		return string(p.Symbol)
	case map[string]any:
		if sym, ok := p["symbol"].(string); ok {
			return sym
		}
	}
	return ""
}

func (s *Service) addToBatch(symbol, msg string, withChart bool, ev domain.Event, eventType string) {
	s.batchMu.Lock()
	defer s.batchMu.Unlock()

	entry, exists := s.batches[symbol]
	if !exists {
		entry = &batchEntry{symbol: symbol, createdAt: time.Now()}
		s.batches[symbol] = entry
	}

	entry.parts = append(entry.parts, msg)
	entry.withChart = entry.withChart || withChart
	entry.lastEvent = ev

	switch p := ev.Payload.(type) {
	case domain.OrderIntentEventPayload:
		if !entry.chartSet && p.Direction != string(domain.DirectionCloseLong) && p.LimitPrice > 0 {
			entryTime := ev.OccurredAt
			opts := ports.ChartOptions{}
			opts.Levels = append(opts.Levels, domain.PriceLevel{
				Label:     fmt.Sprintf("Entry $%s", domain.FmtPrice(p.LimitPrice)),
				Price:     p.LimitPrice,
				Color:     "green",
				StartTime: entryTime,
			})
			if p.StopLoss > 0 {
				opts.Levels = append(opts.Levels, domain.PriceLevel{
					Label:     fmt.Sprintf("Stop $%s", domain.FmtPrice(p.StopLoss)),
					Price:     p.StopLoss,
					Color:     "red",
					StartTime: entryTime,
				})
			}
			if tp := bestTargetPrice(p.Meta); tp > 0 {
				opts.Levels = append(opts.Levels, domain.PriceLevel{
					Label:     fmt.Sprintf("Target $%s", domain.FmtPrice(tp)),
					Price:     tp,
					Color:     "blue",
					StartTime: entryTime,
				})
				s.entryInfos[p.Symbol] = entryInfo{target: tp, entryTime: entryTime}
			}
			opts.Markers = append(opts.Markers, domain.TimeMarker{
				Time:  entryTime,
				Label: "Entry",
				Color: "green",
			})
			entry.chartOpts = opts
			entry.chartSet = true
		}
	case domain.TradeRealizedPayload:
		exitTime := ev.OccurredAt
		info := s.entryInfos[string(p.Symbol)]
		delete(s.entryInfos, string(p.Symbol))

		opts := ports.ChartOptions{}
		opts.Levels = append(opts.Levels, domain.PriceLevel{
			Label:     fmt.Sprintf("Exit $%s", domain.FmtPrice(p.ExitPrice)),
			Price:     p.ExitPrice,
			Color:     "silver",
			StartTime: info.entryTime,
			EndTime:   exitTime,
		})
		if info.target > 0 {
			opts.Levels = append(opts.Levels, domain.PriceLevel{
				Label:     fmt.Sprintf("Target $%s", domain.FmtPrice(info.target)),
				Price:     info.target,
				Color:     "blue",
				StartTime: info.entryTime,
				EndTime:   exitTime,
			})
		}
		if !info.entryTime.IsZero() {
			opts.Markers = append(opts.Markers, domain.TimeMarker{
				Time:  info.entryTime,
				Label: "Entry",
				Color: "green",
			})
		}
		opts.Markers = append(opts.Markers, domain.TimeMarker{
			Time:  exitTime,
			Label: "Exit",
			Color: "red",
		})
		absPnL := p.RealizedPnL
		if absPnL < 0 {
			absPnL = -absPnL
		}
		opts.PnL = &ports.ChartPnL{
			PnLPct:       p.PnLPct,
			PnLUSD:       absPnL,
			HoldDuration: fmtDuration(p.HoldDuration),
		}
		if !info.entryTime.IsZero() {
			opts.WindowStart = info.entryTime
			opts.WindowEnd = exitTime
		}
		entry.chartOpts = opts
		entry.chartSet = true
		entry.withChart = true
	}

	if entry.timer != nil {
		entry.timer.Stop()
	}

	if time.Since(entry.createdAt) >= maxBatchAge {
		s.flushBatchLocked(symbol)
		return
	}

	entry.timer = time.AfterFunc(batchWindow, func() {
		s.batchMu.Lock()
		defer s.batchMu.Unlock()
		s.flushBatchLocked(symbol)
	})
}

func (s *Service) flushBatchLocked(symbol string) {
	entry, exists := s.batches[symbol]
	if !exists {
		return
	}
	delete(s.batches, symbol)

	if entry.timer != nil {
		entry.timer.Stop()
	}

	combined := strings.Join(entry.parts, "\n") + "\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

	s.enqueueJob(notifyJob{
		event:     entry.lastEvent,
		eventType: "batch",
		symbol:    entry.symbol,
		message:   combined,
		withChart: entry.withChart,
		chartOpts: entry.chartOpts,
	})
}

func (s *Service) flushAllBatches() {
	s.batchMu.Lock()
	defer s.batchMu.Unlock()
	for symbol := range s.batches {
		s.flushBatchLocked(symbol)
	}
}

func (s *Service) NotifySync(msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	formatted := fmt.Sprintf("[%s] %s\n", time.Now().In(etLoc).Format("15:04:05"), msg)
	if err := s.notifier.Notify(ctx, "system", formatted); err != nil {
		s.log.Warn().Err(err).Msg("sync notification failed")
	}
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

	jobCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if !job.withChart || s.chartGen == nil || s.repo == nil {
		if err := s.notifier.Notify(jobCtx, job.event.TenantID, job.message); err != nil {
			s.log.Warn().Err(err).Str("event", job.eventType).Msg("notification failed")
		}
		return
	}

	symbol := job.symbol
	if symbol == "" {
		symbol = s.extractSymbol(job.event)
	}
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

	chartCtx, cancelChart := context.WithTimeout(jobCtx, 5*time.Second)
	chartPNG, err := s.getOrGenerateChart(chartCtx, symbol, job.chartOpts)
	cancelChart()
	if err != nil {
		s.log.Warn().Err(err).Str("symbol", symbol).Msg("chart generation failed, sending text-only")
		if err := s.notifier.Notify(jobCtx, job.event.TenantID, job.message); err != nil {
			s.log.Warn().Err(err).Str("event", job.eventType).Msg("notification failed")
		}
		return
	}

	attachment := ports.Attachment{
		Data:     chartPNG,
		Filename: fmt.Sprintf("%s_chart.jpg", symbol),
	}
	if err := imgNotifier.NotifyWithImage(jobCtx, job.event.TenantID, job.message, attachment); err != nil {
		s.log.Warn().Err(err).Str("event", job.eventType).Msg("image notification failed")
	}
}

func (s *Service) extractSymbol(ev domain.Event) string {
	switch p := ev.Payload.(type) {
	case domain.OrderIntentEventPayload:
		return p.Symbol
	case domain.TradeRealizedPayload:
		return string(p.Symbol)
	}
	return ""
}

func (s *Service) getOrGenerateChart(ctx context.Context, symbol string, opts ports.ChartOptions) ([]byte, error) {
	hasAnnotations := len(opts.Levels) > 0 || len(opts.Markers) > 0 || opts.PnL != nil

	if !hasAnnotations {
		s.cacheMu.RLock()
		if entry, ok := s.chartCache[symbol]; ok && time.Since(entry.createdAt) < chartCacheTTL {
			s.cacheMu.RUnlock()
			return entry.data, nil
		}
		s.cacheMu.RUnlock()
	}

	now := time.Now()
	loc, _ := time.LoadLocation("America/New_York")

	barStart := time.Date(now.Year(), now.Month(), now.Day(), 4, 0, 0, 0, loc)
	if barStart.After(now) {
		barStart = now.Add(-4 * time.Hour)
	}
	barEnd := now
	if !opts.WindowStart.IsZero() && !opts.WindowEnd.IsZero() {
		padding := opts.WindowEnd.Sub(opts.WindowStart) / 3
		if padding < 15*time.Minute {
			padding = 15 * time.Minute
		}
		barStart = opts.WindowStart.Add(-padding)
		barEnd = opts.WindowEnd.Add(padding)
	}

	bars, err := s.repo.GetMarketBars(ctx, domain.Symbol(symbol), "1m", barStart, barEnd)
	if err != nil {
		return nil, fmt.Errorf("fetch bars for %s: %w", symbol, err)
	}
	if len(bars) < 2 {
		return nil, fmt.Errorf("insufficient bars for %s: got %d", symbol, len(bars))
	}

	title := fmt.Sprintf("%s — %s", symbol, now.In(loc).Format("Jan 02"))
	chartPNG, err := s.chartGen.GenerateCandlestickChart(ctx, bars, title, opts)
	if err != nil {
		return nil, fmt.Errorf("render chart for %s: %w", symbol, err)
	}

	if !hasAnnotations {
		s.cacheMu.Lock()
		s.chartCache[symbol] = chartCacheEntry{data: chartPNG, createdAt: now}
		s.cacheMu.Unlock()
	}

	return chartPNG, nil
}

func (s *Service) fmtOrderSubmitted(ev domain.Event) string {
	p, ok := ev.Payload.(domain.OrderIntentEventPayload)
	if !ok {
		return "📤 Order Submitted"
	}

	emoji := "📤"
	action := "Order Submitted"
	isExit := p.Direction == string(domain.DirectionCloseLong)
	if isExit {
		emoji = "📕"
		action = "Exit Submitted"
	}
	msg := fmt.Sprintf("%s **%s:** %s **%s** @ **$%s** (qty: %g)",
		emoji, action, p.Direction, p.Symbol, domain.FmtPrice(p.LimitPrice), p.Quantity)
	msg += fmt.Sprintf("\n📊 Strategy: %s | Confidence: **%.0f%%**", p.Strategy, p.Confidence*100)

	if !isExit && p.StopLoss > 0 {
		stopPct := (p.LimitPrice - p.StopLoss) / p.LimitPrice * 100
		msg += fmt.Sprintf("\n🛑 Stop Loss: **$%s** (%.2f%%)", domain.FmtPrice(p.StopLoss), stopPct)
	}

	if !isExit {
		if raw, ok := p.Meta["exit_rules"]; ok {
			msg += fmtExitRules(raw, p.LimitPrice, p.Meta)
		}
	}

	if p.Rationale != "" {
		msg += fmt.Sprintf("\n💡 Rationale: %s", p.Rationale)
	}
	return msg
}

func fmtExitRules(rawJSON string, entryPrice float64, meta map[string]string) string {
	type ruleWire struct {
		Type   string             `json:"type"`
		Params map[string]float64 `json:"params"`
	}
	var rules []ruleWire
	if err := json.Unmarshal([]byte(rawJSON), &rules); err != nil || len(rules) == 0 {
		return ""
	}

	msg := "\n📐 Exit Rules:"
	for _, r := range rules {
		switch r.Type {
		case "VOLATILITY_STOP":
			if m, ok := r.Params["atr_multiplier"]; ok {
				msg += fmt.Sprintf("\n  VOLATILITY_STOP: %.1fx ATR", m)
				if price, ok := meta["exit_price_volatility_stop"]; ok {
					msg += fmt.Sprintf(" ($%s)", price)
				}
			}
		case "SD_TARGET":
			if sd, ok := r.Params["sd_level"]; ok {
				msg += fmt.Sprintf("\n  SD_TARGET: %.1f SD", sd)
				if price, ok := meta["exit_price_sd_target"]; ok {
					msg += fmt.Sprintf(" ($%s)", price)
				}
			}
		case "MAX_LOSS":
			if pct, ok := r.Params["pct"]; ok {
				msg += fmt.Sprintf("\n  MAX_LOSS: %.1f%%", pct*100)
				if entryPrice > 0 {
					msg += fmt.Sprintf(" ($%s)", domain.FmtPrice(entryPrice*(1-pct)))
				}
			}
		case "MAX_HOLDING_TIME":
			if min, ok := r.Params["minutes"]; ok {
				msg += fmt.Sprintf("\n  MAX_HOLD: %.0fm", min)
			}
		case "STAGNATION_EXIT":
			if min, ok := r.Params["minutes"]; ok {
				msg += fmt.Sprintf("\n  STAGNATION: %.0fm", min)
				if sd, ok := r.Params["sd_threshold"]; ok {
					msg += fmt.Sprintf(" (< %.1f SD move)", sd)
				}
				if price, ok := meta["exit_price_stagnation"]; ok {
					msg += fmt.Sprintf(" ($%s)", price)
				}
			}
		case "TRAILING_STOP":
			if pct, ok := r.Params["pct"]; ok {
				msg += fmt.Sprintf("\n  TRAILING_STOP: %.2f%%", pct*100)
				if price, ok := meta["exit_price_trailing_stop"]; ok {
					msg += fmt.Sprintf(" ($%s)", price)
				}
			}
		case "PROFIT_TARGET":
			if pct, ok := r.Params["pct"]; ok {
				msg += fmt.Sprintf("\n  PROFIT_TARGET: %.1f%%", pct*100)
				if price, ok := meta["exit_price_profit_target"]; ok {
					msg += fmt.Sprintf(" ($%s)", price)
				}
			}
		case "STEP_STOP":
			msg += "\n  STEP_STOP: enabled"
			if price, ok := meta["exit_price_step_stop"]; ok {
				msg += fmt.Sprintf(" ($%s)", price)
			}
		default:
			msg += fmt.Sprintf("\n  %s", r.Type)
		}
	}
	return msg
}

// bestTargetPrice returns the best available profit-target price from order
// intent Meta, checking keys in priority order: SD_TARGET (VWAP-based) first,
// then PROFIT_TARGET (percentage-based). Returns 0 if none found.
func bestTargetPrice(meta map[string]string) float64 {
	for _, key := range []string{"exit_price_sd_target", "exit_price_profit_target"} {
		if raw, ok := meta[key]; ok {
			if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
				return v
			}
		}
	}
	return 0
}

func (s *Service) fmtSignalEnriched(ev domain.Event) string {
	if e, ok := ev.Payload.(domain.SignalEnrichment); ok {
		if e.Signal.SignalType == "exit" {
			return ""
		}
		emoji := "🧠"
		statusLabel := string(e.Status)
		if e.Status == domain.EnrichmentSkipped {
			statusLabel = "AI discussion skipped"
		}
		msg := fmt.Sprintf("%s **Signal Enriched:** %s %s **%s** [%s] (Confidence: **%.0f%%**)",
			emoji, e.Signal.SignalType, e.Signal.Side, e.Signal.Symbol,
			statusLabel, e.Confidence*100)
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
			msg += fmt.Sprintf("\n%s Est. P&L: **%s$%.2f (%+.2f%%)** | Entry: $%s", pnlEmoji, sign, absUSD, e.UnrealizedPnLPct*100, domain.FmtPrice(e.EntryPrice))
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
		if len(e.NewsHeadlines) > 0 {
			msg += "\n📰 News:"
			limit := len(e.NewsHeadlines)
			if limit > 3 {
				limit = 3
			}
			for _, h := range e.NewsHeadlines[:limit] {
				msg += fmt.Sprintf("\n  • %s", h)
			}
			if len(e.NewsHeadlines) > 3 {
				msg += fmt.Sprintf("\n  … +%d more", len(e.NewsHeadlines)-3)
			}
		}
		return msg
	}
	return "🧠 Signal Enriched"
}

func (s *Service) fmtSignalGated(ev domain.Event) string {
	if p, ok := ev.Payload.(domain.SignalGatedPayload); ok {
		return fmt.Sprintf("🚫 **Signal Blocked:** %s %s **%s** — %s\n📊 Strategy: %s | Confidence: **%.0f%%**",
			p.SignalType, p.Side, p.Symbol, p.Reason, p.Strategy, p.Confidence*100)
	}
	return "🚫 Signal Blocked by risk gate"
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
			return fmt.Sprintf("⚠️ **Intent Rejected:** %s **%s** — %s\n📊 Strategy: %s", p.Direction, p.Symbol, p.Reason, p.Strategy)
		}
		return fmt.Sprintf("⚠️ **Intent Rejected:** %s **%s** — failed risk/slippage check\n📊 Strategy: %s",
			p.Direction, p.Symbol, p.Strategy)
	}
	return "⚠️ Order Intent Rejected (risk/slippage check failed)"
}

func (s *Service) fmtFillReceived(ev domain.Event) string {
	if m, ok := ev.Payload.(map[string]any); ok {
		sym, _ := m["symbol"].(string)
		side, _ := m["side"].(string)
		qty, _ := m["quantity"].(float64)
		price, _ := m["price"].(float64)
		return fmt.Sprintf("💰 **Fill:** %s **%s** — %g shares @ **$%s**", side, sym, qty, domain.FmtPrice(price))
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

	msg := fmt.Sprintf("%s **P&L: %s$%.2f (%+.2f%%)**", pnlEmoji, sign, absPnL, p.PnLPct)
	msg += fmt.Sprintf(" | Entry: $%s", domain.FmtPrice(p.EntryPrice))

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
	return "🚨 **KILL SWITCH ENGAGED** — Trading halted for symbol"
}

func (s *Service) fmtCircuitBreaker(ev domain.Event) string {
	if reason, ok := ev.Payload.(string); ok {
		return fmt.Sprintf("🔴 **CIRCUIT BREAKER TRIPPED:** %s", reason)
	}
	return "🔴 **CIRCUIT BREAKER TRIPPED** — System-wide trading halt"
}

func (s *Service) fmtDebateCompleted(ev domain.Event) string {
	if d, ok := ev.Payload.(domain.AdvisoryDecision); ok {
		msg := fmt.Sprintf("🤖 **AI Debate** — %s (Confidence: **%.0f%%**)", d.Direction, d.Confidence*100)
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

func (s *Service) fmtFeedDegraded(ev domain.Event) string {
	if p, ok := ev.Payload.(domain.FeedDegradedPayload); ok {
		return fmt.Sprintf("⚠️ **Feed Degraded** [%s]: %s", p.Feed, p.Reason)
	}
	return "⚠️ Market data feed degraded"
}

func (s *Service) fmtWSCircuitBreaker(ev domain.Event) string {
	if p, ok := ev.Payload.(domain.WSCircuitBreakerTrippedPayload); ok {
		return fmt.Sprintf("🔌 **WS Circuit Breaker Open** [%s]: %d consecutive failures — retries blocked for %.0fs",
			p.Feed, p.ConsecutiveFails, p.BlockedForSeconds)
	}
	return "🔌 WebSocket circuit breaker tripped — retries blocked"
}

func (s *Service) fmtFillPollTimeout(ev domain.Event) string {
	if p, ok := ev.Payload.(domain.FillPollTimeoutPayload); ok {
		return fmt.Sprintf("⏰ **Fill Poll Timeout:** %s **%s** %s — order %s not filled within 2m (qty: %g)",
			p.Direction, string(p.Symbol), p.Strategy, p.BrokerOrderID, p.Quantity)
	}
	return "⏰ Fill poll timed out — order not filled within 2 minutes"
}

func (s *Service) fmtStaleOrderCancelled(ev domain.Event) string {
	if p, ok := ev.Payload.(domain.StaleOrderCancelledPayload); ok {
		return fmt.Sprintf("🗑️ **Stale Order Canceled:** %s **%s** — order %s expired after %.0fs (strategy: %s)",
			p.Direction, string(p.Symbol), p.BrokerOrderID, p.AgeSeconds, p.Strategy)
	}
	return "🗑️ Stale order force-canceled"
}

func (s *Service) fmtExitCircuitBroken(ev domain.Event) string {
	if p, ok := ev.Payload.(domain.ExitCircuitBrokenPayload); ok {
		return fmt.Sprintf("🚨 **Exit Circuit Breaker:** **%s** — %d consecutive exit failures, blocked for %.0fs. Broker may be rejecting all sells for this symbol.",
			string(p.Symbol), p.Failures, p.CooldownSecs)
	}
	return "🚨 Exit circuit breaker tripped — exit retries blocked"
}

func (s *Service) fmtSystemStarted(ev domain.Event) string {
	p, ok := ev.Payload.(domain.SystemStartedPayload)
	if !ok {
		return "✅ System started"
	}

	brokerStatus := strings.ToUpper(p.Broker)
	if p.Broker == "ibkr" {
		if p.IBKRConnected {
			brokerStatus += " 🟢 connected"
		} else {
			brokerStatus += " 🔴 disconnected"
		}
		if p.IBKRPaperMode {
			brokerStatus += " (delayed data)"
		} else {
			brokerStatus += " (live data)"
		}
	}

	dataSource := "Alpaca WebSocket + Alpaca REST historical"
	if p.Broker == "ibkr" {
		dataSource = "IBKR real-time bars + Alpaca REST historical"
	}

	fmtEMALine := func(succeeded int, failed []string) string {
		total := succeeded + len(failed)
		line := fmt.Sprintf("✅ %d/%d", succeeded, total)
		if len(failed) > 0 {
			names := strings.Join(failed, ", ")
			if len(failed) > 4 {
				names = strings.Join(failed[:4], ", ") + fmt.Sprintf(" +%d", len(failed)-4)
			}
			line += fmt.Sprintf(" ⚠️ %s", names)
		}
		return line
	}
	ema50Line := fmtEMALine(p.EMA50Succeeded, p.EMA50Failed)
	ema200Line := fmtEMALine(p.EMA200Succeeded, p.EMA200Failed)

	var stratLines []string
	for _, name := range p.Strategies {
		syms := p.StrategySymbols[name]
		symStr := strings.Join(syms, ", ")
		if len(syms) > 8 {
			symStr = strings.Join(syms[:8], ", ") + fmt.Sprintf(" +%d", len(syms)-8)
		}
		stratLines = append(stratLines, fmt.Sprintf("  • **%s** (%d): %s", name, len(syms), symStr))
	}
	stratSection := strings.Join(stratLines, "\n")
	if stratSection == "" {
		stratSection = "  none"
	}

	return strings.Join([]string{
		"✅ **System Started: omo-core**",
		fmt.Sprintf("📋 **Mode:** %s | **Broker:** %s", p.EnvMode, brokerStatus),
		fmt.Sprintf("📡 **Data:** %s", dataSource),
		fmt.Sprintf("📊 **Symbols:** %d total — %d equity, %d crypto", len(p.Symbols), p.EquityCount, p.CryptoCount),
		fmt.Sprintf("📈 **EMA50 (1H):** %s", ema50Line),
		fmt.Sprintf("📈 **EMA200 (1D):** %s", ema200Line),
		fmt.Sprintf("🎯 **Strategies (%d):**", len(p.Strategies)),
		stratSection,
	}, "\n")
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

	msg := fmt.Sprintf("🔍 **Risk Re-evaluation:** **%s** (%s)", r.Symbol, r.Strategy)
	msg += fmt.Sprintf("\n%s Position: Entry **$%s** → Now **$%s** (%+.2f%%) | HWM $%s",
		pnlEmoji, domain.FmtPrice(r.EntryPrice), domain.FmtPrice(r.CurrentPrice), r.UnrealizedPnL*100, domain.FmtPrice(r.HighWaterMark))
	msg += fmt.Sprintf("\n⏱️ Held: %s", r.HoldDuration)
	msg += fmt.Sprintf("\n%s Thesis: %s", thesisEmoji, r.ThesisStatus)
	msg += fmt.Sprintf("\n%s Action: **%s** (Risk: %s, Confidence: **%.0f%%**)",
		actionEmoji, r.Action, r.UpdatedModifier, r.Confidence*100)

	if len(r.RuleChanges) > 0 {
		msg += "\n📐 Exit Rules:"
		for _, ch := range r.RuleChanges {
			var line string
			if ch.OldValue != ch.NewValue {
				line = fmt.Sprintf("\n  %s [%s]: %.4f → %.4f", ch.Rule, ch.Param, ch.OldValue, ch.NewValue)
				if ch.Param == "pct" && r.CurrentPrice > 0 {
					oldTrigger := exitTriggerPrice(ch.Rule, ch.OldValue, r.EntryPrice, r.HighWaterMark)
					newTrigger := exitTriggerPrice(ch.Rule, ch.NewValue, r.EntryPrice, r.HighWaterMark)
					line += fmt.Sprintf(" (@ $%s → @ $%s)", domain.FmtPrice(oldTrigger), domain.FmtPrice(newTrigger))
				}
			} else {
				line = fmt.Sprintf("\n  %s [%s]: %.4f", ch.Rule, ch.Param, ch.NewValue)
				if ch.Param == "pct" && r.CurrentPrice > 0 {
					trigger := exitTriggerPrice(ch.Rule, ch.NewValue, r.EntryPrice, r.HighWaterMark)
					line += fmt.Sprintf(" (@ $%s)", domain.FmtPrice(trigger))
				}
			}
			msg += line
		}
	}

	if r.Reasoning != "" {
		msg += fmt.Sprintf("\n💡 %s", r.Reasoning)
	}

	return msg
}

func exitTriggerPrice(rule string, pct, entryPrice, highWaterMark float64) float64 {
	switch rule {
	case "TRAILING_STOP":
		return highWaterMark * (1 - pct)
	case "PROFIT_TARGET":
		return entryPrice * (1 + pct)
	case "MAX_LOSS":
		return entryPrice * (1 - pct)
	default:
		return pct * entryPrice
	}
}
