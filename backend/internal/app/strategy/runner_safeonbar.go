package strategy

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/oh-my-opentrade/backend/internal/ports"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// SetNotifier injects a notifier used to surface recovered strategy panics
// to operators (Discord, etc.). Safe to leave nil — the runner will still
// log + increment the prometheus panic counter without emitting notifications.
func (r *Runner) SetNotifier(n ports.NotifierPort) { r.notifier = n }

// safeOnBar invokes inst.OnBar with panic recovery.
//
// Rationale: before this helper, a single nil-pointer dereference or slice
// out-of-bounds in ANY strategy instance would panic the entire runner
// goroutine and crash omo-core, taking down strategy processing for all 34
// symbols at once. This matches the "FAULTED component" isolation pattern
// from NautilusTrader: a single faulty strategy is quarantined to the failing
// instance+bar while everything else keeps running.
//
// Behavior on panic:
//   - recover the panic
//   - log the instance id, symbol, panic value, and full stack
//   - increment r.metrics.Strategy.Panics (if metrics are wired)
//   - emit a notifier alert (if a notifier is wired)
//   - return (nil, error) so the caller's existing error path skips the instance
func (r *Runner) safeOnBar(
	inst *Instance,
	instCtx *instanceContext,
	symbol string,
	bar start.Bar,
	indicators start.IndicatorData,
) (signals []start.Signal, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			stack := debug.Stack()
			instID := inst.ID().String()
			r.logger.Error("instance OnBar panicked",
				"instance_id", instID,
				"symbol", symbol,
				"panic", rec,
				"stack", string(stack),
			)
			if r.metrics != nil {
				r.metrics.Strategy.Panics.WithLabelValues(instID, symbol).Inc()
			}
			if r.notifier != nil {
				_ = r.notifier.Notify(
					context.Background(),
					r.tenantID,
					fmt.Sprintf("strategy %s panicked on %s: %v", instID, symbol, rec),
				)
			}
			signals = nil
			err = fmt.Errorf("strategy %s panicked: %v", instID, rec)
		}
	}()
	return inst.OnBar(instCtx, symbol, bar, indicators)
}

// safeWarmupOnBar invokes inst.WarmupOnBar with panic recovery. Same isolation
// semantics as safeOnBar; warmup replay errors must never crash the runner.
func (r *Runner) safeWarmupOnBar(
	inst *Instance,
	instCtx *instanceContext,
	symbol string,
	bar start.Bar,
	indicators start.IndicatorData,
) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			stack := debug.Stack()
			instID := inst.ID().String()
			r.logger.Error("instance WarmupOnBar panicked",
				"instance_id", instID,
				"symbol", symbol,
				"panic", rec,
				"stack", string(stack),
			)
			if r.metrics != nil {
				r.metrics.Strategy.Panics.WithLabelValues(instID, symbol).Inc()
			}
			if r.notifier != nil {
				_ = r.notifier.Notify(
					context.Background(),
					r.tenantID,
					fmt.Sprintf("strategy %s panicked during warmup on %s: %v", instID, symbol, rec),
				)
			}
			err = fmt.Errorf("strategy %s panicked during warmup: %v", instID, rec)
		}
	}()
	return inst.WarmupOnBar(instCtx, symbol, bar, indicators)
}
