package execution

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/gate"
	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/oh-my-opentrade/backend/internal/observability/parity"
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
	// suppressTerminalActions, when true, tells cleanupPendingOrder to skip
	// BOTH positionGate.RecordExitFailure AND the sweepDustPosition launch
	// for this order. Set by MarkRepegCancel so that re-peg cycles — which
	// by design cancel-and-resubmit up to N times per exit attempt — don't
	// (a) amplify the exit-failure counter 4x and trip the circuit breaker,
	// nor (b) fire a dust sweep against a broker order whose "remainder"
	// is actually a concurrent fill the re-peg cancel raced. The SOFI
	// phantom-short on 2026-04-16 originated in case (b): the re-peg
	// cancel of order 1604 triggered sweepDustPosition even though 1603
	// had just filled broker-side and flattened the position.
	suppressTerminalActions bool
}

// PositionStateLookup is a narrow interface over the position monitor that
// lets execution read live MFE/MAE off a position at fill time. Strategy-
// emitted exits bypass the monitor's exit_eval.go fill-metadata attachment
// path, so without this lookup the backtest collector never sees MFE/MAE
// for strategy-driven exits. Nil-safe: when unset, execution falls back to
// intent.Meta (legacy behavior).
type PositionStateLookup interface {
	LookupPosition(symbol string) (domain.MonitoredPosition, bool)
}

type Service struct {
	eventBus                     ports.EventBusPort
	broker                       ports.BrokerPort
	orderStream                  ports.OrderStreamPort
	repo                         ports.RepositoryPort
	intentJournal                ports.OrderIntentJournal // Sprint 2 write-ahead journal; nil when OMO_ORDER_JOURNAL_ENABLED is false
	riskEngine                   *RiskEngine
	slippageGuard                *SlippageGuard
	spreadGuard                  *SpreadGuard
	tradingWindowGuard           *TradingWindowGuard
	killSwitch                   *KillSwitch
	dailyLossBreaker             *risk.DailyLossBreaker
	positionGate                 *PositionGate
	exposureGuard                *ExposureGuard
	portfolioGuard               *PortfolioGuard
	buyingPowerGuard             *BuyingPowerGuard
	optionsRiskEngine            *OptionsRiskEngine
	executionGateChain           *gate.ExecutionGateChain
	positionLookup               PositionStateLookup
	optionsPricePort             ports.OptionsPricePort // optional — enables marketable-limit dust sweeps on options
	dustSweepLimitWindowOverride time.Duration          // nonzero to override dustSweepLimitWindow in tests
	accountEquity                float64
	log                          zerolog.Logger
	metrics                      *metrics.Metrics
	pendingOrders                sync.Map // brokerOrderID → *pendingOrder
	tenantID                     string
	envMode                      domain.EnvMode
	syncFill                     bool
	brokerName                   string
	nowFn                        func() time.Time
}

// Option is a functional option for Service.
type Option func(*Service)

// WithNowFunc sets a custom time function (for deterministic backtests).
func WithNowFunc(fn func() time.Time) Option {
	return func(s *Service) { s.nowFn = fn }
}

// WithPositionGate attaches a PositionGate to the execution pipeline.
func WithBrokerName(name string) Option {
	return func(s *Service) { s.brokerName = name }
}

func WithPositionGate(pg *PositionGate) Option {
	return func(s *Service) { s.positionGate = pg }
}

func WithExposureGuard(eg *ExposureGuard) Option {
	return func(s *Service) { s.exposureGuard = eg }
}

