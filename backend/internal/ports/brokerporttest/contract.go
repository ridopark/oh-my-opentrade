package brokerporttest

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// RunBrokerPortContract executes the BrokerPort contract suite against
// the supplied adapter. Subtests run in a fixed order against a single
// broker instance; each subtest can rely on the state the previous one
// left.
//
// stream may be nil — the OrderStreamPort contract is a separate suite
// that this PR does not yet cover; nil-tolerance keeps SimBroker
// (which doesn't implement OrderStreamPort) free of ceremony.
func RunBrokerPortContract(t *testing.T, broker ports.BrokerPort, stream ports.OrderStreamPort, env *Env) {
	t.Helper()
	if env == nil {
		t.Fatal("brokerporttest: Env is required")
	}
	if len(env.TestSymbols) == 0 {
		t.Fatal("brokerporttest: Env.TestSymbols must be non-empty")
	}
	for _, sym := range env.TestSymbols {
		if _, ok := env.InitialPrice[sym]; !ok {
			t.Fatalf("brokerporttest: Env.InitialPrice missing entry for %q (every TestSymbols entry needs a price; newIntent uses it as LimitPrice for cross-adapter compatibility)", sym)
		}
	}
	if env.Setup != nil {
		if err := env.Setup(t); err != nil {
			t.Fatalf("brokerporttest: Env.Setup: %v", err)
		}
	}

	ctx := context.Background()

	if !env.SkipFreshPositionsCheck {
		t.Run("GetPositions_Fresh_ReturnsEmpty", func(t *testing.T) {
			pos, err := broker.GetPositions(ctx, env.TestTenantID, env.TestEnvMode)
			if err != nil {
				t.Fatalf("GetPositions: %v", err)
			}
			if len(pos) != 0 {
				t.Errorf("GetPositions on fresh adapter returned %d positions; want 0 (set Env.SkipFreshPositionsCheck if your fixture pre-seeds)", len(pos))
			}
		})
	}

	t.Run("GetPosition_UnknownSymbol_ReturnsZeroNoError", func(t *testing.T) {
		// Per BrokerPort contract: "Returns (0, nil) if no position
		// exists — this is not an error." Pick a symbol unlikely to
		// have a position seeded in any adapter's fixture.
		unknown := domain.Symbol("ZZUNKNOWN")
		qty, err := broker.GetPosition(ctx, unknown)
		if err != nil {
			t.Errorf("GetPosition on unknown symbol returned err=%v; contract requires (0, nil)", err)
		}
		if qty != 0 {
			t.Errorf("GetPosition on unknown symbol returned qty=%v; want 0", qty)
		}
	})

	// State-mutating subtests run last. They share the orderID across
	// the SubmitOrder / GetOrderStatus pair via a closure.
	var submittedOrderID string

	t.Run("SubmitOrder_ValidIntent_ReturnsNonEmptyOrderID", func(t *testing.T) {
		intent := newIntent(env, env.TestSymbols[0], domain.DirectionLong, 1)
		orderID, err := broker.SubmitOrder(ctx, intent)
		if err != nil {
			t.Fatalf("SubmitOrder: %v", err)
		}
		if orderID == "" {
			t.Fatal("SubmitOrder returned empty orderID; contract requires a non-empty broker order id on success")
		}
		submittedOrderID = orderID
	})

	t.Run("GetOrderStatus_RepeatedCalls_ReturnSameStatus", func(t *testing.T) {
		if submittedOrderID == "" {
			t.Skip("no order from SubmitOrder subtest — skipping idempotency check")
		}
		first, err := broker.GetOrderStatus(ctx, submittedOrderID)
		if err != nil {
			t.Fatalf("GetOrderStatus first call: %v", err)
		}
		second, err := broker.GetOrderStatus(ctx, submittedOrderID)
		if err != nil {
			t.Fatalf("GetOrderStatus second call: %v", err)
		}
		if first != second {
			t.Errorf("GetOrderStatus returned %q then %q for the same orderID; contract requires idempotent reads", first, second)
		}
	})

	// Stream sub-suite runs after BrokerPort invariants. Adapters
	// passing nil here (SimBroker, Hyperliquid today) skip the entire
	// stream suite via RunOrderStreamPortContract's nil-tolerance.
	RunOrderStreamPortContract(t, stream, env)
}

// newIntent builds an OrderIntent for the harness's SubmitOrder probe.
// OrderType=market and a non-zero LimitPrice (sourced from
// Env.InitialPrice) are both required by at least one current adapter
// (IBKR rejects empty OrderType paired with zero LimitPrice;
// Hyperliquid uses LimitPrice as the slippage-bound base for market
// orders). The harness validates that every TestSymbols entry has an
// InitialPrice before any subtest runs, so the lookup here is safe.
func newIntent(env *Env, sym domain.Symbol, dir domain.Direction, qty float64) domain.OrderIntent {
	return domain.OrderIntent{
		ID:             uuid.New(),
		TenantID:       env.TestTenantID,
		EnvMode:        env.TestEnvMode,
		Symbol:         sym,
		Direction:      dir,
		Quantity:       qty,
		OrderType:      "market",
		LimitPrice:     env.InitialPrice[sym],
		IdempotencyKey: "brokerporttest-" + uuid.NewString(),
	}
}
