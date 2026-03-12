package execution

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("omo-core/execution")

type pendingOrder struct {
	intent      domain.OrderIntent
	tenantID    string
	envMode     domain.EnvMode
	submitStart time.Time
}

type Service struct {
	eventBus           ports.EventBusPort
	broker             ports.BrokerPort
	orderStream        ports.OrderStreamPort
	repo               ports.RepositoryPort
	riskEngine         *RiskEngine
	slippageGuard      *SlippageGuard
	spreadGuard        *SpreadGuard
	tradingWindowGuard *TradingWindowGuard
	killSwitch         *KillSwitch
	dailyLossBreaker   *risk.DailyLossBreaker
	positionGate       *PositionGate
	exposureGuard      *ExposureGuard
	buyingPowerGuard   *BuyingPowerGuard
	optionsRiskEngine  *OptionsRiskEngine
	accountEquity      float64
	log                zerolog.Logger
	metrics            *metrics.Metrics
	pendingOrders      sync.Map // brokerOrderID → *pendingOrder
	tenantID           string
	envMode            domain.EnvMode
	syncFill           bool
}

// Option is a functional option for Service.
type Option func(*Service)

// WithPositionGate attaches a PositionGate to the execution pipeline.
func WithPositionGate(pg *PositionGate) Option {
	return func(s *Service) { s.positionGate = pg }
}

func WithExposureGuard(eg *ExposureGuard) Option {
	return func(s *Service) { s.exposureGuard = eg }
}

func WithBuyingPowerGuard(bpg *BuyingPowerGuard) Option {
	return func(s *Service) { s.buyingPowerGuard = bpg }
}

func WithSpreadGuard(sg *SpreadGuard) Option {
	return func(s *Service) { s.spreadGuard = sg }
}

func WithOptionsRiskEngine(ore *OptionsRiskEngine) Option {
	return func(s *Service) { s.optionsRiskEngine = ore }
}

func WithTradingWindowGuard(twg *TradingWindowGuard) Option {
	return func(s *Service) { s.tradingWindowGuard = twg }
}

func WithOrderStream(os ports.OrderStreamPort) Option {
	return func(s *Service) { s.orderStream = os }
}

func WithSyncFill() Option {
	return func(s *Service) { s.syncFill = true }
}

