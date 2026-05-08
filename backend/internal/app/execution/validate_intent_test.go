package execution

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingBroker counts GetPositions calls. Used to verify which gates
// reach the broker (positionGate, exposureGuard, portfolioGuard all do).
type countingBroker struct {
	getPositionsCalls atomic.Int32
	positions         []domain.Trade
	err               error
}

func (b *countingBroker) GetPositions(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
	b.getPositionsCalls.Add(1)
	return b.positions, b.err
}
func (b *countingBroker) SubmitOrder(_ context.Context, _ domain.OrderIntent) (string, error) {
	return "", nil
}
func (b *countingBroker) CancelOrder(_ context.Context, _ string) error    { return nil }
func (b *countingBroker) GetOrderStatus(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (b *countingBroker) CancelOpenOrders(_ context.Context, _ domain.Symbol, _ string) (int, error) {
	return 0, nil
}
func (b *countingBroker) CancelAllOpenOrders(_ context.Context) (int, error) { return 0, nil }
func (b *countingBroker) GetOpenOrders(_ context.Context) ([]ports.OpenOrder, error) {
	return nil, nil
}
func (b *countingBroker) GetPosition(_ context.Context, _ domain.Symbol) (float64, error) {
	return 0, nil
}
func (b *countingBroker) CloseAtMarket(_ context.Context, _ domain.Symbol) (string, error) {
	return "", nil
}
func (b *countingBroker) GetOrderDetails(_ context.Context, _ string) (ports.OrderDetails, error) {
	return ports.OrderDetails{}, nil
}

// staticPnL is a DailyPnLSource returning a fixed P&L.
type staticPnL struct{ pnl float64 }

func (s staticPnL) GetDailyRealizedPnL(_ string, _ domain.EnvMode) float64 { return s.pnl }

// passQuoteProvider returns bid/ask matching limit price so SlippageGuard passes.
type passQuoteProvider struct{}

func (passQuoteProvider) GetQuote(_ context.Context, _ domain.Symbol) (float64, float64, error) {
	return 100.0, 100.0, nil
}

// validIntent is a long entry on a tech-equity-cluster symbol with risk well
// under any reasonable cap.
func validIntent(t *testing.T) domain.OrderIntent {
	t.Helper()
	id := uuid.New()
	intent, err := domain.NewOrderIntent(
		id, "tenant-1", domain.EnvModePaper,
		"AAPL", domain.DirectionLong,
		100.0, 99.0, 10, 1.0,
		"strategy-1", "rationale", 0.8, id.String(),
	)
	require.NoError(t, err)
	return intent
}

func exitIntent(t *testing.T) domain.OrderIntent {
	t.Helper()
	id := uuid.New()
	intent, err := domain.NewOrderIntent(
		id, "tenant-1", domain.EnvModePaper,
		"AAPL", domain.DirectionCloseLong,
		100.0, 99.0, 10, 1.0,
		"strategy-1", "rationale", 0.8, id.String(),
	)
	require.NoError(t, err)
	return intent
}

// passingService builds a Service whose every gate is configured to pass.
// Individual tests override fields to make a specific gate fail.
func passingService(equity float64) *Service {
	now := func() time.Time { return time.Date(2026, 5, 7, 14, 30, 0, 0, time.UTC) }
	return &Service{
		killSwitch:         NewKillSwitch(3, time.Hour, time.Hour, now),
		riskEngine:         NewRiskEngine(0.02),
		slippageGuard:      NewSlippageGuard(passQuoteProvider{}),
		tradingWindowGuard: NewTradingWindowGuard(zerolog.Nop()),
		accountEquity:      equity,
		log:                zerolog.Nop(),
		nowFn:              now,
	}
}

func TestValidateIntent_AllGatesPass(t *testing.T) {
	s := passingService(100000.0)
	gerr := s.ValidateIntent(context.Background(), validIntent(t))
	assert.Nil(t, gerr)
}

func TestValidateIntent_FirstFailingGateWins(t *testing.T) {
	s := passingService(100000.0)
	broker := &countingBroker{}
	s.positionGate = NewPositionGate(broker, zerolog.Nop())
	s.exposureGuard = NewExposureGuard(broker, 100000.0, zerolog.Nop())

	intent := validIntent(t)
	// Pre-lock inflight so positionGate fails immediately, BEFORE its
	// own broker.GetPositions call. If exposureGuard runs, it will hit
	// broker.GetPositions and bump the counter to 1.
	s.positionGate.MarkInflight(intent.TenantID, intent.EnvMode, intent.Symbol)

	gerr := s.ValidateIntent(context.Background(), intent)
	require.NotNil(t, gerr)
	assert.Equal(t, "position_gate", gerr.Gate)
	assert.Equal(t, int32(0), broker.getPositionsCalls.Load(),
		"exposureGuard.Check must NOT run after position_gate fails")
}

func TestValidateIntent_DailyLossTripped_NoMutation(t *testing.T) {
	s := passingService(10000.0)
	now := func() time.Time { return time.Date(2026, 5, 7, 14, 30, 0, 0, time.UTC) }
	breaker := risk.NewDailyLossBreaker(0.10, 100.0, staticPnL{pnl: -150.0}, now, zerolog.Nop())
	s.dailyLossBreaker = breaker

	gerr := s.ValidateIntent(context.Background(), validIntent(t))
	require.NotNil(t, gerr)
	assert.Equal(t, "daily_loss", gerr.Gate)
	assert.NotEmpty(t, gerr.Reason)

	// Phase 1 invariant: Inspect must not flip kill-switch state or set haltDate.
	assert.False(t, breaker.IsHalted(), "breaker.IsHalted must stay false after Inspect")
	assert.Equal(t, risk.KillSwitchActive, breaker.State(), "breaker.State must stay ACTIVE after Inspect")
}

// TestValidateIntent_GateOrder_LockstepWithProcess pins the canonical gate
// sequence. If you change gate order in either ValidateIntent (validate_intent.go)
// or process() (service.go:828-949 + :986-987), this test must fail.
// Update both lists together — the proposal handler relies on the parallel paths
// staying in lockstep.
func TestValidateIntent_GateOrder_LockstepWithProcess(t *testing.T) {
	wantOrder := []string{
		"kill_switch",
		"position_gate",
		"exposure_guard",
		"author_mirror",
		"portfolio_guard",
		"risk_engine",
		"slippage",
		"trading_window",
		"daily_loss",
	}
	s := passingService(100000.0)
	assert.Equal(t, wantOrder, s.gateOrder())
}

func TestValidateIntent_KillSwitchHalted_SkipsDownstream(t *testing.T) {
	s := passingService(100000.0)
	broker := &countingBroker{}
	s.positionGate = NewPositionGate(broker, zerolog.Nop())
	s.exposureGuard = NewExposureGuard(broker, 100000.0, zerolog.Nop())

	// Trip kill-switch: 3 stops in window halts the symbol.
	intent := validIntent(t)
	for i := 0; i < 3; i++ {
		_ = s.killSwitch.RecordStop(intent.TenantID, intent.Symbol)
	}
	require.True(t, s.killSwitch.IsHalted(intent.TenantID, intent.Symbol))

	gerr := s.ValidateIntent(context.Background(), intent)
	require.NotNil(t, gerr)
	assert.Equal(t, "kill_switch", gerr.Gate)
	assert.Equal(t, int32(0), broker.getPositionsCalls.Load(),
		"no downstream gate may run after kill_switch fails")
}

func TestValidateIntent_ExitIntent_BypassesKillSwitchAndDailyLoss(t *testing.T) {
	s := passingService(10000.0)

	intent := exitIntent(t)
	// Trip kill-switch.
	for i := 0; i < 3; i++ {
		_ = s.killSwitch.RecordStop(intent.TenantID, intent.Symbol)
	}
	require.True(t, s.killSwitch.IsHalted(intent.TenantID, intent.Symbol))

	// Trip daily loss.
	now := func() time.Time { return time.Date(2026, 5, 7, 14, 30, 0, 0, time.UTC) }
	s.dailyLossBreaker = risk.NewDailyLossBreaker(0.10, 100.0, staticPnL{pnl: -150.0}, now, zerolog.Nop())

	gerr := s.ValidateIntent(context.Background(), intent)
	assert.Nil(t, gerr, "exit intent must bypass both kill_switch and daily_loss")
}
