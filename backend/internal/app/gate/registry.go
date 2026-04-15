package gate

import (
	"context"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// MonitorGateFactory creates a MonitorGate from TOML params and shared dependencies.
type MonitorGateFactory func(params map[string]any, deps *GateDeps) (MonitorGate, error)

// GateDeps holds shared dependencies that gates may need.
type GateDeps struct {
	DNAGate     DNAGateChecker   // for dna_approval gate
	TideTracker *IndexTideTracker // for market_tide gate
}

// DNAGateChecker is the interface for DNA approval checking.
// Defined here to avoid importing the monitor package.
type DNAGateChecker interface {
	IsDNAApproved(ctx context.Context, strategyKey string) (bool, error)
}

// GateConfig represents one entry in the TOML [gate_chain.monitor] array.
type GateConfig struct {
	Name   string         `toml:"name"`
	Params map[string]any `toml:"params"`
}

// MonitorGateRegistry maps gate names to their factory functions.
type MonitorGateRegistry struct {
	factories map[string]MonitorGateFactory
}

// NewMonitorGateRegistry creates an empty registry.
func NewMonitorGateRegistry() *MonitorGateRegistry {
	return &MonitorGateRegistry{
		factories: make(map[string]MonitorGateFactory),
	}
}

// Register adds a factory for the given gate name.
func (r *MonitorGateRegistry) Register(name string, factory MonitorGateFactory) {
	r.factories[name] = factory
}

// BuildChain creates a MonitorGateChain by instantiating each gate config
// through its registered factory. Returns an error if any gate name is unknown.
func (r *MonitorGateRegistry) BuildChain(configs []GateConfig, deps *GateDeps, log zerolog.Logger) (*MonitorGateChain, error) {
	gates := make([]MonitorGate, 0, len(configs))
	for _, cfg := range configs {
		factory, ok := r.factories[cfg.Name]
		if !ok {
			return nil, fmt.Errorf("gate: unknown monitor gate %q", cfg.Name)
		}
		g, err := factory(cfg.Params, deps)
		if err != nil {
			return nil, fmt.Errorf("gate: building %q: %w", cfg.Name, err)
		}
		gates = append(gates, g)
	}
	return NewMonitorGateChain(gates, log), nil
}

// NewDefaultRegistry returns a registry with all built-in monitor gates registered.
func NewDefaultRegistry() *MonitorGateRegistry {
	r := NewMonitorGateRegistry()
	r.Register("dna_approval", newDNAApprovalGate)
	r.Register("vix", newVIXGate)
	r.Register("regime", newRegimeGate)
	r.Register("htf_bias", newHTFBiasGate)
	r.Register("min_atr_pct", newMinATRPctGate)
	r.Register("market_tide", newMarketTideGate)
	return r
}

// ---------------------------------------------------------------------------
// Execution gate registry
// ---------------------------------------------------------------------------

// ExecutionGateFactory creates an ExecutionGate from TOML params and shared dependencies.
type ExecutionGateFactory func(params map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error)

// ExecutionGateDeps holds the concrete guard instances injected at bootstrap time.
// Each field is an interface so the gate package does not import the execution package.
type ExecutionGateDeps struct {
	ExposureGuard      ExposureChecker
	PortfolioGuard     PortfolioChecker
	PortfolioHeatGuard PortfolioHeatChecker
	RiskEngine         RiskValidator
	OptionsRiskEngine  OptionsRiskValidator
	SlippageGuard      SlippageChecker
	TradingWindowGuard TradingWindowChecker
	SpreadGuard        SpreadChecker
	BuyingPowerGuard   BuyingPowerChecker
}

// Minimal interfaces for each execution guard.

// ExposureChecker validates exposure limits.
type ExposureChecker interface {
	Check(ctx context.Context, intent domain.OrderIntent) error
}

// PortfolioChecker validates portfolio constraints.
type PortfolioChecker interface {
	Check(ctx context.Context, intent domain.OrderIntent) error
}

// PortfolioHeatChecker validates that the aggregate risk across open
// positions plus the proposed intent stays below the configured max
// heat fraction of account equity.
type PortfolioHeatChecker interface {
	Check(ctx context.Context, intent domain.OrderIntent) error
}

// RiskValidator validates equity risk limits.
type RiskValidator interface {
	Validate(intent domain.OrderIntent, accountEquity float64) error
}

// OptionsRiskValidator validates options-specific risk limits.
type OptionsRiskValidator interface {
	ValidateOptionIntent(intent domain.OrderIntent, accountEquity float64) error
}

// SlippageChecker validates slippage limits.
type SlippageChecker interface {
	Check(ctx context.Context, intent domain.OrderIntent) error
}

// TradingWindowChecker validates trading window constraints.
type TradingWindowChecker interface {
	Check(intent domain.OrderIntent) error
}

// SpreadChecker validates bid-ask spread constraints.
type SpreadChecker interface {
	Check(ctx context.Context, intent domain.OrderIntent) error
}

// BuyingPowerChecker validates buying power constraints.
type BuyingPowerChecker interface {
	Check(ctx context.Context, intent domain.OrderIntent) error
}

// ExecutionGateRegistry maps gate names to their factory functions.
type ExecutionGateRegistry struct {
	factories map[string]ExecutionGateFactory
}

// NewExecutionGateRegistry creates an empty registry.
func NewExecutionGateRegistry() *ExecutionGateRegistry {
	return &ExecutionGateRegistry{
		factories: make(map[string]ExecutionGateFactory),
	}
}

// Register adds a factory for the given gate name.
func (r *ExecutionGateRegistry) Register(name string, factory ExecutionGateFactory) {
	r.factories[name] = factory
}

// BuildChain creates an ExecutionGateChain by instantiating each gate config
// through its registered factory. Returns an error if any gate name is unknown.
func (r *ExecutionGateRegistry) BuildChain(configs []GateConfig, deps *ExecutionGateDeps, log zerolog.Logger) (*ExecutionGateChain, error) {
	gates := make([]ExecutionGate, 0, len(configs))
	for _, cfg := range configs {
		factory, ok := r.factories[cfg.Name]
		if !ok {
			return nil, fmt.Errorf("gate: unknown execution gate %q", cfg.Name)
		}
		g, err := factory(cfg.Params, deps)
		if err != nil {
			return nil, fmt.Errorf("gate: building %q: %w", cfg.Name, err)
		}
		gates = append(gates, g)
	}
	return NewExecutionGateChain(gates, log), nil
}

// NewDefaultExecutionRegistry returns a registry with all built-in execution gates registered.
func NewDefaultExecutionRegistry() *ExecutionGateRegistry {
	r := NewExecutionGateRegistry()
	r.Register("short_direction", newShortDirectionGate)
	r.Register("exposure_guard", newExposureGate)
	r.Register("portfolio_guard", newPortfolioGate)
	r.Register("portfolio_heat_guard", newPortfolioHeatGate)
	r.Register("risk_engine", newRiskGate)
	r.Register("slippage_guard", newSlippageGate)
	r.Register("trading_window", newTradingWindowGate)
	r.Register("spread_guard", newSpreadGate)
	r.Register("buying_power_guard", newBuyingPowerGate)
	return r
}
