package execution

import (
	"context"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// GateError carries the name of the gate that rejected an intent and the
// underlying reason. Returned by Service.ValidateIntent.
type GateError struct {
	Gate   string
	Reason string
}

func (e *GateError) Error() string { return e.Gate + ": " + e.Reason }

// gateOrder returns the canonical ValidateIntent gate sequence. Pinned by
// TestValidateIntent_GateOrder_LockstepWithProcess against the order
// process() calls these gates at service.go:828-949 + :986-987.
func (s *Service) gateOrder() []string {
	return []string{
		"kill_switch",
		"position_gate",
		"exposure_guard",
		"portfolio_guard",
		"risk_engine",
		"slippage",
		"trading_window",
		"daily_loss",
	}
}

// ValidateIntent runs the same gate sequence as process() but uses the
// read-only DailyLossBreaker.Inspect instead of Check. No side effects:
// no MarkInflight, no metrics, no transitionOnTrip. Returns *GateError
// naming the failing gate, or nil if every gate passes.
//
// Gate order MUST stay in lockstep with process() (service.go:828-949 +
// :986-987) and Service.gateOrder().
func (s *Service) ValidateIntent(ctx context.Context, intent domain.OrderIntent) *GateError {
	isExit := intent.Direction.IsExit()

	if !isExit && s.killSwitch != nil && s.killSwitch.IsHalted(intent.TenantID, intent.Symbol) {
		return &GateError{Gate: "kill_switch", Reason: "halted"}
	}

	if s.positionGate != nil {
		if err := s.positionGate.Check(ctx, intent); err != nil {
			return &GateError{Gate: "position_gate", Reason: err.Error()}
		}
	}

	if !isExit {
		if s.exposureGuard != nil {
			if err := s.exposureGuard.Check(ctx, intent); err != nil {
				return &GateError{Gate: "exposure_guard", Reason: err.Error()}
			}
		}
		if s.portfolioGuard != nil {
			if err := s.portfolioGuard.Check(ctx, intent); err != nil {
				return &GateError{Gate: "portfolio_guard", Reason: err.Error()}
			}
		}
		if s.riskEngine != nil {
			if err := s.riskEngine.Validate(intent, s.accountEquity); err != nil {
				return &GateError{Gate: "risk_engine", Reason: err.Error()}
			}
		}
		if s.slippageGuard != nil {
			if err := s.slippageGuard.Check(ctx, intent); err != nil {
				return &GateError{Gate: "slippage", Reason: err.Error()}
			}
		}
		if s.tradingWindowGuard != nil {
			if err := s.tradingWindowGuard.Check(intent); err != nil {
				return &GateError{Gate: "trading_window", Reason: err.Error()}
			}
		}
	}

	if !isExit && s.dailyLossBreaker != nil {
		lossUSD, _, tripped, err := s.dailyLossBreaker.Inspect(intent.TenantID, intent.EnvMode, s.accountEquity)
		if err != nil {
			return &GateError{Gate: "daily_loss", Reason: err.Error()}
		}
		if tripped {
			return &GateError{Gate: "daily_loss", Reason: fmt.Sprintf("would trip (loss $%.2f USD)", lossUSD)}
		}
	}

	return nil
}
