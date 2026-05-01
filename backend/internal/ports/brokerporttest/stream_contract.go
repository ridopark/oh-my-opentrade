package brokerporttest

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
)

// channelCloseTimeout caps how long the channel-closes-on-cancel
// invariant waits after ctx cancel before failing. IBKR's stream
// teardown closes the channel after both pollOrderUpdates (2s poll
// cadence) and execReconciler (2s cadence) goroutines exit; 6s
// gives both a full poll cycle to notice the canceled ctx plus
// margin for goroutine scheduling.
const channelCloseTimeout = 6 * time.Second

// RunOrderStreamPortContract executes the OrderStreamPort contract
// suite against an adapter that implements ports.OrderStreamPort.
// Adapters that don't (SimBroker, Hyperliquid today) skip this entire
// suite — the function returns immediately when stream is nil.
//
// Scope: lifecycle invariants only — Subscribe returns a non-nil
// channel; the channel closes when ctx is canceled. Stricter
// fill-event invariants (FilledQty monotonicity, partial->fill
// ordering, terminal-event idempotency) need a stream-aware mock that
// can simulate fill emissions; that mock isn't built yet, so those
// invariants are deferred to a follow-up PR that pairs the mock
// extension with the stricter assertions.
func RunOrderStreamPortContract(t *testing.T, stream ports.OrderStreamPort, env *Env) {
	t.Helper()
	if stream == nil {
		return
	}
	if env == nil {
		t.Fatal("brokerporttest: Env is required for OrderStreamPort contract")
	}

	t.Run("SubscribeOrderUpdates_ReturnsNonNilChannel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ch, err := stream.SubscribeOrderUpdates(ctx)
		if err != nil {
			t.Fatalf("SubscribeOrderUpdates: %v", err)
		}
		if ch == nil {
			t.Fatal("SubscribeOrderUpdates returned nil channel; contract requires a non-nil channel on success")
		}
	})

	t.Run("SubscribeOrderUpdates_ChannelClosesOnCtxCancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ch, err := stream.SubscribeOrderUpdates(ctx)
		if err != nil {
			t.Fatalf("SubscribeOrderUpdates: %v", err)
		}
		if ch == nil {
			t.Fatal("SubscribeOrderUpdates returned nil channel")
		}

		cancel()

		// Drain any in-flight events while waiting for the channel
		// close. The contract requires close-on-ctx-cancel — events
		// arriving during teardown are not a violation as long as the
		// channel eventually closes within the timeout.
		deadline := time.After(channelCloseTimeout)
		for {
			select {
			case _, open := <-ch:
				if !open {
					return
				}
			case <-deadline:
				t.Fatalf("channel did not close within %s of ctx cancel; contract requires close-on-ctx-cancel for OrderStreamPort", channelCloseTimeout)
			}
		}
	})
}
