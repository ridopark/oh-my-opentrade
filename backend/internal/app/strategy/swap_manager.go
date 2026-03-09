package strategy

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// SwapRequest represents a pending blue/green swap. The "green" (new) instance
// runs as a shadow — it receives bars but its signals are suppressed because
// it starts in Draft lifecycle. Once it finishes warmup on ALL assigned symbols,
// the swap is executed atomically at the next bar boundary.
type SwapRequest struct {
	OldInstanceID  start.InstanceID
	NewInstance    *Instance
	pendingSymbols map[string]bool
}

// IsPending returns true if any symbols still need warmup.
func (sr *SwapRequest) IsPending() bool {
	for _, pending := range sr.pendingSymbols {
		if pending {
			return true
		}
	}
	return false
}

// SwapManager handles the blue/green swap lifecycle:
// 1. Accept a swap request (old instance ID → new instance)
// 2. Initialize the new instance with state handoff from the old instance
// 3. Feed bars to the new instance (shadow mode — Draft lifecycle suppresses signals)
// 4. Once warmup is complete on all symbols, execute atomic swap
//
// The SwapManager integrates with the Runner: after each bar is processed,
// the Runner calls SwapManager.OnBarProcessed() to feed shadow instances
// and check if any swaps are ready.
type SwapManager struct {
	mu      sync.Mutex
	router  *Router
	pending map[start.InstanceID]*SwapRequest // keyed by OLD instance ID
	logger  *slog.Logger
}

// NewSwapManager creates a SwapManager that uses the given router for
// atomic instance replacement.
func NewSwapManager(router *Router, logger *slog.Logger) *SwapManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &SwapManager{
		router:  router,
		pending: make(map[start.InstanceID]*SwapRequest),
		logger:  logger.With("component", "swap_manager"),
	}
}

// RequestSwap initiates a blue/green swap. The new instance is initialized
// with state from the old instance (if available). The new instance starts
// in Draft lifecycle (signals suppressed) and will be promoted to the old
// instance's lifecycle once warmup completes.
//
// Returns an error if:
// - The old instance is not found in the router
// - A swap is already pending for the old instance
// - Initialization fails for any symbol
func (sm *SwapManager) RequestSwap(oldID start.InstanceID, newInst *Instance) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, exists := sm.pending[oldID]; exists {
		return fmt.Errorf("swap already pending for instance %s", oldID)
	}

	oldInst, ok := sm.router.Instance(oldID)
	if !ok {
		return fmt.Errorf("%w: %s", start.ErrInstanceNotFound, oldID)
	}

	newInst.SetLifecycle(start.LifecycleDraft)

	symbols := newInst.Assignment().Symbols
	pendingSymbols := make(map[string]bool, len(symbols))

	for _, sym := range symbols {
		var prior start.State

		if oldState, found := oldInst.GetState(sym); found {
			prior = oldState
		}

		ctx := NewContext(
			time.Now(),
			sm.logger.With("symbol", sym, "new_instance", newInst.ID().String()),
			nil,
		)

		if err := newInst.InitSymbol(ctx, sym, prior); err != nil {
			sm.logger.Warn("state handoff failed, initializing without prior state",
				"old_instance", oldID.String(),
				"new_instance", newInst.ID().String(),
				"symbol", sym,
				"error", err,
			)
			if err := newInst.InitSymbol(ctx, sym, nil); err != nil {
				return fmt.Errorf("failed to initialize new instance for symbol %s: %w", sym, err)
			}
		}

		pendingSymbols[sym] = true
	}

	sm.pending[oldID] = &SwapRequest{
		OldInstanceID:  oldID,
		NewInstance:    newInst,
		pendingSymbols: pendingSymbols,
	}

	sm.logger.Info("swap requested",
		"old_instance", oldID.String(),
		"new_instance", newInst.ID().String(),
		"symbols", symbols,
		"warmup_bars", newInst.Strategy().WarmupBars(),
	)

	return nil
}

// OnBarProcessed is called by the Runner after processing a bar for a symbol.
// It feeds the bar to any shadow instances that are warming up for that symbol,
// and triggers the atomic swap if warmup is complete.
//
// Returns a list of completed swap descriptions (for logging/events).
func (sm *SwapManager) OnBarProcessed(ctx start.Context, symbol string, bar start.Bar, indicators start.IndicatorData) []CompletedSwap {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	var completed []CompletedSwap

	for oldID, req := range sm.pending {
		if !req.pendingSymbols[symbol] {
			continue
		}

		err := req.NewInstance.WarmupOnBar(ctx, symbol, bar, indicators)
		if err != nil {
			sm.logger.Error("shadow instance OnBar failed",
				"old_instance", oldID.String(),
				"new_instance", req.NewInstance.ID().String(),
				"symbol", symbol,
				"error", err,
			)
			continue
		}

		if req.NewInstance.IsWarmedUp(symbol) {
			req.pendingSymbols[symbol] = false
		}

		if !req.IsPending() {
			swap := sm.executeSwap(oldID, req)
			completed = append(completed, swap)
			delete(sm.pending, oldID)
		}
	}

	return completed
}

// CompletedSwap describes a swap that was successfully executed.
type CompletedSwap struct {
	OldInstanceID start.InstanceID
	NewInstanceID start.InstanceID
	OldLifecycle  start.LifecycleState
}

// executeSwap performs the atomic swap: promotes new instance to old's lifecycle,
// archives the old instance, and atomically replaces in the router.
// Must be called with sm.mu held.
func (sm *SwapManager) executeSwap(oldID start.InstanceID, req *SwapRequest) CompletedSwap {
	oldInst, _ := sm.router.Instance(oldID)
	oldLifecycle := start.LifecyclePaperActive
	if oldInst != nil {
		oldLifecycle = oldInst.Lifecycle()
	}

	req.NewInstance.SetLifecycle(oldLifecycle)

	replaced := sm.router.Replace(oldID, req.NewInstance)

	if replaced != nil {
		replaced.SetLifecycle(start.LifecycleArchived)
	}

	sm.logger.Info("swap completed",
		"old_instance", oldID.String(),
		"new_instance", req.NewInstance.ID().String(),
		"lifecycle", oldLifecycle.String(),
	)

	return CompletedSwap{
		OldInstanceID: oldID,
		NewInstanceID: req.NewInstance.ID(),
		OldLifecycle:  oldLifecycle,
	}
}

// PendingSwaps returns the number of pending swap requests.
func (sm *SwapManager) PendingSwaps() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return len(sm.pending)
}

// CancelSwap cancels a pending swap for the given old instance ID.
// Returns true if a swap was canceled, false if none was pending.
func (sm *SwapManager) CancelSwap(oldID start.InstanceID) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, exists := sm.pending[oldID]; exists {
		delete(sm.pending, oldID)
		sm.logger.Info("swap canceled", "old_instance", oldID.String())
		return true
	}
	return false
}

// HasPendingSwap returns true if there's a pending swap for the given old instance.
func (sm *SwapManager) HasPendingSwap(oldID start.InstanceID) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	_, exists := sm.pending[oldID]
	return exists
}
