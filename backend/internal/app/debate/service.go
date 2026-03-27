package debate

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/options"
	"github.com/oh-my-opentrade/backend/internal/domain"
	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/rs/zerolog"
)

// Default order parameters used when generating an OrderIntent from an AI decision.
const (
	defaultLimitPrice     = 50000.0
	defaultMaxSlippageBPS = 10
)

// defaultAdvisorTimeout is the maximum time the service will wait for the LLM
// to return a debate result. Free-tier endpoints can be very slow under load;
// this hard cap prevents a slow LLM from causing trade slippage.
const defaultAdvisorTimeout = 5 * time.Second

// Service is the debate application service.
// It subscribes to SetupDetected events, runs each setup through the AI adversarial debate,
// and emits DebateRequested, DebateCompleted, and (conditionally) OrderIntentCreated events.
type Service struct {
	eventBus          ports.EventBusPort
	aiAdvisor         ports.AIAdvisorPort
	repo              ports.RepositoryPort
	specStore         portstrategy.SpecStore
	optionsMarket     ports.OptionsMarketDataPort
	historicalOptions ports.HistoricalOptionsPort
	minConfidence     float64
	equity            float64
	advisorTimeout    time.Duration
	log               zerolog.Logger
}

// SetHistoricalOptions injects a historical options data source for backtesting.
func (s *Service) SetHistoricalOptions(h ports.HistoricalOptionsPort) {
	s.historicalOptions = h
}

// Option is a functional option for Service.
type Option func(*Service)

// WithAdvisorTimeout sets the maximum duration to wait for the AI advisor to respond.
// If the advisor does not return within this duration, the debate is skipped (non-fatal).
func WithAdvisorTimeout(d time.Duration) Option {
	return func(s *Service) { s.advisorTimeout = d }
}

// SetEquity sets the account equity used for position sizing.
func (s *Service) SetEquity(equity float64) {
	s.equity = equity
}

// SetSpecStore sets the strategy spec store for reading sizing params from config.
func (s *Service) SetSpecStore(store portstrategy.SpecStore) {
	s.specStore = store
}

// SetOptionsMarket sets the options market data provider for options routing.
func (s *Service) SetOptionsMarket(m ports.OptionsMarketDataPort) {
	s.optionsMarket = m
}

