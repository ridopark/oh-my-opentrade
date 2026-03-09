package strategy

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"sync"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
)

// Service is the strategy application service.
// It subscribes to SetupDetected events, matches a StrategyDNA,
// filters by regime, computes order parameters, and emits OrderIntentCreated.
type Service struct {
	eventBus      ports.EventBusPort
	mu            sync.RWMutex
	dnas          map[string]*StrategyDNA // keyed by strategy ID
	accountEquity float64
	equityMu      sync.RWMutex
}

// NewService creates a new strategy Service backed by the given event bus.
func NewService(eventBus ports.EventBusPort) *Service {
	return &Service{
		eventBus:      eventBus,
		dnas:          make(map[string]*StrategyDNA),
		accountEquity: 100000.0, // conservative fallback until real equity is set
	}
}

// RegisterDNA stores a StrategyDNA so it can be looked up during setup handling.
// This is also used by DNAManager callers to register loaded DNAs.
func (s *Service) RegisterDNA(dna *StrategyDNA) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dnas[dna.ID] = dna
}

// SetAccountEquity updates the account equity used for position sizing.
// Safe to call concurrently (e.g. from a periodic refresh goroutine).
func (s *Service) SetAccountEquity(equity float64) {
	if equity <= 0 {
		return
	}
	s.equityMu.Lock()
	defer s.equityMu.Unlock()
	s.accountEquity = equity
}

// Start subscribes the service to SetupDetected events on the event bus.
func (s *Service) Start(ctx context.Context) error {
	if err := s.eventBus.Subscribe(ctx, domain.EventSetupDetected, s.handleSetup); err != nil {
		return fmt.Errorf("strategy: failed to subscribe to SetupDetected: %w", err)
	}
	return nil
}

// handleSetup processes a SetupDetected event.
func (s *Service) handleSetup(ctx context.Context, event domain.Event) error {
	setup, ok := event.Payload.(monitor.SetupCondition)
	if !ok {
		return nil
	}

	// 1. Look up DNA — use the first registered DNA if no symbol-specific one exists.
	dna := s.lookupDNA()

	// 2. Apply regime filter if DNA has one.
	if dna != nil && len(dna.RegimeFilter.AllowedRegimes) > 0 {
		if !s.regimeAllowed(setup.Regime, dna.RegimeFilter) {
			return nil
		}
	}

	// 3. Compute limit price and stop loss.
	limitPrice, stopLoss := s.computePrices(setup, dna)

	// 4. Compute position size from risk parameters.
	s.equityMu.RLock()
	equity := s.accountEquity
	s.equityMu.RUnlock()

	maxRiskBPS := 200 // default 2%
	if dna != nil {
		if v, ok := extractInt(dna.Parameters, "max_risk_bps"); ok {
			maxRiskBPS = v
		}
	}
	maxRiskUSD := (float64(maxRiskBPS) / 10000.0) * equity
	riskPerShare := math.Abs(limitPrice - stopLoss)
	qty := 1.0 // floor minimum
	if riskPerShare > 0 && maxRiskUSD > 0 {
		computed := math.Floor(maxRiskUSD / riskPerShare)
		if computed >= 1 {
			qty = computed
		}
	}

	// Clamp position size so notional exposure does not exceed max_position_bps of equity.
	maxPositionBPS := 1000 // default 10% of equity
	if dna != nil {
		if v, ok := extractInt(dna.Parameters, "max_position_bps"); ok && v > 0 {
			maxPositionBPS = v
		}
	}
	if limitPrice > 0 {
		maxNotional := (float64(maxPositionBPS) / 10000.0) * equity
		maxQty := math.Floor(maxNotional / limitPrice)
		if maxQty < 1 {
			maxQty = 1
		}
		if qty > maxQty {
			qty = maxQty
		}
	}

	// 5. Build OrderIntent.
	intentID := uuid.New()
	strategyName := "default"
	if dna != nil {
		strategyName = dna.ID
	}

	intent, err := domain.NewOrderIntent(
		intentID,
		event.TenantID,
		event.EnvMode,
		setup.Symbol,
		setup.Direction,
		limitPrice,
		stopLoss,
		10, // maxSlippageBPS
		qty,
		strategyName,
		"computed by strategy DNA engine",
		orbConfidenceFromDNA(dna), // use DNA min_confidence as default signal confidence
		intentID.String(),
	)
	if err != nil {
		return fmt.Errorf("strategy: failed to create order intent: %w", err)
	}

	// 6. Emit OrderIntentCreated.
	s.emit(ctx, domain.EventOrderIntentCreated, event.TenantID, event.EnvMode, intentID.String(), intent)
	return nil
}

