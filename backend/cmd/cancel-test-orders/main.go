// cancel-test-orders connects to IB Gateway and issues a global cancel
// across all clients. Used to clean up after submit-limit-order runs
// that leave unmanaged orders staged for Sprint 2 reconciliation tests.
//
// Uses IBKR_CLIENT_ID=2, same as omo-core. Must be run while omo-core
// is NOT connected (otherwise the client_id is rejected by IB Gateway).
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/ibkr"
	"github.com/oh-my-opentrade/backend/internal/config"
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

	canceled, err := adapter.CancelAllOpenOrders(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "cancel-all failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("issued global cancel (%d working orders visible to client 2)\n", canceled)

	// Wait for IB Gateway to actually process the cancels before disconnecting.
	// ReqGlobalCancel is fire-and-forget; the cancels take effect asynchronously
	// as the gateway acknowledges each order. If we disconnect immediately the
	// cancels may be dropped on the floor (observed during Sprint 3 testing:
	// "issued global cancel (1 working)" followed by "unmanaged" alert on the
	// next startup because the order was still there).
	time.Sleep(3 * time.Second)
	fmt.Println("cancels flushed to gateway")
}