// NewService creates a new debate Service.
// minConfidence is the minimum AI confidence [0,1] required to emit an OrderIntentCreated event.
// opts are functional options (e.g. WithAdvisorTimeout).
func NewService(eventBus ports.EventBusPort, aiAdvisor ports.AIAdvisorPort, repo ports.RepositoryPort, minConfidence float64, log zerolog.Logger, opts ...Option) *Service {
	s := &Service{
		eventBus:       eventBus,
		aiAdvisor:      aiAdvisor,
		repo:           repo,
		minConfidence:  minConfidence,
		equity:         100000, // default, overridden by SetEquity
		advisorTimeout: defaultAdvisorTimeout,
		log:            log,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start subscribes the service to SetupDetected events on the event bus.
func (s *Service) Start(ctx context.Context) error {
	if err := s.eventBus.Subscribe(ctx, domain.EventSetupDetected, s.handleSetup); err != nil {
		return fmt.Errorf("debate: failed to subscribe to SetupDetected: %w", err)
	}
	s.log.Info().Msg("subscribed to SetupDetected events")
	return nil
}

// handleSetup processes a single SetupDetected event through the AI debate pipeline.
func (s *Service) handleSetup(ctx context.Context, event domain.Event) error {
	setup, ok := event.Payload.(monitor.SetupCondition)
	if !ok {
		return nil
	}

	l := s.log.With().
		Str("symbol", string(setup.Symbol)).
		Str("idempotency_key", event.IdempotencyKey).
		Logger()

	// 1. Emit DebateRequested to signal the debate is starting.
	s.emit(ctx, domain.EventDebateRequested, event.TenantID, event.EnvMode, event.IdempotencyKey+"-debate-requested", setup)
	l.Info().Msg("debate requested, querying AI advisor")

	// 2. Call AI advisor — capped by advisorTimeout so a slow free-tier LLM
	// cannot delay order execution or cause slippage.
	advCtx, advCancel := context.WithTimeout(ctx, s.advisorTimeout)
	defer advCancel()
	decision, err := s.aiAdvisor.RequestDebate(advCtx, setup.Symbol, setup.Regime, setup.Snapshot)
	if err != nil {
		// AI disabled or errored — pass through the setup's own direction and confidence.
		l.Info().Err(err).Msg("AI advisor unavailable — using setup direction as passthrough")
		decision = &domain.AdvisoryDecision{
			Direction:  setup.Direction,
			Confidence: setup.Confidence,
			Rationale:  fmt.Sprintf("passthrough (no-ai): %s %s confidence=%.2f", setup.Trigger, setup.Direction, setup.Confidence),
		}
	}
	if decision == nil {
		l.Info().Msg("AI advisor returned NEUTRAL — no trade")
		return nil
	}

	l.Info().
		Float64("confidence", decision.Confidence).
		Str("direction", string(decision.Direction)).
		Msg("AI debate completed")

	// 3. Emit DebateCompleted with the full AI decision payload.
	s.emit(ctx, domain.EventDebateCompleted, event.TenantID, event.EnvMode, event.IdempotencyKey+"-debate-completed", decision)

	// 4. Only proceed to execution if confidence meets the minimum threshold.
	if decision.Confidence < s.minConfidence {
		l.Info().
			Float64("confidence", decision.Confidence).
			Float64("min_confidence", s.minConfidence).
			Msg("confidence below threshold — not creating order intent")
		return nil
	}

	// 5. Build an enriched OrderIntent using the AI direction and rationale.
	// Use the setup's bar close as reference price; stop defaults to 2% away.
	limitPrice := setup.BarClose
	if limitPrice <= 0 {
		limitPrice = defaultLimitPrice
	}
	var stopLoss float64

	// Position sizing from strategy config (or defaults).
	riskBPS := int64(100)    // 1% risk per trade
	stopBPS := int64(200)    // 2% stop distance
	maxPosBPS := int64(700)  // 7% max notional
	if s.specStore != nil && setup.Trigger != "" {
		sid, sidErr := domstrategy.NewStrategyID(setup.Trigger)
		if sidErr == nil {
			if spec, specErr := s.specStore.GetLatest(context.Background(), sid); specErr == nil {
				if v, ok := spec.Params["risk_per_trade_bps"].(int64); ok && v > 0 {
					riskBPS = v
				}
				if v, ok := spec.Params["stop_bps"].(int64); ok && v > 0 {
					stopBPS = v
				}
				if v, ok := spec.Params["max_position_bps"].(int64); ok && v > 0 {
					maxPosBPS = v
				}
			}
		}
	}

	// Widen stops when VIX is elevated (per research: 1.5x in VIX 15-25 zone)
	vixMult := 1.0
	if setup.VIXAdjust == "widen_stops" {
		vixMult = 1.5
		l.Info().Msg("VIX elevated — will widen stops 1.5x")
	}

	// Stop-loss priority: FVG structure > per-symbol daily ATR > fixed BPS fallback
	if setup.FVGStop > 0 {
		// FVG-based stop: most precise, derived from market structure
		stopLoss = setup.FVGStop
		l.Info().Float64("fvg_stop", stopLoss).Float64("limit_price", limitPrice).Msg("using FVG-based stop-loss")
	} else if dailyATR := setup.Snapshot.HTFDailyATR(); dailyATR > 0 && limitPrice > 0 {
		// Per-symbol ATR-based stop: 1.5x daily ATR (adjusts to each stock's volatility)
		atrMult := 1.5 * vixMult
		stopDist := dailyATR * atrMult
		if decision.Direction == domain.DirectionLong {
			stopLoss = limitPrice - stopDist
		} else {
			stopLoss = limitPrice + stopDist
		}
		l.Info().
			Float64("daily_atr", dailyATR).
			Float64("atr_mult", atrMult).
			Float64("stop_dist", stopDist).
			Float64("stop_loss", stopLoss).
			Str("symbol", string(setup.Symbol)).
			Msg("using per-symbol ATR-based stop-loss")
	} else {
		// Fixed BPS fallback
		stopBPS = int64(float64(stopBPS) * vixMult)
		stopPct := float64(stopBPS) / 10000.0
		stopLoss = limitPrice * (1 - stopPct)
		if decision.Direction == domain.DirectionShort {
			stopLoss = limitPrice * (1 + stopPct)
		}
	}

	riskPerShare := math.Abs(limitPrice - stopLoss)
	qty := 1.0
	if riskPerShare > 0 && s.equity > 0 {
		riskAmount := s.equity * float64(riskBPS) / 10000.0
		qty = math.Floor(riskAmount / riskPerShare)
		maxNotional := s.equity * float64(maxPosBPS) / 10000.0
		if limitPrice > 0 && qty*limitPrice > maxNotional {
			qty = math.Floor(maxNotional / limitPrice)
		}
		if qty < 1 {
			qty = 1
		}
	}

	intentID := uuid.New()
	intent, err := domain.NewOrderIntent(
		intentID,
		event.TenantID,
		event.EnvMode,
		setup.Symbol,
		decision.Direction,
		limitPrice,
		stopLoss,
		defaultMaxSlippageBPS,
		qty,
		setup.Trigger,
		decision.Rationale,
		decision.Confidence,
		intentID.String(),
	)
	if err != nil {
		l.Error().Err(err).Msg("failed to create order intent from AI decision")
		return nil
	}

	intent.Meta = map[string]string{
		"bull":  decision.BullArgument,
		"bear":  decision.BearArgument,
		"judge": decision.JudgeReasoning,
	}
	// Copy trading hours from strategy config so the TradingWindowGuard can enforce them.
	// Attach regime labels to intent meta for downstream (collector, trade log).
	if setup.EMARegime != "" {
		intent.Meta["regime"] = setup.EMARegime
	}
	if setup.VIXBucket != "" {
		intent.Meta["vix_bucket"] = setup.VIXBucket
	}
	if setup.MarketContext != "" {
		intent.Meta["market_context"] = setup.MarketContext
	}

	if s.specStore != nil && setup.Trigger != "" {
		sid, sidErr := domstrategy.NewStrategyID(setup.Trigger)
		if sidErr == nil {
			if spec, specErr := s.specStore.GetLatest(context.Background(), sid); specErr == nil {
				for _, key := range []string{"allowed_hours_start", "allowed_hours_end", "allowed_hours_tz", "skip_weekends"} {
					if v, ok := spec.Params[key].(string); ok && v != "" {
						intent.Meta[key] = v
					}
				}
			}
		}
	}

	// 5b. Options routing: if options enabled, try options first.
	// If options are enabled but no contract found, skip the trade entirely (no equity fallback).
	optionsEnabled := false
	if s.specStore != nil && setup.Trigger != "" {
		if sid, sidErr := domstrategy.NewStrategyID(setup.Trigger); sidErr == nil {
			if optSpec, specErr := s.specStore.GetLatest(ctx, sid); specErr == nil && optSpec.Options != nil && optSpec.Options.Enabled {
				optionsEnabled = true
			}
		}
	}

	if optionsEnabled {
		switch {
		case s.optionsMarket != nil:
			// Live market (production).
			optIntent, routed := s.tryOptionsRoute(ctx, event, setup, decision, intent, l)
			if routed {
				intent = optIntent
			} else {
				l.Info().Msg("options enabled but no contract found — skipping trade (no equity fallback)")
				return nil
			}
		case s.historicalOptions != nil:
			// Historical data (backtesting with DoltHub).
			optIntent, routed := s.tryHistoricalOptionsRoute(ctx, event, setup, decision, intent, l)
			if routed {
				intent = optIntent
			} else {
				// Fallback to BSM for symbols not in DoltHub (e.g., SOXL).
				optIntent, routed = s.tryBSMOptionsRoute(ctx, event, setup, decision, intent, l)
				if routed {
					intent = optIntent
				} else {
					l.Info().Msg("options enabled (historical+BSM) but no contract — skipping trade")
					return nil
				}
			}
		default:
			// Pure BSM synthetic (original fallback).
			optIntent, routed := s.tryBSMOptionsRoute(ctx, event, setup, decision, intent, l)
			if routed {
				intent = optIntent
			} else {
				l.Info().Msg("options enabled (BSM mode) but no contract — skipping trade")
				return nil
			}
		}
	}

	l.Info().Str("intent_id", intent.ID.String()).Msg("order intent created from AI debate")
	s.emit(ctx, domain.EventOrderIntentCreated, event.TenantID, event.EnvMode, intent.ID.String(), intent)

	// 6. Persist thought log for historical audit.
	if s.repo != nil {
		tl := domain.ThoughtLog{
			Time:           time.Now().UTC(),
			TenantID:       event.TenantID,
			EnvMode:        event.EnvMode,
			Symbol:         setup.Symbol,
			EventType:      "DebateCompleted",
			Direction:      string(decision.Direction),
			Confidence:     decision.Confidence,
			BullArgument:   decision.BullArgument,
			BearArgument:   decision.BearArgument,
			JudgeReasoning: decision.JudgeReasoning,
			Rationale:      decision.Rationale,
			IntentID:       intentID.String(),
		}
		if err := s.repo.SaveThoughtLog(ctx, tl); err != nil {
			l.Error().Err(err).Msg("failed to save thought log")
		}
	}

	return nil
}

// emit publishes a domain event on the event bus, discarding creation/publish errors
// (events are best-effort; the pipeline should not fail due to event emission).
// tryOptionsRoute attempts to convert an equity order intent to an options order.
// Returns (optionsIntent, true) if successful, or (nil, false) to fall through to equity.
func (s *Service) tryOptionsRoute(
	ctx context.Context,
	event domain.Event,
	setup monitor.SetupCondition,
	decision *domain.AdvisoryDecision,
	equityIntent domain.OrderIntent,
	l zerolog.Logger,
) (domain.OrderIntent, bool) {
	sid, err := domstrategy.NewStrategyID(setup.Trigger)
	if err != nil {
		return domain.OrderIntent{}, false
	}
	spec, err := s.specStore.GetLatest(ctx, sid)
	if err != nil || spec.Options == nil || !spec.Options.Enabled {
		return domain.OrderIntent{}, false
	}

	optRight := domain.OptionRightCall
	if decision.Direction == domain.DirectionShort {
		optRight = domain.OptionRightPut
	}

	// Compute target expiry from DTE range midpoint
	targetDTE := spec.Options.Defaults.MinDTE +
		(spec.Options.Defaults.MaxDTE-spec.Options.Defaults.MinDTE)/2
	targetExpiry := time.Now().AddDate(0, 0, targetDTE)

	chain, chainErr := s.optionsMarket.GetOptionChain(ctx, setup.Symbol, targetExpiry, optRight)
	if chainErr != nil || len(chain) == 0 {
		l.Warn().Err(chainErr).Str("symbol", string(setup.Symbol)).
			Str("right", string(optRight)).
			Msg("options chain unavailable — falling through to equity")
		return domain.OrderIntent{}, false
	}

	// Build contract selector with regime awareness
	regime := domain.RegimeTrend
	if setup.EMARegime != "" {
		if parsed, parseErr := domain.NewRegimeType(setup.EMARegime); parseErr == nil {
			regime = parsed
		}
	}

	regimes := spec.Options.ToRegimeConstraintsMap()
	selector := options.NewContractSelectionServiceWithRegimes(spec.Options.Defaults, regimes, time.Now)

	best, selectErr := selector.SelectBestContract(decision.Direction, regime, chain)
	if selectErr != nil {
		l.Warn().Err(selectErr).Str("symbol", string(setup.Symbol)).
			Str("regime", string(regime)).
			Msg("no suitable option contract — falling through to equity")
		return domain.OrderIntent{}, false
	}

	midPrice := (best.Bid + best.Ask) / 2
	if midPrice <= 0 {
		midPrice = best.Last
	}
	if midPrice <= 0 {
		return domain.OrderIntent{}, false
	}

	// Size by premium risk: contracts = risk_budget / premium_per_contract
	riskBPS := int64(100)
	if v, ok := spec.Params["risk_per_trade_bps"].(int64); ok && v > 0 {
		riskBPS = v
	}
	maxRiskUSD := (float64(riskBPS) / 10000.0) * s.equity
	premiumPerContract := midPrice * float64(best.Multiplier)
	qty := math.Floor(maxRiskUSD / premiumPerContract)
	if qty <= 0 {
		l.Warn().Float64("premium", premiumPerContract).Float64("risk_budget", maxRiskUSD).
			Msg("option premium exceeds risk budget — falling through to equity")
		return domain.OrderIntent{}, false
	}
	maxLossUSD := premiumPerContract * qty

	inst, instErr := domain.NewInstrument(domain.InstrumentTypeOption, string(best.ContractSymbol), string(setup.Symbol))
	if instErr != nil {
		return domain.OrderIntent{}, false
	}

	intentID := uuid.New()
	rationale := fmt.Sprintf("option: %s %s delta=%.2f DTE=%d | %s",
		optRight, best.ContractSymbol, best.Delta,
		int(time.Until(best.Expiry).Hours()/24),
		decision.Rationale)

	optIntent, intentErr := domain.NewOptionOrderIntent(
		intentID,
		event.TenantID,
		event.EnvMode,
		inst,
		domain.DirectionLong, // buying calls/puts
		midPrice,
		qty,
		setup.Trigger,
		rationale,
		decision.Confidence,
		intentID.String(),
		maxLossUSD,
	)
	if intentErr != nil {
		return domain.OrderIntent{}, false
	}

	// Copy meta from equity intent and add options-specific fields
	optIntent.AssetClass = domain.AssetClassEquity
	optIntent.Meta = make(map[string]string)
	for k, v := range equityIntent.Meta {
		optIntent.Meta[k] = v
	}
	optIntent.Meta["instrument_type"] = "OPTION"
	optIntent.Meta["option_right"] = string(optRight)
	optIntent.Meta["underlying"] = string(setup.Symbol)
	optIntent.Meta["strike"] = fmt.Sprintf("%.2f", best.Strike)
	optIntent.Meta["expiry"] = best.Expiry.Format("2006-01-02")
	optIntent.Meta["delta_at_entry"] = fmt.Sprintf("%.4f", best.Delta)
	optIntent.Meta["iv_at_entry"] = fmt.Sprintf("%.4f", best.IV)
	optIntent.Meta["premium"] = fmt.Sprintf("%.2f", midPrice)
	optIntent.Meta["open_interest"] = strconv.Itoa(best.OpenInterest)

	l.Info().
		Str("contract", string(best.ContractSymbol)).
		Float64("delta", best.Delta).
		Float64("premium", midPrice).
		Float64("qty", qty).
		Float64("max_loss", maxLossUSD).
		Str("right", string(optRight)).
		Msg("options order routed")

	return optIntent, true
}

// optionHalfSpreadPct estimates the half bid-ask spread as a fraction of premium.
// Used to simulate realistic entry fills (buying at the ask, above mid-price).
func optionHalfSpreadPct(premium float64, dte int, absDelta float64) float64 {
	var base float64
	switch {
	case premium >= 10.0:
		base = 0.008
	case premium >= 5.0:
		base = 0.012
	case premium >= 2.0:
		base = 0.020
	case premium >= 1.0:
		base = 0.030
	default:
		base = 0.050
	}
	if absDelta < 0.30 {
		base *= 1.5
	} else if absDelta > 0.60 {
		base *= 1.2
	}
	if dte < 14 {
		base *= 1.3
	}
	return base
}

// tryHistoricalOptionsRoute creates an options order using real historical chain data from DoltHub.
// Used in backtesting when historical options data is available.
func (s *Service) tryHistoricalOptionsRoute(
	ctx context.Context,
	event domain.Event,
	setup monitor.SetupCondition,
	decision *domain.AdvisoryDecision,
	equityIntent domain.OrderIntent,
	l zerolog.Logger,
) (domain.OrderIntent, bool) {
	sid, err := domstrategy.NewStrategyID(setup.Trigger)
	if err != nil {
		return domain.OrderIntent{}, false
	}
	spec, err := s.specStore.GetLatest(ctx, sid)
	if err != nil || spec.Options == nil || !spec.Options.Enabled {
		return domain.OrderIntent{}, false
	}

	isCall := decision.Direction != domain.DirectionShort
	optRight := domain.OptionRightCall
	if !isCall {
		optRight = domain.OptionRightPut
	}

	targetDeltaLow := spec.Options.Defaults.TargetDeltaLow
	targetDeltaHigh := spec.Options.Defaults.TargetDeltaHigh
	targetDeltaMid := (targetDeltaLow + targetDeltaHigh) / 2

	underlyingPrice := setup.BarClose
	if underlyingPrice <= 0 {
		return domain.OrderIntent{}, false
	}

	// Extract trade date from the bar timestamp.
	tradeDate := setup.Snapshot.Time
	if tradeDate.IsZero() {
		tradeDate = event.OccurredAt
	}

	// Query historical chain from local DB.
	chain, err := s.historicalOptions.GetHistoricalChain(ctx, domain.Symbol(setup.Symbol),
		tradeDate, optRight, spec.Options.Defaults.MinDTE, spec.Options.Defaults.MaxDTE)
	if err != nil || len(chain) == 0 {
		l.Debug().Err(err).Str("symbol", string(setup.Symbol)).Str("date", tradeDate.Format("2006-01-02")).
			Msg("no historical options data — falling back to BSM")
		return domain.OrderIntent{}, false
	}

	// Find the contract with delta closest to the target.
	bestIdx := -1
	bestDeltaDiff := math.MaxFloat64
	for i, row := range chain {
		absDelta := math.Abs(row.Delta)
		diff := math.Abs(absDelta - targetDeltaMid)
		if diff < bestDeltaDiff && row.Ask > 0 {
			bestDeltaDiff = diff
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return domain.OrderIntent{}, false
	}

	best := chain[bestIdx]
	absDelta := math.Abs(best.Delta)
	if absDelta < targetDeltaLow*0.8 || absDelta > targetDeltaHigh*1.2 {
		l.Warn().Float64("delta", best.Delta).Msg("historical: no strike within target delta range")
		return domain.OrderIntent{}, false
	}

	// Use the ask price for entry (buyer pays the ask).
	premium := best.Ask
	if premium <= 0 {
		premium = best.Mid()
	}
	if premium <= 0 {
		return domain.OrderIntent{}, false
	}

	iv := best.IV
	strike := best.Strike
	expiry := best.Expiration
	delta := best.Delta

	// Position sizing.
	riskBPS := int64(100)
	if v, ok := spec.Params["risk_per_trade_bps"].(int64); ok && v > 0 {
		riskBPS = v
	}
	maxRiskUSD := (float64(riskBPS) / 10000.0) * s.equity
	multiplier := 100.0
	premiumPerContract := premium * multiplier
	qty := math.Floor(maxRiskUSD / premiumPerContract)
	if qty <= 0 {
		l.Warn().Float64("premium", premium).Float64("risk_budget", maxRiskUSD).
			Msg("historical option premium exceeds risk budget")
		return domain.OrderIntent{}, false
	}
	maxLossUSD := premiumPerContract * qty

	// Build OCC symbol from historical data.
	occSymbol := fmt.Sprintf("%s%s%s%08d",
		setup.Symbol,
		expiry.Format("060102"),
		string(optRight)[:1],
		int(strike*1000))

	inst, instErr := domain.NewInstrument(domain.InstrumentTypeOption, occSymbol, string(setup.Symbol))
	if instErr != nil {
		return domain.OrderIntent{}, false
	}

	intentID := uuid.New()
	rationale := fmt.Sprintf("option(HIST): %s %s strike=%.0f delta=%.2f DTE=%d IV=%.0f%% bid=%.2f ask=%.2f | %s",
		optRight, setup.Symbol, strike, delta, int(expiry.Sub(tradeDate).Hours()/24), iv*100, best.Bid, best.Ask,
		decision.Rationale)

	optIntent, intentErr := domain.NewOptionOrderIntent(
		intentID, event.TenantID, event.EnvMode, inst,
		domain.DirectionLong, premium, qty,
		setup.Trigger, rationale, decision.Confidence,
		intentID.String(), maxLossUSD,
	)
	if intentErr != nil {
		return domain.OrderIntent{}, false
	}

	optIntent.AssetClass = domain.AssetClassEquity
	optIntent.Meta = make(map[string]string)
	for k, v := range equityIntent.Meta {
		optIntent.Meta[k] = v
	}
	optIntent.Meta["instrument_type"] = "OPTION"
	optIntent.Meta["option_right"] = string(optRight)
	optIntent.Meta["underlying"] = string(setup.Symbol)
	optIntent.Meta["strike"] = fmt.Sprintf("%.2f", strike)
	optIntent.Meta["expiry"] = expiry.Format("2006-01-02")
	optIntent.Meta["delta_at_entry"] = fmt.Sprintf("%.4f", delta)
	optIntent.Meta["iv_at_entry"] = fmt.Sprintf("%.4f", iv)
	optIntent.Meta["premium"] = fmt.Sprintf("%.2f", premium)
	optIntent.Meta["underlying_entry_price"] = fmt.Sprintf("%.4f", underlyingPrice)
	optIntent.Meta["bsm_synthetic"] = "false"
	optIntent.Meta["historical_bid"] = fmt.Sprintf("%.2f", best.Bid)
	optIntent.Meta["historical_ask"] = fmt.Sprintf("%.2f", best.Ask)

	l.Info().
		Str("contract", occSymbol).
		Float64("strike", strike).
		Float64("delta", delta).
		Float64("premium", premium).
		Float64("iv", iv).
		Float64("bid", best.Bid).
		Float64("ask", best.Ask).
		Float64("qty", qty).
		Str("right", string(optRight)).
		Msg("historical options route: order intent created")

	return optIntent, true
}

// tryBSMOptionsRoute creates a synthetic options order using Black-Scholes pricing.
// Used in backtesting when no OptionsMarketDataPort is available.
func (s *Service) tryBSMOptionsRoute(
	ctx context.Context,
	event domain.Event,
	setup monitor.SetupCondition,
	decision *domain.AdvisoryDecision,
	equityIntent domain.OrderIntent,
	l zerolog.Logger,
) (domain.OrderIntent, bool) {
	sid, err := domstrategy.NewStrategyID(setup.Trigger)
	if err != nil {
		return domain.OrderIntent{}, false
	}
	spec, err := s.specStore.GetLatest(ctx, sid)
	if err != nil || spec.Options == nil || !spec.Options.Enabled {
		return domain.OrderIntent{}, false
	}

	isCall := decision.Direction != domain.DirectionShort
	optRight := domain.OptionRightCall
	if !isCall {
		optRight = domain.OptionRightPut
	}

	// Use midpoint of DTE range
	targetDTE := spec.Options.Defaults.MinDTE + (spec.Options.Defaults.MaxDTE-spec.Options.Defaults.MinDTE)/2
	dteYears := float64(targetDTE) / 365.0

	underlyingPrice := setup.BarClose
	if underlyingPrice <= 0 {
		return domain.OrderIntent{}, false
	}

	// Strike: start at ATM, then adjust for target delta
	strike := math.Round(underlyingPrice)

	// IV: derive from daily ATR (not 1m ATR which is too small).
	// Daily ATR ≈ price × σ × sqrt(1/252), so σ ≈ dailyATR / price × sqrt(252)
	iv := 0.25 // default 25% if no data
	dailyATR := setup.Snapshot.HTFDailyATR()
	if dailyATR > 0 && underlyingPrice > 0 {
		iv = (dailyATR / underlyingPrice) * math.Sqrt(252)
	}
	// Add variance risk premium (~3 vol points) — IV typically exceeds realized vol
	iv += 0.03
	if iv < 0.10 {
		iv = 0.10 // minimum 10% IV (no stock trades below this)
	}
	if iv > 2.0 {
		iv = 2.0
	}

	riskFreeRate := 0.05 // approximate current rates

	// Use default target delta range from config
	targetDeltaLow := spec.Options.Defaults.TargetDeltaLow
	targetDeltaHigh := spec.Options.Defaults.TargetDeltaHigh
	targetDeltaMid := (targetDeltaLow + targetDeltaHigh) / 2

	// Search for the strike that gives the closest delta to target.
	// Scan strikes in $1 increments around ATM.
	bestStrike := strike
	bestDelta := 0.0
	bestPremium := 0.0
	bestDeltaDiff := 999.0

	for offset := -20.0; offset <= 20.0; offset += 1.0 {
		tryStrike := math.Round(underlyingPrice + offset)
		if tryStrike <= 0 {
			continue
		}
		p, d, _, _ := options.BSMPrice(underlyingPrice, tryStrike, dteYears, riskFreeRate, iv, isCall)
		if p <= 0 {
			continue
		}
		absDelta := math.Abs(d)
		diff := math.Abs(absDelta - targetDeltaMid)
		if diff < bestDeltaDiff {
			bestDeltaDiff = diff
			bestStrike = tryStrike
			bestDelta = d
			bestPremium = p
		}
	}

	strike = bestStrike
	delta := bestDelta
	premium := bestPremium

	// Final check: delta must be within the acceptable range
	absDelta := math.Abs(delta)
	if absDelta < targetDeltaLow*0.8 || absDelta > targetDeltaHigh*1.2 {
		l.Warn().Float64("delta", delta).Float64("target_low", targetDeltaLow).Float64("target_high", targetDeltaHigh).
			Msg("BSM: no strike within target delta range — skipping")
		return domain.OrderIntent{}, false
	}

	if premium <= 0 {
		return domain.OrderIntent{}, false
	}

	// Apply bid-ask half-spread: buying at the ask (above mid-price).
	// Spread widens for cheap options, OTM strikes, and short DTE.
	halfSpreadPct := optionHalfSpreadPct(premium, targetDTE, absDelta)
	premium *= (1.0 + halfSpreadPct)

	// Position sizing: premium at risk
	riskBPS := int64(100)
	if v, ok := spec.Params["risk_per_trade_bps"].(int64); ok && v > 0 {
		riskBPS = v
	}
	maxRiskUSD := (float64(riskBPS) / 10000.0) * s.equity
	multiplier := 100.0
	premiumPerContract := premium * multiplier
	qty := math.Floor(maxRiskUSD / premiumPerContract)
	if qty <= 0 {
		l.Warn().Float64("premium", premium).Float64("risk_budget", maxRiskUSD).
			Msg("BSM option premium exceeds risk budget — skipping")
		return domain.OrderIntent{}, false
	}
	maxLossUSD := premiumPerContract * qty

	// Create synthetic OCC symbol: SYMBOL + YYMMDD + C/P + strike
	expiry := time.Now().AddDate(0, 0, targetDTE)
	occSymbol := fmt.Sprintf("%s%s%s%08d",
		setup.Symbol,
		expiry.Format("060102"),
		string(optRight)[:1],
		int(strike*1000))

	inst, instErr := domain.NewInstrument(domain.InstrumentTypeOption, occSymbol, string(setup.Symbol))
	if instErr != nil {
		return domain.OrderIntent{}, false
	}

	intentID := uuid.New()
	rationale := fmt.Sprintf("option(BSM): %s %s strike=%.0f delta=%.2f DTE=%d IV=%.0f%% premium=%.2f | %s",
		optRight, setup.Symbol, strike, delta, targetDTE, iv*100, premium,
		decision.Rationale)

	optIntent, intentErr := domain.NewOptionOrderIntent(
		intentID,
		event.TenantID,
		event.EnvMode,
		inst,
		domain.DirectionLong,
		premium,
		qty,
		setup.Trigger,
		rationale,
		decision.Confidence,
		intentID.String(),
		maxLossUSD,
	)
	if intentErr != nil {
		return domain.OrderIntent{}, false
	}

	optIntent.AssetClass = domain.AssetClassEquity
	optIntent.Meta = make(map[string]string)
	for k, v := range equityIntent.Meta {
		optIntent.Meta[k] = v
	}
	optIntent.Meta["instrument_type"] = "OPTION"
	optIntent.Meta["option_right"] = string(optRight)
	optIntent.Meta["underlying"] = string(setup.Symbol)
	optIntent.Meta["strike"] = fmt.Sprintf("%.2f", strike)
	optIntent.Meta["expiry"] = expiry.Format("2006-01-02")
	optIntent.Meta["delta_at_entry"] = fmt.Sprintf("%.4f", delta)
	optIntent.Meta["iv_at_entry"] = fmt.Sprintf("%.4f", iv)
	optIntent.Meta["premium"] = fmt.Sprintf("%.2f", premium)
	optIntent.Meta["underlying_entry_price"] = fmt.Sprintf("%.4f", underlyingPrice)
	optIntent.Meta["bsm_synthetic"] = "true"

	l.Info().
		Str("contract", occSymbol).
		Float64("strike", strike).
		Float64("delta", delta).
		Float64("premium", premium).
		Float64("iv", iv).
		Float64("qty", qty).
		Float64("max_loss", maxLossUSD).
		Str("right", string(optRight)).
		Msg("BSM synthetic options order created")

	return optIntent, true
}

func (s *Service) emit(ctx context.Context, eventType string, tenantID string, envMode domain.EnvMode, idempotencyKey string, payload any) {
	ev, err := domain.NewEvent(eventType, tenantID, envMode, idempotencyKey, payload)
	if err != nil {
		return
	}
	_ = s.eventBus.Publish(ctx, *ev)
}