func WithPortfolioGuard(pg *PortfolioGuard) Option {
	return func(s *Service) { s.portfolioGuard = pg }
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

// WithIntentJournal enables the Sprint 2 write-ahead journal. When set,
// handleIntent persists each intent before broker.SubmitOrder and
// handleOrderUpdate stamps terminal events back onto the row. When nil
// (the default), execution behavior is byte-identical to pre-Sprint-2.
func WithIntentJournal(j ports.OrderIntentJournal) Option {
	return func(s *Service) { s.intentJournal = j }
}

// WithOptionsPricePort wires a live option quote provider. When set, the
// dust sweep can submit a marketable-limit order on options before falling
// back to a true market order, which avoids getting filled at the bid on
// wide-spread contracts. Nil-safe: when unset, the sweep keeps its legacy
// pure-market behavior.
func WithOptionsPricePort(p ports.OptionsPricePort) Option {
	return func(s *Service) { s.optionsPricePort = p }
}

// WithPositionLookup wires a live position-state reader so handleFill can
// attach MFE/MAE to strategy-emitted exit fills. Without this, MFE/MAE is
// only present for exits routed through the position monitor's own exit_eval
// path, which skips strategies that set strategy_exits_priority=true.
func WithPositionLookup(p PositionStateLookup) Option {
	return func(s *Service) { s.positionLookup = p }
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
	if s.nowFn == nil {
		s.nowFn = time.Now
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

// SetPositionLookup installs the position-state reader after construction.
// Needed because the position monitor is built after the execution service
// (it depends on execBundle.PositionGate), so we cannot wire this through
// the functional option at construction time in that flow.
func (s *Service) SetPositionLookup(p PositionStateLookup) { s.positionLookup = p }

// SetExecutionGateChain installs a configurable execution gate chain.
// When set, the chain replaces the inline guard if-blocks for entry intents.
func (s *Service) SetExecutionGateChain(chain *gate.ExecutionGateChain) { s.executionGateChain = chain }

func (s *Service) Start(ctx context.Context, tenantID string, envMode domain.EnvMode) error {
	s.tenantID = tenantID
	s.envMode = envMode

	if err := s.eventBus.SubscribeAsync(ctx, domain.EventOrderIntentCreated, s.handleIntent); err != nil {
		return fmt.Errorf("execution: failed to subscribe to OrderIntentCreated: %w", err)
	}
	if err := s.eventBus.SubscribeAsync(ctx, domain.EventRiskDowngraded, s.handleRiskDowngrade); err != nil {
		return fmt.Errorf("execution: failed to subscribe to RiskDowngraded: %w", err)
	}
	if err := s.eventBus.SubscribeAsync(ctx, domain.EventCopytradeEntryExpired, s.handleCopytradeEntryExpired); err != nil {
		return fmt.Errorf("execution: failed to subscribe to CopytradeEntryExpired: %w", err)
	}
	s.log.Info().Msg("subscribed to OrderIntentCreated, RiskDowngraded, CopytradeEntryExpired events")

	s.reconcileOnBoot(ctx)

	if s.orderStream != nil {
		ch, err := s.orderStream.SubscribeOrderUpdates(ctx)
		if err != nil {
			if errors.Is(err, ports.ErrBrokerNotAvailable) {
				s.log.Warn().Msg("order stream not available — retrying in background until broker connects")
				go s.retryFillListener(ctx)
			} else {
				return fmt.Errorf("execution: failed to subscribe to order stream: %w", err)
			}
		} else {
			go s.runFillListener(ctx, ch)
			go s.runReconciliationLoop(ctx)
			s.log.Info().Msg("WebSocket fill listener and reconciliation loop started")
		}
	}

	return nil
}

func (s *Service) retryFillListener(ctx context.Context) {
	delay := 5 * time.Second
	maxDelay := 30 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		ch, err := s.orderStream.SubscribeOrderUpdates(ctx)
		if err != nil {
			if errors.Is(err, ports.ErrBrokerNotAvailable) {
				delay *= 2
				if delay > maxDelay {
					delay = maxDelay
				}
				continue
			}
			s.log.Error().Err(err).Msg("fill listener retry failed with unexpected error")
			return
		}
		go s.runFillListener(ctx, ch)
		go s.runReconciliationLoop(ctx)
		s.log.Info().Msg("fill listener connected after deferred retry")
		s.reconcileOnBoot(ctx)
		s.log.Info().Msg("deferred boot reconciliation completed")
		return
	}
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
			if errors.Is(err, ports.ErrOrderNotFound) {
				// Order not in broker's trade list. Check if broker holds a position
				// for this symbol — if so, the order was filled and then dropped.
				posQty, posErr := s.broker.GetPosition(ctx, order.Symbol)
				if posErr == nil && posQty > 0 && strings.EqualFold(order.Side, "BUY") {
					// Position exists → treat as filled, not canceled.
					ol.Info().Float64("broker_qty", posQty).Msg("reconcile: order not found but position exists — inferring fill")
					details = ports.OrderDetails{
						Status:         "filled",
						FilledQty:      order.Quantity,
						FilledAvgPrice: order.LimitPrice,
					}
					// Fall through to normal fill processing below
				} else {
					if updErr := s.repo.UpdateOrderStatus(ctx, order.BrokerOrderID, "canceled"); updErr != nil {
						ol.Error().Err(updErr).Msg("reconcile: failed to mark vanished order canceled")
					} else {
						ol.Info().Msg("reconcile: order not found at broker — marked canceled")
						updated++
						s.emit(ctx, domain.EventOrderSubmitted, s.tenantID, s.envMode, order.IntentID.String(),
							domain.NewOrderIntentEventPayload(domain.OrderIntent{
								ID: order.IntentID, Symbol: order.Symbol, Direction: domain.Direction(order.Side),
								Quantity: order.Quantity, Strategy: order.Strategy,
							}, domain.OrderIntentStatus("canceled")))
					}
					continue
				}
			} else {
				ol.Warn().Err(err).Msg("reconcile: failed to get order details — skipping")
				continue
			}
		}

		isTerminal := details.Status == "canceled" || details.Status == "expired" || details.Status == "rejected" || details.Status == "filled"
		if isTerminal {
			if err := s.repo.UpdateOrderStatus(ctx, order.BrokerOrderID, details.Status); err != nil {
				ol.Error().Err(err).Msg("reconcile: failed to update terminal status")
			} else {
				ol.Info().Str("status", details.Status).Msg("reconcile: marked order terminal")
				updated++
				// Notify dashboard via SSE so it updates without a page refresh.
				s.emit(ctx, domain.EventOrderSubmitted, s.tenantID, s.envMode, order.IntentID.String(),
					domain.NewOrderIntentEventPayload(domain.OrderIntent{
						ID:        order.IntentID,
						Symbol:    order.Symbol,
						Direction: domain.Direction(order.Side),
						Quantity:  order.Quantity,
						Strategy:  order.Strategy,
					}, domain.OrderIntentStatus(details.Status)))
			}
			if details.FilledQty <= 0 {
				if details.Status == "filled" {
					// ibsync doesn't populate Filled/AvgFillPrice on reconnect,
					// so both details.FilledQty and details.Qty can be 0.
					// Fall back to the DB order quantity (for a fully filled order
					// these are identical) and the limit price as best estimate.
					details.FilledQty = order.Quantity
					if details.FilledAvgPrice <= 0 {
						details.FilledAvgPrice = order.LimitPrice
					}
					ol.Info().Float64("inferred_qty", details.FilledQty).Float64("inferred_price", details.FilledAvgPrice).Msg("reconcile: filled order has no fill data — inferring from order qty")
				} else {
					continue
				}
			}
			ol.Info().Float64("filled_qty", details.FilledQty).Msg("reconcile: terminal order has fills — checking for missed trades")
		}

		if details.FilledQty <= 0 {
			ol.Info().Str("broker_status", details.Status).Msg("reconcile: order still open at broker — no fills yet")
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
			// Stable fallback: prefer the orders row's filled_at (set by an
			// earlier reconcile pass), then the order placement time. Using
			// nowFn() here would make (trade_id, time) drift between passes
			// for the same broker_order_id+filled_qty, defeating the PK
			// dedup if the delta-vs-dbFilledQty check ever returns a stale
			// answer (async write batching, float jitter, etc).
			if order.FilledAt != nil && !order.FilledAt.IsZero() {
				fillTime = *order.FilledAt
			} else {
				fillTime = order.Time
			}
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
		trade.BrokerOrderID = order.BrokerOrderID

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

	s.backfillFromBrokerHistory(ctx)
	s.reconcileFillsOnBoot(ctx)
}

// reconcileFillsOnBoot diffs per-execution fills from the broker against
// trades.execution_id and inserts any that the stream missed (crash, WS
// gap). Independent of reconcileOnBoot (orders table walk) and
// backfillFromBrokerHistory (per-order seed) — this one operates at the
// fill-leg granularity and is idempotent on re-run: execution_id UNIQUE
// skips dupes.
//
// Only runs when the broker implements ports.FillLister (IBKR). Simbroker
// and alpaca stub skip this path; simbroker has no gap bug and alpaca's
// trade stream already delivers per-exec events.
func (s *Service) reconcileFillsOnBoot(ctx context.Context) {
	l := s.log.With().Str("component", "reconcile_fills_on_boot").Logger()

	lister, ok := s.broker.(ports.FillLister)
	if !ok {
		return
	}

	fills, err := lister.GetAllFills(ctx)
	if err != nil {
		l.Warn().Err(err).Msg("fill reconcile: GetAllFills failed — skipping")
		return
	}
	if len(fills) == 0 {
		return
	}

	// IBKR's ReqFills returns the current session (today). Query the same
	// window from the DB so we're comparing apples to apples. Use UTC day
	// start with a 24h slack to handle sessions that straddle midnight.
	since := s.nowFn().UTC().Add(-24 * time.Hour)
	recorded, err := s.repo.GetRecordedExecutionIDs(ctx, s.tenantID, s.envMode, since)
	if err != nil {
		l.Warn().Err(err).Msg("fill reconcile: GetRecordedExecutionIDs failed — skipping")
		return
	}
	// Second dedup gate: orders already represented in trades regardless of
	// execution_id. Live IBKR option fills land via fastPollPosition which
	// writes with executionID="", so the exec_id set above never sees them
	// and reconcile would otherwise insert a duplicate Path A row per leg
	// at every boot. fastPoll only triggers when the position has reached
	// the full intent quantity, so a recorded broker_order_id implies the
	// full order is covered.
	reconciledOrders, err := s.repo.GetReconciledOrderIDs(ctx, s.tenantID, s.envMode, since)
	if err != nil {
		l.Warn().Err(err).Msg("fill reconcile: GetReconciledOrderIDs failed — skipping")
		return
	}

	inserted := 0
	skippedByOrder := 0
	for _, f := range fills {
		if f.ExecutionID == "" {
			continue
		}
		if _, seen := recorded[f.ExecutionID]; seen {
			continue
		}
		if f.BrokerOrderID != "" {
			if _, seen := reconciledOrders[f.BrokerOrderID]; seen {
				skippedByOrder++
				continue
			}
		}

		ol := l.With().
			Str("broker_order_id", f.BrokerOrderID).
			Str("execution_id", f.ExecutionID).
			Str("symbol", f.Symbol).
			Logger()

		// Look up the existing orders row for this broker_order_id to
		// recover tenant/env/strategy/etc. If the orders row is missing
		// too, backfillFromBrokerHistory ran just before us and should
		// have seeded it — but on a race we just skip and let the next
		// boot heal.
		existing, err := s.repo.GetOrderByBrokerOrderID(ctx, f.BrokerOrderID)
		if err != nil {
			ol.Warn().Err(err).Msg("fill reconcile: order lookup failed — skipping")
			continue
		}
		if existing == nil {
			ol.Debug().Msg("fill reconcile: orders row not found — deferring to next boot")
			continue
		}

		filledAt := f.FilledAt
		if filledAt.IsZero() {
			filledAt = s.nowFn().UTC()
		}
		cumQty := f.CumQty
		if cumQty <= 0 {
			cumQty = f.Qty
		}
		cumAvgPrice := f.AvgPrice
		if cumAvgPrice <= 0 {
			cumAvgPrice = f.Price
		}

		trade, tErr := domain.NewTrade(
			filledAt, s.tenantID, s.envMode, uuid.New(),
			domain.Symbol(f.Symbol), f.Side, f.Qty, f.Price, 0,
			"FILLED", existing.Strategy,
			fmt.Sprintf("reconcile_fills_on_boot: recovered leg %s for order %s", f.ExecutionID, f.BrokerOrderID),
		)
		if tErr != nil {
			ol.Error().Err(tErr).Msg("fill reconcile: NewTrade failed")
			continue
		}
		trade.ExecutionID = f.ExecutionID
		trade.BrokerOrderID = f.BrokerOrderID
		if existing.InstrumentType == domain.InstrumentTypeOption {
			trade.InstrumentType = domain.InstrumentTypeOption
			trade.OptionSymbol = existing.OptionSymbol
			trade.Underlying = existing.Underlying
			trade.OptionRight = existing.OptionRight
			trade.Strike = existing.Strike
			trade.Expiry = existing.Expiry
			trade.Premium = f.Price
		}

		if rErr := s.repo.RecordFill(ctx, f.BrokerOrderID, filledAt, cumAvgPrice, cumQty, trade); rErr != nil {
			ol.Error().Err(rErr).Msg("fill reconcile: RecordFill failed")
			continue
		}
		inserted++
		ol.Info().
			Float64("leg_qty", f.Qty).
			Float64("leg_price", f.Price).
			Float64("cum_qty", cumQty).
			Msg("fill reconcile: inserted missed leg")
	}

	if inserted > 0 || skippedByOrder > 0 {
		l.Info().
			Int("broker_fills", len(fills)).
			Int("already_recorded", len(recorded)).
			Int("skipped_by_order", skippedByOrder).
			Int("inserted", inserted).
			Msg("fill reconciliation complete")
	}
}

// backfillFromBrokerHistory restores orders the DB never recorded — the
// crash-before-SaveOrder gap reconcileOnBoot cannot see, since that loop
// iterates the orders table only.
//
// Order matters: row is seeded as "submitted" first, then SaveTrade, then
// UpdateOrderFill flips to "filled". If SaveTrade fails we leave a non-terminal
// row, so the next reconcileOnBoot tick heals it via the existing GetOrderDetails
// path rather than orphaning a half-complete row with no trade attached.
//
// Idempotent: deterministicTradeID + "backfill:"-prefixed execution_id mean
// re-runs hit the orders ON CONFLICT (UPDATE) path and the trades unique
// (execution_id, time) index. Skips zero price / zero FilledAt: the first would
// poison PnL, the second would break idempotency across runs because the trades
// unique key includes time.
func (s *Service) backfillFromBrokerHistory(ctx context.Context) {
	l := s.log.With().Str("component", "backfill_from_broker").Logger()

	lister, ok := s.broker.(ports.FilledOrderLister)
	if !ok {
		return
	}

	filled, err := lister.GetFilledOrders(ctx)
	if err != nil {
		l.Warn().Err(err).Msg("broker history backfill: GetFilledOrders failed — skipping")
		return
	}
	if len(filled) == 0 {
		return
	}

	backfilled := 0
	for _, fo := range filled {
		ol := l.With().
			Str("broker_order_id", fo.BrokerOrderID).
			Str("symbol", fo.Symbol).
			Str("side", fo.Side).
			Logger()

		if fo.FilledAvgPrice <= 0 || fo.FilledAt.IsZero() {
			ol.Warn().
				Float64("filled_avg_price", fo.FilledAvgPrice).
				Time("filled_at", fo.FilledAt).
				Msg("broker history backfill: skipping order with missing price or fill time")
			continue
		}

		existing, err := s.repo.GetOrderByBrokerOrderID(ctx, fo.BrokerOrderID)
		if err != nil {
			ol.Warn().Err(err).Msg("broker history backfill: DB lookup failed — skipping")
			continue
		}
		if existing != nil {
			continue
		}

		sym := domain.Symbol(fo.Symbol)
		side := strings.ToUpper(fo.Side)
		order := domain.BrokerOrder{
			Time:           fo.FilledAt,
			TenantID:       s.tenantID,
			EnvMode:        s.envMode,
			IntentID:       uuid.New(),
			BrokerOrderID:  fo.BrokerOrderID,
			Symbol:         sym,
			Side:           side,
			Quantity:       fo.Quantity,
			LimitPrice:     fo.FilledAvgPrice,
			Status:         "submitted",
			Strategy:       "backfill",
			Rationale:      "broker history backfill: orders row missing at boot",
			InstrumentType: domain.InstrumentTypeEquity,
		}
		if domain.IsOCCSymbol(sym) {
			if underlying, expiry, right, strike, ok := domain.ParseOCC(sym); ok {
				order.InstrumentType = domain.InstrumentTypeOption
				order.OptionSymbol = string(sym)
				order.Underlying = underlying
				order.Strike = strike
				order.Expiry = expiry
				order.OptionRight = right
			}
		}

		if err := s.repo.SaveOrder(ctx, order); err != nil {
			ol.Error().Err(err).Msg("broker history backfill: SaveOrder failed — skipping trade write")
			continue
		}

		tradeID := deterministicTradeID(fo.BrokerOrderID, fo.FilledQty)
		trade, tErr := domain.NewTrade(
			fo.FilledAt, s.tenantID, s.envMode, tradeID,
			sym, side, fo.FilledQty, fo.FilledAvgPrice, 0,
			"FILLED", order.Strategy,
			fmt.Sprintf("broker history backfill: recovered %.8f @ %.6f from broker for order %s", fo.FilledQty, fo.FilledAvgPrice, fo.BrokerOrderID),
		)
		if tErr != nil {
			ol.Error().Err(tErr).Msg("broker history backfill: NewTrade failed — submitted row left for next reconcile")
			continue
		}
		trade.ExecutionID = "backfill:" + fo.BrokerOrderID
		trade.BrokerOrderID = fo.BrokerOrderID
		if order.InstrumentType == domain.InstrumentTypeOption {
			trade.InstrumentType = domain.InstrumentTypeOption
			trade.OptionSymbol = order.OptionSymbol
			trade.Underlying = order.Underlying
			trade.OptionRight = order.OptionRight
			trade.Strike = order.Strike
			trade.Expiry = order.Expiry
			trade.Premium = fo.FilledAvgPrice
		}
		if err := s.repo.SaveTrade(ctx, trade); err != nil {
			ol.Error().Err(err).Msg("broker history backfill: SaveTrade failed")
			continue
		}

		if err := s.repo.UpdateOrderFill(ctx, fo.BrokerOrderID, fo.FilledAt, fo.FilledAvgPrice, fo.FilledQty); err != nil {
			ol.Error().Err(err).Msg("broker history backfill: UpdateOrderFill failed")
			continue
		}

		s.emit(ctx, domain.EventFillReceived, s.tenantID, s.envMode, fo.BrokerOrderID, map[string]any{
			"broker_order_id": fo.BrokerOrderID,
			"symbol":          string(sym),
			"side":            side,
			"quantity":        fo.FilledQty,
			"price":           fo.FilledAvgPrice,
			"filled_at":       fo.FilledAt,
			"strategy":        order.Strategy,
			"synthetic":       true,
			"source":          "broker_history_backfill",
		})

		backfilled++
		ol.Info().
			Float64("qty", fo.FilledQty).
			Float64("price", fo.FilledAvgPrice).
			Time("filled_at", fo.FilledAt).
			Msg("broker history backfill: synthetic orders+trade row inserted")
	}

	if backfilled > 0 {
		l.Info().
			Int("candidates", len(filled)).
			Int("backfilled", backfilled).
			Msg("broker history backfill complete")
	}
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
		Str("order_type", intent.OrderType).
		Msg("order intent received, starting execution pipeline")

	// 1. Check kill switch before any work (skip for exits — closing reduces exposure).
	if !intent.Direction.IsExit() && s.killSwitch.IsHalted(event.TenantID, intent.Symbol) {
		l.Warn().Msg("kill switch engaged — trading halted for symbol")
		s.emit(ctx, domain.EventKillSwitchEngaged, event.TenantID, event.EnvMode, event.IdempotencyKey, nil)
		return nil
	}

	// 1a. Position gate — reject duplicate/conflicting entries.
	var inflightHandedOff bool
	if s.positionGate != nil {
		if err := s.positionGate.Check(ctx, intent); err != nil {
			l.Warn().Err(err).Msg("order intent rejected by position gate")
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "position_gate").Inc()
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
			return nil
		}
		// Mark inflight immediately after gate passes so that subsequent intents
		// processed from the async queue (which may arrive before the fill clears
		// the lock) are rejected rather than submitting duplicate orders.
		// Ownership transfers to pollForFill/syncFill on the happy path;
		// rejection paths clear it via this deferred fallback.
		if isEntry(intent) {
			s.positionGate.MarkInflight(event.TenantID, event.EnvMode, intent.Symbol)
			defer func() {
				if !inflightHandedOff {
					s.positionGate.ClearInflight(event.TenantID, event.EnvMode, intent.Symbol)
				}
			}()
		}
	}

	// Pre-compute whether this is an option order (used by gates and broker submission).
	isOptionOrder := intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeOption

	// --- Execution gate chain (or legacy inline guards) ---
	if s.executionGateChain != nil && !intent.Direction.IsExit() {
		gctx := &gate.ExecutionGateContext{
			Intent:        intent,
			AccountEquity: s.accountEquity,
			TenantID:      event.TenantID,
			EnvMode:       event.EnvMode,
		}
		if result := s.executionGateChain.Run(ctx, gctx); result != nil {
			l.Warn().
				Str("gate", result.GateName).
				Str("reason", result.Reason).
				Msg("order intent rejected by execution gate chain")
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, result.GateName).Inc()
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(),
				domain.NewOrderIntentRejectedPayload(intent, result.Error()))
			return nil
		}
	} else if !intent.Direction.IsExit() {
		// Legacy inline guards (backward compat when no execution gate chain is configured).
		if intent.Direction == domain.DirectionShort && !intent.AssetClass.SupportsShort() {
			reason := "SHORT direction not supported — crypto is long-only on Alpaca"
			l.Warn().Str("asset_class", intent.AssetClass.String()).Msg(reason)
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "short_disabled").Inc()
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, reason))
			return nil
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

		if s.portfolioGuard != nil {
			if err := s.portfolioGuard.Check(ctx, intent); err != nil {
				l.Warn().Err(err).Msg("order intent rejected by portfolio guard")
				if s.metrics != nil {
					s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "portfolio").Inc()
				}
				s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
				return nil
			}
		}

		// Validate risk (skip for exit orders — closing reduces exposure).
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

		// Validate slippage (skip for exit orders — we want to exit regardless).
		if err := s.slippageGuard.Check(ctx, intent); err != nil {
			l.Warn().Err(err).Msg("order intent rejected by slippage guard")
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "validation").Inc()
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
			return nil
		}
		// Trading window gate — reject entries outside allowed hours (opt-in via Meta).
		if s.tradingWindowGuard != nil {
			if err := s.tradingWindowGuard.Check(intent); err != nil {
				l.Warn().Err(err).Msg("order intent rejected by trading window guard")
				if s.metrics != nil {
					s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "trading_window").Inc()
				}
				s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
				return nil
			}
		}

		// Spread gate — reject entries when bid-ask spread is too wide (opt-in via Meta).
		if s.spreadGuard != nil {
			if err := s.spreadGuard.Check(ctx, intent); err != nil {
				l.Warn().Err(err).Msg("order intent rejected by spread guard")
				if s.metrics != nil {
					s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "spread").Inc()
				}
				s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
				return nil
			}
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

	// 5. Check daily loss circuit breaker (skip for exits — closing reduces exposure).
	if s.dailyLossBreaker != nil && !intent.Direction.IsExit() {
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
			if s.positionGate != nil {
				s.positionGate.ClearInflightExit(event.TenantID, event.EnvMode, intent.Symbol)
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, fmt.Sprintf("exit position query failed: %v", posErr)))
			return nil
		}
		var posQty float64
		for _, p := range positions {
			if p.Symbol == intent.Symbol {
				posQty += p.SignedQuantity()
			}
		}
		// Reject both "no position" (posQty==0) and "short position"
		// (posQty<0). Issuing a long-close SELL on top of a broker short
		// would deepen the short — which is exactly how 2026-04-16's
		// phantom-short race amplified past the broker. Treat any short
		// as "not ours to exit" at this gate; the global reconciler will
		// surface the anomaly for manual intervention.
		if posQty <= 0 {
			l.Warn().Msg("exit intent but no long position found — rejecting")
			if s.positionGate != nil {
				s.positionGate.ClearInflightExit(event.TenantID, event.EnvMode, intent.Symbol)
			}
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

	// 5f. Write-ahead journal (Sprint 2, gap #2). Must succeed before SubmitOrder
	// so that a crash mid-submit leaves a durable trail. Gated by OMO_ORDER_JOURNAL_ENABLED.
	if s.intentJournal != nil {
		if jerr := s.intentJournal.SaveOrderIntent(ctx, intent); jerr != nil {
			if errors.Is(jerr, ports.ErrDuplicateIntent) {
				// Another worker already journaled this idempotency key. Treat as
				// a deduplication signal: we must not double-submit. Clear the
				// exit inflight lock if relevant and emit a rejection event.
				l.Warn().Err(jerr).Msg("intent already journaled — skipping duplicate submission")
				if s.positionGate != nil && intent.Direction.IsExit() {
					s.positionGate.ClearInflightExit(event.TenantID, event.EnvMode, intent.Symbol)
				}
				s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(),
					domain.NewOrderIntentRejectedPayload(intent, "journal_duplicate"))
				return nil
			}
			// Journal write failed for non-duplicate reason (DB down, etc). The
			// whole point of the write-ahead is "never submit without durable
			// audit" — log loudly, alert, and skip. Fall through to a rejection.
			l.Error().Err(jerr).Msg("order intent journal write failed — skipping broker submission to preserve audit invariant")
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues(s.brokerName, intent.Strategy, "journal_write_failed").Inc()
			}
			if s.positionGate != nil && intent.Direction.IsExit() {
				s.positionGate.ClearInflightExit(event.TenantID, event.EnvMode, intent.Symbol)
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(),
				domain.NewOrderIntentRejectedPayload(intent, fmt.Sprintf("journal_write_failed: %v", jerr)))
			return nil
		}
	}

	// 6. Submit to broker. Re-check kill switch — entry intents can spend
	// hundreds of lines in pre-submit work (journal, guards, position query),
	// during which a concurrent intent's stop-out can trip the halt. Without
	// this re-check, intent A submits against a policy that was repealed
	// while it was queued.
	if !intent.Direction.IsExit() && s.killSwitch.IsHalted(event.TenantID, intent.Symbol) {
		l.Warn().Msg("kill switch tripped during pre-submit pipeline — aborting")
		if s.intentJournal != nil {
			if jerr := s.intentJournal.MarkIntentSubmitFailed(ctx, intent.ID, "kill_switch_engaged", s.nowFn()); jerr != nil {
				l.Error().Err(jerr).Msg("failed to mark intent submit-failed in journal")
			}
		}
		s.emit(ctx, domain.EventKillSwitchEngaged, event.TenantID, event.EnvMode, event.IdempotencyKey, nil)
		return nil
	}
	submitStart := s.nowFn()
	if parity.Enabled() {
		l.Info().
			Str("stage", parity.StageOrderSubmitted).
			Str("symbol", string(intent.Symbol)).
			Str("strategy", intent.Strategy).
			Str("direction", string(intent.Direction)).
			Float64("quantity", intent.Quantity).
			Float64("limit_price", intent.LimitPrice).
			Time("submit_start", submitStart).
			Msg("parity-diag")
	}
	brokerOrderID, err := s.broker.SubmitOrder(ctx, intent)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "broker rejected order")
		l.Error().Err(err).Msg("broker rejected order")
		if s.intentJournal != nil {
			if jerr := s.intentJournal.MarkIntentSubmitFailed(ctx, intent.ID, err.Error(), s.nowFn()); jerr != nil {
				l.Error().Err(jerr).Msg("failed to mark intent submit-failed in journal")
			}
		}
		if s.metrics != nil {
			side := strings.ToLower(brokerSideFor(intent.Direction))
			s.metrics.Orders.Total.WithLabelValues("alpaca", intent.Strategy, side, "limit", "rejected").Inc()
			s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "api").Inc()
			s.metrics.Orders.SubmitLat.WithLabelValues("alpaca", intent.Strategy, "limit").Observe(time.Since(submitStart).Seconds())
		}
		if s.positionGate != nil && intent.Direction.IsExit() {
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
		side := strings.ToLower(brokerSideFor(intent.Direction))
		s.metrics.Orders.Total.WithLabelValues("alpaca", intent.Strategy, side, "limit", "placed").Inc()
		s.metrics.Orders.SubmitLat.WithLabelValues("alpaca", intent.Strategy, "limit").Observe(time.Since(submitStart).Seconds())
	}
	span.SetAttributes(attribute.String("order.broker_order_id", brokerOrderID))
	l.Info().Str("broker_order_id", brokerOrderID).Msg("order submitted to broker")
	if s.intentJournal != nil {
		// Retry on transient DB errors: without a journal row recording the
		// broker_order_id, reconcileOpenOrdersOnBoot cannot match the live
		// broker order back to its intent, and MarkIntentTerminal (keyed by
		// broker_order_id) will miss the fill, leaving the journal row stuck
		// in pending_submit forever. The broker already has the order, so we
		// do NOT cancel on exhaustion — the order+trade tables still correlate
		// via brokerOrderID; only the audit journal degrades.
		var jerr error
		for attempt := range 3 {
			attemptCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			jerr = s.intentJournal.MarkIntentSubmitted(attemptCtx, intent.ID, brokerOrderID, s.nowFn())
			cancel()
			if jerr == nil {
				break
			}
			if attempt < 2 {
				time.Sleep(100 * time.Millisecond)
			}
		}
		if jerr != nil {
			l.Error().Err(jerr).Str("broker_order_id", brokerOrderID).Msg("journal MarkIntentSubmitted failed after retries — journal will be stale for this order, broker state unchanged")
		}
	}
	submittedPayload := domain.NewOrderIntentEventPayload(intent, domain.OrderIntentStatusSubmitted)
	submittedPayload.BrokerOrderID = brokerOrderID
	submittedPayload.Broker = s.brokerName
	s.emit(ctx, domain.EventOrderSubmitted, event.TenantID, event.EnvMode, intent.ID.String(), submittedPayload)

	// 7. Persist the order record.
	side := brokerSideFor(intent.Direction)
	order := domain.BrokerOrder{
		Time:          s.nowFn().UTC(),
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

	// Ownership of the inflight lock transfers to fill detection (syncFill,
	// pollForFill, or orderStream). The deferred fallback must NOT clear it.
	inflightHandedOff = true

	switch {
	case s.syncFill:
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
		if s.positionGate != nil {
			if isEntry(intent) {
				s.positionGate.ClearInflight(event.TenantID, event.EnvMode, intent.Symbol)
			} else if intent.Direction.IsExit() {
				s.positionGate.ClearInflightExit(event.TenantID, event.EnvMode, intent.Symbol)
			}
		}
	case s.orderStream == nil:
		go s.pollForFill(event.TenantID, event.EnvMode, intent, brokerOrderID, submitStart, l)
	case isEntry(intent):
		// WS stream active but unreliable on IBKR paper — fast-poll
		// livePos (PositionChan-backed) to detect fills within ~1s.
		go s.fastPollPosition(ctx, event.TenantID, event.EnvMode, po, brokerOrderID, false, l)
	case intent.Direction.IsExit():
		// Fast-poll for exit fill: position goes to zero.
		go s.fastPollPosition(ctx, event.TenantID, event.EnvMode, po, brokerOrderID, true, l)
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

// fastPollPosition polls the live position tracker every 200ms for up to 30s
// after order submission. PositionChan updates within milliseconds of a fill,
// so this catches fills that the ibsync WS order-status stream misses.
// For entries (isExit=false): detects position appearing (qty != 0).
// For exits (isExit=true): detects position disappearing (qty == 0).
func (s *Service) fastPollPosition(ctx context.Context, tenantID string, envMode domain.EnvMode, po *pendingOrder, brokerOrderID string, isExit bool, l zerolog.Logger) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(30 * time.Second)

	direction := "entry"
	if isExit {
		direction = "exit"
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout:
			l.Debug().Str("broker_order_id", brokerOrderID).Str("direction", direction).
				Msg("fast position poll: timed out — falling back to reconciliation loop")
			return
		case <-ticker.C:
			// Check if the WS stream already handled it.
			if _, ok := s.pendingOrders.Load(brokerOrderID); !ok {
				return // already resolved
			}
			posQty, err := s.broker.GetPosition(ctx, po.intent.Symbol)
			if err != nil {
				continue
			}

			var detected bool
			if isExit {
				detected = posQty == 0
			} else {
				// Wait for full entry fill before short-circuiting. Multi-fill
				// orders deliver N stream events keyed by ExecID — firing on
				// the first partial would claim pending and drop subsequent
				// legs. The 30s timeout still catches the stream-silent case.
				detected = posQty+1e-9 >= po.intent.Quantity
			}

			if detected {
				// Atomically claim the pending order before writing. If WS has
				// already claimed it, LoadAndDelete returns ok=false and we exit
				// without writing a duplicate trade row. A prior TOCTOU between
				// Load and cleanupPendingOrder let both paths race through and
				// each insert a trade (one with a proper IBKR execution_id, one
				// with ExecutionID="" from recordFillFromDetails).
				raw, ok := s.pendingOrders.LoadAndDelete(brokerOrderID)
				if !ok {
					return
				}
				po := raw.(*pendingOrder)
				l.Info().Float64("position_qty", posQty).Str("broker_order_id", brokerOrderID).
					Str("direction", direction).
					Msg("fast position poll: fill detected via livePos")
				s.recordFillFromDetails(po, brokerOrderID, ports.OrderDetails{
					BrokerOrderID:  brokerOrderID,
					Status:         "filled",
					FilledQty:      po.intent.Quantity,
					FilledAvgPrice: po.intent.LimitPrice,
					Symbol:         string(po.intent.Symbol),
					Side:           string(po.intent.Direction),
					Qty:            po.intent.Quantity,
				}, l)
				if s.positionGate != nil {
					if isExit {
						s.positionGate.ClearInflightExit(tenantID, envMode, po.intent.Symbol)
					} else {
						s.positionGate.ClearInflight(tenantID, envMode, po.intent.Symbol)
					}
				}
				// Trigger dust sweep on full exit (previously handled inside cleanupPendingOrder).
				if isExit && !strings.Contains(po.intent.Rationale, "SCALE_OUT") {
					go s.sweepDustPosition(tenantID, envMode, po.intent.Symbol, brokerOrderID, po.intent.Strategy)
				}
				return
			}
		}
	}
}