// NewService creates a new execution Service.
func NewService(
	eventBus ports.EventBusPort,
	broker ports.BrokerPort,
	repo ports.RepositoryPort,
	riskEngine *RiskEngine,
	slippageGuard *SlippageGuard,
	killSwitch *KillSwitch,
	dailyLossBreaker *risk.DailyLossBreaker,
	accountEquity float64,
	log zerolog.Logger,
	opts ...Option,
) *Service {
	s := &Service{
		eventBus:         eventBus,
		broker:           broker,
		repo:             repo,
		riskEngine:       riskEngine,
		slippageGuard:    slippageGuard,
		killSwitch:       killSwitch,
		dailyLossBreaker: dailyLossBreaker,
		accountEquity:    accountEquity,
		log:              log,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// SetAccountEquity updates the account equity used by the risk engine.
// Safe to call concurrently from a periodic refresh goroutine.
func (s *Service) SetAccountEquity(equity float64) {
	if equity <= 0 {
		return
	}
	s.accountEquity = equity
	if s.exposureGuard != nil {
		s.exposureGuard.UpdateCaps(equity)
	}
}

// SetMetrics injects Prometheus collectors. Safe to leave nil (no-op).
func (s *Service) SetMetrics(m *metrics.Metrics) { s.metrics = m }

func (s *Service) Start(ctx context.Context, tenantID string, envMode domain.EnvMode) error {
	s.tenantID = tenantID
	s.envMode = envMode

	if err := s.eventBus.SubscribeAsync(ctx, domain.EventOrderIntentCreated, s.handleIntent); err != nil {
		return fmt.Errorf("execution: failed to subscribe to OrderIntentCreated: %w", err)
	}
	if err := s.eventBus.SubscribeAsync(ctx, domain.EventRiskDowngraded, s.handleRiskDowngrade); err != nil {
		return fmt.Errorf("execution: failed to subscribe to RiskDowngraded: %w", err)
	}
	s.log.Info().Msg("subscribed to OrderIntentCreated and RiskDowngraded events")

	s.reconcileOnBoot(ctx)

	if s.orderStream != nil {
		ch, err := s.orderStream.SubscribeOrderUpdates(ctx)
		if err != nil {
			return fmt.Errorf("execution: failed to subscribe to order stream: %w", err)
		}
		go s.runFillListener(ctx, ch)
		go s.runReconciliationLoop(ctx)
		s.log.Info().Msg("WebSocket fill listener and reconciliation loop started")
	}

	return nil
}

// reconcileOnBoot runs once at startup BEFORE the WS listener starts.
// It queries the DB for non-terminal orders and compares their recorded fill qty
// against the broker's cumulative fill qty. Any delta is inserted as a synthetic
// trade to bring the DB in sync with the broker (source of truth).
func (s *Service) reconcileOnBoot(ctx context.Context) {
	l := s.log.With().Str("component", "reconcile_on_boot").Logger()
	l.Info().Msg("starting startup fill reconciliation")

	orders, err := s.repo.GetNonTerminalOrders(ctx, s.tenantID, s.envMode)
	if err != nil {
		l.Error().Err(err).Msg("failed to query non-terminal orders — skipping reconciliation")
		return
	}

	if len(orders) == 0 {
		l.Info().Msg("no non-terminal orders to reconcile")
		return
	}

	reconciled, updated := 0, 0
	for _, order := range orders {
		ol := l.With().
			Str("broker_order_id", order.BrokerOrderID).
			Str("symbol", string(order.Symbol)).
			Str("side", order.Side).
			Logger()

		details, err := s.broker.GetOrderDetails(ctx, order.BrokerOrderID)
		if err != nil {
			ol.Warn().Err(err).Msg("reconcile: failed to get order details — skipping")
			continue
		}

		isTerminal := details.Status == "canceled" || details.Status == "expired" || details.Status == "rejected"
		if isTerminal {
			if err := s.repo.UpdateOrderStatus(ctx, order.BrokerOrderID, details.Status); err != nil {
				ol.Error().Err(err).Msg("reconcile: failed to update terminal status")
			} else {
				ol.Info().Str("status", details.Status).Msg("reconcile: marked order terminal")
				updated++
			}
			if details.FilledQty <= 0 {
				continue
			}
			ol.Info().Float64("filled_qty", details.FilledQty).Msg("reconcile: terminal order has fills — checking for missed trades")
		}

		if details.FilledQty <= 0 {
			ol.Debug().Str("status", details.Status).Msg("reconcile: order has no fills yet — skipping")
			continue
		}

		dbFilledQty, err := s.repo.GetRecordedFillQty(ctx, s.tenantID, s.envMode, order.Symbol, order.Side, order.Time.Add(-1*time.Minute))
		if err != nil {
			ol.Error().Err(err).Msg("reconcile: failed to query recorded fill qty")
			continue
		}

		delta := details.FilledQty - dbFilledQty
		if delta < 1e-9 {
			ol.Debug().
				Float64("broker_filled", details.FilledQty).
				Float64("db_filled", dbFilledQty).
				Msg("reconcile: DB is in sync — no delta")
			if details.Status == "filled" {
				if err := s.repo.UpdateOrderStatus(ctx, order.BrokerOrderID, "filled"); err != nil {
					ol.Error().Err(err).Msg("reconcile: failed to update order status to filled")
				}
			}
			continue
		}

		tradeID := deterministicTradeID(order.BrokerOrderID, details.FilledQty)
		fillTime := details.FilledAt
		if fillTime.IsZero() {
			fillTime = time.Now().UTC()
		}

		trade, tErr := domain.NewTrade(
			fillTime, s.tenantID, s.envMode, tradeID,
			order.Symbol, order.Side, delta, details.FilledAvgPrice, 0,
			"FILLED", order.Strategy,
			fmt.Sprintf("reconcile_on_boot: missed %.8f (broker=%.8f db=%.8f) for order %s", delta, details.FilledQty, dbFilledQty, order.BrokerOrderID),
		)
		if tErr != nil {
			ol.Error().Err(tErr).Msg("reconcile: failed to construct synthetic trade")
			continue
		}

		if sErr := s.repo.SaveTrade(ctx, trade); sErr != nil {
			ol.Error().Err(sErr).Msg("reconcile: failed to save synthetic trade")
			continue
		}

		if uErr := s.repo.UpdateOrderFill(ctx, order.BrokerOrderID, fillTime, details.FilledAvgPrice, details.FilledQty); uErr != nil {
			ol.Error().Err(uErr).Msg("reconcile: failed to update order fill record")
		}
		if !isTerminal {
			if uErr := s.repo.UpdateOrderStatus(ctx, order.BrokerOrderID, details.Status); uErr != nil {
				ol.Error().Err(uErr).Msg("reconcile: failed to update order status")
			}
		}

		reconciled++
		ol.Info().
			Float64("delta", delta).
			Float64("broker_filled", details.FilledQty).
			Float64("db_filled", dbFilledQty).
			Float64("fill_price", details.FilledAvgPrice).
			Str("trade_id", tradeID.String()).
			Msg("reconcile: synthetic fill inserted for missed quantity")

		s.emit(ctx, domain.EventFillReceived, s.tenantID, s.envMode, order.BrokerOrderID, map[string]any{
			"broker_order_id": order.BrokerOrderID,
			"symbol":          string(order.Symbol),
			"side":            order.Side,
			"quantity":        delta,
			"price":           details.FilledAvgPrice,
			"filled_at":       fillTime,
			"strategy":        order.Strategy,
			"synthetic":       true,
		})
	}

	l.Info().
		Int("orders_checked", len(orders)).
		Int("fills_reconciled", reconciled).
		Int("statuses_updated", updated).
		Msg("startup fill reconciliation complete")
}

// deterministicTradeID generates an idempotent UUID from the broker order ID and cumulative
// filled qty. Running reconciliation multiple times with the same data produces the same
// trade ID, making the operation safe to repeat (INSERT will conflict on duplicate trade_id).
func deterministicTradeID(brokerOrderID string, cumulativeFilledQty float64) uuid.UUID {
	h := sha256.New()
	h.Write([]byte(brokerOrderID))
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(cumulativeFilledQty*1e8))
	h.Write(buf[:])
	sum := h.Sum(nil)
	id, _ := uuid.FromBytes(sum[:16])
	id[6] = (id[6] & 0x0f) | 0x50 // version 5
	id[8] = (id[8] & 0x3f) | 0x80 // variant RFC 4122
	return id
}

// handleIntent processes a single OrderIntentCreated event through the execution pipeline.
func (s *Service) handleIntent(ctx context.Context, event domain.Event) error {
	ctx, span := tracer.Start(ctx, "execution.handleIntent",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	intent, ok := event.Payload.(domain.OrderIntent)
	if !ok {
		return nil
	}

	span.SetAttributes(
		attribute.String("order.symbol", string(intent.Symbol)),
		attribute.String("order.direction", string(intent.Direction)),
		attribute.String("order.intent_id", intent.ID.String()),
		attribute.String("order.strategy", intent.Strategy),
		attribute.Float64("order.quantity", intent.Quantity),
		attribute.Float64("order.limit_price", intent.LimitPrice),
	)

	l := s.log.With().
		Str("symbol", string(intent.Symbol)).
		Str("direction", string(intent.Direction)).
		Str("idempotency_key", event.IdempotencyKey).
		Str("intent_id", intent.ID.String()).
		Logger()

	l.Info().
		Float64("limit_price", intent.LimitPrice).
		Float64("stop_loss", intent.StopLoss).
		Float64("quantity", intent.Quantity).
		Msg("order intent received, starting execution pipeline")

	// 1. Check kill switch before any work (skip for exits — closing reduces exposure).
	if !intent.Direction.IsExit() && s.killSwitch.IsHalted(event.TenantID, intent.Symbol) {
		l.Warn().Msg("kill switch engaged — trading halted for symbol")
		s.emit(ctx, domain.EventKillSwitchEngaged, event.TenantID, event.EnvMode, event.IdempotencyKey, nil)
		return nil
	}

	// 1a. Position gate — reject duplicate/conflicting entries.
	if s.positionGate != nil {
		if err := s.positionGate.Check(ctx, intent); err != nil {
			l.Warn().Err(err).Msg("order intent rejected by position gate")
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "position_gate").Inc()
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
			return nil
		}
	}

	if s.exposureGuard != nil {
		if err := s.exposureGuard.Check(ctx, intent); err != nil {
			l.Warn().Err(err).Msg("order intent rejected by exposure guard")
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "exposure").Inc()
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
			return nil
		}
	}

	if intent.Direction == domain.DirectionShort {
		var reason string
		if intent.AssetClass == domain.AssetClassCrypto {
			reason = "SHORT direction not supported — crypto is long-only on Alpaca"
		} else {
			reason = "SHORT direction not supported — paper account cannot short sell"
		}
		l.Warn().Str("asset_class", intent.AssetClass.String()).Msg(reason)
		if s.metrics != nil {
			s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "short_disabled").Inc()
		}
		s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, reason))
		return nil
	}

	// 2. Validate risk (skip for exit orders — closing reduces exposure).
	isOptionOrder := intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeOption
	if !intent.Direction.IsExit() {
		if isOptionOrder && s.optionsRiskEngine != nil {
			if err := s.optionsRiskEngine.ValidateOptionIntent(intent, s.accountEquity); err != nil {
				l.Warn().Err(err).Msg("order intent rejected by options risk engine")
				if s.metrics != nil {
					s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "options_risk").Inc()
				}
				s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
				return nil
			}
		} else if !isOptionOrder {
			if err := s.riskEngine.Validate(intent, s.accountEquity); err != nil {
				l.Warn().Err(err).Msg("order intent rejected by risk engine")
				if s.metrics != nil {
					s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "risk").Inc()
				}
				s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
				return nil
			}
		}
	}

	// 3. Validate slippage (skip for exit orders — we want to exit regardless).
	if !intent.Direction.IsExit() {
		if err := s.slippageGuard.Check(ctx, intent); err != nil {
			l.Warn().Err(err).Msg("order intent rejected by slippage guard")
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "validation").Inc()
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
			return nil
		}
	}
	// 3a. Trading window gate — reject entries outside allowed hours (opt-in via Meta).
	if !intent.Direction.IsExit() && s.tradingWindowGuard != nil {
		if err := s.tradingWindowGuard.Check(intent); err != nil {
			l.Warn().Err(err).Msg("order intent rejected by trading window guard")
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "trading_window").Inc()
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
			return nil
		}
	}

	// 3b. Spread gate — reject entries when bid-ask spread is too wide (opt-in via Meta).
	if !intent.Direction.IsExit() && s.spreadGuard != nil {
		if err := s.spreadGuard.Check(ctx, intent); err != nil {
			l.Warn().Err(err).Msg("order intent rejected by spread guard")
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "spread").Inc()
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
			return nil
		}
	}

	l.Info().Msg("order intent validated — passed risk, slippage, and market quality checks")
	s.emit(ctx, domain.EventOrderIntentValidated, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentEventPayload(intent, domain.OrderIntentStatusValidated))

	// 4. Record stop — if this trips the kill switch, abort before broker submission.
	// Skip for exits — only new entries should count toward the whipsaw circuit breaker.
	if !intent.Direction.IsExit() {
		if err := s.killSwitch.RecordStop(event.TenantID, intent.Symbol); err != nil {
			l.Warn().Err(err).Msg("kill switch tripped — aborting broker submission")
			s.emit(ctx, domain.EventCircuitBreakerTripped, event.TenantID, event.EnvMode, event.IdempotencyKey, err.Error())
			return nil
		}
	}

	// 5. Check daily loss circuit breaker.
	if s.dailyLossBreaker != nil {
		if err := s.dailyLossBreaker.Check(event.TenantID, event.EnvMode, s.accountEquity); err != nil {
			l.Warn().Err(err).Msg("daily loss circuit breaker tripped — aborting broker submission")
			s.emit(ctx, domain.EventCircuitBreakerTripped, event.TenantID, event.EnvMode, event.IdempotencyKey, err.Error())
			return nil
		}
	}

	// 5b. Buying power guard — pre-check DTBP for equity entries (only when DTBP_FALLBACK enabled).
	if s.buyingPowerGuard != nil {
		if err := s.buyingPowerGuard.Check(ctx, intent); err != nil {
			l.Warn().Err(err).Msg("order intent rejected by buying power guard")
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "buying_power").Inc()
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
			return nil
		}
	}

	// 5c. For exit intents, acquire exit inflight lock to prevent double-selling.
	if intent.Direction.IsExit() && s.positionGate != nil {
		if !s.positionGate.TryMarkInflightExit(event.TenantID, event.EnvMode, intent.Symbol) {
			l.Warn().Msg("exit already inflight — rejecting to prevent double-sell")
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "inflight_exit").Inc()
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, "position_gate: inflight_exit"))
			return nil
		}
	}

	// 5d. For exit intents, resolve the full position quantity from the broker.
	if intent.Direction.IsExit() {
		positions, posErr := s.broker.GetPositions(ctx, event.TenantID, event.EnvMode)
		if posErr != nil {
			l.Error().Err(posErr).Msg("failed to query positions for exit — rejecting conservatively")
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, fmt.Sprintf("exit position query failed: %v", posErr)))
			return nil
		}
		var posQty float64
		for _, p := range positions {
			if p.Symbol == intent.Symbol {
				posQty += p.Quantity
			}
		}
		if posQty <= 0 {
			l.Warn().Msg("exit intent but no position found — rejecting")
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, "position_gate: no_position_to_exit"))
			return nil
		}
		intent.Quantity = posQty
		if intent.TimeInForce == "" {
			intent.TimeInForce = "ioc"
			buffer := exitLimitBuffer(intent.Symbol, intent.AssetClass)
			intent.LimitPrice *= (1 - buffer)
		}
		l.Info().Float64("exit_qty", posQty).Float64("exit_buffer_bps", exitLimitBuffer(intent.Symbol, intent.AssetClass)*10000).Msg("resolved exit quantity from broker position")
	}

	// 5e. Cancel stale open buy orders for this symbol to prevent position doubling and wash trades.
	cancelSide := "buy"
	if canceled, cancelErr := s.broker.CancelOpenOrders(ctx, intent.Symbol, cancelSide); cancelErr != nil {
		l.Warn().Err(cancelErr).Msg("failed to cancel open orders — proceeding with submission")
	} else if canceled > 0 {
		l.Info().Int("canceled", canceled).Str("side", cancelSide).Msg("canceled stale open orders before submission")
	}

	// 6. Submit to broker.
	submitStart := time.Now()
	brokerOrderID, err := s.broker.SubmitOrder(ctx, intent)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "broker rejected order")
		l.Error().Err(err).Msg("broker rejected order")
		if s.metrics != nil {
			side := "sell"
			if intent.Direction == domain.DirectionLong {
				side = "buy"
			}
			s.metrics.Orders.Total.WithLabelValues("alpaca", intent.Strategy, side, "limit", "rejected").Inc()
			s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "api").Inc()
			s.metrics.Orders.SubmitLat.WithLabelValues("alpaca", intent.Strategy, "limit").Observe(time.Since(submitStart).Seconds())
		}
		if intent.Direction.IsExit() && s.positionGate != nil {
			s.positionGate.ClearInflightExit(event.TenantID, event.EnvMode, intent.Symbol)
			if tripped := s.positionGate.RecordExitFailure(event.TenantID, event.EnvMode, intent.Symbol); tripped {
				s.emit(ctx, domain.EventExitCircuitBroken, event.TenantID, event.EnvMode, intent.ID.String(), domain.ExitCircuitBrokenPayload{
					Symbol:       intent.Symbol,
					Failures:     maxExitFailures,
					CooldownSecs: exitCooldownDuration.Seconds(),
				})
			}
			s.emit(ctx, domain.EventExitOrderTerminal, event.TenantID, event.EnvMode, intent.ID.String(), map[string]any{
				"symbol":          string(intent.Symbol),
				"broker_order_id": "",
			})
		}
		s.emit(ctx, domain.EventOrderRejected, event.TenantID, event.EnvMode, intent.ID.String(), err.Error())
		return nil
	}
	if s.metrics != nil {
		side := "sell"
		if intent.Direction == domain.DirectionLong {
			side = "buy"
		}
		s.metrics.Orders.Total.WithLabelValues("alpaca", intent.Strategy, side, "limit", "placed").Inc()
		s.metrics.Orders.SubmitLat.WithLabelValues("alpaca", intent.Strategy, "limit").Observe(time.Since(submitStart).Seconds())
	}
	span.SetAttributes(attribute.String("order.broker_order_id", brokerOrderID))
	l.Info().Str("broker_order_id", brokerOrderID).Msg("order submitted to broker")
	submittedPayload := domain.NewOrderIntentEventPayload(intent, domain.OrderIntentStatusSubmitted)
	submittedPayload.BrokerOrderID = brokerOrderID
	s.emit(ctx, domain.EventOrderSubmitted, event.TenantID, event.EnvMode, intent.ID.String(), submittedPayload)

	// 6a. Mark inflight to prevent duplicate entries while awaiting fill.
	if s.positionGate != nil && isEntry(intent) {
		s.positionGate.MarkInflight(event.TenantID, event.EnvMode, intent.Symbol)
	}

	// 7. Persist the order record.
	side := "SELL"
	if intent.Direction == domain.DirectionLong {
		side = "BUY"
	}
	order := domain.BrokerOrder{
		Time:          time.Now().UTC(),
		TenantID:      event.TenantID,
		EnvMode:       event.EnvMode,
		IntentID:      intent.ID,
		BrokerOrderID: brokerOrderID,
		Symbol:        intent.Symbol,
		Side:          side,
		Quantity:      intent.Quantity,
		LimitPrice:    intent.LimitPrice,
		StopLoss:      intent.StopLoss,
		Status:        "submitted",
		Strategy:      intent.Strategy,
		Rationale:     intent.Rationale,
		Confidence:    intent.Confidence,
	}
	if isOptionOrder {
		order.InstrumentType = domain.InstrumentTypeOption
		order.OptionSymbol = intent.Instrument.Symbol.String()
		order.Underlying = string(intent.Instrument.UnderlyingSymbol)
		if r := intent.Meta["option_right"]; r != "" {
			order.OptionRight = r
		}
		if s, err := strconv.ParseFloat(intent.Meta["strike"], 64); err == nil {
			order.Strike = s
		}
		if exp, err := time.Parse("2006-01-02", intent.Meta["expiry"]); err == nil {
			order.Expiry = exp
		}
	}
	if saveErr := s.repo.SaveOrder(ctx, order); saveErr != nil {
		l.Error().Err(saveErr).Msg("failed to persist order — continuing to poll")
	}

	// 8. Register intent for fill correlation and start fill detection.
	po := &pendingOrder{
		intent:      intent,
		tenantID:    event.TenantID,
		envMode:     event.EnvMode,
		submitStart: submitStart,
	}
	s.pendingOrders.Store(brokerOrderID, po)

	if s.syncFill {
		s.pendingOrders.Delete(brokerOrderID)
		details, err := s.broker.GetOrderDetails(ctx, brokerOrderID)
		fillPrice := details.FilledAvgPrice
		if fillPrice <= 0 {
			fillPrice = intent.LimitPrice
		}
		fillQty := details.FilledQty
		if fillQty <= 0 {
			fillQty = intent.Quantity
		}
		filledAt := details.FilledAt
		if filledAt.IsZero() {
			filledAt = submitStart
		}
		if err != nil {
			fillPrice = intent.LimitPrice
			fillQty = intent.Quantity
			filledAt = submitStart
		}
		s.handleFillWithPrice(po, brokerOrderID, fillPrice, fillQty, filledAt, "", l)
	} else if s.orderStream == nil {
		go s.pollForFill(event.TenantID, event.EnvMode, intent, brokerOrderID, submitStart, l)
	}

	return nil
}

