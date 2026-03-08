package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/perf"
	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/app/symbolrouter"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/oh-my-opentrade/backend/internal/ports"
	stratports "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/rs/zerolog"
)

// SharedDeps are dependencies shared across all account runtimes.
// Per-account wires should still create their own broker adapter instances and
// per-account services; only the shared infrastructure (bus, repos, spec store,
// market data stream) is passed through this struct.
type SharedDeps struct {
	EventBus   ports.EventBusPort
	Repo       ports.RepositoryPort
	PnLRepo    ports.PnLPort
	MarketData ports.MarketDataPort
	SpecStore  stratports.SpecStore
	Metrics    *metrics.Metrics
	Log        zerolog.Logger
}

type EquitySource interface {
	GetAccountEquity(ctx context.Context) (float64, error)
}

type Closable interface {
	Close() error
}

type AccountHandle struct {
	TenantID string
	Label    string
	EnvMode  domain.EnvMode

	Equity EquitySource
	Close  Closable

	Execution        *execution.Service
	LedgerWriter     *perf.LedgerWriter
	DailyLossBreaker *risk.DailyLossBreaker

	StrategyRunner *strategy.Runner
	RiskSizer      *strategy.RiskSizer
	Lifecycle      *strategy.LifecycleService
	SymbolRouter   *symbolrouter.Service

	cancel context.CancelFunc
}

func (a *AccountHandle) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
	if a.Close != nil {
		_ = a.Close.Close()
	}
}

type AccountOrchestrator struct {
	shared       SharedDeps
	refreshEvery time.Duration

	mu       sync.RWMutex
	accounts map[string]*AccountHandle

	started   atomic.Bool
	globalHlt atomic.Bool

	ctx    context.Context
	cancel context.CancelFunc
}

func New(shared SharedDeps) *AccountOrchestrator {
	return &AccountOrchestrator{
		shared:       shared,
		refreshEvery: 5 * time.Minute,
		accounts:     make(map[string]*AccountHandle),
	}
}

// SetMetrics wires Prometheus metrics into the orchestrator after construction.
// Must be called before Start() for metrics to propagate to per-account services.
func (o *AccountOrchestrator) SetMetrics(m *metrics.Metrics) {
	o.shared.Metrics = m
}

func (o *AccountOrchestrator) Add(h *AccountHandle) error {
	if h == nil {
		return fmt.Errorf("orchestrator: account handle is nil")
	}
	if h.TenantID == "" {
		return fmt.Errorf("orchestrator: account handle missing tenant_id")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.accounts[h.TenantID]; ok {
		return fmt.Errorf("orchestrator: duplicate tenant_id %q", h.TenantID)
	}
	o.accounts[h.TenantID] = h
	return nil
}

func (o *AccountOrchestrator) Accounts() []*AccountHandle {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]*AccountHandle, 0, len(o.accounts))
	for _, h := range o.accounts {
		out = append(out, h)
	}
	return out
}

func (o *AccountOrchestrator) GlobalHalt() { o.globalHlt.Store(true) }

func (o *AccountOrchestrator) IsGloballyHalted() bool { return o.globalHlt.Load() }

func (o *AccountOrchestrator) Start(parent context.Context) error {
	if o.started.Swap(true) {
		return nil
	}
	o.ctx, o.cancel = context.WithCancel(parent)

	for _, h := range o.Accounts() {
		if err := o.startAccount(h); err != nil {
			o.Stop()
			return err
		}
	}
	return nil
}

func (o *AccountOrchestrator) startAccount(h *AccountHandle) error {
	ctx, cancel := context.WithCancel(o.ctx)
	h.cancel = cancel

	if o.shared.Metrics != nil {
		if h.Execution != nil {
			h.Execution.SetMetrics(o.shared.Metrics)
		}
		if h.LedgerWriter != nil {
			h.LedgerWriter.SetMetrics(o.shared.Metrics)
		}
		if h.DailyLossBreaker != nil {
			h.DailyLossBreaker.SetMetrics(o.shared.Metrics)
		}
		if h.StrategyRunner != nil {
			h.StrategyRunner.SetMetrics(o.shared.Metrics)
		}
	}
	if h.DailyLossBreaker != nil {
		h.DailyLossBreaker.SetGlobalHalt(o.IsGloballyHalted)
	}

	if h.LedgerWriter == nil || h.Execution == nil {
		return fmt.Errorf("orchestrator: tenant %q missing required services", h.TenantID)
	}
	if err := h.LedgerWriter.Start(ctx, h.TenantID, h.EnvMode); err != nil {
		return fmt.Errorf("orchestrator: tenant %q ledger start: %w", h.TenantID, err)
	}
	if err := h.Execution.Start(ctx); err != nil {
		return fmt.Errorf("orchestrator: tenant %q execution start: %w", h.TenantID, err)
	}
	if h.StrategyRunner != nil {
		if err := h.StrategyRunner.Start(ctx); err != nil {
			return fmt.Errorf("orchestrator: tenant %q strategy runner start: %w", h.TenantID, err)
		}
	}
	if h.RiskSizer != nil {
		if err := h.RiskSizer.Start(ctx); err != nil {
			return fmt.Errorf("orchestrator: tenant %q risk sizer start: %w", h.TenantID, err)
		}
	}
	if h.SymbolRouter != nil {
		if err := h.SymbolRouter.Start(ctx); err != nil {
			return fmt.Errorf("orchestrator: tenant %q symbol router start: %w", h.TenantID, err)
		}
	}

	go o.equityRefreshLoop(ctx, h)

	o.shared.Log.Info().
		Str("tenant_id", h.TenantID).
		Str("label", h.Label).
		Msg("orchestrator: account runtime started")
	return nil
}

func (o *AccountOrchestrator) equityRefreshLoop(ctx context.Context, h *AccountHandle) {
	if h.Equity == nil {
		return
	}
	logger := o.shared.Log.With().Str("tenant_id", h.TenantID).Str("component", "equity_refresh").Logger()
	if eq, err := h.Equity.GetAccountEquity(ctx); err == nil && eq > 0 {
		o.applyEquity(h, eq)
		logger.Info().Float64("equity", eq).Msg("account equity refreshed")
	}

	t := time.NewTicker(o.refreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			eq, err := h.Equity.GetAccountEquity(ctx)
			if err != nil {
				logger.Warn().Err(err).Msg("failed to refresh account equity")
				continue
			}
			if eq <= 0 {
				continue
			}
			o.applyEquity(h, eq)
			logger.Info().Float64("equity", eq).Msg("account equity refreshed")
		}
	}
}

func (o *AccountOrchestrator) applyEquity(h *AccountHandle, equity float64) {
	if h.Execution != nil {
		h.Execution.SetAccountEquity(equity)
	}
	if h.LedgerWriter != nil {
		h.LedgerWriter.SetAccountEquity(equity)
	}
	if h.RiskSizer != nil {
		h.RiskSizer.SetAccountEquity(equity)
	}
}

func (o *AccountOrchestrator) Stop() {
	if o.cancel != nil {
		o.cancel()
	}
	for _, h := range o.Accounts() {
		h.Stop()
	}
}
