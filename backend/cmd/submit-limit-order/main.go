// submit-limit-order is a one-shot test harness for staging Sprint 2
// startup-reconciliation scenarios. It submits a single limit order to
// IBKR paper trading at a price far from the current market, then exits
// WITHOUT canceling — leaving the order in the broker's open-trades
// list so the next omo-core restart can observe it.
//
// Intended use: validate the bootstrap UNMANAGED case (broker has an
// order that is not in the intent journal) by running this tool while
// omo-core is stopped, then restarting omo-core with the journal flag
// enabled.
//
// Cleanup: cancel the order via TWS/IB Gateway manually after the test,
// or run this tool again — each invocation uses a deterministic limit
// that stacks rather than duplicates.
//
// Uses IBKR_CLIENT_ID=2 (same as omo-core) intentionally. IBKR's
// OpenTrades() only returns orders visible to the connected client id,
// so orders placed under a different client id are invisible to
// omo-core's startup reconciler. Submitting under client_id=2, then
// disconnecting, then letting omo-core reconnect on the same id makes
// IBKR redeliver the pending open order to omo-core as part of the
// client resync — which is exactly the "unmanaged broker order, no
// journal entry" scenario we want to trigger at startup.
//
// Must be run when omo-core is NOT connected (otherwise the client_id
// collision is rejected by IB Gateway).
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/ibkr"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

func main() {
	log := zerolog.New(os.Stdout).With().Timestamp().Logger()

	cfg := config.IBKRConfig{
		Host:      "localhost",
		Port:      4002,
		ClientID:  2,
		PaperMode: true,
	}

	adapter, err := ibkr.NewAdapter(cfg, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect failed: %v\n", err)
		os.Exit(1)
	}
	defer adapter.Close()

	ctx := context.Background()

	// BUY 1 SOXL @ $1.00 — far below market (SOXL trades near $30+ typically),
	// so the limit will sit as "PreSubmitted" / "Submitted" until manually
	// canceled. Safe, never executes.
	intent := domain.OrderIntent{
		Symbol:     "SOXL",
		Direction:  domain.DirectionLong,
		Quantity:   1,
		LimitPrice: 1.00,
		OrderType:  "limit",
	}

	fmt.Println("=== submitting BUY 1 SOXL @ $1.00 (unmanaged test order) ===")
	id, err := adapter.SubmitOrder(ctx, intent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Order submitted: broker_order_id=%s\n", id)

	// Give IBKR a moment to confirm the order is working before we exit.
	time.Sleep(2 * time.Second)

	status, serr := adapter.GetOrderStatus(ctx, id)
	fmt.Printf("Post-submit status: %s (err=%v)\n", status, serr)

	fmt.Println("=== exiting WITHOUT canceling ===")
}