// pollForFill polls broker.GetOrderStatus until the order is filled, canceled,
// or the 2-minute timeout is reached. On fill it persists a Trade and emits FillReceived.
func (s *Service) pollForFill(tenantID string, envMode domain.EnvMode, intent domain.OrderIntent, brokerOrderID string, submitStart time.Time, l zerolog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	defer s.pendingOrders.Delete(brokerOrderID)

	if s.positionGate != nil && isEntry(intent) {
		defer s.positionGate.ClearInflight(tenantID, envMode, intent.Symbol)
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			l.Warn().Str("broker_order_id", brokerOrderID).Msg("fill poll timed out — order not filled within 2 minutes")
			s.emit(context.Background(), domain.EventFillPollTimeout, tenantID, envMode, brokerOrderID, domain.FillPollTimeoutPayload{
				Symbol:        intent.Symbol,
				BrokerOrderID: brokerOrderID,
				Strategy:      intent.Strategy,
				Direction:     string(intent.Direction),
				Quantity:      intent.Quantity,
			})
			return
		case <-ticker.C:
			status, err := s.broker.GetOrderStatus(ctx, brokerOrderID)
			if err != nil {
				l.Warn().Err(err).Str("broker_order_id", brokerOrderID).Msg("fill poll: error fetching order status")
				continue
			}

			l.Debug().Str("broker_order_id", brokerOrderID).Str("status", status).Msg("fill poll: order status")

			switch status {
			case "filled":
				s.handleFill(tenantID, envMode, intent, brokerOrderID, submitStart, l)
				return
			case "canceled", "expired", "rejected":
				l.Info().Str("broker_order_id", brokerOrderID).Str("status", status).Msg("fill poll: order terminal without fill")
				return
			}
			// "new", "accepted", "pending_new", "partially_filled" — keep polling
		}
	}
}