// lookupDNA returns the first registered DNA, or nil if none are registered.
// In a production system this would be keyed by symbol or strategy selector.
func (s *Service) lookupDNA() *StrategyDNA {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, dna := range s.dnas {
		return dna
	}
	return nil
}

// regimeAllowed checks whether the setup's regime passes the DNA filter.
func (s *Service) regimeAllowed(regime domain.MarketRegime, filter RegimeFilter) bool {
	regimeStr := regime.Type.String()
	allowed := false
	for _, r := range filter.AllowedRegimes {
		if r == regimeStr {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}
	if regime.Strength < filter.MinRegimeStrength {
		return false
	}
	return true
}

// computePrices uses the DNA's optional script (via Yaegi) or the deterministic
// formula to compute limit price and stop loss.
func (s *Service) computePrices(setup monitor.SetupCondition, dna *StrategyDNA) (limitPrice, stopLoss float64) {
	limitOffsetBPS := 5
	stopBPS := 10

	if dna != nil {
		if v, ok := extractInt(dna.Parameters, "limit_offset_bps"); ok {
			limitOffsetBPS = v
		}
		if v, ok := extractInt(dna.Parameters, "stop_bps_below_low"); ok {
			stopBPS = v
		}

		// Try Yaegi script execution.
		if scriptVal, hasScript := dna.Parameters["script"]; hasScript {
			if script, ok := scriptVal.(string); ok {
				lp, sl, err := runScript(script, setup.Snapshot.VWAP, setup.Snapshot.EMA21, stopBPS, limitOffsetBPS)
				if err == nil {
					return lp, sl
				}
				// Fall through to deterministic formula on script error.
			}
		}
	}

	// Deterministic formula: use bar close as reference price.
	// Fall back to VWAP then EMA21 if close is somehow zero.
	close := setup.BarClose
	if close <= 0 {
		close = setup.Snapshot.VWAP
	}
	if close <= 0 {
		close = setup.Snapshot.EMA21
	}
	if close <= 0 {
		close = 100.0
	}

	// Stop is placed below the bar low (EMA21 as proxy when no low available).
	low := setup.Snapshot.EMA21
	if low <= 0 {
		low = close * 0.99
	}
	limitPrice = close * (1.0 + float64(limitOffsetBPS)/10000.0)
	stopLoss = low * (1.0 - float64(stopBPS)/10000.0)
	return limitPrice, stopLoss
}

// extractInt retrieves an integer from a parameters map, handling both int and int64.
func extractInt(params map[string]any, key string) (int, bool) {
	v, ok := params[key]
	if !ok {
		return 0, false
	}
	// TOML decodes integers as int64.
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return int(rv.Float()), true
	}
	return 0, false
}

// orbConfidenceFromDNA extracts min_confidence from the DNA parameters.
// Falls back to 0.65 (the ORBConfig default) if no DNA is registered.
func orbConfidenceFromDNA(dna *StrategyDNA) float64 {
	if dna == nil {
		return 0.65
	}
	v, ok := dna.Parameters["min_confidence"]
	if !ok {
		return 0.65
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int())
	}
	return 0.65
}

// runScript executes a Go script via Yaegi.
// The script must define a top-level function:
//
//	func Compute(close, low float64, stopBPS, limitOffsetBPS int) (float64, float64)
func runScript(script string, close, low float64, stopBPS, limitOffsetBPS int) (limitPrice, stopLoss float64, err error) {
	i := interp.New(interp.Options{})
	_ = i.Use(stdlib.Symbols)

	if _, err = i.Eval(script); err != nil {
		return 0, 0, fmt.Errorf("strategy: script eval error: %w", err)
	}

	v, err := i.Eval("main.Compute")
	if err != nil {
		return 0, 0, fmt.Errorf("strategy: script Compute symbol not found: %w", err)
	}

	fn, ok := v.Interface().(func(float64, float64, int, int) (float64, float64))
	if !ok {
		return 0, 0, fmt.Errorf("strategy: script Compute has wrong signature, got %T", v.Interface())
	}

	limitPrice, stopLoss = fn(close, low, stopBPS, limitOffsetBPS)
	return limitPrice, stopLoss, nil
}

// emit publishes a domain event, discarding errors (best-effort).
func (s *Service) emit(ctx context.Context, eventType string, tenantID string, envMode domain.EnvMode, idempotencyKey string, payload any) {
	ev, err := domain.NewEvent(eventType, tenantID, envMode, idempotencyKey, payload)
	if err != nil {
		return
	}
	_ = s.eventBus.Publish(ctx, *ev)
}

// ValidateScript verifies that a Go script compiles under Yaegi and exposes
// the required Compute function with the correct signature.
// Returns nil if the script is valid, an error otherwise.
func ValidateScript(script string) error {
	_, _, err := runScript(script, 100.0, 99.0, 10, 5)
	if err != nil {
		return err
	}
	return nil
}