// handleFill records the fill in the DB and emits FillReceived.
func (s *Service) handleFill(tenantID string, envMode domain.EnvMode, intent domain.OrderIntent, brokerOrderID string, submitStart time.Time, l zerolog.Logger) {
	now := s.nowFn().UTC()
	ctx := context.Background()

	// Use limit price as fill price proxy (paper trading; actual fill price = limit price).
	fillPrice := intent.LimitPrice

	// Update order record.
	if err := s.repo.UpdateOrderFill(ctx, brokerOrderID, now, fillPrice, intent.Quantity); err != nil {
		l.Error().Err(err).Str("broker_order_id", brokerOrderID).Msg("failed to update order fill")
	}

	// Persist trade.
	side := brokerSideFor(intent.Direction)
	trade, err := domain.NewTrade(now, tenantID, envMode, uuid.New(), intent.Symbol, side, intent.Quantity, fillPrice, 0, "FILLED", intent.Strategy, intent.Rationale)
	if err != nil {
		l.Error().Err(err).Msg("failed to construct trade on fill")
	} else {
		trade.BrokerOrderID = brokerOrderID
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

	// Collect signal tags (sig_* prefixed keys) from intent Meta.
	signalTags := make(map[string]string)
	for k, v := range intent.Meta {
		if len(k) > 4 && k[:4] == "sig_" {
			signalTags[k[4:]] = v // strip "sig_" prefix
		}
	}

	// Pull MFE/MAE straight off the live position state when available.
	// Strategy-emitted exits don't flow through positionmonitor.exit_eval's
	// fill-metadata attachment path, so intent.Meta is empty for them. The
	// monitor tracks spot_mfe_pct/spot_mae_pct on every tick regardless of
	// who fires the exit, so a lookup here closes that gap. Prefer monitor
	// values over intent.Meta — they're the authoritative live HWM/LWM.
	spotMFE := intent.Meta["spot_mfe_pct"]
	spotMAE := intent.Meta["spot_mae_pct"]
	minutesToFirstProfit := intent.Meta["minutes_to_first_profit"]
	minutesHeld := intent.Meta["minutes_held"]
	isExit := intent.Direction == domain.DirectionCloseLong || intent.Direction == domain.DirectionCloseShort
	if isExit && s.positionLookup != nil {
		if pos, ok := s.positionLookup.LookupPosition(string(intent.Symbol)); ok && pos.CustomState != nil {
			if v, has := pos.CustomState["spot_mfe_pct"]; has {
				spotMFE = fmt.Sprintf("%.6f", v)
			}
			if v, has := pos.CustomState["spot_mae_pct"]; has {
				spotMAE = fmt.Sprintf("%.6f", v)
			}
			if v, has := pos.CustomState["minutes_to_first_profit"]; has {
				minutesToFirstProfit = fmt.Sprintf("%.1f", v)
			} else if minutesToFirstProfit == "" {
				minutesToFirstProfit = "-1"
			}
			if v, has := pos.CustomState["minutes_since_entry"]; has {
				minutesHeld = fmt.Sprintf("%.1f", v)
			}
		}
	}

	fillPayload := map[string]any{
		"broker_order_id":         brokerOrderID,
		"intent_id":               intent.ID.String(),
		"symbol":                  string(intent.Symbol),
		"side":                    side,
		"direction":               string(intent.Direction),
		"quantity":                intent.Quantity,
		"price":                   fillPrice,
		"filled_at":               now,
		"strategy":                intent.Strategy,
		"rationale":               intent.Rationale,
		"risk_modifier":           intent.Meta["risk_modifier"],
		"regime":                  intent.Meta["regime"],
		"vix_bucket":              intent.Meta["vix_bucket"],
		"market_context":          intent.Meta["market_context"],
		"premium_mfe_pct":         intent.Meta["premium_mfe_pct"],
		"premium_mae_pct":         intent.Meta["premium_mae_pct"],
		"spot_mfe_pct":            spotMFE,
		"spot_mae_pct":            spotMAE,
		"minutes_to_first_profit": minutesToFirstProfit,
		"minutes_held":            minutesHeld,
		"signal_tags":             signalTags,
	}
	if intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeOption {
		fillPayload["instrument_type"] = string(domain.InstrumentTypeOption)
		fillPayload["option_right"] = intent.Meta["option_right"]
		fillPayload["option_expiry"] = intent.Meta["expiry"]
		fillPayload["iv_at_entry"] = intent.Meta["iv_at_entry"]
		fillPayload["delta_at_entry"] = intent.Meta["delta_at_entry"]
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

// handleCopytradeEntryExpired pairs with the strategy's optimistic slot free:
// we cancel the outstanding BTO at the broker so a late fill can't materialize
// and over-position the account. SimBroker's cancel is a no-op (orders fill
// same-tick), so this only matters in paper/live.
func (s *Service) handleCopytradeEntryExpired(ctx context.Context, event domain.Event) error {
	payload, ok := event.Payload.(domain.CopytradeEntryExpiredPayload)
	if !ok {
		return fmt.Errorf("execution: invalid payload for CopytradeEntryExpired: %T", event.Payload)
	}
	var toCancel []string
	s.pendingOrders.Range(func(key, value any) bool {
		brokerOrderID := key.(string)
		po := value.(*pendingOrder)
		if po.intent.Strategy != payload.StrategyID {
			return true
		}
		if po.intent.Direction.IsExit() {
			return true
		}
		if string(po.intent.Symbol) != payload.ContractSymbol {
			return true
		}
		toCancel = append(toCancel, brokerOrderID)
		return true
	})
	for _, brokerOrderID := range toCancel {
		if err := s.broker.CancelOrder(ctx, brokerOrderID); err != nil {
			s.log.Error().Err(err).
				Str("broker_order_id", brokerOrderID).
				Str("contract_symbol", payload.ContractSymbol).
				Float64("age_seconds", payload.AgeSeconds).
				Msg("execution: failed to cancel expired copytrade BTO")
			continue
		}
		s.log.Warn().
			Str("broker_order_id", brokerOrderID).
			Str("contract_symbol", payload.ContractSymbol).
			Float64("age_seconds", payload.AgeSeconds).
			Msg("execution: canceled expired copytrade BTO")
	}
	return nil
}

func (s *Service) handleOrderUpdate(update ports.OrderUpdate) {
	l := s.log.With().
		Str("broker_order_id", update.BrokerOrderID).
		Str("event", update.Event).
		Logger()

	switch update.Event {
	case ports.OrderEventFill, ports.OrderEventPartialFill:
		s.handleStreamFill(update, l)
	case ports.OrderEventCanceled, ports.OrderEventExpired, ports.OrderEventRejected:
		l.Info().Msg("order terminal via stream")
		if s.intentJournal != nil {
			if jerr := s.intentJournal.MarkIntentTerminal(context.Background(), update.BrokerOrderID, update.Event, update.FilledQty, update.FilledAvgPrice, s.nowFn()); jerr != nil {
				l.Error().Err(jerr).Msg("failed to mark intent terminal in journal")
			}
		}
		s.cleanupPendingOrder(update.BrokerOrderID)
	}
}

const fastFillRetryDelay = 500 * time.Millisecond
const fastFillMaxRetries = 3

func (s *Service) handleStreamFill(update ports.OrderUpdate, l zerolog.Logger) {
	// Peek (not claim). The pending entry must survive across partial fills
	// so N exec events all land against it. Final fill atomically claims it
	// and runs the terminal-cleanup pipeline.
	raw, ok := s.pendingOrders.Load(update.BrokerOrderID)
	if !ok {
		for range fastFillMaxRetries {
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

	// Incremental fields for the trade row (what THIS exec leg filled).
	// For options the intent.LimitPrice fallback is unsafe: triggerExit can
	// silently route the underlying spot into intent.LimitPrice when BSM is
	// unavailable (see 2026-04-28 LLY 850P incident). Recording that as the
	// trade price contaminates ledger P&L by a factor of 30x or more. If the
	// broker stream gave us no price for this exec, defer the leg — the
	// next event or the boot reconciler's ReqFills will heal it. Non-option
	// instruments keep the legacy fallback because intent.LimitPrice in
	// equity/crypto contexts cannot carry an underlying-magnitude leak.
	legPrice := firstPositive(update.Price, update.FilledAvgPrice)
	if legPrice == 0 {
		isOpt := po.intent.Instrument != nil && po.intent.Instrument.Type == domain.InstrumentTypeOption
		if isOpt {
			l.Warn().
				Str("broker_order_id", update.BrokerOrderID).
				Str("execution_id", update.ExecutionID).
				Str("symbol", string(po.intent.Symbol)).
				Msg("option fill update missing broker price — deferring leg (reconcile will heal)")
			return
		}
		legPrice = po.intent.LimitPrice
	}
	legQty := firstPositive(update.Qty, update.FilledQty, po.intent.Quantity)

	// Cumulative values for the orders-row bump (broker-authoritative
	// running totals as of THIS exec).
	cumQty := firstPositive(update.FilledQty, legQty)
	cumAvgPrice := firstPositive(update.FilledAvgPrice, legPrice)
	filledAt := update.FilledAt
	if filledAt.IsZero() {
		filledAt = s.nowFn().UTC()
	}

	s.insertFillLeg(po, update.BrokerOrderID, update.ExecutionID, filledAt, legPrice, legQty, cumQty, cumAvgPrice, l)

	// Final fill? Either the broker marked this event as terminal
	// (Alpaca/simbroker pattern) OR cumulative reached intent qty (defensive
	// fallback for brokers that omit the terminal label).
	isFinal := update.Event == ports.OrderEventFill || cumQty+1e-9 >= po.intent.Quantity
	if !isFinal {
		// Emit a per-leg FillReceived so downstream consumers keyed by
		// execution_id pick up the partial immediately. PnL aggregator
		// and the position monitor both tolerate per-leg events.
		s.emitPartialFillReceived(po, update.BrokerOrderID, legPrice, legQty, filledAt, update.ExecutionID, l)
		return
	}

	// Atomically claim pending. If fastPollPosition or the reconcile loop
	// already claimed, LoadAndDelete returns false and we exit without
	// double-firing the cleanup pipeline. The per-leg trade is already in
	// the DB via insertFillLeg above.
	if _, claimed := s.pendingOrders.LoadAndDelete(update.BrokerOrderID); !claimed {
		return
	}
	s.runFillFinalization(po, update.BrokerOrderID, cumAvgPrice, cumQty, filledAt, l)
}

// enrichTradeOptionsFromIntent populates option-specific trade fields from
// intent.Meta (strike, expiry, premium, Greeks). No-op on non-option intents.
func enrichTradeOptionsFromIntent(trade *domain.Trade, intent domain.OrderIntent) {
	if intent.Instrument == nil || intent.Instrument.Type != domain.InstrumentTypeOption {
		return
	}
	trade.InstrumentType = domain.InstrumentTypeOption
	trade.OptionSymbol = intent.Instrument.Symbol.String()
	trade.Underlying = string(intent.Instrument.UnderlyingSymbol)
	trade.OptionRight = intent.Meta["option_right"]
	if st, err := strconv.ParseFloat(intent.Meta["strike"], 64); err == nil {
		trade.Strike = st
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

// firstPositive returns the first non-zero/positive value in the list.
// Used to resolve fill prices/qtys with a chain of fallbacks (adapter
// value → cumulative → intent default).
func firstPositive(values ...float64) float64 {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

// insertFillLeg persists ONE execution leg (trade row + monotonic orders.filled_qty
// bump). Multi-fill orders call this N times, once per ExecID. The trade row
// carries the incremental qty/price; the orders row carries broker-cumulative
// values from THIS exec. Safe under out-of-order delivery: execution_id UNIQUE
// dedups trades, GREATEST guards the orders update.
//
// Exit-side guard: when the intent is a CLOSE_*, reject the persist if the
// fill quantity exceeds the position's currently-open qty per positionLookup.
// Closes the duplicate-exit-fill window observed on RIVN 2026-04-27, where a
// re-pegged exit raced the original order's fill: both filled at IBKR within
// 31ms and both got persisted, leaving the trades ledger at -47 net (BUY 47 -
// SELL 47 - SELL 47). The reconciler caught the negative inventory but only
// post-fact; this guard refuses the second fill at write time.
func (s *Service) insertFillLeg(po *pendingOrder, brokerOrderID, executionID string, filledAt time.Time, legPrice, legQty, cumQty, cumAvgPrice float64, l zerolog.Logger) {
	if reason, blocked := s.shouldBlockExitFill(po, legQty); blocked {
		l.Error().
			Str("broker_order_id", brokerOrderID).
			Str("execution_id", executionID).
			Str("symbol", string(po.intent.Symbol)).
			Float64("leg_qty", legQty).
			Str("reason", reason).
			Msg("exit fill rejected: would exceed open position quantity (likely duplicate from race)")
		return
	}
	if reason, blocked := s.shouldBlockOptionLegMagnitude(po, legPrice); blocked {
		l.Error().
			Str("broker_order_id", brokerOrderID).
			Str("execution_id", executionID).
			Str("symbol", string(po.intent.Symbol)).
			Float64("leg_price", legPrice).
			Str("reason", reason).
			Msg("option fill rejected: leg_price exceeds sane multiple of reference premium (likely underlying-spot contamination)")
		return
	}
	ctx := context.Background()
	side := brokerSideFor(po.intent.Direction)
	trade, err := domain.NewTrade(filledAt, po.tenantID, po.envMode, uuid.New(), po.intent.Symbol, side, legQty, legPrice, 0, "FILLED", po.intent.Strategy, po.intent.Rationale)
	if err != nil {
		l.Error().Err(err).Msg("failed to construct trade leg — skipping persist (reconcile will heal)")
		return
	}
	trade.ExecutionID = executionID
	trade.BrokerOrderID = brokerOrderID
	enrichTradeOptionsFromIntent(&trade, po.intent)
	if parity.Enabled() {
		l.Info().
			Str("stage", parity.StageFillRecorded).
			Str("symbol", string(po.intent.Symbol)).
			Str("strategy", po.intent.Strategy).
			Str("direction", string(po.intent.Direction)).
			Str("broker_order_id", brokerOrderID).
			Str("execution_id", executionID).
			Time("filled_at", filledAt).
			Float64("leg_qty", legQty).
			Float64("leg_price", legPrice).
			Float64("cum_qty", cumQty).
			Float64("cum_avg_price", cumAvgPrice).
			Msg("parity-diag")
	}
	if err := s.repo.RecordFill(ctx, brokerOrderID, filledAt, cumAvgPrice, cumQty, trade); err != nil {
		l.Error().Err(err).Str("execution_id", executionID).Msg("failed to record fill leg")
	}
}

// optionLegMagnitudeMultiple is the upper bound on legPrice / referencePremium
// before a recorded option leg is treated as poisoned. A 5x cap is well above
// any realistic intraday option move (3-4x is exceptional even on event days)
// while comfortably below the 30-100x ratio produced when the underlying spot
// price contaminates an option order limit.
const optionLegMagnitudeMultiple = 5.0

// shouldBlockOptionLegMagnitude returns (reason, true) when an option fill
// leg's recorded price is more than optionLegMagnitudeMultiple times its
// reference premium — almost always a sign that the underlying spot leaked
// into the trade row via a corrupted intent.LimitPrice. Reference premium
// resolution order:
//  1. position monitor's CustomState["option_premium"] (entry premium for
//     exits; authoritative when the position is still tracked)
//  2. intent.Meta["premium"] (set on entry intents by risk_sizer)
//  3. intent.LimitPrice itself, only when it is itself plausibly an
//     option-magnitude number (skip when it could already be contaminated)
//
// When no reference is resolvable the leg is allowed through — the magnitude
// check is a safety net, not a hard requirement. Non-option legs always pass.
func (s *Service) shouldBlockOptionLegMagnitude(po *pendingOrder, legPrice float64) (string, bool) {
	if po == nil || po.intent.Instrument == nil || po.intent.Instrument.Type != domain.InstrumentTypeOption {
		return "", false
	}
	if legPrice <= 0 {
		return "", false
	}

	var ref float64
	isExit := po.intent.Direction == domain.DirectionCloseLong || po.intent.Direction == domain.DirectionCloseShort
	if isExit && s.positionLookup != nil {
		if pos, ok := s.positionLookup.LookupPosition(string(po.intent.Symbol)); ok && pos.CustomState != nil {
			ref = pos.CustomState["option_premium"]
		}
	}
	if ref <= 0 {
		if v, err := strconv.ParseFloat(po.intent.Meta["premium"], 64); err == nil && v > 0 {
			ref = v
		}
	}
	if ref <= 0 {
		return "", false
	}

	if legPrice > ref*optionLegMagnitudeMultiple {
		return fmt.Sprintf("leg_price=%.4f exceeds %.0fx reference premium=%.4f", legPrice, optionLegMagnitudeMultiple, ref), true
	}
	return "", false
}

// shouldBlockExitFill returns (reason, true) when an exit-direction fill leg
// would exceed the position's open quantity per positionLookup, which means
// it's almost certainly a duplicate from a re-peg race. Entry directions
// always pass through; missing position lookup or absent position data also
// pass (best-effort guard, not a hard constraint).
func (s *Service) shouldBlockExitFill(po *pendingOrder, legQty float64) (string, bool) {
	if po == nil || s.positionLookup == nil {
		return "", false
	}
	if po.intent.Direction != domain.DirectionCloseLong && po.intent.Direction != domain.DirectionCloseShort {
		return "", false
	}
	pos, ok := s.positionLookup.LookupPosition(string(po.intent.Symbol))
	if !ok {
		// No tracked position. Could be a stale exit chasing a closed position
		// (the prior fill already drove qty to 0 and removed the entry from
		// the monitor) — treat as duplicate.
		return "no open position for symbol", true
	}
	if legQty > pos.Quantity+1e-9 {
		return fmt.Sprintf("leg_qty=%.4f exceeds open_qty=%.4f", legQty, pos.Quantity), true
	}
	return "", false
}

// emitPartialFillReceived emits a FillReceived carrying the per-exec qty/price
// so downstream consumers pick up partials as they arrive. Lean vs the final
// payload — no MFE/MAE lookup (those only matter on the completing leg).
func (s *Service) emitPartialFillReceived(po *pendingOrder, brokerOrderID string, legPrice, legQty float64, filledAt time.Time, executionID string, l zerolog.Logger) {
	side := brokerSideFor(po.intent.Direction)
	payload := map[string]any{
		"broker_order_id": brokerOrderID,
		"intent_id":       po.intent.ID.String(),
		"execution_id":    executionID,
		"symbol":          string(po.intent.Symbol),
		"side":            side,
		"direction":       string(po.intent.Direction),
		"quantity":        legQty,
		"price":           legPrice,
		"filled_at":       filledAt,
		"strategy":        po.intent.Strategy,
		"asset_class":     string(po.intent.AssetClass),
		"rationale":       po.intent.Rationale,
		"partial":         true,
	}
	if po.intent.Instrument != nil && po.intent.Instrument.Type == domain.InstrumentTypeOption {
		payload["instrument_type"] = string(domain.InstrumentTypeOption)
	}
	s.emit(context.Background(), domain.EventFillReceived, po.tenantID, po.envMode, brokerOrderID, payload)
	l.Info().
		Str("execution_id", executionID).
		Float64("leg_qty", legQty).
		Float64("leg_price", legPrice).
		Msg("partial fill leg persisted")
}

func (s *Service) handleFillWithPrice(po *pendingOrder, brokerOrderID string, fillPrice, fillQty float64, filledAt time.Time, executionID string, l zerolog.Logger) {
	// Write the single (all-at-once) trade leg. Non-stream callers (syncFill,
	// recordFillFromDetails, reconcilePendingOrders) treat every fill as a
	// one-shot — they lack per-exec detail from the broker.
	s.insertFillLeg(po, brokerOrderID, executionID, filledAt, fillPrice, fillQty, fillQty, fillPrice, l)
	s.runFillFinalization(po, brokerOrderID, fillPrice, fillQty, filledAt, l)
}

// runFillFinalization performs the end-of-order cleanup: intent-journal
// terminal mark, FillReceived emit with full payload (MFE/MAE, signal tags,
// option metadata), metrics, position-gate transitions. Does NOT write a
// trade row — callers (handleFillWithPrice, handleStreamFill) handle that
// via insertFillLeg.
func (s *Service) runFillFinalization(po *pendingOrder, brokerOrderID string, fillPrice, fillQty float64, filledAt time.Time, l zerolog.Logger) {
	ctx := context.Background()

	if s.intentJournal != nil {
		if jerr := s.intentJournal.MarkIntentTerminal(ctx, brokerOrderID, domain.OrderIntentJournalFilled, fillQty, fillPrice, filledAt); jerr != nil {
			l.Error().Err(jerr).Msg("failed to mark intent filled in journal")
		}
	}

	side := brokerSideFor(po.intent.Direction)

	// Collect signal tags (sig_* prefixed keys) from intent Meta.
	sigTags := make(map[string]string)
	for k, v := range po.intent.Meta {
		if len(k) > 4 && k[:4] == "sig_" {
			sigTags[k[4:]] = v // strip "sig_" prefix
		}
	}

	// MFE/MAE lookup for strategy-emitted exits — see handleFill for the
	// same pattern. Backtest fills flow through this poll-based path when
	// SimBroker's stream returns the fill via OrderStream, so both handlers
	// need the lookup.
	spotMFE := po.intent.Meta["spot_mfe_pct"]
	spotMAE := po.intent.Meta["spot_mae_pct"]
	minutesToFirstProfit := po.intent.Meta["minutes_to_first_profit"]
	minutesHeld := po.intent.Meta["minutes_held"]
	isExit := po.intent.Direction == domain.DirectionCloseLong || po.intent.Direction == domain.DirectionCloseShort
	if isExit && s.positionLookup != nil {
		if pos, ok := s.positionLookup.LookupPosition(string(po.intent.Symbol)); ok && pos.CustomState != nil {
			if v, has := pos.CustomState["spot_mfe_pct"]; has {
				spotMFE = fmt.Sprintf("%.6f", v)
			}
			if v, has := pos.CustomState["spot_mae_pct"]; has {
				spotMAE = fmt.Sprintf("%.6f", v)
			}
			if v, has := pos.CustomState["minutes_to_first_profit"]; has {
				minutesToFirstProfit = fmt.Sprintf("%.1f", v)
			} else if minutesToFirstProfit == "" {
				minutesToFirstProfit = "-1"
			}
			if v, has := pos.CustomState["minutes_since_entry"]; has {
				minutesHeld = fmt.Sprintf("%.1f", v)
			}
		}
	}

	fillPayload := map[string]any{
		"broker_order_id":         brokerOrderID,
		"intent_id":               po.intent.ID.String(),
		"symbol":                  string(po.intent.Symbol),
		"side":                    side,
		"direction":               string(po.intent.Direction),
		"quantity":                fillQty,
		"price":                   fillPrice,
		"filled_at":               filledAt,
		"strategy":                po.intent.Strategy,
		"asset_class":             string(po.intent.AssetClass),
		"rationale":               po.intent.Rationale,
		"regime":                  po.intent.Meta["regime"],
		"vix_bucket":              po.intent.Meta["vix_bucket"],
		"market_context":          po.intent.Meta["market_context"],
		"premium_mfe_pct":         po.intent.Meta["premium_mfe_pct"],
		"premium_mae_pct":         po.intent.Meta["premium_mae_pct"],
		"spot_mfe_pct":            spotMFE,
		"spot_mae_pct":            spotMAE,
		"minutes_to_first_profit": minutesToFirstProfit,
		"minutes_held":            minutesHeld,
		"signal_tags":             sigTags,
		"partial":                 false,
	}
	if po.intent.Instrument != nil && po.intent.Instrument.Type == domain.InstrumentTypeOption {
		fillPayload["instrument_type"] = string(domain.InstrumentTypeOption)
		fillPayload["option_right"] = po.intent.Meta["option_right"]
		fillPayload["option_expiry"] = po.intent.Meta["expiry"]
		fillPayload["iv_at_entry"] = po.intent.Meta["iv_at_entry"]
		fillPayload["delta_at_entry"] = po.intent.Meta["delta_at_entry"]
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

// MarkRepegCancel tags a live pending order so its upcoming terminal event
// won't (a) count as an exit failure, nor (b) launch a dust-sweep. Called
// by the position monitor just before it issues a CancelOrder-for-repeg,
// which will inevitably terminate the order without a fill. Returns true
// if a pending order with the given broker ID was found and tagged;
// returns false when the order is already gone (cleanupPendingOrder ran
// first) — callers treat that as a no-op because there's nothing left to
// record against, and any racing dust-sweep is already in flight.
func (s *Service) MarkRepegCancel(brokerOrderID string) bool {
	raw, ok := s.pendingOrders.Load(brokerOrderID)
	if !ok {
		return false
	}
	po := raw.(*pendingOrder)
	po.suppressTerminalActions = true
	return true
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
			isFullExit := po.intent.Direction.IsExit() &&
				!strings.Contains(po.intent.Rationale, "SCALE_OUT")

			// Suppress BOTH the dust-sweep launch AND the failure-count
			// record when the cancel came from a position-monitor re-peg.
			// See pendingOrder.suppressTerminalActions for rationale.
			if isFullExit && !po.suppressTerminalActions {
				go s.sweepDustPosition(po.tenantID, po.envMode, po.intent.Symbol, brokerOrderID, po.intent.Strategy)
			} else {
				s.positionGate.ClearInflightExit(po.tenantID, po.envMode, po.intent.Symbol)
			}

			if !po.suppressTerminalActions {
				if tripped := s.positionGate.RecordExitFailure(po.tenantID, po.envMode, po.intent.Symbol); tripped {
					s.emit(context.Background(), domain.EventExitCircuitBroken, po.tenantID, po.envMode, brokerOrderID, domain.ExitCircuitBrokenPayload{
						Symbol:       po.intent.Symbol,
						Failures:     maxExitFailures,
						CooldownSecs: exitCooldownDuration.Seconds(),
					})
				}
			}

			s.emit(context.Background(), domain.EventExitOrderTerminal, po.tenantID, po.envMode, brokerOrderID, map[string]any{
				"symbol":          string(po.intent.Symbol),
				"broker_order_id": brokerOrderID,
			})
		}
	}
}

// Dust-sweep pricing and fallback tunables. See sweepDustPosition for details.
//
// dustSweepLimitWindow: how long we wait for the marketable-limit to fill
// before canceling and falling back to market. Short because dust is small
// and we want the position flat — not the best possible fill.
//
// dustSweepMinAdverseBps / dustSweepBlownSpreadRatio / dustSweepQuoteMaxAge:
// mirror the positionmonitor exit_pricer guardrails so the sweep skips the
// marketable-limit path on stale, halted, or blown-out quotes and goes
// straight to market.
//
// dustSweepNearCloseMinutes: hard override window before US equity close.
// Inside this window, 0DTE or deep-ITM options can get exercised-by-exception
// if we end the day flat-on-limit-no-fill — so we prefer a guaranteed market
// fill over price improvement.
//
// dustSweepCancelConfirmWait: cap on polling for terminal status after a
// cancel, BEFORE submitting the market fallback. Without this guard, IBKR
// could fill both the canceled limit AND the market — flipping net short.
const (
	dustSweepLimitWindow       = 15 * time.Second
	dustSweepMinAdverseBps     = 150.0
	dustSweepQuoteMaxAge       = 5 * time.Second
	dustSweepBlownSpreadRatio  = 0.25
	dustSweepNearCloseMinutes  = 15
	dustSweepCancelConfirmWait = 1 * time.Second
)

// isNearClose reports whether now is within dustSweepNearCloseMinutes of
// the 16:00 ET US equity-options close. Used to skip the marketable-limit
// path for end-of-day dust sweeps where exercise-by-exception risk
// outweighs the benefit of better pricing.
func isNearClose(now time.Time) bool {
	loc := domain.NYLocation()
	if loc == nil {
		return false
	}
	et := now.In(loc)
	if et.Weekday() == time.Saturday || et.Weekday() == time.Sunday {
		return false
	}
	close := time.Date(et.Year(), et.Month(), et.Day(), 16, 0, 0, 0, loc)
	delta := close.Sub(et)
	return delta > 0 && delta <= dustSweepNearCloseMinutes*time.Minute
}

// dustSweepLimitPrice computes a marketable-limit SELL price anchored below
// the bid. The limit is priced to cross the book (so it is marketable) but
// floored so we never give up more than max(minBps, spread_bps/2) of the
// mid. A spread-adaptive floor is needed because illiquid weeklies routinely
// carry 300-500 bps spreads; a flat 150 bps cap would guarantee non-fills.
//
// Returns usable=false when the quote is unusable (stale, halted, blown-
// spread), so callers fall back to pure market. Caller is responsible for
// the halt check (bid==0 || ask==0) — this helper still returns false in
// that case as defense-in-depth.
func dustSweepLimitPrice(quote domain.OptionQuote, now time.Time) (price float64, usable bool) {
	if quote.Bid <= 0 || quote.Ask <= 0 {
		return 0, false
	}
	if quote.BidSize == 0 {
		return 0, false
	}
	if !quote.Timestamp.IsZero() && now.Sub(quote.Timestamp) > dustSweepQuoteMaxAge {
		return 0, false
	}
	mid := (quote.Bid + quote.Ask) / 2.0
	if mid <= 0 {
		return 0, false
	}
	spread := quote.Ask - quote.Bid
	if spread < 0 {
		return 0, false
	}
	if spread/mid > dustSweepBlownSpreadRatio {
		return 0, false
	}
	spreadBps := (spread / mid) * 10000.0
	maxAdverseBps := dustSweepMinAdverseBps
	if half := spreadBps / 2.0; half > maxAdverseBps {
		maxAdverseBps = half
	}
	capped := quote.Bid * (1.0 - maxAdverseBps/10000.0)
	floor := quote.Bid - 0.01 // never AT the bid — keep one tick above so IBKR treats it as a cross
	if capped > floor {
		return capped, true
	}
	return floor, true
}

func (s *Service) sweepDustPosition(tenantID string, envMode domain.EnvMode, symbol domain.Symbol, brokerOrderID, originStrategy string) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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
		Msg("dust sweep: remainder detected — evaluating marketable-limit vs market fallback")

	// Try marketable-limit-first on liquid option quotes. Equity/crypto dust
	// sweeps stay on the legacy market path because spreads are usually tight
	// and the savings don't justify the extra RTT. For options with usable
	// live quotes and enough time before close, price a limit one tick below
	// the bid (floored by an adaptive bps cap) so we cross the book without
	// eating the full spread. On non-fill within the window, cancel, await
	// terminal status, and submit true market.
	now := s.nowFn()
	canTryLimit := domain.IsOCCSymbol(symbol) && s.optionsPricePort != nil && !isNearClose(now)
	if canTryLimit {
		if limitPrice, ok := s.fetchDustSweepLimitPrice(ctx, symbol, now, l); ok {
			if s.runDustSweepLimit(ctx, tenantID, envMode, symbol, remainingQty, limitPrice, brokerOrderID, originStrategy, l, &sweepFilled) {
				return
			}
			// Limit phase did not fill (timeout, broker rejection, etc.).
			// Fall through to market fallback.
		}
	}

	s.runDustSweepMarket(ctx, tenantID, envMode, symbol, remainingQty, brokerOrderID, originStrategy, l, &sweepFilled)
}

// fetchDustSweepLimitPrice reads a live option quote and computes the
// marketable-limit price. Returns ok=false on any guard trip (halt, stale,
// blown spread, quote fetch error) so the caller can fall through to the
// market path.
func (s *Service) fetchDustSweepLimitPrice(ctx context.Context, symbol domain.Symbol, now time.Time, l zerolog.Logger) (float64, bool) {
	quoteCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	quotes, err := s.optionsPricePort.GetOptionPrices(quoteCtx, []domain.Symbol{symbol})
	if err != nil {
		l.Warn().Err(err).Msg("dust sweep: option quote fetch failed — falling back to market")
		return 0, false
	}
	quote, present := quotes[symbol]
	if !present {
		l.Warn().Msg("dust sweep: no quote returned — falling back to market")
		return 0, false
	}
	// Halt detection: an option with bid=0 or ask=0 is either halted or the
	// upstream feed has dropped the quote. Either way, limit pricing is
	// meaningless; prefer the guaranteed market fill.
	if quote.Bid <= 0 || quote.Ask <= 0 {
		l.Info().Float64("bid", quote.Bid).Float64("ask", quote.Ask).
			Msg("dust sweep: halt detected — falling back to market")
		return 0, false
	}
	price, ok := dustSweepLimitPrice(quote, now)
	if !ok {
		l.Info().
			Float64("bid", quote.Bid).Float64("ask", quote.Ask).
			Int("bid_size", quote.BidSize).
			Time("quote_ts", quote.Timestamp).
			Msg("dust sweep: quote unusable — falling back to market")
		return 0, false
	}
	return price, true
}

// runDustSweepLimit submits a marketable-limit sell and polls for fill. On
// fill, records the trade and returns true. On timeout or terminal-without-
// fill, cancels the order (if still live), waits for terminal confirmation,
// and returns false so the caller can run the market fallback.
func (s *Service) runDustSweepLimit(
	ctx context.Context,
	tenantID string,
	envMode domain.EnvMode,
	symbol domain.Symbol,
	qty, limitPrice float64,
	brokerOrderID, originStrategy string,
	l zerolog.Logger,
	sweepFilled *bool,
) bool {
	intent, intentErr := s.buildSweepIntent(tenantID, envMode, symbol, qty, brokerOrderID, originStrategy)
	if intentErr != nil {
		l.Error().Err(intentErr).Msg("dust sweep: failed to build limit intent — falling back to market")
		return false
	}
	intent.OrderType = "limit"
	intent.LimitPrice = limitPrice
	intent.TimeInForce = "day"

	orderID, err := s.broker.SubmitOrder(ctx, intent)
	if err != nil {
		l.Error().Err(err).Msg("dust sweep: marketable-limit submit failed — falling back to market")
		return false
	}
	if orderID == "" {
		l.Info().Msg("dust sweep: position already closed during limit submit")
		*sweepFilled = true
		return true
	}
	l.Info().
		Str("sweep_order_id", orderID).
		Float64("limit_price", limitPrice).
		Msg("dust sweep: marketable-limit accepted — polling")

	window := dustSweepLimitWindow
	if s.dustSweepLimitWindowOverride > 0 {
		window = s.dustSweepLimitWindowOverride
	}
	filled, terminal := s.pollSweepFill(ctx, orderID, window, tenantID, envMode, symbol, brokerOrderID, originStrategy, l, sweepFilled)
	if filled {
		return true
	}
	if terminal {
		// Already canceled/rejected/expired by broker — no need to cancel again.
		l.Info().Str("sweep_order_id", orderID).Msg("dust sweep: limit terminal without fill — falling back to market")
		return false
	}

	// Timeout: order still open. Cancel, await terminal status, then return
	// false so caller submits market. Awaiting terminal matters because
	// IBKR may still fill a partially-working limit while we're submitting
	// the market — which would cross us net short.
	l.Info().Str("sweep_order_id", orderID).Msg("dust sweep: limit window expired — canceling")
	if cerr := s.broker.CancelOrder(ctx, orderID); cerr != nil {
		l.Warn().Err(cerr).Str("sweep_order_id", orderID).
			Msg("dust sweep: cancel failed — may already be terminal")
	}
	if s.waitSweepTerminal(ctx, orderID, tenantID, envMode, symbol, brokerOrderID, originStrategy, l, sweepFilled) {
		// During the cancel-confirm wait, the limit actually filled — treat
		// as a success and skip the market fallback.
		return true
	}
	return false
}

// runDustSweepMarket submits a true market sell and polls for fill. This is
// both the legacy path and the fallback after a limit non-fill.
func (s *Service) runDustSweepMarket(
	ctx context.Context,
	tenantID string,
	envMode domain.EnvMode,
	symbol domain.Symbol,
	qty float64,
	brokerOrderID, originStrategy string,
	l zerolog.Logger,
	sweepFilled *bool,
) {
	intent, intentErr := s.buildSweepIntent(tenantID, envMode, symbol, qty, brokerOrderID, originStrategy)
	if intentErr != nil {
		l.Error().Err(intentErr).Msg("dust sweep: failed to build market intent — clearing gate for retry")
		return
	}
	intent.OrderType = "market"
	intent.TimeInForce = "ioc"

	orderID, err := s.broker.SubmitOrder(ctx, intent)
	if err != nil {
		l.Error().Err(err).Msg("dust sweep: market sell failed — clearing gate for retry")
		return
	}
	if orderID == "" {
		l.Info().Msg("dust sweep: position already closed")
		return
	}

	l.Info().Str("sweep_order_id", orderID).Msg("dust sweep: market sell accepted — polling for fill confirmation")

	// Remaining ctx budget after any prior limit phase is still enough for
	// the market poll because the outer context is 45s.
	budget := 30 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < budget {
			budget = remaining
		}
	}
	s.pollSweepFill(ctx, orderID, budget, tenantID, envMode, symbol, brokerOrderID, originStrategy, l, sweepFilled)
}

// buildSweepIntent constructs the OrderIntent shared by limit and market
// phases. Rationale embeds the origin strategy so per-strategy P&L can be
// reconstructed downstream via pattern match on the trade row. The raw
// Strategy field stays "dust_sweep" for the immutable audit trail (SEC
// 17a-4 / FINRA 4511).
func (s *Service) buildSweepIntent(tenantID string, envMode domain.EnvMode, symbol domain.Symbol, qty float64, brokerOrderID, originStrategy string) (domain.OrderIntent, error) {
	rationale := fmt.Sprintf("sweep remainder after exit %s (origin=%s)", brokerOrderID, originStrategy)
	return domain.NewOrderIntent(
		uuid.New(),
		tenantID,
		envMode,
		symbol,
		domain.DirectionCloseLong,
		1.0,
		0,
		0,
		qty,
		"dust_sweep",
		rationale,
		1.0,
		fmt.Sprintf("SWEEP:%s:%s:%s:%s", tenantID, string(envMode), string(symbol), brokerOrderID),
	)
}

// pollSweepFill polls the broker for the sweep order's terminal status.
// Returns (filled=true, _) on fill, (false, true) on canceled/expired/
// rejected without fill, and (false, false) on ctx/budget timeout.
func (s *Service) pollSweepFill(
	ctx context.Context,
	orderID string,
	budget time.Duration,
	tenantID string,
	envMode domain.EnvMode,
	symbol domain.Symbol,
	brokerOrderID, originStrategy string,
	l zerolog.Logger,
	sweepFilled *bool,
) (filled, terminalNoFill bool) {
	pollCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pollCtx.Done():
			return false, false
		case <-ticker.C:
			details, err := s.broker.GetOrderDetails(pollCtx, orderID)
			if err != nil {
				l.Warn().Err(err).Str("sweep_order_id", orderID).Msg("dust sweep: order-details fetch failed — retrying")
				continue
			}
			switch details.Status {
			case "filled":
				s.recordSweepFill(ctx, tenantID, envMode, symbol, orderID, brokerOrderID, originStrategy, details, l)
				*sweepFilled = true
				return true, false
			case "canceled", "expired", "rejected":
				l.Warn().Str("sweep_order_id", orderID).Str("status", details.Status).
					Msg("dust sweep: order terminal without fill")
				return false, true
			}
			// "new", "accepted", "pending_new", "partially_filled" — keep polling
		}
	}
}

// waitSweepTerminal polls briefly after a cancel to confirm the order
// reached a terminal state before the caller submits a fallback market.
// If during this wait the order actually fills, the caller treats that as
// the sweep's outcome and skips the market fallback. Returns true if the
// order filled during the wait.
func (s *Service) waitSweepTerminal(
	ctx context.Context,
	orderID string,
	tenantID string,
	envMode domain.EnvMode,
	symbol domain.Symbol,
	brokerOrderID, originStrategy string,
	l zerolog.Logger,
	sweepFilled *bool,
) bool {
	waitCtx, cancel := context.WithTimeout(ctx, dustSweepCancelConfirmWait)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			l.Warn().Str("sweep_order_id", orderID).Msg("dust sweep: cancel-confirm wait expired — proceeding with market fallback")
			return false
		case <-ticker.C:
			details, err := s.broker.GetOrderDetails(waitCtx, orderID)
			if err != nil {
				continue
			}
			switch details.Status {
			case "canceled", "expired", "rejected":
				return false
			case "filled":
				// Unusual but possible: cancel lost the race with a fill.
				s.recordSweepFill(ctx, tenantID, envMode, symbol, orderID, brokerOrderID, originStrategy, details, l)
				*sweepFilled = true
				return true
			}
		}
	}
}

// recordSweepFill persists the trade and emits FillReceived. The raw
// Strategy stays "dust_sweep" for audit immutability; origin_strategy is
// surfaced via rationale (already set on the intent) and the event payload.
func (s *Service) recordSweepFill(
	ctx context.Context,
	tenantID string,
	envMode domain.EnvMode,
	symbol domain.Symbol,
	orderID, brokerOrderID, originStrategy string,
	details ports.OrderDetails,
	l zerolog.Logger,
) {
	l.Info().
		Str("sweep_order_id", orderID).
		Float64("filled_qty", details.FilledQty).
		Float64("filled_avg_price", details.FilledAvgPrice).
		Msg("dust sweep: fill confirmed via REST — recording trade")

	fillTime := details.FilledAt
	if fillTime.IsZero() {
		fillTime = s.nowFn().UTC()
	}
	rationale := fmt.Sprintf("sweep remainder after exit %s (origin=%s)", brokerOrderID, originStrategy)
	trade, tErr := domain.NewTrade(fillTime, tenantID, envMode, uuid.New(), symbol, "SELL", details.FilledQty, details.FilledAvgPrice, 0, "FILLED", "dust_sweep", rationale)
	if tErr != nil {
		l.Error().Err(tErr).Msg("dust sweep: failed to construct trade")
		return
	}
	trade.BrokerOrderID = orderID
	if sErr := s.repo.SaveTrade(ctx, trade); sErr != nil {
		l.Error().Err(sErr).Msg("dust sweep: failed to save trade")
		return
	}

	s.emit(ctx, domain.EventFillReceived, tenantID, envMode, orderID, map[string]any{
		"broker_order_id": orderID,
		"symbol":          string(symbol),
		"side":            "SELL",
		"quantity":        details.FilledQty,
		"price":           details.FilledAvgPrice,
		"filled_at":       fillTime,
		"strategy":        "dust_sweep",
		"origin_strategy": originStrategy,
	})
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
			if errors.Is(err, ports.ErrOrderNotFound) {
				s.log.Info().Str("broker_order_id", brokerOrderID).Msg("reconcile: order not found at broker — cleaning up")
				if updErr := s.repo.UpdateOrderStatus(ctx, brokerOrderID, "canceled"); updErr != nil {
					s.log.Error().Err(updErr).Str("broker_order_id", brokerOrderID).Msg("reconcile: failed to mark vanished order canceled")
				}
				s.cleanupPendingOrder(brokerOrderID)
			} else {
				s.log.Warn().Err(err).Str("broker_order_id", brokerOrderID).Msg("reconcile: order details check failed")
			}
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

		// Early position-check: ibsync order status can be stale (stays
		// pending_new after IBKR fills it). Check livePos on every tick —
		// PositionChan updates within milliseconds of a fill.
		// Entry: position appears (qty != 0). Exit: position disappears (qty == 0).
		if details.Status != "filled" && details.Status != "canceled" {
			posQty, posErr := s.broker.GetPosition(ctx, po.intent.Symbol)
			if posErr == nil {
				entryFilled := isEntry(po.intent) && posQty != 0
				exitFilled := po.intent.Direction.IsExit() && posQty == 0
				if entryFilled || exitFilled {
					l.Info().Float64("position_qty", posQty).Bool("is_exit", exitFilled).
						Msg("reconcile: order status stale but position check confirms fill")
					s.recordFillFromDetails(po, brokerOrderID, ports.OrderDetails{
						BrokerOrderID:  brokerOrderID,
						Status:         "filled",
						FilledQty:      po.intent.Quantity,
						FilledAvgPrice: po.intent.LimitPrice,
						Symbol:         string(po.intent.Symbol),
						Side:           string(po.intent.Direction),
						Qty:            po.intent.Quantity,
					}, l)
					s.cleanupPendingOrder(brokerOrderID)
					return true
				}
			}
		}

		// Stale order timeout. Equity: 2 min. Options: configurable via meta
		// (default 120s — IBKR paper trading is slow on option limit fills).
		staleTimeout := 2 * time.Minute
		if po.intent.Instrument != nil && po.intent.Instrument.Type == domain.InstrumentTypeOption {
			staleTimeout = 120 * time.Second
			if v, ok := po.intent.Meta["stale_cancel_secs"]; ok {
				if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
					staleTimeout = time.Duration(secs) * time.Second
				}
			}
		}
		if time.Since(po.submitStart) > staleTimeout {
			if details.Status == "filled" || details.Status == "canceled" || details.Status == "expired" || details.Status == "rejected" {
				return true
			}

			// Skip stale-cancel for exit orders — the position monitor owns
			// exit retry/escalation logic. Canceling here would race with
			// the monitor's handleExitTimeout which already cancels and
			// resubmits with escalated pricing (limit -> market).
			if po.intent.Direction.IsExit() {
				l.Debug().Msg("reconcile: skipping stale cancel for exit order — position monitor owns retry")
				return true
			}

			// Market orders should fill or get rejected by the exchange.
			// Position check already ran above — if we're here, no position
			// was detected yet. Don't cancel, keep waiting.
			if po.intent.OrderType == "market" {
				l.Info().Msg("reconcile: skipping stale cancel for market order — waiting for exchange fill/reject")
				return true
			}

			if err := s.broker.CancelOrder(ctx, brokerOrderID); err != nil {
				l.Warn().Err(err).Msg("reconcile: failed to cancel stale order — may already be terminal")
			} else {
				l.Info().Msg("reconcile: canceled stale order on broker")
			}

			postCancel, err := s.broker.GetOrderDetails(ctx, brokerOrderID)
			if err == nil && (postCancel.FilledQty > 0 || postCancel.Status == "filled") {
				l.Info().Float64("filled_qty", postCancel.FilledQty).Str("status", postCancel.Status).Msg("reconcile: stale order was actually filled — recording")
				s.recordFillFromDetails(po, brokerOrderID, postCancel, l)
				return true
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
		filledAt = s.nowFn().UTC()
	}
	s.handleFillWithPrice(po, brokerOrderID, fillPrice, fillQty, filledAt, "", l)
}

// exitLimitBuffer returns the IOC limit price buffer (as a fraction) for exit orders.
// Options need much wider buffers (5-20% bid/ask spreads on IBKR paper).
// Wide-spread crypto assets get a larger buffer to avoid instant cancellation.
func exitLimitBuffer(sym domain.Symbol, ac domain.AssetClass) float64 {
	// Options: use 5% buffer. OCC symbols are 21 chars (e.g. "AAPL  260620C00150000").
	// The position monitor already uses market on retry, but this covers the
	// first-attempt limit path in the execution service.
	if domain.IsOCCSymbol(sym) {
		return 0.05 // 500bps for options — their spreads are 5-20%
	}
	if ac == domain.AssetClassCrypto {
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
	return 0.001 // 10bps for equities
}

func (s *Service) emit(ctx context.Context, eventType string, tenantID string, envMode domain.EnvMode, idempotencyKey string, payload any) {
	ev, err := domain.NewEvent(eventType, tenantID, envMode, idempotencyKey, payload)
	if err != nil {
		return
	}
	_ = s.eventBus.Publish(ctx, *ev)
}