// handleFill records the fill in the DB and emits FillReceived.
func (s *Service) handleFill(tenantID string, envMode domain.EnvMode, intent domain.OrderIntent, brokerOrderID string, submitStart time.Time, l zerolog.Logger) {
	now := time.Now().UTC()
	ctx := context.Background()

	// Use limit price as fill price proxy (paper trading; actual fill price = limit price).
	fillPrice := intent.LimitPrice

	// Update order record.
	if err := s.repo.UpdateOrderFill(ctx, brokerOrderID, now, fillPrice, intent.Quantity); err != nil {
		l.Error().Err(err).Str("broker_order_id", brokerOrderID).Msg("failed to update order fill")
	}

	// Persist trade.
	side := "SELL"
	if intent.Direction == domain.DirectionLong {
		side = "BUY"
	}
	trade, err := domain.NewTrade(now, tenantID, envMode, uuid.New(), intent.Symbol, side, intent.Quantity, fillPrice, 0, "FILLED", intent.Strategy, intent.Rationale)
	if err != nil {
		l.Error().Err(err).Msg("failed to construct trade on fill")
	} else {
		if intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeOption {
			trade.InstrumentType = domain.InstrumentTypeOption
			trade.OptionSymbol = intent.Instrument.Symbol.String()
			trade.Underlying = string(intent.Instrument.UnderlyingSymbol)
			trade.OptionRight = intent.Meta["option_right"]
			if s, err := strconv.ParseFloat(intent.Meta["strike"], 64); err == nil {
				trade.Strike = s
			}
			if exp, err := time.Parse("2006-01-02", intent.Meta["expiry"]); err == nil {
				trade.Expiry = exp
			}
			if p, err := strconv.ParseFloat(intent.Meta["premium"], 64); err == nil {
				trade.Premium = p
			}
			if d, err := strconv.ParseFloat(intent.Meta["delta_at_entry"], 64); err == nil {
				trade.DeltaAtEntry = d
			}
			if iv, err := strconv.ParseFloat(intent.Meta["iv_at_entry"], 64); err == nil {
				trade.IVAtEntry = iv
			}
		}
		if err := s.repo.SaveTrade(ctx, trade); err != nil {
			l.Error().Err(err).Msg("failed to save trade on fill")
		}
	}

	fillPayload := map[string]any{
		"broker_order_id": brokerOrderID,
		"intent_id":       intent.ID.String(),
		"symbol":          string(intent.Symbol),
		"side":            side,
		"quantity":        intent.Quantity,
		"price":           fillPrice,
		"filled_at":       now,
		"strategy":        intent.Strategy,
		"risk_modifier":   intent.Meta["risk_modifier"],
	}
	if intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeOption {
		fillPayload["instrument_type"] = string(domain.InstrumentTypeOption)
		fillPayload["option_right"] = intent.Meta["option_right"]
		fillPayload["option_expiry"] = intent.Meta["expiry"]
	}
	s.emit(ctx, domain.EventFillReceived, tenantID, envMode, brokerOrderID, fillPayload)

	l.Info().
		Str("broker_order_id", brokerOrderID).
		Float64("fill_price", fillPrice).
		Float64("quantity", intent.Quantity).
		Msg("order filled — trade persisted and FillReceived emitted")

	// Record fill metrics.
	if s.metrics != nil {
		s.metrics.Orders.FillsTotal.WithLabelValues("alpaca", intent.Strategy, side, "filled").Inc()
		s.metrics.Orders.FillLat.WithLabelValues("alpaca", intent.Strategy).Observe(time.Since(submitStart).Seconds())
	}
}

func (s *Service) runFillListener(ctx context.Context, ch <-chan ports.OrderUpdate) {
	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-ch:
			if !ok {
				return
			}
			s.handleOrderUpdate(update)
		}
	}
}

func (s *Service) handleRiskDowngrade(ctx context.Context, event domain.Event) error {
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		return nil
	}
	symbol, _ := payload["symbol"].(string)
	if symbol == "" {
		return nil
	}

	var canceled int
	s.pendingOrders.Range(func(key, value any) bool {
		brokerOrderID := key.(string)
		po := value.(*pendingOrder)
		if string(po.intent.Symbol) != symbol {
			return true
		}
		if po.intent.Direction.IsExit() {
			return true
		}

		s.log.Warn().
			Str("symbol", symbol).
			Str("broker_order_id", brokerOrderID).
			Msg("canceling pending entry order due to risk downgrade")

		if err := s.broker.CancelOrder(ctx, brokerOrderID); err != nil {
			s.log.Warn().Err(err).
				Str("broker_order_id", brokerOrderID).
				Msg("failed to cancel pending order on risk downgrade — may already be terminal")
		} else {
			canceled++
		}
		return true
	})

	if canceled > 0 {
		s.log.Info().
			Str("symbol", symbol).
			Int("canceled", canceled).
			Msg("pending entry orders canceled due to risk downgrade")
	}

	return nil
}

func (s *Service) handleOrderUpdate(update ports.OrderUpdate) {
	l := s.log.With().
		Str("broker_order_id", update.BrokerOrderID).
		Str("event", update.Event).
		Logger()

	switch update.Event {
	case "fill", "partial_fill":
		s.handleStreamFill(update, l)
	case "canceled", "expired", "rejected":
		l.Info().Msg("order terminal via stream")
		s.cleanupPendingOrder(update.BrokerOrderID)
	}
}

const fastFillRetryDelay = 500 * time.Millisecond
const fastFillMaxRetries = 3

func (s *Service) handleStreamFill(update ports.OrderUpdate, l zerolog.Logger) {
	raw, ok := s.pendingOrders.Load(update.BrokerOrderID)
	if !ok {
		for i := 0; i < fastFillMaxRetries; i++ {
			time.Sleep(fastFillRetryDelay)
			raw, ok = s.pendingOrders.Load(update.BrokerOrderID)
			if ok {
				break
			}
		}
		if !ok {
			l.Warn().Msg("fill received for unknown order (not in pending map)")
			return
		}
	}

	po := raw.(*pendingOrder)

	// partial_fill: broker is working the order in multiple micro-executions.
	// Alpaca paper API often sends partial_fill with qty=0, making it impossible
	// to record accurate incremental trade rows. Skip recording — the terminal
	// "fill" event carries the definitive filled_qty and filled_avg_price.
	if update.Event == "partial_fill" {
		l.Debug().
			Float64("incremental_qty", update.Qty).
			Float64("cumulative_qty", update.FilledQty).
			Msg("partial fill received — deferring trade record to terminal fill event")
		return
	}

	s.pendingOrders.Delete(update.BrokerOrderID)

	fillPrice := update.Price
	if fillPrice <= 0 {
		fillPrice = update.FilledAvgPrice
	}
	if fillPrice <= 0 {
		fillPrice = po.intent.LimitPrice
	}

	fillQty := update.Qty
	if fillQty <= 0 {
		fillQty = update.FilledQty
	}
	if fillQty <= 0 {
		fillQty = po.intent.Quantity
	}

	s.handleFillWithPrice(po, update.BrokerOrderID, fillPrice, fillQty, update.FilledAt, update.ExecutionID, l)
}

func (s *Service) handleFillWithPrice(po *pendingOrder, brokerOrderID string, fillPrice, fillQty float64, filledAt time.Time, executionID string, l zerolog.Logger) {
	ctx := context.Background()

	if err := s.repo.UpdateOrderFill(ctx, brokerOrderID, filledAt, fillPrice, fillQty); err != nil {
		l.Error().Err(err).Msg("failed to update order fill")
	}

	side := "SELL"
	if po.intent.Direction == domain.DirectionLong {
		side = "BUY"
	}
	trade, err := domain.NewTrade(filledAt, po.tenantID, po.envMode, uuid.New(), po.intent.Symbol, side, fillQty, fillPrice, 0, "FILLED", po.intent.Strategy, po.intent.Rationale)
	if err != nil {
		l.Error().Err(err).Msg("failed to construct trade on fill")
	} else {
		trade.ExecutionID = executionID
		if po.intent.Instrument != nil && po.intent.Instrument.Type == domain.InstrumentTypeOption {
			trade.InstrumentType = domain.InstrumentTypeOption
			trade.OptionSymbol = po.intent.Instrument.Symbol.String()
			trade.Underlying = string(po.intent.Instrument.UnderlyingSymbol)
			trade.OptionRight = po.intent.Meta["option_right"]
			if s, err := strconv.ParseFloat(po.intent.Meta["strike"], 64); err == nil {
				trade.Strike = s
			}
			if exp, err := time.Parse("2006-01-02", po.intent.Meta["expiry"]); err == nil {
				trade.Expiry = exp
			}
			if p, err := strconv.ParseFloat(po.intent.Meta["premium"], 64); err == nil {
				trade.Premium = p
			}
			if d, err := strconv.ParseFloat(po.intent.Meta["delta_at_entry"], 64); err == nil {
				trade.DeltaAtEntry = d
			}
			if iv, err := strconv.ParseFloat(po.intent.Meta["iv_at_entry"], 64); err == nil {
				trade.IVAtEntry = iv
			}
		}
		if err := s.repo.SaveTrade(ctx, trade); err != nil {
			l.Error().Err(err).Msg("failed to save trade on fill")
		}
	}

	fillPayload := map[string]any{
		"broker_order_id": brokerOrderID,
		"intent_id":       po.intent.ID.String(),
		"symbol":          string(po.intent.Symbol),
		"side":            side,
		"quantity":        fillQty,
		"price":           fillPrice,
		"filled_at":       filledAt,
		"strategy":        po.intent.Strategy,
		"asset_class":     string(po.intent.AssetClass),
	}
	if po.intent.Instrument != nil && po.intent.Instrument.Type == domain.InstrumentTypeOption {
		fillPayload["instrument_type"] = string(domain.InstrumentTypeOption)
		fillPayload["option_right"] = po.intent.Meta["option_right"]
		fillPayload["option_expiry"] = po.intent.Meta["expiry"]
	}
	s.emit(ctx, domain.EventFillReceived, po.tenantID, po.envMode, brokerOrderID, fillPayload)

	l.Info().
		Str("broker_order_id", brokerOrderID).
		Float64("fill_price", fillPrice).
		Float64("quantity", fillQty).
		Msg("order filled — trade persisted and FillReceived emitted")

	if s.metrics != nil {
		s.metrics.Orders.FillsTotal.WithLabelValues("alpaca", po.intent.Strategy, side, "filled").Inc()
		s.metrics.Orders.FillLat.WithLabelValues("alpaca", po.intent.Strategy).Observe(time.Since(po.submitStart).Seconds())
	}

	if s.positionGate != nil {
		if isEntry(po.intent) {
			s.positionGate.ClearInflight(po.tenantID, po.envMode, po.intent.Symbol)
		} else if po.intent.Direction.IsExit() {
			s.positionGate.ResetExitFailures(po.tenantID, po.envMode, po.intent.Symbol)
		}
		// Exit fills do NOT clear the inflight exit gate. For IOC orders, WS delivers
		// partial_fill then canceled. Clearing on partial_fill lets the position monitor
		// fire another exit before the cancel arrives — creating an infinite dust loop.
		// Only cleanupPendingOrder (terminal event) clears the exit gate.
	}
}

func (s *Service) cleanupPendingOrder(brokerOrderID string) {
	raw, ok := s.pendingOrders.LoadAndDelete(brokerOrderID)
	if !ok {
		return
	}
	po := raw.(*pendingOrder)
	if s.positionGate != nil {
		if isEntry(po.intent) {
			s.positionGate.ClearInflight(po.tenantID, po.envMode, po.intent.Symbol)
		} else if po.intent.Direction.IsExit() {
			isFullExit := po.intent.Direction == domain.DirectionCloseLong &&
				!strings.Contains(po.intent.Rationale, "SCALE_OUT")

			if isFullExit {
				go s.sweepDustPosition(po.tenantID, po.envMode, po.intent.Symbol, brokerOrderID)
			} else {
				s.positionGate.ClearInflightExit(po.tenantID, po.envMode, po.intent.Symbol)
			}

			if tripped := s.positionGate.RecordExitFailure(po.tenantID, po.envMode, po.intent.Symbol); tripped {
				s.emit(context.Background(), domain.EventExitCircuitBroken, po.tenantID, po.envMode, brokerOrderID, domain.ExitCircuitBrokenPayload{
					Symbol:       po.intent.Symbol,
					Failures:     maxExitFailures,
					CooldownSecs: exitCooldownDuration.Seconds(),
				})
			}

			s.emit(context.Background(), domain.EventExitOrderTerminal, po.tenantID, po.envMode, brokerOrderID, map[string]any{
				"symbol":          string(po.intent.Symbol),
				"broker_order_id": brokerOrderID,
			})
		}
	}
}

func (s *Service) sweepDustPosition(tenantID string, envMode domain.EnvMode, symbol domain.Symbol, brokerOrderID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sweepFilled := false
	defer func() {
		if s.positionGate != nil {
			s.positionGate.ClearInflightExit(tenantID, envMode, symbol)
			if sweepFilled {
				s.positionGate.ResetExitFailures(tenantID, envMode, symbol)
			}
		}
	}()

	l := s.log.With().
		Str("symbol", string(symbol)).
		Str("broker_order_id", brokerOrderID).
		Str("component", "dust_sweep").
		Logger()

	remainingQty, err := s.broker.GetPosition(ctx, symbol)
	if err != nil {
		l.Warn().Err(err).Msg("dust sweep: failed to query broker — clearing gate for retry")
		return
	}

	if remainingQty <= 0 {
		l.Info().Msg("dust sweep: broker confirms fully closed")
		sweepFilled = true
		return
	}

	l.Info().Float64("remaining_qty", remainingQty).
		Msg("dust sweep: remainder detected — sending DELETE to sweep")

	sweepOrderID, err := s.broker.ClosePosition(ctx, symbol)
	if err != nil {
		l.Error().Err(err).Msg("dust sweep: DELETE failed — clearing gate for retry")
		return
	}

	if sweepOrderID == "" {
		l.Info().Msg("dust sweep: position already closed (404/422)")
		return
	}

	l.Info().Str("sweep_order_id", sweepOrderID).Msg("dust sweep: DELETE accepted — polling for fill confirmation")

	// Poll broker for fill instead of relying on WS (the sweep creates a new order ID
	// that isn't in pendingOrders, so WS fills would be silently dropped).
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			l.Warn().Str("sweep_order_id", sweepOrderID).Msg("dust sweep: timed out waiting for fill — will be caught by reconciliation")
			return
		case <-ticker.C:
			details, err := s.broker.GetOrderDetails(ctx, sweepOrderID)
			if err != nil {
				l.Warn().Err(err).Str("sweep_order_id", sweepOrderID).Msg("dust sweep: failed to get order details — retrying")
				continue
			}

			switch details.Status {
			case "filled":
				l.Info().
					Str("sweep_order_id", sweepOrderID).
					Float64("filled_qty", details.FilledQty).
					Float64("filled_avg_price", details.FilledAvgPrice).
					Msg("dust sweep: fill confirmed via REST — recording trade")

				fillTime := details.FilledAt
				if fillTime.IsZero() {
					fillTime = time.Now().UTC()
				}
				trade, tErr := domain.NewTrade(fillTime, tenantID, envMode, uuid.New(), symbol, "SELL", details.FilledQty, details.FilledAvgPrice, 0, "FILLED", "dust_sweep", fmt.Sprintf("sweep remainder after exit %s", brokerOrderID))
				if tErr != nil {
					l.Error().Err(tErr).Msg("dust sweep: failed to construct trade")
					return
				}
				if sErr := s.repo.SaveTrade(ctx, trade); sErr != nil {
					l.Error().Err(sErr).Msg("dust sweep: failed to save trade")
					return
				}

				s.emit(ctx, domain.EventFillReceived, tenantID, envMode, sweepOrderID, map[string]any{
					"broker_order_id": sweepOrderID,
					"symbol":          string(symbol),
					"side":            "SELL",
					"quantity":        details.FilledQty,
					"price":           details.FilledAvgPrice,
					"filled_at":       fillTime,
					"strategy":        "dust_sweep",
				})
				sweepFilled = true
				return

			case "canceled", "expired", "rejected":
				l.Warn().Str("sweep_order_id", sweepOrderID).Str("status", details.Status).Msg("dust sweep: order terminal without fill")
				return
			}
			// "new", "accepted", "pending_new", "partially_filled" — keep polling
		}
	}
}

const reconcileInterval = 60 * time.Second
const dbReconcileInterval = 5 * time.Minute

func (s *Service) runReconciliationLoop(ctx context.Context) {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()

	dbTicker := time.NewTicker(dbReconcileInterval)
	defer dbTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcilePendingOrders(ctx)
		case <-dbTicker.C:
			s.reconcileOnBoot(ctx)
		}
	}
}

func (s *Service) reconcilePendingOrders(ctx context.Context) {
	s.pendingOrders.Range(func(key, value any) bool {
		brokerOrderID := key.(string)
		po := value.(*pendingOrder)

		if time.Since(po.submitStart) < 10*time.Second {
			return true
		}

		details, err := s.broker.GetOrderDetails(ctx, brokerOrderID)
		if err != nil {
			s.log.Warn().Err(err).Str("broker_order_id", brokerOrderID).Msg("reconcile: order details check failed")
			return true
		}

		l := s.log.With().Str("broker_order_id", brokerOrderID).Str("status", details.Status).Logger()

		switch details.Status {
		case "filled":
			l.Info().Msg("reconcile: detected fill via REST")
			s.recordFillFromDetails(po, brokerOrderID, details, l)
			s.cleanupPendingOrder(brokerOrderID)

		case "canceled", "expired", "rejected":
			if details.FilledQty > 0 {
				l.Info().Float64("filled_qty", details.FilledQty).Msg("reconcile: terminal order has partial fills — recording")
				s.recordFillFromDetails(po, brokerOrderID, details, l)
			}
			l.Info().Msg("reconcile: order terminal via REST")
			s.cleanupPendingOrder(brokerOrderID)
		}

		if time.Since(po.submitStart) > 2*time.Minute {
			if details.Status == "filled" || details.Status == "canceled" || details.Status == "expired" || details.Status == "rejected" {
				return true
			}
			if err := s.broker.CancelOrder(ctx, brokerOrderID); err != nil {
				l.Warn().Err(err).Msg("reconcile: failed to cancel stale order — may already be terminal")
			} else {
				l.Info().Msg("reconcile: canceled stale order on broker")
			}

			postCancel, err := s.broker.GetOrderDetails(ctx, brokerOrderID)
			if err == nil && postCancel.FilledQty > 0 {
				l.Info().Float64("filled_qty", postCancel.FilledQty).Msg("reconcile: stale order had fills before cancel — recording")
				s.recordFillFromDetails(po, brokerOrderID, postCancel, l)
			}

			l.Warn().Msg("reconcile: pending order expired")
			s.emit(ctx, domain.EventStaleOrderCancelled, po.tenantID, po.envMode, brokerOrderID, domain.StaleOrderCancelledPayload{
				Symbol:        po.intent.Symbol,
				BrokerOrderID: brokerOrderID,
				Strategy:      po.intent.Strategy,
				Direction:     string(po.intent.Direction),
				AgeSeconds:    time.Since(po.submitStart).Seconds(),
			})
			s.cleanupPendingOrder(brokerOrderID)
		}

		return true
	})
}

// recordFillFromDetails records a fill using actual broker data instead of intent data.
// Uses GetOrderDetails response for accurate fill price and quantity.
func (s *Service) recordFillFromDetails(po *pendingOrder, brokerOrderID string, details ports.OrderDetails, l zerolog.Logger) {
	fillPrice := details.FilledAvgPrice
	if fillPrice <= 0 {
		fillPrice = po.intent.LimitPrice
	}
	fillQty := details.FilledQty
	if fillQty <= 0 {
		fillQty = po.intent.Quantity
	}
	filledAt := details.FilledAt
	if filledAt.IsZero() {
		filledAt = time.Now().UTC()
	}
	s.handleFillWithPrice(po, brokerOrderID, fillPrice, fillQty, filledAt, "", l)
}

// exitLimitBuffer returns the IOC limit price buffer (as a fraction) for exit orders.
// Wide-spread crypto assets get a larger buffer to avoid instant cancellation.
func exitLimitBuffer(sym domain.Symbol, ac domain.AssetClass) float64 {
	if ac != domain.AssetClassCrypto {
		return 0.001 // 10bps for equities
	}
	// Illiquid altcoins need wider buffers; their spreads often exceed 50bps.
	s := sym.String()
	switch {
	case strings.Contains(s, "DOGE"),
		strings.Contains(s, "PEPE"),
		strings.Contains(s, "AVAX"),
		strings.Contains(s, "SHIB"):
		return 0.01 // 100bps for illiquid altcoins
	default:
		return 0.005 // 50bps for liquid crypto (BTC, ETH, SOL)
	}
}

func (s *Service) emit(ctx context.Context, eventType string, tenantID string, envMode domain.EnvMode, idempotencyKey string, payload any) {
	ev, err := domain.NewEvent(eventType, tenantID, envMode, idempotencyKey, payload)
	if err != nil {
		return
	}
	_ = s.eventBus.Publish(ctx, *ev)
}
